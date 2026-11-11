package engine

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found from %s", wd)
		}
		dir = parent
	}
}

func importsViolating(t *testing.T, dir string, forbidden map[string]bool) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Errorf("parse %s: %v", dir, err)
		return nil
	}
	seen := map[string]bool{}
	var v []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if forbidden[path] && !seen[path] {
					seen[path] = true
					v = append(v, path)
				}
			}
		}
	}
	return v
}

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// mkPerMarket synthesizes one PerMarketLiquidity row with the
// requested spread/depth/quote settings.
func mkPerMarket(condID string, spreadA, spreadB, depthA, depthB float64, hasQuotes bool) scanner.PerMarketLiquidity {
	return scanner.PerMarketLiquidity{
		ConditionID:        condID,
		HasTwoSidedQuotesA: hasQuotes,
		HasTwoSidedQuotesB: hasQuotes,
		SpreadA:            spreadA,
		SpreadB:            spreadB,
		TopDepthA:          depthA,
		TopDepthB:          depthB,
	}
}

func mkCandidate(slug string, binaryCount int, per []scanner.PerMarketLiquidity) scanner.Candidate {
	return scanner.Candidate{
		EventSlug:   slug,
		BinaryCount: binaryCount,
		MarketCount: binaryCount,
		LiquidityResult: scanner.LiquidityResult{
			PerMarket:        per,
			MarketsConsidered: len(per),
			MarketsWithBoth:  len(per),
		},
	}
}

// ---------------------------------------------------------------------------
// Closed-enum PhaseLabel
// ---------------------------------------------------------------------------

func TestEngine_ClosedEnumPhaseLabel(t *testing.T) {
	want := map[PhaseLabel]bool{
		PaperIntent: true, PaperAccumulateA: true, PaperOpenB: true,
		PaperExpand: true, PaperLayer: true, PaperMergeHarvest: true,
		PaperRedeem: true, PaperIdle: true, PaperStopped: true,
	}
	if len(AllPhaseLabels) != len(want) {
		t.Fatalf("AllPhaseLabels length mismatch: got %d want %d", len(AllPhaseLabels), len(want))
	}
	for _, p := range AllPhaseLabels {
		if !want[p] {
			t.Errorf("unexpected phase %q in AllPhaseLabels", p)
		}
	}
	// Anything not in the closed set must be rejected.
	for _, bad := range []PhaseLabel{"BUY", "SELL", "ENTER", "EXIT", "TRADE", "", "paper_buy"} {
		if IsValidPhase(bad) {
			t.Errorf("IsValidPhase(%q) returned true; closed-enum violation", bad)
		}
	}
	// And every phase value must use the paper_ prefix.
	for _, p := range AllPhaseLabels {
		if !strings.HasPrefix(string(p), "paper_") {
			t.Errorf("phase label %q must use paper_ prefix", p)
		}
	}
}

// ---------------------------------------------------------------------------
// Defaults lock
// ---------------------------------------------------------------------------

func TestEngine_DefaultSizingLimitsLocked(t *testing.T) {
	d := DefaultSizingLimits()
	if d.MaxPerEventUsdc != 100.0 {
		t.Errorf("MaxPerEventUsdc = %.2f, want 100.0 (design §5.1)", d.MaxPerEventUsdc)
	}
	if d.MaxActivePaperEvents != 30 {
		t.Errorf("MaxActivePaperEvents = %d, want 30 (design §5.4)", d.MaxActivePaperEvents)
	}
	if d.MaxDailyNewEntries != 10 {
		t.Errorf("MaxDailyNewEntries = %d, want 10 (design §5.4)", d.MaxDailyNewEntries)
	}
	if d.MinTopDepthForFull != 200.0 {
		t.Errorf("MinTopDepthForFull = %.2f, want 200.0 (design §5.2)", d.MinTopDepthForFull)
	}
	if d.MaxSpreadAcceptable != 0.10 {
		t.Errorf("MaxSpreadAcceptable = %.2f, want 0.10 (design §5.2)", d.MaxSpreadAcceptable)
	}
}

// ---------------------------------------------------------------------------
// ComputeMarketSize
// ---------------------------------------------------------------------------

func TestSizing_BaseEqualsHalfMaxOverBinaryCount(t *testing.T) {
	// 10 binary markets at full depth, spread 0.02 each. Per-side
	// budget = $100 / (2 * 10) = $5. Both sides spend $5 each, so
	// per-market spend = $10 and per-event spend = $100 (the cap).
	per := make([]scanner.PerMarketLiquidity, 10)
	for i := range per {
		per[i] = mkPerMarket(string(rune('a'+i)), 0.02, 0.02, 500, 500, true)
	}
	c := mkCandidate("evt-x", 10, per)
	limits := DefaultSizingLimits()

	for i := range per {
		d := ComputeMarketSize(&c, i, limits)
		if !d.Accepted {
			t.Fatalf("market %d rejected: %s", i, d.Reason)
		}
		if !approxEqual(d.PaperSizeUsdc, 5.0) {
			t.Errorf("market %d per-side size = %.4f, want 5.0 (= $100 / (2*10))",
				i, d.PaperSizeUsdc)
		}
	}
}

func TestSizing_DepthAdjustmentBelowFloor(t *testing.T) {
	// 4 binary markets. Per-side budget = $100 / (2 * 4) = $12.50.
	// Market 1 has depth 100 (below 200 floor) → adjusted = 12.5 *
	// (100/200) = 6.25 per side.
	per := []scanner.PerMarketLiquidity{
		mkPerMarket("c1", 0.02, 0.02, 500, 500, true),
		mkPerMarket("c2", 0.02, 0.02, 100, 100, true),
		mkPerMarket("c3", 0.02, 0.02, 300, 300, true), // 300 > 200 → no adj
		mkPerMarket("c4", 0.02, 0.02, 50, 50, true),    // 50 → 12.5 * 0.25 = 3.125
	}
	c := mkCandidate("evt-d", 4, per)
	limits := DefaultSizingLimits()

	d0 := ComputeMarketSize(&c, 0, limits)
	if !approxEqual(d0.PaperSizeUsdc, 12.5) {
		t.Errorf("market 0 per-side size = %.4f, want 12.5", d0.PaperSizeUsdc)
	}
	d1 := ComputeMarketSize(&c, 1, limits)
	if !approxEqual(d1.PaperSizeUsdc, 6.25) {
		t.Errorf("market 1 per-side size = %.4f, want 6.25 (depth adj)", d1.PaperSizeUsdc)
	}
	d2 := ComputeMarketSize(&c, 2, limits)
	if !approxEqual(d2.PaperSizeUsdc, 12.5) {
		t.Errorf("market 2 per-side size = %.4f, want 12.5 (depth 300 >= floor)", d2.PaperSizeUsdc)
	}
	d3 := ComputeMarketSize(&c, 3, limits)
	if !approxEqual(d3.PaperSizeUsdc, 3.125) {
		t.Errorf("market 3 per-side size = %.4f, want 3.125", d3.PaperSizeUsdc)
	}
}

// TestSizing_PerEventCapStaysWithinLimit is the invariant that the
// 2026-05-16 production cycle audit surfaced: when every market in
// a bundle accepts, the SUM of per-market spend (= 2 * per-side
// budget) across all binary markets must not exceed MaxPerEventUsdc.
// This is the contract operators rely on for capital-at-risk
// budgeting.
func TestSizing_PerEventCapStaysWithinLimit(t *testing.T) {
	for _, binaryCount := range []int{2, 3, 5, 9, 12, 25} {
		per := make([]scanner.PerMarketLiquidity, binaryCount)
		for i := range per {
			per[i] = mkPerMarket(string(rune('a'+i)), 0.02, 0.02, 500, 500, true)
		}
		c := mkCandidate("evt", binaryCount, per)
		limits := DefaultSizingLimits()
		_, total := ComputeEventSizing(&c, limits)
		// total is the SUM of accepted per-side budgets. Both sides
		// fill at each per-side budget, so per-event spend = 2 * total.
		perEventSpend := 2.0 * total
		if perEventSpend > limits.MaxPerEventUsdc+1e-6 {
			t.Errorf("binaryCount=%d: per-event spend %.4f exceeds cap %.4f",
				binaryCount, perEventSpend, limits.MaxPerEventUsdc)
		}
	}
}

func TestSizing_RejectWideSpread(t *testing.T) {
	// Default cap is 0.10. Spread on side A = 0.11 → reject.
	per := []scanner.PerMarketLiquidity{
		mkPerMarket("c1", 0.11, 0.02, 500, 500, true),
		mkPerMarket("c2", 0.02, 0.15, 500, 500, true), // side B too wide
	}
	c := mkCandidate("evt-s", 2, per)
	limits := DefaultSizingLimits()

	for i := range per {
		d := ComputeMarketSize(&c, i, limits)
		if d.Accepted {
			t.Errorf("market %d should be rejected for wide spread, got accept", i)
		}
		if !strings.Contains(d.Reason, "spread") {
			t.Errorf("market %d reason = %q; expected to mention spread", i, d.Reason)
		}
	}
}

func TestSizing_RejectNoTwoSidedQuotes(t *testing.T) {
	per := []scanner.PerMarketLiquidity{
		mkPerMarket("c1", 0.02, 0.02, 500, 500, false), // no quotes
	}
	c := mkCandidate("evt-q", 1, per)
	d := ComputeMarketSize(&c, 0, DefaultSizingLimits())
	if d.Accepted {
		t.Errorf("market without two-sided quotes should be rejected, got accept")
	}
}

func TestSizing_RejectZeroDepth(t *testing.T) {
	per := []scanner.PerMarketLiquidity{
		mkPerMarket("c1", 0.02, 0.02, 0, 0, true),
	}
	c := mkCandidate("evt-z", 1, per)
	d := ComputeMarketSize(&c, 0, DefaultSizingLimits())
	if d.Accepted {
		t.Errorf("market with zero depth should be rejected, got accept")
	}
}

func TestSizing_GuardsAgainstBadInput(t *testing.T) {
	limits := DefaultSizingLimits()
	if d := ComputeMarketSize(nil, 0, limits); d.Accepted {
		t.Error("nil candidate should not be accepted")
	}
	c := mkCandidate("evt", 0, nil)
	if d := ComputeMarketSize(&c, 0, limits); d.Accepted {
		t.Error("zero-binary candidate should not be accepted")
	}
	c2 := mkCandidate("evt", 1, []scanner.PerMarketLiquidity{mkPerMarket("c1", 0.02, 0.02, 500, 500, true)})
	if d := ComputeMarketSize(&c2, 5, limits); d.Accepted {
		t.Error("out-of-range market index should not be accepted")
	}
}

// ---------------------------------------------------------------------------
// ComputeEventSizing aggregation
// ---------------------------------------------------------------------------

func TestSizing_EventSizing_TotalsCorrectly(t *testing.T) {
	// 4-market event, per-side budget = $100 / (2*4) = $12.50.
	// Market 1 has depth 100 (below floor) → 12.5 * 0.5 = 6.25.
	// Market 2 has wide spread (0.15) → reject.
	per := []scanner.PerMarketLiquidity{
		mkPerMarket("c1", 0.02, 0.02, 500, 500, true), // accept 12.5
		mkPerMarket("c2", 0.02, 0.02, 100, 100, true), // accept 6.25
		mkPerMarket("c3", 0.15, 0.02, 500, 500, true), // reject spread
		mkPerMarket("c4", 0.02, 0.02, 500, 500, true), // accept 12.5
	}
	c := mkCandidate("evt", 4, per)
	decisions, total := ComputeEventSizing(&c, DefaultSizingLimits())
	if len(decisions) != 4 {
		t.Fatalf("decisions len = %d, want 4", len(decisions))
	}
	want := 12.5 + 6.25 + 12.5
	if !approxEqual(total, want) {
		t.Errorf("total per-side = %.4f, want %.4f", total, want)
	}
}

func TestSizing_EventSizing_NilCandidate(t *testing.T) {
	ds, total := ComputeEventSizing(nil, DefaultSizingLimits())
	if ds != nil || total != 0 {
		t.Errorf("nil candidate should yield nil decisions + zero total, got %v / %.2f", ds, total)
	}
}

// ---------------------------------------------------------------------------
// CapAccepts: belt-and-suspenders cap enforcement
// ---------------------------------------------------------------------------

func TestSizing_CapAccepts_UnderCapNoOp(t *testing.T) {
	ds := []SizingDecision{
		{Accepted: true, PaperSizeUsdc: 30},
		{Accepted: true, PaperSizeUsdc: 40},
		{Accepted: false},
	}
	got, total := CapAccepts(ds, 100)
	if !approxEqual(total, 70) {
		t.Errorf("total = %.2f, want 70 (under cap)", total)
	}
	if got[0].PaperSizeUsdc != 30 || got[1].PaperSizeUsdc != 40 {
		t.Errorf("under-cap accepts should not be trimmed")
	}
}

func TestSizing_CapAccepts_ShrinksLargestFirst(t *testing.T) {
	// Total = 60+30+25 = 115; cap = 100; overshoot = 15.
	// Largest (60) shrinks to 45; total → 100.
	ds := []SizingDecision{
		{Accepted: true, PaperSizeUsdc: 60},
		{Accepted: true, PaperSizeUsdc: 30},
		{Accepted: true, PaperSizeUsdc: 25},
	}
	got, total := CapAccepts(ds, 100)
	if !approxEqual(total, 100) {
		t.Errorf("total = %.2f, want 100", total)
	}
	if !approxEqual(got[0].PaperSizeUsdc, 45) {
		t.Errorf("largest size = %.2f, want 45 (60 - 15 overshoot)", got[0].PaperSizeUsdc)
	}
	if got[1].PaperSizeUsdc != 30 || got[2].PaperSizeUsdc != 25 {
		t.Errorf("smaller accepts should be untouched")
	}
}

func TestSizing_CapAccepts_RejectsWhenSmallerThanOvershoot(t *testing.T) {
	// 60+25 = 85; cap = 50; overshoot = 35 which exceeds 60? no, 60 > 35.
	// Largest=60 shrinks to 25; total = 50.
	// Let's do another: 20+15+10 = 45; cap = 25; overshoot = 20.
	// Largest=20 ≤ 20 → reject entirely; total → 25.
	ds := []SizingDecision{
		{Accepted: true, PaperSizeUsdc: 20},
		{Accepted: true, PaperSizeUsdc: 15},
		{Accepted: true, PaperSizeUsdc: 10},
	}
	got, total := CapAccepts(ds, 25)
	if !approxEqual(total, 25) {
		t.Errorf("total = %.2f, want 25", total)
	}
	if got[0].Accepted {
		t.Errorf("largest (20) should have been rejected entirely, still accepted")
	}
	if strings.Contains(got[0].Reason, "trimmed") == false {
		t.Errorf("rejection reason should mention 'trimmed', got %q", got[0].Reason)
	}
}

func TestSizing_CapAccepts_ZeroCapIsPassthrough(t *testing.T) {
	ds := []SizingDecision{
		{Accepted: true, PaperSizeUsdc: 999999},
	}
	got, total := CapAccepts(ds, 0)
	if total != 999999 || !got[0].Accepted {
		t.Errorf("cap=0 should pass through, got total=%.2f accepted=%v", total, got[0].Accepted)
	}
}

// ---------------------------------------------------------------------------
// CI invariant: no network imports
// ---------------------------------------------------------------------------

func TestEngine_NoNetworkImport_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dir := filepath.Join(root, "internal", "engine")
	forbidden := map[string]bool{
		"net":                          true,
		"net/http":                     true,
		"net/url":                      true,
		"net/rpc":                      true,
		"golang.org/x/net/websocket":   true,
		"github.com/gorilla/websocket": true,
		"google.golang.org/grpc":       true,
	}
	v := importsViolating(t, dir, forbidden)
	if len(v) > 0 {
		t.Errorf("internal/engine imports forbidden network packages: %v", v)
	}
}

// ---------------------------------------------------------------------------
// CI invariant: no store/ingest/summary/replay imports (forward-only
// consumer of scanner + simulator + diff outputs)
// ---------------------------------------------------------------------------

func TestEngine_NoFutureLeakage_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dir := filepath.Join(root, "internal", "engine")
	forbidden := map[string]bool{
		"rn1-strategy-extractor/internal/store":   true,
		"rn1-strategy-extractor/internal/ingest":  true,
		"rn1-strategy-extractor/internal/summary": true,
		"rn1-strategy-extractor/internal/replay":  true,
	}
	v := importsViolating(t, dir, forbidden)
	if len(v) > 0 {
		t.Errorf("internal/engine imports forbidden packages: %v", v)
	}
}

// ---------------------------------------------------------------------------
// CI invariant: no wallet/signer/order placement tokens
// ---------------------------------------------------------------------------

func TestEngine_NoWalletOrOrderPath_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dir := filepath.Join(root, "internal", "engine")
	forbiddenSubstrings := []string{
		"signer", "keystore", "private-key", "privatekey", "private_key",
		"PostOrder", "post_order", "CancelOrder", "cancel_order",
		"PolygonRPC", "PolygonProvider", "EthClient", "eth_send",
		"/order", "/cancel",
	}
	// types.go's package doc lists these tokens explicitly to
	// FORBID them, so it is exempted from the static substring
	// check. (The output-vocabulary check below catches anything
	// these tokens would actually emit.)
	exempt := map[string]bool{
		"types.go": true,
	}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if exempt[filepath.Base(p)] {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("read %s: %v", p, err)
			return nil
		}
		lower := strings.ToLower(string(body))
		for _, s := range forbiddenSubstrings {
			if strings.Contains(lower, strings.ToLower(s)) {
				t.Errorf("file %s contains forbidden token %q", p, s)
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// CI invariant: no real/live money vocabulary in output. Every
// monetary field name must start with `paper_`.
// ---------------------------------------------------------------------------

func TestEngine_PaperPrefixOnly_InOutput(t *testing.T) {
	// Marshal one of each output struct and search for
	// disallowed money words.
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	samples := []any{
		SizingDecision{
			ConditionID: "0xabc", Accepted: true, PaperSizeUsdc: 10.0,
			Reason: "ok", MinDepth: 500, MaxSpread: 0.02,
			BaseSizeUsdc: 10.0, DepthAdjustment: 1.0,
		},
		PaperFill{
			ObservedAt: now, CycleTs: "2026-05-16T08-00-00Z",
			EventSlug: "evt", ConditionID: "0xabc", Side: "a",
			PaperShares: 20, PaperAvgPrice: 0.5, PaperCostUsdc: 10.0,
			PaperFeeUsdc: 0.02, Phase: PaperAccumulateA,
		},
		PaperLeg{
			EventSlug: "evt", ConditionID: "0xabc",
			PaperSharesA: 5, PaperSharesB: 5,
			PaperCumCostA: 2.5, PaperCumCostB: 2.4,
			PaperCumFeeUsdc: 0.01,
			PaperWavgA: 0.5, PaperWavgB: 0.48, PaperSumWavg: 0.98,
			FirstFillAt: now, LastFillAt: now,
			MinPaperSumWavg: 0.95, MinPaperSumWavgAt: now,
			CurrentPhase: PaperOpenB,
			PhaseHistory: []PhaseLabel{PaperIntent, PaperAccumulateA, PaperOpenB},
		},
		PaperPosition{
			EventSlug: "evt", SportPrefix: "mls", Title: "X vs Y",
			OpenedAt: now, LastUpdatedAt: now,
			PaperTotalCostUsdc: 50, PaperMergePnlUsdc: 1.5,
			PaperRedeemPnlUsdc: 0.5, PaperRealizedPnlUsdc: 2.0,
			CurrentPhase: PaperMergeHarvest,
		},
	}
	body, err := json.MarshalIndent(samples, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lower := strings.ToLower(string(body))

	// These vocabulary words must NEVER appear in output, even
	// inside `paper_*` prefixed fields.
	forbidden := []string{
		"\"buy\"", "\"sell\"",
		"enter long", "enter short", "exit long", "exit short",
		"place order", "submit order", "cancel order",
		"take profit", "stop loss",
		"arbitrage", "expected profit", "expected return", "expected pnl",
		"wallet", "private_key", "private-key", "keystore",
		"order_id", "fill_id", "tx_hash", "tx hash", "signed",
		// real-money words: any field named real_/live_ is a leak
		"real_", "live_pnl", "live_position",
	}
	for _, bad := range forbidden {
		if strings.Contains(lower, bad) {
			t.Errorf("output contains forbidden token %q\n%s", bad, body)
		}
	}

	// Every JSON field that mentions usdc/shares/price/pnl must be
	// prefixed paper_. Walk the JSON dynamically.
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	moneyFieldRegexes := []string{"usdc", "shares", "price", "pnl", "size", "cost", "fee"}
	walkAndCheckPrefixes(t, raw, "", moneyFieldRegexes)
}

// walkAndCheckPrefixes walks a decoded JSON tree and asserts that
// any field name containing a money-suggesting substring also
// starts with `paper_`. Skips fields under `reason` whose VALUE
// might contain those words (the reason text is exempted, only
// field NAMES are checked).
func walkAndCheckPrefixes(t *testing.T, v any, parent string, moneyTerms []string) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			for _, term := range moneyTerms {
				if !strings.Contains(strings.ToLower(k), term) {
					continue
				}
				// "base_size_usdc" matches term "size" but starts with
				// "base_", not "paper_". That field documents the
				// sizing-decision input, not a paper position; allow
				// it explicitly.
				exempt := map[string]bool{
					"base_size_usdc": true,
					"min_depth":      true,
					"max_spread":     true,
					// "depth_adjustment" matches via "depth" but we
					// only check the money terms above; depth is not
					// in moneyTerms, so no issue.
				}
				if exempt[k] {
					continue
				}
				if !strings.HasPrefix(k, "paper_") {
					t.Errorf("field %q (under %q) suggests money/price but lacks paper_ prefix",
						k, parent)
				}
			}
			walkAndCheckPrefixes(t, vv, k, moneyTerms)
		}
	case []any:
		for i, vv := range x {
			walkAndCheckPrefixes(t, vv, parent+"[]", moneyTerms)
			_ = i
		}
	}
}

// ---------------------------------------------------------------------------
// CI invariant: no bare 9-state names in output. Every phase value
// must start with `paper_`.
// ---------------------------------------------------------------------------

func TestEngine_NoBareNineStateInOutput(t *testing.T) {
	samples := []any{}
	for _, p := range AllPhaseLabels {
		samples = append(samples, struct {
			Phase PhaseLabel `json:"phase"`
		}{p})
	}
	body, _ := json.Marshal(samples)
	for _, bare := range []string{
		`"ACCUMULATE_A"`, `"OPEN_B"`, `"EXPAND"`, `"LAYER"`,
		`"MERGE_HARVEST"`, `"REDEEM"`, `"IDLE"`,
	} {
		if strings.Contains(string(body), bare) {
			t.Errorf("output contains bare 9-state token %q", bare)
		}
	}
}

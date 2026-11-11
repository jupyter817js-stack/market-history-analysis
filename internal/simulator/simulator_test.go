package simulator

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

// ---------------------------------------------------------------------------
// fixture helpers
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

func tptr(t time.Time) *time.Time { return &t }

// mkCandidate builds a baseline very-strong soccer-companion candidate
// matching the canonical Patch 2/3 dry-run output.
func mkCandidate(now time.Time) scanner.Candidate {
	start := now.Add(8 * time.Hour)
	per := make([]scanner.PerMarketLiquidity, 9)
	for i := range per {
		per[i] = scanner.PerMarketLiquidity{
			ConditionID:        fmt.Sprintf("0xcond%02d", i),
			TokenAID:           fmt.Sprintf("tokA%02d", i),
			TokenBID:           fmt.Sprintf("tokB%02d", i),
			HasTwoSidedQuotesA: true,
			HasTwoSidedQuotesB: true,
			SpreadA:            0.02,
			SpreadB:            0.02,
			TopDepthA:          400,
			TopDepthB:          400,
		}
	}
	return scanner.Candidate{
		Rank:          1,
		EventSlug:     "egy1-phf-mos-2026-05-12-more-markets",
		Title:         "Pharco FC vs. Modern SC - More Markets",
		SportPrefix:   "egy1",
		Score:         12.5,
		Band:          "very_strong",
		LiquidityGate: scanner.LiquidityPass,
		LiquidityResult: scanner.LiquidityResult{
			Label:              scanner.LiquidityPass,
			FractionWithQuotes: 1.0,
			AvgSpread:          0.02,
			MinTopDepth:        400,
			MarketsConsidered:  9,
			MarketsWithBoth:    9,
			PerMarket:          per,
		},
		MarketCount: 9,
		BinaryCount: 9,
		Families:    []string{"spread_ml_total_stack", "expanded_companion_stack"},
		StartTs:     tptr(start),
		TimeToStart: 8 * time.Hour,
		Active:      true,
		Closed:      false,
		ScannedAt:   now,
	}
}

// ---------------------------------------------------------------------------
// §J.1 fixture-driven label tests
// ---------------------------------------------------------------------------

func TestSimulator_VeryStrongCompanion_AllLabels(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	c := mkCandidate(now)
	labels, _ := EvaluateLabels(c, now)
	want := []Label{
		LabelScan,
		LabelProbeEligible,
		LabelDualSideObservableA,
		LabelDualSideObservableB,
		LabelExpandReady,
		LabelLayerStructureObservable,
		LabelMergeStructureObservable,
		LabelRedeemHorizonFuture,
	}
	if !sameLabelSet(labels, want) {
		t.Errorf("very-strong companion labels:\n  got  %v\n  want %v", labels, want)
	}
}

func TestSimulator_MediumSoccerBase_NoExpand(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	c := mkCandidate(now)
	// downgrade to the medium 3-binary base shape.
	c.Score = 6.0
	c.Band = "medium"
	c.MarketCount = 3
	c.BinaryCount = 3
	c.Families = []string{"soccer_3way_moneyline"}
	c.LiquidityResult.PerMarket = c.LiquidityResult.PerMarket[:3]
	c.LiquidityResult.MarketsConsidered = 3
	c.LiquidityResult.MarketsWithBoth = 3
	labels, _ := EvaluateLabels(c, now)
	if hasLabel(labels, LabelExpandReady) {
		t.Errorf("3-binary medium event should NOT carry EXPAND_READY: %v", labels)
	}
	if hasLabel(labels, LabelLayerStructureObservable) {
		t.Errorf("3-binary medium event should NOT carry LAYER_STRUCTURE_OBSERVABLE: %v", labels)
	}
	if !hasLabel(labels, LabelMergeStructureObservable) {
		t.Errorf("3-binary soccer event SHOULD carry MERGE_STRUCTURE_OBSERVABLE: %v", labels)
	}
	if !hasLabel(labels, LabelProbeEligible) {
		t.Errorf("medium soccer event SHOULD carry PROBE_ELIGIBLE: %v", labels)
	}
}

func TestSimulator_UnknownGate_NoQuoteDerivedLabels(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	c := mkCandidate(now)
	c.LiquidityGate = scanner.LiquidityUnknown
	for i := range c.LiquidityResult.PerMarket {
		c.LiquidityResult.PerMarket[i].HasTwoSidedQuotesA = false
		c.LiquidityResult.PerMarket[i].HasTwoSidedQuotesB = false
		c.LiquidityResult.PerMarket[i].BookErrorA = "no-quotes"
		c.LiquidityResult.PerMarket[i].BookErrorB = "no-quotes"
	}
	c.LiquidityResult.MarketsWithBoth = 0
	labels, _ := EvaluateLabels(c, now)
	for _, bad := range []Label{
		LabelProbeEligible,
		LabelDualSideObservableA,
		LabelDualSideObservableB,
		LabelExpandReady,
	} {
		if hasLabel(labels, bad) {
			t.Errorf("UNKNOWN-gate candidate must NOT carry %s: %v", bad, labels)
		}
	}
	if !hasLabel(labels, LabelScan) {
		t.Errorf("default SCAN must always fire on non-terminal candidates: %v", labels)
	}
	if !hasLabel(labels, LabelRedeemHorizonFuture) {
		t.Errorf("future-end-date candidate must carry REDEEM_HORIZON_FUTURE: %v", labels)
	}
}

func TestSimulator_ClosedCandidate_OnlyTerminal(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	c := mkCandidate(now)
	c.Closed = true
	labels, reasons := EvaluateLabels(c, now)
	if len(labels) != 1 || labels[0] != LabelIdleOrTerminal {
		t.Errorf("closed candidate must return [IDLE_OR_TERMINAL]; got %v", labels)
	}
	if reasons[LabelIdleOrTerminal] == "" {
		t.Error("IDLE_OR_TERMINAL must carry a reason")
	}
}

func TestSimulator_PastHorizon_OnlyTerminal(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	c := mkCandidate(now)
	past := now.Add(-2 * time.Hour)
	c.StartTs = &past
	labels, _ := EvaluateLabels(c, now)
	if len(labels) != 1 || labels[0] != LabelIdleOrTerminal {
		t.Errorf("past-horizon candidate must return [IDLE_OR_TERMINAL]; got %v", labels)
	}
}

// ---------------------------------------------------------------------------
// §J.2 static safety checks
// ---------------------------------------------------------------------------

// TestSimulator_UnresolvedEventCannotEnterRedeem — a future-end-date
// candidate may carry REDEEM_HORIZON_FUTURE but MUST NOT carry the
// bare REDEEM label. The simulator must never claim a live event is
// in REDEEM.
func TestSimulator_UnresolvedEventCannotEnterRedeem(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	c := mkCandidate(now)
	labels, _ := EvaluateLabels(c, now)
	for _, l := range labels {
		if string(l) == "REDEEM" {
			t.Errorf("unresolved event must NEVER carry bare REDEEM; got %v", labels)
		}
	}
	if !hasLabel(labels, LabelRedeemHorizonFuture) {
		t.Errorf("unresolved event must carry REDEEM_HORIZON_FUTURE placeholder; got %v", labels)
	}
}

// TestSimulator_NoActualWAVGWithoutFills — the Trace struct must not
// expose any field whose name contains `wavg`, `avg_price`, or
// `realized_pnl`. Live mode has no fills.
func TestSimulator_NoActualWAVGWithoutFills(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	c := mkCandidate(now)
	tr := BuildTrace(c, now)
	body, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	lower := strings.ToLower(string(body))
	for _, bad := range []string{
		"wavg",
		"avg_price",
		"avgprice",
		"realized_pnl",
		"realizedpnl",
	} {
		if strings.Contains(lower, bad) {
			t.Errorf("Trace JSON must not contain %q without hypothetical_ namespace; got %s", bad, string(body))
		}
	}
}

// TestSimulator_MergeLabelObservableNotActionable — when the merge
// label fires, its reason string MUST contain the disclaimer
// "if a position existed".
func TestSimulator_MergeLabelObservableNotActionable(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	c := mkCandidate(now)
	_, reasons := EvaluateLabels(c, now)
	reason, ok := reasons[LabelMergeStructureObservable]
	if !ok {
		t.Fatal("expected MERGE_STRUCTURE_OBSERVABLE on canonical candidate")
	}
	lower := strings.ToLower(reason)
	if !strings.Contains(lower, "if a position existed") {
		t.Errorf("MERGE_STRUCTURE_OBSERVABLE reason must contain 'if a position existed' disclaimer; got %q", reason)
	}
	for _, badAction := range []string{"buy", "sell", "execute", "place order", "submit"} {
		if strings.Contains(lower, badAction) {
			t.Errorf("MERGE_STRUCTURE_OBSERVABLE reason must NOT contain action verb %q; got %q", badAction, reason)
		}
	}
}

// TestSimulator_NoForbiddenVocabularyInOutput — table + JSON output
// of a representative trace must NOT contain any forbidden trade
// vocabulary or bare 9-state name.
func TestSimulator_NoForbiddenVocabularyInOutput(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	c := mkCandidate(now)
	tr := BuildTrace(c, now)
	body, err := json.MarshalIndent([]Trace{tr}, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lower := strings.ToLower(string(body))
	for _, bad := range ForbiddenTradeVocabulary {
		if strings.Contains(lower, strings.ToLower(bad)) {
			t.Errorf("output contains forbidden trade token %q\n%s", bad, body)
		}
	}
	// bare 9-state names must never appear in output.
	for _, bare := range ForbiddenBareLabels {
		// Use whole-string search; the suffixed forms (e.g.
		// MERGE_STRUCTURE_OBSERVABLE, REDEEM_HORIZON_FUTURE) start
		// with the bare token, so we need a word-boundary check.
		// The trick: scan the JSON for `"REDEEM"` exactly.
		quoted := fmt.Sprintf(`"%s"`, bare)
		if strings.Contains(string(body), quoted) {
			t.Errorf("output contains bare 9-state token %q; only the live-mode suffixed form is permitted", bare)
		}
	}
}

// TestSimulator_NoFutureLeakage_StaticCheck — internal/simulator and
// cmd/rn1-live-sim must not import internal/store, internal/ingest,
// internal/summary, or internal/replay.
func TestSimulator_NoFutureLeakage_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "simulator"),
		filepath.Join(root, "cmd", "rn1-live-sim"),
	}
	forbidden := map[string]bool{
		"rn1-strategy-extractor/internal/store":   true,
		"rn1-strategy-extractor/internal/ingest":  true,
		"rn1-strategy-extractor/internal/summary": true,
		"rn1-strategy-extractor/internal/replay":  true,
	}
	for _, dir := range dirs {
		violations := importsViolating(t, dir, forbidden)
		if len(violations) > 0 {
			t.Errorf("dir %s imports forbidden packages: %v", dir, violations)
		}
	}
}

// TestSimulator_NoTradingPath_StaticCheck — neither package may
// contain trading-path tokens in any non-test .go file. labels.go is
// the declaration site of the forbidden-list constants and is
// explicitly excluded from this check by file name; the constants it
// declares are themselves enforced by other tests
// (TestSimulator_NoForbiddenVocabularyInOutput etc.) that grep the
// *output* of the simulator.
func TestSimulator_NoTradingPath_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "simulator"),
		filepath.Join(root, "cmd", "rn1-live-sim"),
	}
	forbiddenSubstrings := []string{
		"signer", "keystore", "private-key", "privatekey",
		"PostOrder", "post_order", "CancelOrder", "cancel_order",
		"PolygonRPC", "PolygonProvider", "/order", "/cancel",
		"\"buy\"", "\"sell\"",
	}
	// Files exempted from this check. labels.go is where the
	// forbidden-list constants are DECLARED — the tokens appear as
	// literal strings inside the slice. Other tests verify those
	// constants are honored at output time.
	exemptFilenames := map[string]bool{
		"labels.go": true,
	}
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			if strings.HasSuffix(p, "_test.go") {
				return nil // tests reference these tokens by name; skip
			}
			if exemptFilenames[filepath.Base(p)] {
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
}

// TestSimulator_LabelsAreSetsNotPointers — labels must arrive as a
// slice and the simulator must not collapse them to a "current"
// single value.
func TestSimulator_LabelsAreSetsNotPointers(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	c := mkCandidate(now)
	tr := BuildTrace(c, now)
	if len(tr.Labels) < 3 {
		t.Errorf("very-strong candidate should fire multiple labels; got %v", tr.Labels)
	}
	// the Trace must not have a "CurrentState" field — check JSON for it.
	body, _ := json.Marshal(tr)
	for _, bad := range []string{`"current_state"`, `"CurrentState"`, `"current_label"`, `"CurrentLabel"`} {
		if strings.Contains(string(body), bad) {
			t.Errorf("Trace exposes %q — labels must remain a set, not a pointer", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func importsViolating(t *testing.T, dir string, forbidden map[string]bool) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, parser.ImportsOnly)
	if err != nil {
		t.Errorf("parse %s: %v", dir, err)
		return nil
	}
	var violations []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if forbidden[path] {
					violations = append(violations, path)
				}
			}
		}
	}
	return violations
}

func hasLabel(labels []Label, want Label) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

func sameLabelSet(a, b []Label) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[Label]int{}
	for _, l := range a {
		seen[l]++
	}
	for _, l := range b {
		seen[l]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

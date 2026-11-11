package shadow

import (
	"crypto/sha256"
	"encoding/hex"
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
	"rn1-strategy-extractor/internal/simulator"
)

// ---------------------------------------------------------------------------
// helpers
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

func mkCandidate(slug string, score float64, gate scanner.LiquidityLabel, marketCount int) scanner.Candidate {
	per := make([]scanner.PerMarketLiquidity, marketCount)
	for i := range per {
		per[i] = scanner.PerMarketLiquidity{
			HasTwoSidedQuotesA: gate == scanner.LiquidityPass || gate == scanner.LiquidityWarn,
			HasTwoSidedQuotesB: gate == scanner.LiquidityPass || gate == scanner.LiquidityWarn,
			SpreadA:            0.02, SpreadB: 0.02,
			TopDepthA: 400, TopDepthB: 400,
		}
	}
	c := scanner.Candidate{
		Rank: 1, EventSlug: slug, Title: "Synthetic " + slug, SportPrefix: "mls",
		Score: score, Band: "very_strong", LiquidityGate: gate,
		LiquidityResult: scanner.LiquidityResult{
			Label: gate, FractionWithQuotes: 1.0, AvgSpread: 0.02, MinTopDepth: 400,
			MarketsConsidered: marketCount, MarketsWithBoth: marketCount, PerMarket: per,
		},
		MarketCount: marketCount, BinaryCount: marketCount,
		Families: []string{"expanded_companion_stack"},
		Active:   true, Closed: false,
		StartTs:   tptr(time.Now().UTC().Add(8 * time.Hour)),
		ScannedAt: time.Now().UTC(),
	}
	return c
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// closed-enum + decision validation
// ---------------------------------------------------------------------------

func TestShadow_ClosedEnumShadowDecision(t *testing.T) {
	want := map[ShadowDecision]bool{
		DecisionDiscovered: true, DecisionWatching: true,
		DecisionNeedsRescan: true, DecisionNeedsLiquidityReview: true,
		DecisionRejected: true, DecisionClosedEvent: true, DecisionArchived: true,
	}
	if len(AllShadowDecisions) != len(want) {
		t.Fatalf("AllShadowDecisions length mismatch: got %d want %d", len(AllShadowDecisions), len(want))
	}
	for _, d := range AllShadowDecisions {
		if !want[d] {
			t.Errorf("unexpected decision %q in AllShadowDecisions", d)
		}
	}
	if IsValidDecision("BUY") || IsValidDecision("ENTER") || IsValidDecision("") {
		t.Error("IsValidDecision must reject non-enum values")
	}
}

// ---------------------------------------------------------------------------
// add / update functional tests
// ---------------------------------------------------------------------------

func TestShadow_AddCreatesRecord(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	rec, err := Add(AddInput{
		LogPath: logPath, EventSlug: "evt-1",
		Decision:     DecisionWatching,
		ReviewerNote: "high score, clean labels",
		Obs:          Observation{Candidate: &c, Now: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if rec.ShadowID != "sh-evt-1" {
		t.Errorf("shadow_id mismatch: %q", rec.ShadowID)
	}
	if rec.ShadowDecision != DecisionWatching {
		t.Errorf("decision mismatch: %q", rec.ShadowDecision)
	}
	if len(rec.SelectorScoreHistory) != 1 || rec.SelectorScoreHistory[0] != 12.5 {
		t.Errorf("history not initialized: %v", rec.SelectorScoreHistory)
	}

	records, err := ReadLog(logPath)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record in log, got %d", len(records))
	}
}

func TestShadow_AddRejectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	if _, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &c}}); err != nil {
		t.Fatal(err)
	}
	_, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &c}})
	if err == nil {
		t.Error("expected duplicate add to fail")
	}
}

func TestShadow_AddRejectsInvalidDecision(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	_, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: "BUY", Obs: Observation{Candidate: &c}})
	if err == nil {
		t.Error("expected --decision=BUY to be rejected")
	}
}

func TestShadow_UpdateExtendsHistory(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	ca := mkCandidate("evt-1", 12.5, scanner.LiquidityWarn, 9)
	cb := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	_, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &ca}})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := Update(UpdateInput{LogPath: logPath, EventSlug: "evt-1", Obs: Observation{Candidate: &cb, DiffClassesSince: []string{"BECAME_PASS"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.LiquidityGateHistory) != 2 || rec.LiquidityGateHistory[0] != "WARN" || rec.LiquidityGateHistory[1] != "PASS" {
		t.Errorf("history not extended: %v", rec.LiquidityGateHistory)
	}
	if rec.ShadowDecision != DecisionWatching {
		t.Errorf("decision auto-changed (must be manual): %q", rec.ShadowDecision)
	}
}

func TestShadow_UpdateRequiresExistingAdd(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-x", 12.5, scanner.LiquidityPass, 9)
	_, err := Update(UpdateInput{LogPath: logPath, EventSlug: "evt-x", Obs: Observation{Candidate: &c}})
	if err == nil {
		t.Error("expected update on missing event_slug to fail")
	}
}

func TestShadow_NoAutoStateTransitionExceptClosedEvent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	// Add as WATCHING with PASS. Then update with FAIL gate.
	// Decision MUST remain WATCHING (no auto-REJECTED).
	ca := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	cb := mkCandidate("evt-1", 12.5, scanner.LiquidityFail, 9)
	_, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &ca}})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := Update(UpdateInput{LogPath: logPath, EventSlug: "evt-1", Obs: Observation{Candidate: &cb}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ShadowDecision != DecisionWatching {
		t.Errorf("FAIL gate must NOT auto-promote to REJECTED; got %q", rec.ShadowDecision)
	}
}

func TestShadow_ClosedEventAutoTransition(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	ca := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	_, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &ca}})
	if err != nil {
		t.Fatal(err)
	}
	cb := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	cb.Closed = true
	rec, err := Update(UpdateInput{LogPath: logPath, EventSlug: "evt-1", Obs: Observation{Candidate: &cb}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ShadowDecision != DecisionClosedEvent {
		t.Errorf("Closed=true should auto-promote to CLOSED_EVENT; got %q", rec.ShadowDecision)
	}
}

func TestShadow_ClosedEventAutoTransitionByPastStart(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	ca := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	_, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &ca}})
	if err != nil {
		t.Fatal(err)
	}
	cb := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	past := time.Now().UTC().Add(-1 * time.Hour)
	cb.StartTs = &past
	rec, err := Update(UpdateInput{LogPath: logPath, EventSlug: "evt-1", Obs: Observation{Candidate: &cb, Now: time.Now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ShadowDecision != DecisionClosedEvent {
		t.Errorf("past StartTs should auto-promote to CLOSED_EVENT; got %q", rec.ShadowDecision)
	}
}

// ---------------------------------------------------------------------------
// append-only invariant + report read-only invariant
// ---------------------------------------------------------------------------

func TestShadow_AppendOnlyInvariant(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	ca := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	if _, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &ca}}); err != nil {
		t.Fatal(err)
	}
	// Snapshot the file body, then append several updates, then
	// verify the original bytes are unchanged.
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cb := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	for i := 0; i < 3; i++ {
		if _, err := Update(UpdateInput{LogPath: logPath, EventSlug: "evt-1", Obs: Observation{Candidate: &cb}}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) < len(before) {
		t.Errorf("log shrank after updates: before=%d after=%d", len(before), len(after))
	}
	if string(after[:len(before)]) != string(before) {
		t.Error("first N bytes of log changed across updates; append-only invariant violated")
	}
}

func TestShadow_ReportDoesNotMutateLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	if _, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &c}}); err != nil {
		t.Fatal(err)
	}
	sumBefore := fileSHA256(t, logPath)
	mtimeBefore, _ := os.Stat(logPath)
	records, err := ReadLog(logPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = Report(records, ReportFilter{})
	sumAfter := fileSHA256(t, logPath)
	mtimeAfter, _ := os.Stat(logPath)
	if sumBefore != sumAfter {
		t.Error("report path mutated the log content")
	}
	if !mtimeAfter.ModTime().Equal(mtimeBefore.ModTime()) {
		t.Error("report path mutated the log mtime")
	}
}

// ---------------------------------------------------------------------------
// malformed input handling
// ---------------------------------------------------------------------------

func TestShadow_MalformedJSONLRejected(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(bad, []byte("{not valid json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLog(bad); err == nil {
		t.Error("expected malformed JSONL to be rejected")
	}
}

func TestShadow_MissingScanCandidateError(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	_, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{}})
	if err == nil {
		t.Error("expected error when scan candidate missing")
	}
}

func TestShadow_OptionalSimTraceGraceful(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	rec, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &c}})
	if err != nil {
		t.Fatalf("Add without sim trace should not error: %v", err)
	}
	if len(rec.Labels) != 0 {
		t.Errorf("Labels should be empty without sim trace; got %v", rec.Labels)
	}
}

func TestShadow_WithSimTraceLabels(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	tr := simulator.Trace{
		EventSlug: "evt-1",
		Labels: []simulator.Label{
			simulator.LabelScan, simulator.LabelProbeEligible,
			simulator.LabelDualSideObservableA, simulator.LabelRedeemHorizonFuture,
		},
	}
	rec, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &c, Trace: &tr}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Labels) != 4 {
		t.Errorf("expected 4 labels; got %v", rec.Labels)
	}
}

// ---------------------------------------------------------------------------
// static safety checks
// ---------------------------------------------------------------------------

func TestShadow_NoFutureLeakage_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "shadow"),
		filepath.Join(root, "cmd", "rn1-shadow"),
	}
	forbidden := map[string]bool{
		"rn1-strategy-extractor/internal/store":   true,
		"rn1-strategy-extractor/internal/ingest":  true,
		"rn1-strategy-extractor/internal/summary": true,
		"rn1-strategy-extractor/internal/replay":  true,
	}
	for _, dir := range dirs {
		v := importsViolating(t, dir, forbidden)
		if len(v) > 0 {
			t.Errorf("%s imports forbidden packages: %v", dir, v)
		}
	}
}

func TestShadow_NoNetworkImport_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "shadow"),
		filepath.Join(root, "cmd", "rn1-shadow"),
	}
	forbidden := map[string]bool{
		"net/http":                     true,
		"net":                          true,
		"net/url":                      true,
		"golang.org/x/net/websocket":   true,
		"github.com/gorilla/websocket": true,
		"google.golang.org/grpc":       true,
	}
	for _, dir := range dirs {
		v := importsViolating(t, dir, forbidden)
		if len(v) > 0 {
			t.Errorf("%s imports forbidden network packages: %v", dir, v)
		}
	}
}

func TestShadow_NoTradingPath_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "shadow"),
		filepath.Join(root, "cmd", "rn1-shadow"),
	}
	forbiddenSubstrings := []string{
		"signer", "keystore", "private-key", "privatekey",
		"PostOrder", "post_order", "CancelOrder", "cancel_order",
		"PolygonRPC", "PolygonProvider", "/order", "/cancel",
		"\"buy\"", "\"sell\"",
	}
	exemptFilenames := map[string]bool{
		// types.go + main.go carry the package-doc disclaimer that
		// literally names the forbidden tokens.
		"types.go": true,
		"main.go":  true,
	}
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			if strings.HasSuffix(p, "_test.go") {
				return nil
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

func TestShadow_NoForbiddenSchemaFields(t *testing.T) {
	// The schema is enforced by marshaling a representative
	// ShadowRecord and grepping the JSON for forbidden field
	// names. Note that we check for the *JSON* key names (lower
	// snake), not Go field names.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	rec, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, ReviewerNote: "synthetic test", Obs: Observation{Candidate: &c}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(&rec)
	lower := strings.ToLower(string(body))
	forbidden := []string{
		`"stake"`, `"position_size"`, `"units"`, `"lots"`,
		`"wallet"`, `"wallet_address"`, `"wallet_balance"`,
		`"private_key"`, `"keystore_path"`, `"signer"`,
		`"order_id"`, `"execution_id"`, `"submit_order"`, `"place_order"`,
		`"expected_profit"`, `"expected_return"`, `"expected_pnl"`,
		`"realized_pnl"`, `"unrealized_pnl"`,
		`"wavg"`, `"avg_fill_price"`,
		`"arbitrage"`, `"arb_opportunity"`,
		`"entry_price"`, `"exit_price"`, `"take_profit"`, `"stop_loss"`,
	}
	for _, f := range forbidden {
		if strings.Contains(lower, strings.ToLower(f)) {
			t.Errorf("ShadowRecord JSON contains forbidden field %q\n%s", f, string(body))
		}
	}
}

func TestShadow_NoForbiddenVocabularyInOutput(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-1", 12.5, scanner.LiquidityUnknown, 9)
	tr := simulator.Trace{
		EventSlug: "evt-1",
		Labels:    []simulator.Label{simulator.LabelScan, simulator.LabelRedeemHorizonFuture},
	}
	_, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, ReviewerNote: "synthetic", Obs: Observation{Candidate: &c, Trace: &tr}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(logPath)
	// Also include the report output in the check.
	records, _ := ReadLog(logPath)
	summaries := Report(records, ReportFilter{})
	summaryBody, _ := json.MarshalIndent(summaries, "", "  ")
	combined := strings.ToLower(string(body) + "\n" + string(summaryBody))

	forbidden := []string{
		"buy", "sell", "enter long", "enter short", "exit long", "exit short",
		"stake", "position size", "expected profit", "expected return",
		"expected pnl", "arbitrage", "place order", "submit order",
		"take profit", "stop loss",
	}
	for _, f := range forbidden {
		if strings.Contains(combined, f) {
			t.Errorf("shadow output contains forbidden token %q", f)
		}
	}
}

func TestShadow_NoBareNineStateInOutput(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	tr := simulator.Trace{
		EventSlug: "evt-1",
		Labels: []simulator.Label{
			simulator.LabelScan, simulator.LabelProbeEligible,
			simulator.LabelDualSideObservableA, simulator.LabelDualSideObservableB,
			simulator.LabelExpandReady, simulator.LabelLayerStructureObservable,
			simulator.LabelMergeStructureObservable, simulator.LabelRedeemHorizonFuture,
		},
	}
	_, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &c, Trace: &tr}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(logPath)
	for _, bare := range []string{`"ACCUMULATE_A"`, `"OPEN_B"`, `"MERGE_HARVEST"`, `"REDEEM"`} {
		if strings.Contains(string(body), bare) {
			t.Errorf("shadow output contains bare 9-state token %q", bare)
		}
	}
}

func TestShadow_NoForbiddenCLIFlags(t *testing.T) {
	root := findRepoRoot(t)
	main := filepath.Join(root, "cmd", "rn1-shadow", "main.go")
	body, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	// Forbidden CLI flags as defined in the design report. Each
	// must NOT appear as a flag declaration in main.go (i.e. not
	// as `"flag-name"` inside a fs.StringVar / BoolVar call).
	// We check both `--name` and the inner-quoted form.
	forbidden := []string{
		"--wallet", `"wallet"`,
		"--signer", `"signer"`,
		"--private-key", `"private-key"`,
		"--keystore", `"keystore"`,
		"--endpoint", `"endpoint"`,
		"--rpc", `"rpc"`,
		"--api-key", `"api-key"`,
		"--auth-token", `"auth-token"`,
		"--stake", `"stake"`,
		"--position-size", `"position-size"`,
		"--units", `"units"`,
		"--lots", `"lots"`,
		"--buy", `"buy"`,
		"--sell", `"sell"`,
		"--side", `"side"`,
		"--enter", `"enter"`,
		"--exit", `"exit"`,
		"--place-order", `"place-order"`,
		"--submit-order", `"submit-order"`,
		"--cancel", `"cancel"`,
		"--profit-target", `"profit-target"`,
		"--stop-loss", `"stop-loss"`,
		"--take-profit", `"take-profit"`,
		"--arbitrage", `"arbitrage"`,
		"--strategy", `"strategy"`,
		"--auto", `"auto"`,
	}
	src := string(body)
	for _, f := range forbidden {
		if strings.Contains(src, f) {
			t.Errorf("cmd/rn1-shadow/main.go contains forbidden CLI flag %q", f)
		}
	}
}

// Sanity: WATCHING / REJECTED must not appear as position-state
// in the JSON output (i.e. there is no "is_position" / "has_position"
// / "open_position" field). Decisions are observation states.
func TestShadow_DecisionsAreNotPositionState(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	c := mkCandidate("evt-1", 12.5, scanner.LiquidityPass, 9)
	if _, err := Add(AddInput{LogPath: logPath, EventSlug: "evt-1", Decision: DecisionWatching, Obs: Observation{Candidate: &c}}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(logPath)
	for _, bad := range []string{
		`"has_position"`, `"is_position"`, `"open_position"`,
		`"closed_position"`, `"position_status"`, `"position_state"`,
	} {
		if strings.Contains(string(body), bad) {
			t.Errorf("output contains position-state field %q (decisions must remain observation labels)", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

func importsViolating(t *testing.T, dir string, forbidden map[string]bool) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, parser.ImportsOnly)
	if err != nil {
		t.Errorf("parse %s: %v", dir, err)
		return nil
	}
	var v []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if forbidden[path] {
					v = append(v, path)
				}
			}
		}
	}
	return v
}

// silence import warnings if needed
var _ = fmt.Sprintf

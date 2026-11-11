package diff

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
	"rn1-strategy-extractor/internal/simulator"
)

// ---------------------------------------------------------------------------
// fixture helpers
// ---------------------------------------------------------------------------

func tptr(t time.Time) *time.Time { return &t }

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

// mkCandidate builds a baseline very-strong soccer-companion
// candidate. Callers tweak fields to produce specific scenarios.
func mkCandidate(slug, sport string, score float64, gate scanner.LiquidityLabel, marketCount int) scanner.Candidate {
	per := make([]scanner.PerMarketLiquidity, marketCount)
	for i := range per {
		per[i] = scanner.PerMarketLiquidity{
			ConditionID:        fmt.Sprintf("0xcond%02d-%s", i, slug),
			HasTwoSidedQuotesA: gate == scanner.LiquidityPass || gate == scanner.LiquidityWarn,
			HasTwoSidedQuotesB: gate == scanner.LiquidityPass || gate == scanner.LiquidityWarn,
			SpreadA:            0.02,
			SpreadB:            0.02,
			TopDepthA:          400,
			TopDepthB:          400,
		}
	}
	c := scanner.Candidate{
		Rank:          1,
		EventSlug:     slug,
		EventID:       "ev-" + slug,
		Title:         "Synthetic " + slug,
		SportPrefix:   sport,
		Score:         score,
		Band:          "very_strong",
		LiquidityGate: gate,
		LiquidityResult: scanner.LiquidityResult{
			Label:              gate,
			FractionWithQuotes: 1.0,
			AvgSpread:          0.02,
			MinTopDepth:        400,
			MarketsConsidered:  marketCount,
			MarketsWithBoth:    marketCount,
			PerMarket:          per,
		},
		MarketCount: marketCount,
		BinaryCount: marketCount,
		Families:    []string{"expanded_companion_stack"},
		Active:      true,
		Closed:      false,
		ScannedAt:   time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC),
	}
	if gate == scanner.LiquidityWarn {
		c.LiquidityResult.AvgSpread = 0.09
	}
	if gate == scanner.LiquidityFail {
		c.LiquidityResult.AvgSpread = 0.35
		c.LiquidityResult.MinTopDepth = 150
		c.RiskFlags = []scanner.RiskFlag{scanner.RiskLowLiquidity, scanner.RiskHumanReviewRequired}
	}
	if gate == scanner.LiquidityUnknown {
		for i := range per {
			per[i].HasTwoSidedQuotesA = false
			per[i].HasTwoSidedQuotesB = false
			per[i].SpreadA = 0
			per[i].SpreadB = 0
			per[i].TopDepthA = 0
			per[i].TopDepthB = 0
			per[i].BookErrorA = "book-budget-exhausted"
			per[i].BookErrorB = "book-budget-exhausted"
		}
		c.LiquidityResult.PerMarket = per
		c.LiquidityResult.FractionWithQuotes = 0
		c.LiquidityResult.AvgSpread = 0
		c.LiquidityResult.MinTopDepth = 0
		c.LiquidityResult.MarketsWithBoth = 0
		c.LiquidityResult.BookFetchErrors = marketCount
		c.RiskFlags = []scanner.RiskFlag{scanner.RiskNoLiveQuotes, scanner.RiskHumanReviewRequired}
	}
	return c
}

func mkSnapshot(label string, scannedAt time.Time, cands ...scanner.Candidate) *Snapshot {
	for i := range cands {
		cands[i].ScannedAt = scannedAt
	}
	return &Snapshot{
		Meta:       SnapshotMeta{Label: label, ScannedAt: scannedAt},
		Candidates: cands,
	}
}

var (
	tsA = time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC)
	tsB = time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
)

// ---------------------------------------------------------------------------
// J.1 fixture-driven diff class tests
// ---------------------------------------------------------------------------

func TestDiff_NewCandidate(t *testing.T) {
	a := mkSnapshot("A", tsA, mkCandidate("evt-1", "mls", 10.5, scanner.LiquidityPass, 9))
	b := mkSnapshot("B", tsB,
		mkCandidate("evt-1", "mls", 10.5, scanner.LiquidityPass, 9),
		mkCandidate("evt-new", "lal", 12.5, scanner.LiquidityPass, 11),
	)
	got, err := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	rec := findRecord(t, got.Records, "evt-new")
	if !hasClass(rec, ClassNewCandidate) {
		t.Errorf("expected NEW_CANDIDATE; got %v", rec.DiffClasses)
	}
	if rec.ReviewNote != NoteNewlyAppeared {
		t.Errorf("expected NewlyAppeared note; got %q", rec.ReviewNote)
	}
}

func TestDiff_DroppedCandidate(t *testing.T) {
	a := mkSnapshot("A", tsA,
		mkCandidate("evt-1", "mls", 10.5, scanner.LiquidityPass, 9),
		mkCandidate("evt-drop", "kor", 10.5, scanner.LiquidityPass, 9),
	)
	b := mkSnapshot("B", tsB, mkCandidate("evt-1", "mls", 10.5, scanner.LiquidityPass, 9))
	got, err := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	rec := findRecord(t, got.Records, "evt-drop")
	if !hasClass(rec, ClassDroppedCandidate) {
		t.Errorf("expected DROPPED_CANDIDATE; got %v", rec.DiffClasses)
	}
	if rec.ReviewNote != NoteDropped {
		t.Errorf("expected Dropped note; got %q", rec.ReviewNote)
	}
}

func TestDiff_UnknownToPass(t *testing.T) {
	ca := mkCandidate("evt-x", "lal", 12.5, scanner.LiquidityUnknown, 11)
	cb := mkCandidate("evt-x", "lal", 12.5, scanner.LiquidityPass, 11)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, err := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	rec := findRecord(t, got.Records, "evt-x")
	for _, want := range []DiffClass{
		ClassUnknownResolved, ClassGateImproved, ClassBecamePass,
		ClassBudgetExhaustedResolved, ClassQuoteCoverageImproved,
		ClassDepthImproved, ClassRiskFlagsRemoved,
	} {
		if !hasClass(rec, want) {
			t.Errorf("expected %s in classes; got %v", want, rec.DiffClasses)
		}
	}
	if hasClass(rec, ClassBecameFail) {
		t.Errorf("BECAME_FAIL must NOT fire when gate moved to PASS")
	}
	if rec.ReviewNote != NoteBudgetResolvedToPass {
		t.Errorf("expected BudgetResolvedToPass note; got %q", rec.ReviewNote)
	}
}

func TestDiff_UnknownBudgetExhaustedToFail(t *testing.T) {
	ca := mkCandidate("evt-bo3", "lol", 10.5, scanner.LiquidityUnknown, 20)
	cb := mkCandidate("evt-bo3", "lol", 10.5, scanner.LiquidityFail, 20)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, err := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	rec := findRecord(t, got.Records, "evt-bo3")
	for _, want := range []DiffClass{
		ClassUnknownResolved, ClassGateDegraded, ClassBecameFail,
		ClassBudgetExhaustedResolved,
	} {
		if !hasClass(rec, want) {
			t.Errorf("expected %s; got %v", want, rec.DiffClasses)
		}
	}
	if rec.ReviewNote != NoteBudgetResolvedToFail {
		t.Errorf("expected BudgetResolvedToFail note; got %q", rec.ReviewNote)
	}
}

func TestDiff_PassToWarn(t *testing.T) {
	ca := mkCandidate("evt-y", "mls", 10.5, scanner.LiquidityPass, 9)
	cb := mkCandidate("evt-y", "mls", 10.5, scanner.LiquidityWarn, 9)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	rec := findRecord(t, got.Records, "evt-y")
	if !hasClass(rec, ClassBecameWarn) || !hasClass(rec, ClassGateDegraded) {
		t.Errorf("expected BECAME_WARN+GATE_DEGRADED; got %v", rec.DiffClasses)
	}
	if hasClass(rec, ClassBecamePass) || hasClass(rec, ClassBecameFail) {
		t.Errorf("must not fire BECAME_PASS or BECAME_FAIL: %v", rec.DiffClasses)
	}
}

func TestDiff_WarnToPass(t *testing.T) {
	ca := mkCandidate("evt-z", "mls", 10.5, scanner.LiquidityWarn, 9)
	cb := mkCandidate("evt-z", "mls", 10.5, scanner.LiquidityPass, 9)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	rec := findRecord(t, got.Records, "evt-z")
	if !hasClass(rec, ClassBecamePass) || !hasClass(rec, ClassGateImproved) {
		t.Errorf("expected BECAME_PASS+GATE_IMPROVED; got %v", rec.DiffClasses)
	}
}

func TestDiff_FailStaysFail_NoMaterialChange(t *testing.T) {
	ca := mkCandidate("evt-fail", "lol", 10.5, scanner.LiquidityFail, 20)
	cb := mkCandidate("evt-fail", "lol", 10.5, scanner.LiquidityFail, 20)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	rec := findRecord(t, got.Records, "evt-fail")
	if !hasClass(rec, ClassHumanReviewStillReq) && !hasClass(rec, ClassNoMaterialChange) {
		t.Errorf("expected HUMAN_REVIEW_STILL_REQUIRED or NO_MATERIAL_CHANGE; got %v", rec.DiffClasses)
	}
	if hasClass(rec, ClassBecamePass) || hasClass(rec, ClassUnknownResolved) {
		t.Errorf("must not fire success classes: %v", rec.DiffClasses)
	}
}

func TestDiff_LabelsAdded(t *testing.T) {
	ca := mkCandidate("evt-l", "mls", 10.5, scanner.LiquidityPass, 9)
	cb := mkCandidate("evt-l", "mls", 10.5, scanner.LiquidityPass, 9)
	a := &Snapshot{Meta: SnapshotMeta{Label: "A", ScannedAt: tsA}, Candidates: []scanner.Candidate{ca},
		Traces: []simulator.Trace{{EventSlug: "evt-l", Labels: []simulator.Label{simulator.LabelScan, simulator.LabelRedeemHorizonFuture}}}}
	b := &Snapshot{Meta: SnapshotMeta{Label: "B", ScannedAt: tsB}, Candidates: []scanner.Candidate{cb},
		Traces: []simulator.Trace{{EventSlug: "evt-l", Labels: []simulator.Label{simulator.LabelScan, simulator.LabelProbeEligible, simulator.LabelDualSideObservableA, simulator.LabelRedeemHorizonFuture}}}}
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	rec := findRecord(t, got.Records, "evt-l")
	if !hasClass(rec, ClassLabelsAdded) || !hasClass(rec, ClassLabelSetGrew) {
		t.Errorf("expected LABELS_ADDED + LABEL_SET_GREW; got %v", rec.DiffClasses)
	}
	if !containsAny(rec.LabelsAdded, "PROBE_ELIGIBLE") {
		t.Errorf("expected LabelsAdded to contain PROBE_ELIGIBLE; got %v", rec.LabelsAdded)
	}
}

func TestDiff_LabelsRemoved(t *testing.T) {
	ca := mkCandidate("evt-r", "mls", 10.5, scanner.LiquidityPass, 9)
	cb := mkCandidate("evt-r", "mls", 10.5, scanner.LiquidityUnknown, 9)
	a := &Snapshot{Meta: SnapshotMeta{Label: "A", ScannedAt: tsA}, Candidates: []scanner.Candidate{ca},
		Traces: []simulator.Trace{{EventSlug: "evt-r", Labels: []simulator.Label{simulator.LabelScan, simulator.LabelProbeEligible, simulator.LabelDualSideObservableA, simulator.LabelRedeemHorizonFuture}}}}
	b := &Snapshot{Meta: SnapshotMeta{Label: "B", ScannedAt: tsB}, Candidates: []scanner.Candidate{cb},
		Traces: []simulator.Trace{{EventSlug: "evt-r", Labels: []simulator.Label{simulator.LabelScan, simulator.LabelRedeemHorizonFuture}}}}
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	rec := findRecord(t, got.Records, "evt-r")
	if !hasClass(rec, ClassLabelsRemoved) || !hasClass(rec, ClassLabelSetShrank) {
		t.Errorf("expected LABELS_REMOVED + LABEL_SET_SHRANK; got %v", rec.DiffClasses)
	}
}

func TestDiff_RiskFlagsAddedRemoved(t *testing.T) {
	ca := mkCandidate("evt-rf", "mls", 10.5, scanner.LiquidityPass, 9)
	cb := mkCandidate("evt-rf", "mls", 10.5, scanner.LiquidityPass, 9)
	cb.RiskFlags = []scanner.RiskFlag{scanner.RiskWideSpread, scanner.RiskHumanReviewRequired}
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	rec := findRecord(t, got.Records, "evt-rf")
	if !hasClass(rec, ClassRiskFlagsAdded) {
		t.Errorf("expected RISK_FLAGS_ADDED; got %v", rec.DiffClasses)
	}
	if !containsAny(rec.RiskFlagsAdded, "WIDE_SPREAD") {
		t.Errorf("expected WIDE_SPREAD in added; got %v", rec.RiskFlagsAdded)
	}
}

// Score branch coverage. The 3-day server dry-run (docs/rn1-3day-dryrun-analysis-2026-05-15.md
// §9.3 Recommendation 2) observed 0 SCORE_DEGRADED occurrences across 893
// diff records over 48h, because in practice scores only ever increased
// (events approached the 2-day "near-term" boundary; market lists were
// stable). The SCORE_DEGRADED branch in classify.go therefore had no
// integration-test exercise. These two tests give the branch explicit
// fixture coverage so a future regression that swaps > / < in the
// classifier is caught by `go test`.
func TestDiff_ScoreImproved(t *testing.T) {
	ca := mkCandidate("evt-si", "mls", 9.5, scanner.LiquidityPass, 9)
	cb := mkCandidate("evt-si", "mls", 10.5, scanner.LiquidityPass, 9)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	rec := findRecord(t, got.Records, "evt-si")
	if !hasClass(rec, ClassScoreChanged) || !hasClass(rec, ClassScoreImproved) {
		t.Errorf("expected SCORE_CHANGED + SCORE_IMPROVED; got %v", rec.DiffClasses)
	}
	if hasClass(rec, ClassScoreDegraded) {
		t.Errorf("must not fire SCORE_DEGRADED when score went up: %v", rec.DiffClasses)
	}
}

func TestDiff_ScoreDegraded(t *testing.T) {
	// Realistic shape: an event loses a market (binary count 6 -> 5) so
	// its score drops from 10.5 to 9.5. The classifier must emit
	// SCORE_CHANGED + SCORE_DEGRADED and must NOT emit SCORE_IMPROVED.
	ca := mkCandidate("evt-sd", "mls", 10.5, scanner.LiquidityPass, 9)
	cb := mkCandidate("evt-sd", "mls", 9.5, scanner.LiquidityPass, 9)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	rec := findRecord(t, got.Records, "evt-sd")
	if !hasClass(rec, ClassScoreChanged) || !hasClass(rec, ClassScoreDegraded) {
		t.Errorf("expected SCORE_CHANGED + SCORE_DEGRADED; got %v", rec.DiffClasses)
	}
	if hasClass(rec, ClassScoreImproved) {
		t.Errorf("must not fire SCORE_IMPROVED when score went down: %v", rec.DiffClasses)
	}
}

func TestDiff_TitleChanged(t *testing.T) {
	ca := mkCandidate("evt-t", "mls", 10.5, scanner.LiquidityPass, 9)
	cb := mkCandidate("evt-t", "mls", 10.5, scanner.LiquidityPass, 9)
	cb.Title = "Updated Title"
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	rec := findRecord(t, got.Records, "evt-t")
	if !hasClass(rec, ClassTitleChanged) {
		t.Errorf("expected TITLE_CHANGED; got %v", rec.DiffClasses)
	}
}

func TestDiff_BudgetExhaustedResolvedSeparately(t *testing.T) {
	// Pure budget delta with same gate (UNKNOWN → UNKNOWN), no other change.
	// In practice this is unusual; check the class still fires.
	ca := mkCandidate("evt-b", "lol", 10.5, scanner.LiquidityUnknown, 20)
	cb := mkCandidate("evt-b", "lol", 10.5, scanner.LiquidityUnknown, 20)
	// Strip B's budget-exhausted markers.
	for i := range cb.LiquidityResult.PerMarket {
		cb.LiquidityResult.PerMarket[i].BookErrorA = ""
		cb.LiquidityResult.PerMarket[i].BookErrorB = ""
	}
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	rec := findRecord(t, got.Records, "evt-b")
	if !hasClass(rec, ClassBudgetExhaustedResolved) {
		t.Errorf("expected BUDGET_EXHAUSTED_RESOLVED; got %v", rec.DiffClasses)
	}
}

func TestDiff_NoMaterialChange_SuppressedByDefault(t *testing.T) {
	ca := mkCandidate("evt-stable", "mls", 10.5, scanner.LiquidityPass, 9)
	cb := mkCandidate("evt-stable", "mls", 10.5, scanner.LiquidityPass, 9)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})

	// Default filter suppresses NO_MATERIAL_CHANGE.
	defaultFilter := DefaultFilterOptions()
	visible := defaultFilter.Apply(got.Records)
	for _, r := range visible {
		if r.EventSlug == "evt-stable" {
			t.Errorf("NO_MATERIAL_CHANGE row should be suppressed; got %v", r.DiffClasses)
		}
	}

	// IncludeQuiet=true surfaces it.
	loudFilter := defaultFilter
	loudFilter.IncludeQuiet = true
	visibleQuiet := loudFilter.Apply(got.Records)
	found := false
	for _, r := range visibleQuiet {
		if r.EventSlug == "evt-stable" {
			found = true
			if r.DiffClasses[0] != ClassNoMaterialChange {
				t.Errorf("expected NO_MATERIAL_CHANGE; got %v", r.DiffClasses)
			}
		}
	}
	if !found {
		t.Error("evt-stable should appear with --include-quiet")
	}
}

// ---------------------------------------------------------------------------
// Robustness tests
// ---------------------------------------------------------------------------

func TestDiff_MissingSimTraceGraceful(t *testing.T) {
	ca := mkCandidate("evt-1", "mls", 10.5, scanner.LiquidityPass, 9)
	cb := mkCandidate("evt-1", "mls", 10.5, scanner.LiquidityPass, 9)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, err := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	if err != nil {
		t.Fatalf("missing sim traces should not error: %v", err)
	}
	rec := findRecord(t, got.Records, "evt-1")
	// No LABELS_* classes should fire when both traces missing.
	if hasClass(rec, ClassLabelsAdded) || hasClass(rec, ClassLabelsRemoved) {
		t.Errorf("LABELS_* must not fire without traces; got %v", rec.DiffClasses)
	}
}

func TestDiff_ReversedTimestampsRejected(t *testing.T) {
	ca := mkCandidate("evt-1", "mls", 10.5, scanner.LiquidityPass, 9)
	a := mkSnapshot("A", tsB, ca) // ts B
	b := mkSnapshot("B", tsA, ca) // ts A (earlier than A)
	_, err := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	if err == nil {
		t.Error("expected error for reversed timestamps; got nil")
	}
}

func TestDiff_MalformedScannerInputRejected(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not valid json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSnapshot(bad, "", "A")
	if err == nil {
		t.Error("expected error for malformed scanner JSON; got nil")
	}
}

func TestDiff_DuplicateSlugWarning(t *testing.T) {
	ca1 := mkCandidate("evt-dup", "mls", 10.5, scanner.LiquidityPass, 9)
	ca2 := mkCandidate("evt-dup", "mls", 10.5, scanner.LiquidityPass, 9)
	a := mkSnapshot("A", tsA, ca1, ca2)
	b := mkSnapshot("B", tsB, mkCandidate("evt-dup", "mls", 10.5, scanner.LiquidityPass, 9))
	got, err := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	if err != nil {
		t.Fatalf("dup should warn, not error: %v", err)
	}
	if len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0], "DUPLICATE_SLUG") {
		t.Errorf("expected DUPLICATE_SLUG warning; got %v", got.Warnings)
	}
}

func TestDiff_BaseAndMoreMarketsRemainSeparate(t *testing.T) {
	base := mkCandidate("kor-gwa-seo-2026-05-13", "kor", 6.0, scanner.LiquidityPass, 3)
	companion := mkCandidate("kor-gwa-seo-2026-05-13-more-markets", "kor", 10.5, scanner.LiquidityPass, 9)
	a := mkSnapshot("A", tsA, base)
	b := mkSnapshot("B", tsB, companion)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	// They must NOT be merged. Base drops; companion is new.
	if r := findRecord(t, got.Records, "kor-gwa-seo-2026-05-13"); !hasClass(r, ClassDroppedCandidate) {
		t.Errorf("base event should DROP, not merge: %v", r.DiffClasses)
	}
	if r := findRecord(t, got.Records, "kor-gwa-seo-2026-05-13-more-markets"); !hasClass(r, ClassNewCandidate) {
		t.Errorf("companion event should be NEW: %v", r.DiffClasses)
	}
}

func TestDiff_StaleSnapshotWarning(t *testing.T) {
	ca := mkCandidate("evt-1", "mls", 10.5, scanner.LiquidityPass, 9)
	a := mkSnapshot("A", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), ca)
	b := mkSnapshot("B", time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC), ca)
	got, err := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{MaxAge: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("stale should warn, not error: %v", err)
	}
	if len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0], "STALE_SNAPSHOT") {
		t.Errorf("expected STALE_SNAPSHOT warning; got %v", got.Warnings)
	}
}

// ---------------------------------------------------------------------------
// Static safety checks
// ---------------------------------------------------------------------------

func TestDiff_NoFutureLeakage_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "diff"),
		filepath.Join(root, "cmd", "rn1-live-diff"),
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

func TestDiff_NoNetworkImport_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "diff"),
		filepath.Join(root, "cmd", "rn1-live-diff"),
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

func TestDiff_NoTradingPath_StaticCheck(t *testing.T) {
	root := findRepoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "diff"),
		filepath.Join(root, "cmd", "rn1-live-diff"),
	}
	forbiddenSubstrings := []string{
		"signer", "keystore", "private-key", "privatekey",
		"PostOrder", "post_order", "CancelOrder", "cancel_order",
		"PolygonRPC", "PolygonProvider", "/order", "/cancel",
		"\"buy\"", "\"sell\"",
	}
	// Files exempted from this check. types.go (in internal/diff)
	// and main.go (in cmd/rn1-live-diff) carry the package-level
	// safety disclaimer in their doc comments — those literally
	// name the forbidden tokens as part of the "this tool does
	// NOT use X" statement. Output safety is enforced separately
	// by TestDiff_NoForbiddenVocabularyInOutput and
	// TestDiff_NoBareNineStateInOutput.
	exemptFilenames := map[string]bool{
		"types.go": true,
		"main.go":  true,
	}
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			if strings.HasSuffix(p, "_test.go") {
				return nil // tests reference these tokens by name
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

func TestDiff_NoForbiddenVocabularyInOutput(t *testing.T) {
	ca := mkCandidate("evt-x", "lal", 12.5, scanner.LiquidityUnknown, 11)
	cb := mkCandidate("evt-x", "lal", 12.5, scanner.LiquidityPass, 11)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	body, _ := json.MarshalIndent(got.Records, "", "  ")
	lower := strings.ToLower(string(body))
	forbidden := []string{
		"buy", "sell", "enter long", "enter short", "exit long", "exit short",
		"stake", "position size", "expected profit", "expected return",
		"expected pnl", "arbitrage", "place order", "submit order",
		"take profit", "stop loss",
	}
	for _, bad := range forbidden {
		if strings.Contains(lower, bad) {
			t.Errorf("output contains forbidden token %q\n%s", bad, body)
		}
	}
}

func TestDiff_NoBareNineStateInOutput(t *testing.T) {
	ca := mkCandidate("evt-x", "lal", 12.5, scanner.LiquidityUnknown, 11)
	cb := mkCandidate("evt-x", "lal", 12.5, scanner.LiquidityPass, 11)
	a := mkSnapshot("A", tsA, ca)
	b := mkSnapshot("B", tsB, cb)
	got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
	body, _ := json.Marshal(got.Records)
	for _, bare := range []string{`"ACCUMULATE_A"`, `"OPEN_B"`, `"MERGE_HARVEST"`, `"REDEEM"`} {
		if strings.Contains(string(body), bare) {
			t.Errorf("output contains bare 9-state token %q", bare)
		}
	}
}

func TestDiff_ReviewNoteIsTemplated(t *testing.T) {
	allowed := map[string]bool{}
	for _, n := range AllReviewNotes {
		allowed[n] = true
	}
	// Exercise many combinations through synthetic candidates.
	scenarios := []struct {
		name string
		a, b scanner.Candidate
	}{
		{"unknown_to_pass",
			mkCandidate("e1", "mls", 10.5, scanner.LiquidityUnknown, 9),
			mkCandidate("e1", "mls", 10.5, scanner.LiquidityPass, 9)},
		{"unknown_to_fail",
			mkCandidate("e2", "lol", 10.5, scanner.LiquidityUnknown, 20),
			mkCandidate("e2", "lol", 10.5, scanner.LiquidityFail, 20)},
		{"pass_to_warn",
			mkCandidate("e3", "mls", 10.5, scanner.LiquidityPass, 9),
			mkCandidate("e3", "mls", 10.5, scanner.LiquidityWarn, 9)},
		{"warn_to_pass",
			mkCandidate("e4", "mls", 10.5, scanner.LiquidityWarn, 9),
			mkCandidate("e4", "mls", 10.5, scanner.LiquidityPass, 9)},
		{"fail_stays_fail",
			mkCandidate("e5", "lol", 10.5, scanner.LiquidityFail, 20),
			mkCandidate("e5", "lol", 10.5, scanner.LiquidityFail, 20)},
		{"pass_stays_pass",
			mkCandidate("e6", "mls", 10.5, scanner.LiquidityPass, 9),
			mkCandidate("e6", "mls", 10.5, scanner.LiquidityPass, 9)},
	}
	for _, s := range scenarios {
		a := mkSnapshot("A", tsA, s.a)
		b := mkSnapshot("B", tsB, s.b)
		got, _ := Compute(a, b, DefaultThresholdConfig(), FreshnessOptions{})
		for _, rec := range got.Records {
			if rec.ReviewNote == "" {
				continue
			}
			if !allowed[rec.ReviewNote] {
				t.Errorf("scenario %s: emitted note %q is not in AllReviewNotes", s.name, rec.ReviewNote)
			}
		}
	}
	// New / dropped membership.
	newOnly := mkSnapshot("B", tsB, mkCandidate("e-new", "kor", 10.5, scanner.LiquidityPass, 9))
	emptyA := mkSnapshot("A", tsA)
	gotNew, _ := Compute(emptyA, newOnly, DefaultThresholdConfig(), FreshnessOptions{})
	for _, r := range gotNew.Records {
		if !allowed[r.ReviewNote] {
			t.Errorf("new candidate emitted note %q not in AllReviewNotes", r.ReviewNote)
		}
	}
}

func TestDiff_ClosedEnumDiffClasses(t *testing.T) {
	allowed := map[DiffClass]bool{}
	for _, c := range AllDiffClasses {
		allowed[c] = true
	}
	// Run a wide combination of scenarios and verify every emitted
	// class is in the closed enum.
	scenarios := []struct {
		a, b scanner.Candidate
	}{
		{mkCandidate("a", "mls", 10.5, scanner.LiquidityUnknown, 9),
			mkCandidate("a", "mls", 10.5, scanner.LiquidityPass, 9)},
		{mkCandidate("b", "lol", 10.5, scanner.LiquidityFail, 20),
			mkCandidate("b", "lol", 10.5, scanner.LiquidityFail, 20)},
	}
	for _, s := range scenarios {
		got, _ := Compute(mkSnapshot("A", tsA, s.a), mkSnapshot("B", tsB, s.b), DefaultThresholdConfig(), FreshnessOptions{})
		for _, rec := range got.Records {
			for _, c := range rec.DiffClasses {
				if !allowed[c] {
					t.Errorf("emitted class %q is not in AllDiffClasses", c)
				}
			}
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

func findRecord(t *testing.T, rs []DiffRecord, slug string) DiffRecord {
	t.Helper()
	for _, r := range rs {
		if r.EventSlug == slug {
			return r
		}
	}
	t.Fatalf("no record for slug %q", slug)
	return DiffRecord{}
}

func hasClass(r DiffRecord, c DiffClass) bool {
	for _, x := range r.DiffClasses {
		if x == c {
			return true
		}
	}
	return false
}

func containsAny(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

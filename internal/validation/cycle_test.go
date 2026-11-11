package validation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

func writeCandidatesFixture(t *testing.T, dir, name string, cands []scanner.Candidate) string {
	t.Helper()
	body, err := json.Marshal(cands)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func tptr(tm time.Time) *time.Time { return &tm }

// buildCandidate constructs a minimal scanner.Candidate fixture
// suitable for ProcessCycle tests. The PerMarket entries carry
// both-sided quote flags so the envelope tracker accepts them.
func buildCandidate(slug, sport string, score float64, gate scanner.LiquidityLabel, marketCount int, startTs time.Time, families []string) scanner.Candidate {
	per := make([]scanner.PerMarketLiquidity, marketCount)
	for i := range per {
		per[i] = scanner.PerMarketLiquidity{
			ConditionID:        slug + "-cond-" + string(rune('a'+i)),
			HasTwoSidedQuotesA: gate == scanner.LiquidityPass || gate == scanner.LiquidityWarn,
			HasTwoSidedQuotesB: gate == scanner.LiquidityPass || gate == scanner.LiquidityWarn,
			SpreadA:            0.02,
			SpreadB:            0.02,
			TopDepthA:          400,
			TopDepthB:          400,
		}
	}
	band := "very_strong"
	if score < 10 {
		band = "strong"
	}
	if score < 8 {
		band = "medium"
	}
	return scanner.Candidate{
		Rank:          1,
		EventSlug:     slug,
		SportPrefix:   sport,
		Score:         score,
		Band:          band,
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
		Families:    families,
		StartTs:     &startTs,
		Active:      true,
		Closed:      false,
		ScannedAt:   time.Now().UTC(),
	}
}

// ---------------------------------------------------------------------------
// ProcessCycle end-to-end
// ---------------------------------------------------------------------------

func TestProcessCycle_EndToEnd_SingleCycle(t *testing.T) {
	dir := t.TempDir()
	scanPath := writeCandidatesFixture(t, dir, "candidates.json", []scanner.Candidate{
		buildCandidate("evt-prime", "nba", 9.5, scanner.LiquidityPass, 5,
			time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
			[]string{"spread_ml_total_stack"}),
		buildCandidate("evt-adj", "xxx-unknown", 5.0, scanner.LiquidityWarn, 2,
			time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC), nil),
	})

	profile := loadFixtureProfile(t)
	in := ProcessCycleInput{
		HistoricalProfile:    profile,
		ScanCandidatesPath:   scanPath,
		CycleTs:              "2026-05-16T08-00-00Z",
		LikenessLedgerPath:   filepath.Join(dir, ".val", "likeness.jsonl"),
		EnvelopeLedgerPath:   filepath.Join(dir, ".val", "envelope.jsonl"),
		SettlementLedgerPath: filepath.Join(dir, ".val", "settle.jsonl"),
		Now:                  time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC),
	}
	if err := EnsureValidationDir(in); err != nil {
		t.Fatalf("EnsureValidationDir: %v", err)
	}
	res, err := ProcessCycle(in)
	if err != nil {
		t.Fatalf("ProcessCycle: %v", err)
	}
	if res.ScanCandidates != 2 {
		t.Errorf("ScanCandidates = %d, want 2", res.ScanCandidates)
	}
	if res.LikenessRecorded != 2 {
		t.Errorf("LikenessRecorded = %d, want 2", res.LikenessRecorded)
	}
	if res.PrimeCount != 1 {
		t.Errorf("PrimeCount = %d, want 1 (nba)", res.PrimeCount)
	}
	if res.EnvelopeTicksAppended != 5 {
		t.Errorf("EnvelopeTicksAppended = %d, want 5 (5 PerMarket entries on the prime candidate)",
			res.EnvelopeTicksAppended)
	}
	if res.SettlementsDetected != 0 {
		t.Errorf("SettlementsDetected = %d, want 0 (first cycle)", res.SettlementsDetected)
	}

	// Verify ledger contents.
	likeness, err := ReadLikenesses(in.LikenessLedgerPath)
	if err != nil {
		t.Fatalf("ReadLikenesses: %v", err)
	}
	if len(likeness) != 2 {
		t.Errorf("likeness ledger has %d rows, want 2", len(likeness))
	}
	ticks, err := ReadTicks(in.EnvelopeLedgerPath)
	if err != nil {
		t.Fatalf("ReadTicks: %v", err)
	}
	if len(ticks) != 5 {
		t.Errorf("envelope ledger has %d ticks, want 5", len(ticks))
	}
	// Every tick should carry the EventStartTs.
	for _, tk := range ticks {
		if tk.EventStartTs == nil {
			t.Errorf("tick %s missing EventStartTs", tk.ConditionID)
		}
	}
}

func TestProcessCycle_SettlementDetected_OnSecondCycle(t *testing.T) {
	dir := t.TempDir()
	profile := loadFixtureProfile(t)
	now1 := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	startTs := time.Date(2026, 5, 16, 9, 0, 0, 0, time.UTC) // 1h after now1

	// Cycle 1: event-x is in scan, tracked.
	scanPath1 := writeCandidatesFixture(t, dir, "c1.json", []scanner.Candidate{
		buildCandidate("evt-x", "nba", 9.5, scanner.LiquidityPass, 2, startTs,
			[]string{"spread_ml_total_stack"}),
	})
	in1 := ProcessCycleInput{
		HistoricalProfile:    profile,
		ScanCandidatesPath:   scanPath1,
		CycleTs:              "2026-05-16T08-00-00Z",
		LikenessLedgerPath:   filepath.Join(dir, ".val", "lk.jsonl"),
		EnvelopeLedgerPath:   filepath.Join(dir, ".val", "env.jsonl"),
		SettlementLedgerPath: filepath.Join(dir, ".val", "settle.jsonl"),
		Now:                  now1,
	}
	if err := EnsureValidationDir(in1); err != nil {
		t.Fatalf("EnsureValidationDir: %v", err)
	}
	res1, err := ProcessCycle(in1)
	if err != nil {
		t.Fatalf("ProcessCycle #1: %v", err)
	}
	if res1.EnvelopeTicksAppended != 2 {
		t.Fatalf("cycle 1 ticks = %d, want 2", res1.EnvelopeTicksAppended)
	}

	// Cycle 2 (2h later): evt-x has dropped from scan AND its
	// StartTs has now passed. Settlement should fire.
	now2 := now1.Add(2 * time.Hour)
	scanPath2 := writeCandidatesFixture(t, dir, "c2.json", []scanner.Candidate{})
	in2 := in1
	in2.ScanCandidatesPath = scanPath2
	in2.CycleTs = "2026-05-16T10-00-00Z"
	in2.Now = now2

	res2, err := ProcessCycle(in2)
	if err != nil {
		t.Fatalf("ProcessCycle #2: %v", err)
	}
	if res2.SettlementsDetected != 1 {
		t.Errorf("cycle 2 settlements = %d, want 1", res2.SettlementsDetected)
	}

	settlements, err := ReadSettlements(in1.SettlementLedgerPath)
	if err != nil {
		t.Fatalf("ReadSettlements: %v", err)
	}
	if len(settlements) != 1 || settlements[0].EventSlug != "evt-x" {
		t.Fatalf("settlements = %+v", settlements)
	}
	// Winning side is UNKNOWN until a future resolve-settlements
	// command updates it.
	if len(settlements[0].PerCondition) == 0 {
		t.Fatal("expected per-condition rows in settlement")
	}
	for _, r := range settlements[0].PerCondition {
		if r.WinningSide != "UNKNOWN" {
			t.Errorf("PerCondition winning side = %q, want UNKNOWN", r.WinningSide)
		}
	}
}

func TestProcessCycle_NoTracking_BelowThreshold(t *testing.T) {
	dir := t.TempDir()
	profile := loadFixtureProfile(t)
	// Candidate with likeness below the default 0.60 threshold:
	// unknown sport prefix, no family, small bundle, FAIL gate,
	// low score → all axes near zero.
	scanPath := writeCandidatesFixture(t, dir, "candidates.json",
		[]scanner.Candidate{
			buildCandidate("evt-low", "xxx-unknown", 3.0, scanner.LiquidityFail, 1,
				time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC), nil),
		})
	in := ProcessCycleInput{
		HistoricalProfile:    profile,
		ScanCandidatesPath:   scanPath,
		CycleTs:              "x",
		LikenessLedgerPath:   filepath.Join(dir, ".val", "lk.jsonl"),
		EnvelopeLedgerPath:   filepath.Join(dir, ".val", "env.jsonl"),
		SettlementLedgerPath: filepath.Join(dir, ".val", "settle.jsonl"),
		Now:                  time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC),
	}
	if err := EnsureValidationDir(in); err != nil {
		t.Fatalf("EnsureValidationDir: %v", err)
	}
	res, err := ProcessCycle(in)
	if err != nil {
		t.Fatalf("ProcessCycle: %v", err)
	}
	if res.LikenessRecorded != 1 {
		t.Errorf("LikenessRecorded = %d, want 1", res.LikenessRecorded)
	}
	if res.EnvelopeTicksAppended != 0 {
		t.Errorf("EnvelopeTicksAppended = %d, want 0 (below threshold)",
			res.EnvelopeTicksAppended)
	}
}

func TestProcessCycle_SettlementIdempotent(t *testing.T) {
	// If we run ProcessCycle twice in a row with the same "event
	// has dropped + start passed" condition, the second call must
	// NOT emit a duplicate settlement.
	dir := t.TempDir()
	profile := loadFixtureProfile(t)
	now1 := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	startTs := time.Date(2026, 5, 16, 9, 0, 0, 0, time.UTC)

	scanPath1 := writeCandidatesFixture(t, dir, "c1.json", []scanner.Candidate{
		buildCandidate("evt-x", "nba", 9.5, scanner.LiquidityPass, 2, startTs,
			[]string{"spread_ml_total_stack"}),
	})
	in := ProcessCycleInput{
		HistoricalProfile:    profile,
		ScanCandidatesPath:   scanPath1,
		CycleTs:              "c1",
		LikenessLedgerPath:   filepath.Join(dir, ".val", "lk.jsonl"),
		EnvelopeLedgerPath:   filepath.Join(dir, ".val", "env.jsonl"),
		SettlementLedgerPath: filepath.Join(dir, ".val", "settle.jsonl"),
		Now:                  now1,
	}
	if err := EnsureValidationDir(in); err != nil {
		t.Fatalf("EnsureValidationDir: %v", err)
	}
	if _, err := ProcessCycle(in); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}

	// Cycle 2: event dropped, start passed → settlement fires.
	now2 := now1.Add(2 * time.Hour)
	scanPath2 := writeCandidatesFixture(t, dir, "c2.json", []scanner.Candidate{})
	in.ScanCandidatesPath = scanPath2
	in.CycleTs = "c2"
	in.Now = now2
	res2, err := ProcessCycle(in)
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if res2.SettlementsDetected != 1 {
		t.Fatalf("cycle 2 settlements = %d, want 1", res2.SettlementsDetected)
	}

	// Cycle 3: same shape. Settlement must NOT re-fire.
	now3 := now1.Add(3 * time.Hour)
	in.CycleTs = "c3"
	in.Now = now3
	res3, err := ProcessCycle(in)
	if err != nil {
		t.Fatalf("cycle 3: %v", err)
	}
	if res3.SettlementsDetected != 0 {
		t.Errorf("cycle 3 settlements = %d, want 0 (idempotent)",
			res3.SettlementsDetected)
	}
	all, _ := ReadSettlements(in.SettlementLedgerPath)
	if len(all) != 1 {
		t.Errorf("settlements ledger has %d rows, want 1", len(all))
	}
}

// ---------------------------------------------------------------------------
// AppendLikeness / ReadLikenesses (basic)
// ---------------------------------------------------------------------------

func TestLikenessLedger_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lk.jsonl")
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)

	for _, slug := range []string{"a", "b", "c"} {
		err := AppendLikeness(path, LikenessRecord{
			ObservedAt:  now,
			CycleTs:     "x",
			EventSlug:   slug,
			SportPrefix: "nba",
			Score:       LikenessScore{Total: 0.5, Band: BandRn1Adjacent},
		})
		if err != nil {
			t.Fatalf("AppendLikeness: %v", err)
		}
	}
	got, err := ReadLikenesses(path)
	if err != nil {
		t.Fatalf("ReadLikenesses: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d records, want 3", len(got))
	}
}

func TestLikenessLedger_AppendValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lk.jsonl")
	cases := []struct {
		name string
		rec  LikenessRecord
	}{
		{"empty slug", LikenessRecord{ObservedAt: time.Now().UTC()}},
		{"zero observed_at", LikenessRecord{EventSlug: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := AppendLikeness(path, c.rec); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

package shadow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

// writeScanFixture marshals a synthetic candidates.json under tmpDir
// and returns its path. The candidates are constructed via the
// existing mkCandidate helper in shadow_test.go.
func writeScanFixture(t *testing.T, tmpDir string, name string, cands []scanner.Candidate) string {
	t.Helper()
	body, err := json.Marshal(cands)
	if err != nil {
		t.Fatalf("marshal scan: %v", err)
	}
	p := filepath.Join(tmpDir, name)
	if err := os.WriteFile(p, body, 0644); err != nil {
		t.Fatalf("write scan: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// Cycle()
// ---------------------------------------------------------------------------

func TestCycle_DiscoverOff_TickExistingOnly(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")

	c1 := mkCandidate("evt-1", 10.5, scanner.LiquidityPass, 9)
	c2 := mkCandidate("evt-2", 10.5, scanner.LiquidityWarn, 6)
	scanPath := writeScanFixture(t, dir, "cycle1.json", []scanner.Candidate{c1, c2})

	// Pre-seed log with evt-1 only.
	_, err := Add(AddInput{
		LogPath:   logPath,
		EventSlug: "evt-1",
		Decision:  DecisionWatching,
		Obs:       Observation{Candidate: &c1, Now: time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC), AppendSource: "test-seed"},
	})
	if err != nil {
		t.Fatalf("seed add: %v", err)
	}

	res, err := Cycle(CycleInput{
		LogPath:        logPath,
		ScanPath:       scanPath,
		DiscoverFilter: DiscoverFilter{Enabled: false},
		Now:            time.Date(2026, 5, 13, 14, 0, 0, 0, time.UTC),
		AppendSource:   "test",
	})
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}

	if res.Counts.Discovered != 0 {
		t.Errorf("Discovered=%d, want 0 (filter disabled)", res.Counts.Discovered)
	}
	if res.Counts.Ticked != 1 {
		t.Errorf("Ticked=%d, want 1 (only evt-1 already tracked)", res.Counts.Ticked)
	}
	// evt-2 should appear in Skipped with "auto-discover disabled".
	foundSkip := false
	for _, s := range res.Skipped {
		if s.EventSlug == "evt-2" && s.Reason == "auto-discover disabled" {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Errorf("expected evt-2 in Skipped with reason 'auto-discover disabled'; got %v", res.Skipped)
	}

	// The ticked record should preserve evt-1's WATCHING decision.
	recs, _ := ReadLog(logPath)
	if len(recs) != 2 {
		t.Fatalf("log has %d records, want 2 (seed + tick)", len(recs))
	}
	if recs[1].ShadowDecision != DecisionWatching {
		t.Errorf("after tick decision=%q, want WATCHING (decision carry-prior)", recs[1].ShadowDecision)
	}
}

func TestCycle_DiscoverOn_AppliesBandGateFilter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")

	cPVs := mkCandidate("evt-pass-vs", 10.5, scanner.LiquidityPass, 9)
	cPVs.Band = "very_strong"
	cPS := mkCandidate("evt-pass-s", 8.5, scanner.LiquidityPass, 6)
	cPS.Band = "strong"
	cWVs := mkCandidate("evt-warn-vs", 10.5, scanner.LiquidityWarn, 9)
	cWVs.Band = "very_strong"
	cFVs := mkCandidate("evt-fail-vs", 10.5, scanner.LiquidityFail, 9)
	cFVs.Band = "very_strong"

	scanPath := writeScanFixture(t, dir, "cycle.json", []scanner.Candidate{cPVs, cPS, cWVs, cFVs})

	res, err := Cycle(CycleInput{
		LogPath:  logPath,
		ScanPath: scanPath,
		DiscoverFilter: DiscoverFilter{
			Enabled: true,
			Bands:   map[string]bool{"very_strong": true},
			Gates:   map[string]bool{"PASS": true},
		},
		Now:          time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC),
		AppendSource: "test",
	})
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if res.Counts.Discovered != 1 {
		t.Errorf("Discovered=%d, want 1 (only evt-pass-vs passes both filters)", res.Counts.Discovered)
	}
	if res.Counts.SkippedFilter != 3 {
		t.Errorf("SkippedFilter=%d, want 3", res.Counts.SkippedFilter)
	}
	if len(res.Discovered) != 1 || res.Discovered[0].EventSlug != "evt-pass-vs" {
		t.Errorf("Discovered slugs=%v, want [evt-pass-vs]", slugsOf(res.Discovered))
	}
}

func TestCycle_DiscoverOn_RespectsMaxCap(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")

	var cands []scanner.Candidate
	for i := 0; i < 10; i++ {
		c := mkCandidate("evt-"+string(rune('a'+i)), 10.5, scanner.LiquidityPass, 9)
		cands = append(cands, c)
	}
	scanPath := writeScanFixture(t, dir, "cycle.json", cands)

	res, err := Cycle(CycleInput{
		LogPath:  logPath,
		ScanPath: scanPath,
		DiscoverFilter: DiscoverFilter{
			Enabled:       true,
			MaxCandidates: 3,
		},
		Now:          time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC),
		AppendSource: "test",
	})
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if res.Counts.Discovered != 3 {
		t.Errorf("Discovered=%d, want 3 (cap)", res.Counts.Discovered)
	}
	if res.Counts.SkippedCap != 7 {
		t.Errorf("SkippedCap=%d, want 7", res.Counts.SkippedCap)
	}
}

func TestCycle_AutoCloseEventOnPastStartTs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")

	// Cycle 1: event 1h in the future, decision=WATCHING.
	now1 := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	future := now1.Add(time.Hour)

	c := mkCandidate("evt-x", 10.5, scanner.LiquidityPass, 9)
	c.StartTs = &future
	_, err := Add(AddInput{
		LogPath:   logPath,
		EventSlug: "evt-x",
		Decision:  DecisionWatching,
		Obs:       Observation{Candidate: &c, Now: now1, AppendSource: "test-seed"},
	})
	if err != nil {
		t.Fatalf("seed add: %v", err)
	}

	// Cycle 2: 2h later, StartTs has now passed (relative to Now).
	now2 := now1.Add(2 * time.Hour)
	scanPath2 := writeScanFixture(t, dir, "cycle2.json", []scanner.Candidate{c})

	res, err := Cycle(CycleInput{
		LogPath:        logPath,
		ScanPath:       scanPath2,
		DiscoverFilter: DiscoverFilter{Enabled: false},
		Now:            now2,
		AppendSource:   "test",
	})
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if res.Counts.AutoClosed != 1 {
		t.Errorf("AutoClosed=%d, want 1", res.Counts.AutoClosed)
	}
	if res.Counts.Ticked != 0 {
		t.Errorf("Ticked=%d, want 0 (the only existing slug auto-closed)", res.Counts.Ticked)
	}
	if len(res.AutoClosed) != 1 || res.AutoClosed[0].EventSlug != "evt-x" {
		t.Errorf("AutoClosed slugs=%v, want [evt-x]", slugsOf(res.AutoClosed))
	}
	if res.AutoClosed[0].ShadowDecision != DecisionClosedEvent {
		t.Errorf("AutoClosed decision=%q, want CLOSED_EVENT", res.AutoClosed[0].ShadowDecision)
	}
}

func TestCycle_DoesNotOverwriteOperatorDecision(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")

	c := mkCandidate("evt-y", 10.5, scanner.LiquidityPass, 9)
	scanPath := writeScanFixture(t, dir, "cycle.json", []scanner.Candidate{c})

	// Operator pre-decides REJECTED.
	_, err := Add(AddInput{
		LogPath:   logPath,
		EventSlug: "evt-y",
		Decision:  DecisionRejected,
		Obs:       Observation{Candidate: &c, Now: time.Date(2026, 5, 13, 8, 0, 0, 0, time.UTC), AppendSource: "test-seed"},
	})
	if err != nil {
		t.Fatalf("seed add: %v", err)
	}

	// Cycle should NOT promote REJECTED back to DISCOVERED.
	res, err := Cycle(CycleInput{
		LogPath:        logPath,
		ScanPath:       scanPath,
		DiscoverFilter: DiscoverFilter{Enabled: true, Bands: map[string]bool{"very_strong": true}},
		Now:            time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
		AppendSource:   "test",
	})
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if res.Counts.Ticked != 1 {
		t.Errorf("Ticked=%d, want 1", res.Counts.Ticked)
	}
	recs, _ := ReadLog(logPath)
	if recs[len(recs)-1].ShadowDecision != DecisionRejected {
		t.Errorf("decision after cycle=%q, want REJECTED (operator decision preserved)",
			recs[len(recs)-1].ShadowDecision)
	}
}

// ---------------------------------------------------------------------------
// ReviewQueue()
// ---------------------------------------------------------------------------

func TestReviewQueue_DefaultPicksWaitingStates(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	recs := []ShadowRecord{
		mkShadowRec("evt-watch", DecisionWatching, now, nil),
		mkShadowRec("evt-disc", DecisionDiscovered, now, nil),
		mkShadowRec("evt-nlr", DecisionNeedsLiquidityReview, now, nil),
		mkShadowRec("evt-rej", DecisionRejected, now, nil),
		mkShadowRec("evt-rescan", DecisionNeedsRescan, now, nil),
	}
	q := ReviewQueue(recs, ReviewQueueFilter{Now: now})
	gotSlugs := summarySlugs(q)
	wantSet := map[string]bool{"evt-disc": true, "evt-nlr": true, "evt-rescan": true}
	if len(gotSlugs) != len(wantSet) {
		t.Errorf("queue size=%d, want %d (got %v)", len(gotSlugs), len(wantSet), gotSlugs)
	}
	for _, s := range gotSlugs {
		if !wantSet[s] {
			t.Errorf("unexpected slug %q in queue", s)
		}
	}
}

func TestReviewQueue_StartingWithinFilter(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	soon := now.Add(2 * time.Hour)
	later := now.Add(48 * time.Hour)
	recs := []ShadowRecord{
		mkShadowRec("evt-soon", DecisionNeedsRescan, now, &soon),
		mkShadowRec("evt-later", DecisionNeedsRescan, now, &later),
	}
	q := ReviewQueue(recs, ReviewQueueFilter{
		StartingWithin: 6 * time.Hour,
		Now:            now,
	})
	if len(q) != 1 || q[0].EventSlug != "evt-soon" {
		t.Errorf("queue=%v, want [evt-soon]", summarySlugs(q))
	}
}

func TestReviewQueue_SortsByStartTsAscending(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	t1 := now.Add(5 * time.Hour)
	t2 := now.Add(1 * time.Hour)
	t3 := now.Add(3 * time.Hour)
	recs := []ShadowRecord{
		mkShadowRec("evt-1", DecisionDiscovered, now, &t1),
		mkShadowRec("evt-2", DecisionDiscovered, now, &t2),
		mkShadowRec("evt-3", DecisionDiscovered, now, &t3),
	}
	q := ReviewQueue(recs, ReviewQueueFilter{Now: now})
	if len(q) != 3 {
		t.Fatalf("queue size=%d, want 3", len(q))
	}
	if q[0].EventSlug != "evt-2" || q[1].EventSlug != "evt-3" || q[2].EventSlug != "evt-1" {
		t.Errorf("sort order=%v, want [evt-2, evt-3, evt-1] by StartTs asc",
			summarySlugs(q))
	}
}

func TestReviewQueue_MinScoreFilter(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	r1 := mkShadowRec("evt-low", DecisionDiscovered, now, nil)
	r1.SelectorScore = 7.0
	r2 := mkShadowRec("evt-high", DecisionDiscovered, now, nil)
	r2.SelectorScore = 11.0
	q := ReviewQueue([]ShadowRecord{r1, r2}, ReviewQueueFilter{
		MinScore: 10,
		Now:      now,
	})
	if len(q) != 1 || q[0].EventSlug != "evt-high" {
		t.Errorf("queue=%v, want [evt-high]", summarySlugs(q))
	}
}

// helpers ---------------------------------------------------------

// mkShadowRec is a minimal ShadowRecord constructor for ReviewQueue
// tests. It populates only the fields ReviewQueue / Report look at;
// everything else stays zero.
func mkShadowRec(slug string, decision ShadowDecision, observedAt time.Time, startTs *time.Time) ShadowRecord {
	return ShadowRecord{
		ShadowID:        "sh-" + slug,
		EventSlug:       slug,
		Title:           "Synthetic " + slug,
		ShadowDecision:  decision,
		ObservationAt:   observedAt,
		LastSeenAt:      observedAt,
		AppendedAt:      observedAt,
		EventStartTs:    startTs,
		SelectorScore:   10.5,
		ScoreBand:       "very_strong",
		LiquidityGate:   "PASS",
		DecisionHistory: []string{string(decision)},
	}
}

func slugsOf(rs []ShadowRecord) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.EventSlug)
	}
	return out
}

func summarySlugs(ss []ShadowSummary) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.EventSlug)
	}
	return out
}

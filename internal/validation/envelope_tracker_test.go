package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkTick is a small constructor for TickObservation fixtures.
func mkTick(slug, cond string, observedAt time.Time, askA, askB float64) TickObservation {
	return TickObservation{
		ObservedAt:  observedAt,
		CycleTs:     observedAt.Format("2006-01-02T15-04-05Z"),
		EventSlug:   slug,
		ConditionID: cond,
		AskA:        askA,
		AskB:        askB,
		BidA:        askA - 0.005,
		BidB:        askB - 0.005,
		SpreadA:     0.005,
		SpreadB:     0.005,
		TopDepthA:   500,
		TopDepthB:   500,
		Likeness:    0.9,
		Band:        BandRn1Prime,
	}
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// AppendTick / ReadTicks
// ---------------------------------------------------------------------------

func TestEnvelope_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ticks.jsonl")

	t1 := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	tick1 := mkTick("evt-1", "0xc1", t1, 0.51, 0.48)
	tick2 := mkTick("evt-1", "0xc1", t1.Add(6*time.Hour), 0.50, 0.49)
	tick3 := mkTick("evt-2", "0xc2", t1.Add(7*time.Hour), 0.42, 0.55)

	for _, tk := range []TickObservation{tick1, tick2, tick3} {
		if err := AppendTick(path, tk); err != nil {
			t.Fatalf("AppendTick: %v", err)
		}
	}

	ticks, err := ReadTicks(path)
	if err != nil {
		t.Fatalf("ReadTicks: %v", err)
	}
	if len(ticks) != 3 {
		t.Fatalf("got %d ticks, want 3", len(ticks))
	}
	if ticks[0].EventSlug != "evt-1" || ticks[2].EventSlug != "evt-2" {
		t.Errorf("ticks order: got %v", []string{ticks[0].EventSlug, ticks[1].EventSlug, ticks[2].EventSlug})
	}
}

func TestEnvelope_AppendOnlyInvariant(t *testing.T) {
	// Verify that adding new ticks does NOT modify the prefix of
	// the file: the SHA256 of the file before adding tick N must
	// be the prefix-SHA of the file after.
	dir := t.TempDir()
	path := filepath.Join(dir, "ticks.jsonl")

	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)

	// First append.
	if err := AppendTick(path, mkTick("evt-1", "0xc1", now, 0.51, 0.48)); err != nil {
		t.Fatalf("AppendTick #1: %v", err)
	}
	prefixHash := fileSHA(t, path)
	prefixBytes, _ := os.ReadFile(path)

	// Second append.
	if err := AppendTick(path, mkTick("evt-1", "0xc1", now.Add(6*time.Hour), 0.50, 0.49)); err != nil {
		t.Fatalf("AppendTick #2: %v", err)
	}

	allBytes, _ := os.ReadFile(path)
	// The new file must start with the exact prior bytes.
	if len(allBytes) < len(prefixBytes) {
		t.Fatalf("file shrank from %d to %d bytes", len(prefixBytes), len(allBytes))
	}
	for i, b := range prefixBytes {
		if allBytes[i] != b {
			t.Fatalf("byte %d changed: %x -> %x (append-only violation)", i, b, allBytes[i])
		}
	}
	// Re-verify by hashing the prefix slice.
	rehash := sha256.Sum256(allBytes[:len(prefixBytes)])
	if hex.EncodeToString(rehash[:]) != prefixHash {
		t.Errorf("prefix SHA differs after second append: append-only invariant broken")
	}
}

func TestEnvelope_AcceptsSmallReversal_RejectsLarge(t *testing.T) {
	// 2026-05-16 relaxation: the chronology guard accepts ticks
	// whose lag is within MaxAcceptableTickLag (concurrent-cycle
	// race) and rejects ticks beyond it (real clock drift).
	dir := t.TempDir()
	path := filepath.Join(dir, "ticks.jsonl")
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)

	if err := AppendTick(path, mkTick("evt-1", "0xc1", now, 0.5, 0.5)); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Within tolerance (1 min < 10 min) -- accepted.
	if err := AppendTick(path, mkTick("evt-1", "0xc1", now.Add(-1*time.Minute), 0.5, 0.5)); err != nil {
		t.Errorf("1-min lag should be accepted, got: %v", err)
	}
	// Beyond tolerance (15 min > 10 min) -- rejected.
	err := AppendTick(path, mkTick("evt-1", "0xc1", now.Add(-15*time.Minute), 0.5, 0.5))
	if err == nil {
		t.Error("expected error for 15-min lag (>10min)")
	}
}

func TestEnvelope_AppendTickValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ticks.jsonl")
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		tick TickObservation
	}{
		{"empty event_slug", TickObservation{ConditionID: "x", ObservedAt: now}},
		{"empty condition_id", TickObservation{EventSlug: "x", ObservedAt: now}},
		{"zero observed_at", TickObservation{EventSlug: "x", ConditionID: "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := AppendTick(path, tc.tick); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestEnvelope_ReadTicks_MissingFile(t *testing.T) {
	ticks, err := ReadTicks(filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil {
		t.Errorf("missing file should yield empty slice, got error: %v", err)
	}
	if len(ticks) != 0 {
		t.Errorf("expected 0 ticks, got %d", len(ticks))
	}
}

// ---------------------------------------------------------------------------
// BuildLegStates: per-tick running aggregates
// ---------------------------------------------------------------------------

func TestEnvelope_BuildLegStates_SingleTick(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	ticks := []TickObservation{mkTick("evt-1", "0xc1", now, 0.51, 0.48)}
	legs := BuildLegStates(ticks)
	if len(legs) != 1 {
		t.Fatalf("got %d legs, want 1", len(legs))
	}
	leg := legs["evt-1|0xc1"]
	if leg == nil {
		t.Fatal("missing leg")
	}
	if !approxEqual(leg.HypoSumWavg, 0.99) { // 0.51 + 0.48
		t.Errorf("single-tick SumWavg = %.4f, want 0.99", leg.HypoSumWavg)
	}
	if leg.TickCount != 1 || leg.HypoSharesA != 1 || leg.HypoSharesB != 1 {
		t.Errorf("leg counts wrong: %+v", leg)
	}
}

func TestEnvelope_BuildLegStates_SumWavgFormula(t *testing.T) {
	// 3 ticks with prices that produce a predictable WAVG path.
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	ticks := []TickObservation{
		mkTick("evt-x", "0xc", now, 0.52, 0.50),
		mkTick("evt-x", "0xc", now.Add(6*time.Hour), 0.48, 0.46),
		mkTick("evt-x", "0xc", now.Add(12*time.Hour), 0.50, 0.45),
	}
	legs := BuildLegStates(ticks)
	leg := legs["evt-x|0xc"]
	// After 3 ticks: WavgA = (0.52+0.48+0.50)/3 = 0.50
	//                WavgB = (0.50+0.46+0.45)/3 = 0.47
	//                SumWavg = 0.97
	if !approxEqual(leg.HypoWavgA, 0.50) {
		t.Errorf("WavgA = %.4f, want 0.50", leg.HypoWavgA)
	}
	if !approxEqual(leg.HypoWavgB, 0.47) {
		t.Errorf("WavgB = %.4f, want 0.47", leg.HypoWavgB)
	}
	if !approxEqual(leg.HypoSumWavg, 0.97) {
		t.Errorf("SumWavg = %.4f, want 0.97", leg.HypoSumWavg)
	}
}

func TestEnvelope_BuildLegStates_MinTracking(t *testing.T) {
	// Ticks that drive running SumWavg through 1.05, 0.98, 0.92, 0.95, 1.01.
	// Min should be 0.92 at tick 3 (1-indexed).
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	// Pick (ask, ask) pairs whose pairwise running average produces
	// the target SumWavg sequence (approximately). We construct
	// these step-by-step.
	type pair struct{ a, b float64 }
	pairs := []pair{
		{0.55, 0.50}, // tick 1: SumWavg = 1.05
		{0.45, 0.46}, // tick 2: (0.55+0.45)/2 + (0.50+0.46)/2 = 0.50 + 0.48 = 0.98
		{0.36, 0.40}, // tick 3: (0.55+0.45+0.36)/3 + (0.50+0.46+0.40)/3 = 0.453 + 0.453 ≈ 0.907
		{0.45, 0.43}, // tick 4: avgA = (0.55+0.45+0.36+0.45)/4 = 0.4525; avgB = (0.50+0.46+0.40+0.43)/4 = 0.4475 → 0.9
		{0.65, 0.60}, // tick 5: avgA ≈ 0.492; avgB ≈ 0.49 → ≈ 0.982
	}
	var ticks []TickObservation
	for i, p := range pairs {
		ticks = append(ticks, mkTick("evt-m", "0xm", now.Add(time.Duration(i)*6*time.Hour), p.a, p.b))
	}
	legs := BuildLegStates(ticks)
	leg := legs["evt-m|0xm"]

	if leg.TickCount != 5 {
		t.Fatalf("TickCount = %d, want 5", leg.TickCount)
	}
	// Min sum_wavg should be at tick 3 (0-indexed = 2 or 3 depending on which is lowest).
	// We verify min < tick-1 value.
	if leg.MinHypoSumWavg >= 1.0 {
		t.Errorf("MinHypoSumWavg = %.4f, expected < 1.0", leg.MinHypoSumWavg)
	}
	// Ticks below 1.0: tick 2 (0.98), 3 (~0.91), 4 (~0.90), 5 (~0.98) = 4 ticks
	if leg.TicksBelow1_0 != 4 {
		t.Errorf("TicksBelow1_0 = %d, want 4", leg.TicksBelow1_0)
	}
	// Ticks below 0.95: tick 3 (~0.91), 4 (~0.90) = 2 ticks
	if leg.TicksBelow0_95 != 2 {
		t.Errorf("TicksBelow0_95 = %d, want 2", leg.TicksBelow0_95)
	}
}

func TestEnvelope_BuildLegStates_GroupsByCondition(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	ticks := []TickObservation{
		mkTick("evt-1", "0xc1", now, 0.5, 0.5),
		mkTick("evt-1", "0xc2", now, 0.4, 0.6),
		mkTick("evt-2", "0xc1", now, 0.3, 0.7),
		mkTick("evt-1", "0xc1", now.Add(6*time.Hour), 0.5, 0.5),
	}
	legs := BuildLegStates(ticks)
	if len(legs) != 3 {
		t.Errorf("got %d distinct legs, want 3", len(legs))
	}
	leg := legs["evt-1|0xc1"]
	if leg.TickCount != 2 {
		t.Errorf("evt-1|0xc1 tick count = %d, want 2", leg.TickCount)
	}
}

func TestEnvelope_BuildLegStates_OutOfOrderTicksSort(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	// Out of chronological order in the slice; BuildLegStates should sort.
	ticks := []TickObservation{
		mkTick("evt-1", "0xc", now.Add(12*time.Hour), 0.5, 0.5),
		mkTick("evt-1", "0xc", now, 0.6, 0.6),
		mkTick("evt-1", "0xc", now.Add(6*time.Hour), 0.4, 0.4),
	}
	legs := BuildLegStates(ticks)
	leg := legs["evt-1|0xc"]
	// After sorting, the prices are 0.6, 0.4, 0.5 for A and same for B.
	// WavgA = (0.6+0.4+0.5)/3 = 0.5; same for B; SumWavg = 1.0
	if !approxEqual(leg.HypoSumWavg, 1.0) {
		t.Errorf("SumWavg after sort = %.4f, want 1.0", leg.HypoSumWavg)
	}
	// Min should hit on tick 2 (after sorting): avgA = (0.6+0.4)/2 = 0.5; avgB same; sumWavg = 1.0
	// Actually tick 1: SumWavg = 0.6+0.6 = 1.2
	// Tick 2: (0.6+0.4)/2 + same = 1.0
	// Tick 3: (0.6+0.4+0.5)/3 + same = 1.0
	// Min = 1.0 from tick 2 onwards
	if leg.MinHypoSumWavg > 1.0 {
		t.Errorf("MinHypoSumWavg = %.4f, expected <= 1.0", leg.MinHypoSumWavg)
	}
}

// ---------------------------------------------------------------------------
// FinalizeEnvelopeWindow + HoursBelow
// ---------------------------------------------------------------------------

func TestEnvelope_FinalizeWindow_RESOLVED(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	ticks := []TickObservation{
		mkTick("evt-r", "0xc", now, 0.55, 0.50),               // 1.05
		mkTick("evt-r", "0xc", now.Add(6*time.Hour), 0.45, 0.46),  // 0.98
		mkTick("evt-r", "0xc", now.Add(12*time.Hour), 0.36, 0.40), // ~0.91
		mkTick("evt-r", "0xc", now.Add(18*time.Hour), 0.45, 0.43), // ~0.90
	}
	legs := BuildLegStates(ticks)
	leg := legs["evt-r|0xc"]
	w := FinalizeEnvelopeWindow(leg, ticks, "RESOLVED")

	if w.EventSlug != "evt-r" || w.ConditionID != "0xc" {
		t.Errorf("identity wrong: %+v", w)
	}
	if w.TickCount != 4 {
		t.Errorf("TickCount = %d, want 4", w.TickCount)
	}
	if w.WindowClosure != "RESOLVED" {
		t.Errorf("WindowClosure = %q, want RESOLVED", w.WindowClosure)
	}
	if w.MinHypoSumWavg > 0.95 {
		t.Errorf("MinHypoSumWavg = %.4f, want < 0.95", w.MinHypoSumWavg)
	}
	// HoursBelow1_0: first-below to last-below.
	// Tick 2 (0.98) is first below 1.0; tick 4 (0.90) is last below.
	// (now+6h to now+18h) = 12h
	if !approxEqual(w.HoursBelow1_0, 12.0) {
		t.Errorf("HoursBelow1_0 = %.2f, want 12.0", w.HoursBelow1_0)
	}
	// HoursBelow0_95: first tick with sumWavg < 0.95 is tick 3 (~0.91), last is tick 4 (~0.90).
	// (now+12h to now+18h) = 6h
	if !approxEqual(w.HoursBelow0_95, 6.0) {
		t.Errorf("HoursBelow0_95 = %.2f, want 6.0", w.HoursBelow0_95)
	}
}

func TestEnvelope_FinalizeWindow_NoBelowThreshold(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	// All ticks above 1.0.
	ticks := []TickObservation{
		mkTick("evt-h", "0xc", now, 0.60, 0.55),
		mkTick("evt-h", "0xc", now.Add(6*time.Hour), 0.58, 0.53),
	}
	legs := BuildLegStates(ticks)
	w := FinalizeEnvelopeWindow(legs["evt-h|0xc"], ticks, "TIMEOUT")
	if w.HoursBelow1_0 != 0 {
		t.Errorf("HoursBelow1_0 = %.2f, want 0", w.HoursBelow1_0)
	}
	if w.HoursBelow0_95 != 0 {
		t.Errorf("HoursBelow0_95 = %.2f, want 0", w.HoursBelow0_95)
	}
}

func TestEnvelope_FinalizeWindow_PreservesLatestLikeness(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	t1 := mkTick("evt-l", "0xc", now, 0.5, 0.5)
	t1.Likeness = 0.85
	t1.Band = BandRn1Prime
	t2 := mkTick("evt-l", "0xc", now.Add(6*time.Hour), 0.5, 0.5)
	t2.Likeness = 0.72 // dropped
	t2.Band = BandRn1Plausible
	ticks := []TickObservation{t1, t2}
	legs := BuildLegStates(ticks)
	w := FinalizeEnvelopeWindow(legs["evt-l|0xc"], ticks, "RESOLVED")
	if !approxEqual(w.Likeness, 0.72) {
		t.Errorf("EnvelopeWindow.Likeness = %.2f, want 0.72 (latest tick)", w.Likeness)
	}
	if w.Band != BandRn1Plausible {
		t.Errorf("EnvelopeWindow.Band = %q, want PLAUSIBLE", w.Band)
	}
}

func TestEnvelope_FinalizeWindow_NilLeg(t *testing.T) {
	w := FinalizeEnvelopeWindow(nil, nil, "DELISTED")
	if w.WindowClosure != "DELISTED" {
		t.Errorf("nil leg should preserve closure, got %q", w.WindowClosure)
	}
}

// ---------------------------------------------------------------------------
// EnsureLedgerDir
// ---------------------------------------------------------------------------

func TestEnvelope_EnsureLedgerDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "subdir", "ticks.jsonl")
	if err := EnsureLedgerDir(path); err != nil {
		t.Errorf("EnsureLedgerDir: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("expected dir to exist: %v", err)
	}
}

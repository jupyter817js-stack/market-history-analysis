package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ComputeHypoPnL — the heart of PR-4
// ---------------------------------------------------------------------------

// envelopeFixture builds a (leg, ticks) pair that drops below the
// MERGE_THRESHOLD: prices walk down, then up again, so MERGE is
// optimal somewhere in the middle.
func envelopeFixture(t *testing.T) (*HypotheticalLeg, []TickObservation) {
	t.Helper()
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	type pair struct{ a, b float64 }
	pairs := []pair{
		{0.55, 0.50}, // tick 1: SumWavg = 1.05
		{0.45, 0.46}, // tick 2: ((0.55+0.45)/2 + (0.50+0.46)/2) = 0.50+0.48 = 0.98
		{0.36, 0.40}, // tick 3: ((0.55+0.45+0.36)/3 + (0.50+0.46+0.40)/3) ≈ 0.453+0.453 ≈ 0.907  ← MIN
		{0.45, 0.43}, // tick 4: ≈ 0.4525 + 0.4475 ≈ 0.900  (this is actually below tick 3 !)
		{0.65, 0.60}, // tick 5: bumps SumWavg back up
	}
	var ticks []TickObservation
	for i, p := range pairs {
		ticks = append(ticks, mkTick("evt-pnl", "0xpnl",
			now.Add(time.Duration(i)*6*time.Hour), p.a, p.b))
	}
	legs := BuildLegStates(ticks)
	return legs["evt-pnl|0xpnl"], ticks
}

func TestSettlement_ComputeHypoPnL_WinningA(t *testing.T) {
	leg, ticks := envelopeFixture(t)
	res := ComputeHypoPnL(leg, ticks, "A")

	if res.ConditionID != "0xpnl" {
		t.Errorf("ConditionID = %q, want 0xpnl", res.ConditionID)
	}
	if res.WinningSide != "A" {
		t.Errorf("WinningSide = %q, want A", res.WinningSide)
	}
	if res.HypoMergeUnits <= 0 {
		t.Errorf("HypoMergeUnits = %.2f, expected > 0 (envelope crossed threshold)", res.HypoMergeUnits)
	}
	// MERGE PnL must be positive: we bought below 1.0 USDC and merged for 1.0
	if res.HypoMergePnl <= 0 {
		t.Errorf("HypoMergePnl = %.4f, want > 0 (we merged below threshold)", res.HypoMergePnl)
	}
	// Winning side A: REDEEM remainder gets paid 1.0 per share.
	if res.HypoRedeemSide != "A" {
		t.Errorf("HypoRedeemSide = %q, want A", res.HypoRedeemSide)
	}
	if res.HypoRedeemRealized <= 0 {
		t.Errorf("HypoRedeemRealized = %.4f, want > 0 for winning A", res.HypoRedeemRealized)
	}
	// HypoConditionPnl = merge_pnl + redeem_pnl
	if !approxEqual(res.HypoConditionPnl, res.HypoMergePnl+res.HypoRedeemPnl) {
		t.Errorf("HypoConditionPnl = %.4f, want sum of merge+redeem = %.4f",
			res.HypoConditionPnl, res.HypoMergePnl+res.HypoRedeemPnl)
	}
}

func TestSettlement_ComputeHypoPnL_WinningB(t *testing.T) {
	leg, ticks := envelopeFixture(t)
	res := ComputeHypoPnL(leg, ticks, "B")

	if res.HypoRedeemSide != "B" {
		t.Errorf("HypoRedeemSide = %q, want B", res.HypoRedeemSide)
	}
	// REDEEM realized is the same magnitude regardless of which side
	// wins (we're paid 1.0 USDC per remaining share on the winning
	// side; the LOSING side's remainder is worthless but was already
	// purchased — so total cost = both sides).
	if res.HypoRedeemRealized <= 0 {
		t.Errorf("HypoRedeemRealized = %.4f, want > 0 for winning B", res.HypoRedeemRealized)
	}
}

func TestSettlement_ComputeHypoPnL_DrawZeroPayout(t *testing.T) {
	leg, ticks := envelopeFixture(t)
	res := ComputeHypoPnL(leg, ticks, "DRAW")

	if res.HypoRedeemSide != "DRAW" {
		t.Errorf("HypoRedeemSide = %q, want DRAW", res.HypoRedeemSide)
	}
	if res.HypoRedeemRealized != 0 {
		t.Errorf("HypoRedeemRealized = %.4f, want 0 (DRAW conservative payout)", res.HypoRedeemRealized)
	}
	// REDEEM PnL must be negative because we paid for shares and got nothing.
	if res.HypoRedeemPnl >= 0 {
		t.Errorf("HypoRedeemPnl = %.4f, want < 0 for DRAW with non-zero cost", res.HypoRedeemPnl)
	}
}

func TestSettlement_ComputeHypoPnL_UnknownDeferred(t *testing.T) {
	leg, ticks := envelopeFixture(t)
	res := ComputeHypoPnL(leg, ticks, "UNKNOWN")

	if res.HypoRedeemSide != "UNKNOWN" {
		t.Errorf("HypoRedeemSide = %q, want UNKNOWN", res.HypoRedeemSide)
	}
	if res.HypoRedeemRealized != 0 {
		t.Errorf("HypoRedeemRealized must be 0 for UNKNOWN, got %.4f", res.HypoRedeemRealized)
	}
	// MERGE PnL is still valid even when REDEEM is deferred.
	if res.HypoMergePnl == 0 {
		t.Errorf("HypoMergePnl should be non-zero even with UNKNOWN winning side")
	}
}

func TestSettlement_ComputeHypoPnL_NoMergeWhenAboveThreshold(t *testing.T) {
	// All ticks above MERGE_THRESHOLD: no MERGE simulation.
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	ticks := []TickObservation{
		mkTick("evt-h", "0xh", now, 0.60, 0.55),                  // 1.15
		mkTick("evt-h", "0xh", now.Add(6*time.Hour), 0.58, 0.53), // ~1.13
	}
	legs := BuildLegStates(ticks)
	res := ComputeHypoPnL(legs["evt-h|0xh"], ticks, "A")

	if res.HypoMergeUnits != 0 {
		t.Errorf("HypoMergeUnits = %.2f, want 0 (above threshold)", res.HypoMergeUnits)
	}
	if !res.MergeAt.IsZero() {
		t.Errorf("MergeAt = %v, want zero (no merge happened)", res.MergeAt)
	}
	// REDEEM should still fire on all shares.
	if res.HypoRedeemUnits != 2 {
		t.Errorf("HypoRedeemUnits = %.2f, want 2", res.HypoRedeemUnits)
	}
}

func TestSettlement_ComputeHypoPnL_NilLeg(t *testing.T) {
	res := ComputeHypoPnL(nil, nil, "A")
	if res.HypoMergeUnits != 0 || res.HypoRedeemUnits != 0 || res.HypoConditionPnl != 0 {
		t.Errorf("nil leg should produce zero result, got %+v", res)
	}
}

func TestSettlement_ComputeHypoPnL_EmptyTicks(t *testing.T) {
	leg := &HypotheticalLeg{ConditionID: "0xempty", TickCount: 0}
	res := ComputeHypoPnL(leg, nil, "A")
	if res.HypoMergeUnits != 0 || res.HypoRedeemUnits != 0 {
		t.Errorf("empty ticks should produce zero result, got %+v", res)
	}
	if res.ConditionID != "0xempty" {
		t.Errorf("ConditionID should still be copied, got %q", res.ConditionID)
	}
}

// ---------------------------------------------------------------------------
// MERGE math: closed-form check
// ---------------------------------------------------------------------------

func TestSettlement_MergeMathClosedForm(t *testing.T) {
	// Construct a simple 2-tick leg where the math is hand-verifiable.
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	ticks := []TickObservation{
		mkTick("evt-m", "0xm", now, 0.40, 0.45),               // SumWavg=0.85, K=0
		mkTick("evt-m", "0xm", now.Add(6*time.Hour), 0.60, 0.55), // running SumWavg= (0.40+0.60)/2 + (0.45+0.55)/2 = 0.50+0.50 = 1.00
	}
	legs := BuildLegStates(ticks)
	leg := legs["evt-m|0xm"]
	res := ComputeHypoPnL(leg, ticks, "A")

	// Optimal MERGE is at tick 1 (index 0): SumWavg=0.85.
	// merge units = 1; merge cost = 0.40 + 0.45 = 0.85
	// merge realized = 1.0; merge pnl = 0.15
	if !approxEqual(res.HypoMergeUnits, 1.0) {
		t.Errorf("HypoMergeUnits = %.4f, want 1.0", res.HypoMergeUnits)
	}
	if !approxEqual(res.HypoMergeCost, 0.85) {
		t.Errorf("HypoMergeCost = %.4f, want 0.85", res.HypoMergeCost)
	}
	if !approxEqual(res.HypoMergeRealized, 1.0) {
		t.Errorf("HypoMergeRealized = %.4f, want 1.0", res.HypoMergeRealized)
	}
	if !approxEqual(res.HypoMergePnl, 0.15) {
		t.Errorf("HypoMergePnl = %.4f, want 0.15", res.HypoMergePnl)
	}
	// Remainder (tick 2): shares = 1; cost = 0.60+0.55 = 1.15
	// Winning A: realized = 1.0; pnl = 1.0 - 1.15 = -0.15
	if !approxEqual(res.HypoRedeemUnits, 1.0) {
		t.Errorf("HypoRedeemUnits = %.4f, want 1.0", res.HypoRedeemUnits)
	}
	if !approxEqual(res.HypoRedeemCost, 1.15) {
		t.Errorf("HypoRedeemCost = %.4f, want 1.15", res.HypoRedeemCost)
	}
	if !approxEqual(res.HypoRedeemRealized, 1.0) {
		t.Errorf("HypoRedeemRealized = %.4f, want 1.0", res.HypoRedeemRealized)
	}
	if !approxEqual(res.HypoRedeemPnl, -0.15) {
		t.Errorf("HypoRedeemPnl = %.4f, want -0.15", res.HypoRedeemPnl)
	}
	// Condition PnL = 0.15 + (-0.15) = 0.00
	if !approxEqual(res.HypoConditionPnl, 0.0) {
		t.Errorf("HypoConditionPnl = %.4f, want 0.00", res.HypoConditionPnl)
	}
}

// ---------------------------------------------------------------------------
// BuildSettlementOutcome
// ---------------------------------------------------------------------------

func TestSettlement_BuildOutcome_Aggregates(t *testing.T) {
	resolvedAt := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	per := []ConditionResolution{
		{
			ConditionID: "0x1", WinningSide: "A",
			HypoMergeCost: 4.0, HypoMergeRealized: 5.0, HypoMergePnl: 1.0,
			HypoRedeemCost: 1.0, HypoRedeemRealized: 1.5, HypoRedeemPnl: 0.5,
			HypoConditionPnl: 1.5,
		},
		{
			ConditionID: "0x2", WinningSide: "B",
			HypoMergeCost: 3.0, HypoMergeRealized: 3.5, HypoMergePnl: 0.5,
			HypoRedeemCost: 2.0, HypoRedeemRealized: 1.0, HypoRedeemPnl: -1.0,
			HypoConditionPnl: -0.5,
		},
	}
	out := BuildSettlementOutcome("evt-x", resolvedAt, []string{"YES"}, per)

	if out.EventSlug != "evt-x" || out.ResolvedAt != resolvedAt {
		t.Errorf("identity wrong: %+v", out)
	}
	// Aggregates
	if !approxEqual(out.HypoMergePnl, 1.5) {
		t.Errorf("HypoMergePnl = %.4f, want 1.5", out.HypoMergePnl)
	}
	if !approxEqual(out.HypoRedeemPnl, -0.5) {
		t.Errorf("HypoRedeemPnl = %.4f, want -0.5", out.HypoRedeemPnl)
	}
	if !approxEqual(out.HypoTotalCost, 10.0) { // 4+1+3+2
		t.Errorf("HypoTotalCost = %.4f, want 10.0", out.HypoTotalCost)
	}
	if !approxEqual(out.HypoNetPnl, 1.0) { // 1.5 + -0.5
		t.Errorf("HypoNetPnl = %.4f, want 1.0", out.HypoNetPnl)
	}
	// ReturnPct = 100 * 1.0 / 10.0 = 10.0
	if !approxEqual(out.HypoReturnPct, 10.0) {
		t.Errorf("HypoReturnPct = %.4f, want 10.0", out.HypoReturnPct)
	}
}

func TestSettlement_BuildOutcome_ZeroCostHandled(t *testing.T) {
	out := BuildSettlementOutcome("evt-z", time.Now().UTC(), nil, nil)
	if out.HypoReturnPct != 0 {
		t.Errorf("HypoReturnPct = %.4f, want 0 for zero-cost outcome", out.HypoReturnPct)
	}
}

// ---------------------------------------------------------------------------
// AppendSettlement / ReadSettlements
// ---------------------------------------------------------------------------

func TestSettlement_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settlements.jsonl")

	s1 := SettlementOutcome{
		EventSlug:  "evt-1",
		ResolvedAt: time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
		HypoNetPnl: 1.5,
	}
	s2 := SettlementOutcome{
		EventSlug:  "evt-2",
		ResolvedAt: time.Date(2026, 5, 17, 6, 0, 0, 0, time.UTC),
		HypoNetPnl: -0.3,
	}
	for _, s := range []SettlementOutcome{s1, s2} {
		if err := AppendSettlement(path, s); err != nil {
			t.Fatalf("AppendSettlement: %v", err)
		}
	}
	got, err := ReadSettlements(path)
	if err != nil {
		t.Fatalf("ReadSettlements: %v", err)
	}
	if len(got) != 2 || got[0].EventSlug != "evt-1" || got[1].EventSlug != "evt-2" {
		t.Errorf("got %+v", got)
	}
}

func TestSettlement_AppendOnlyInvariant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settlements.jsonl")

	s1 := SettlementOutcome{
		EventSlug:  "evt-1",
		ResolvedAt: time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC),
	}
	if err := AppendSettlement(path, s1); err != nil {
		t.Fatalf("first append: %v", err)
	}
	prefixBytes, _ := os.ReadFile(path)
	prefixSHA := sha256.Sum256(prefixBytes)

	s2 := SettlementOutcome{
		EventSlug:  "evt-2",
		ResolvedAt: time.Date(2026, 5, 17, 6, 0, 0, 0, time.UTC),
	}
	if err := AppendSettlement(path, s2); err != nil {
		t.Fatalf("second append: %v", err)
	}
	all, _ := os.ReadFile(path)
	if len(all) < len(prefixBytes) {
		t.Fatal("file shrank")
	}
	rehash := sha256.Sum256(all[:len(prefixBytes)])
	if hex.EncodeToString(rehash[:]) != hex.EncodeToString(prefixSHA[:]) {
		t.Error("prefix SHA changed after second append")
	}
}

func TestSettlement_AppendValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settlements.jsonl")
	cases := []struct {
		name string
		s    SettlementOutcome
	}{
		{"empty event_slug", SettlementOutcome{ResolvedAt: time.Now().UTC()}},
		{"zero resolved_at", SettlementOutcome{EventSlug: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := AppendSettlement(path, c.s); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

func TestSettlement_ReadMissingFile(t *testing.T) {
	got, err := ReadSettlements(filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil {
		t.Errorf("missing file should yield nil, got error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 records, got %d", len(got))
	}
}

func TestSettlement_LatestByEvent_LastWriteWins(t *testing.T) {
	// Two records for same event: later one wins.
	t1 := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 17, 6, 0, 0, 0, time.UTC)
	records := []SettlementOutcome{
		{EventSlug: "evt-x", ResolvedAt: t1, HypoNetPnl: 1.0, PerCondition: []ConditionResolution{{WinningSide: "UNKNOWN"}}},
		{EventSlug: "evt-y", ResolvedAt: t1, HypoNetPnl: 2.0},
		{EventSlug: "evt-x", ResolvedAt: t2, HypoNetPnl: 3.5, PerCondition: []ConditionResolution{{WinningSide: "A"}}},
	}
	latest := LatestSettlementByEvent(records)
	if len(latest) != 2 {
		t.Errorf("got %d events in latest, want 2", len(latest))
	}
	if latest["evt-x"].HypoNetPnl != 3.5 {
		t.Errorf("evt-x HypoNetPnl = %.2f, want 3.5 (later record wins)",
			latest["evt-x"].HypoNetPnl)
	}
	if latest["evt-x"].PerCondition[0].WinningSide != "A" {
		t.Errorf("evt-x winning side = %q, want A (later record)",
			latest["evt-x"].PerCondition[0].WinningSide)
	}
}

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mkLegBelowEnvelope returns a synthetic PaperLeg whose SumWavg
// is configurably below 1.0 and whose first-fill time is N
// minutes in the past.
func mkLegBelowEnvelope(sharesEach float64, wavgA, wavgB float64, firstFillMinutesAgo float64, now time.Time) *PaperLeg {
	first := now.Add(-time.Duration(firstFillMinutesAgo * float64(time.Minute)))
	return &PaperLeg{
		EventSlug:    "evt",
		ConditionID:  "0xc",
		PaperSharesA: sharesEach,
		PaperSharesB: sharesEach,
		PaperWavgA:   wavgA,
		PaperWavgB:   wavgB,
		PaperSumWavg: wavgA + wavgB,
		FirstFillAt:  first,
		LastFillAt:   now,
		CurrentPhase: PaperOpenB,
	}
}

// ---------------------------------------------------------------------------
// PlanMerge
// ---------------------------------------------------------------------------

func TestMerge_TriggersBelowThresholdAfterMinHold(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	leg := mkLegBelowEnvelope(10, 0.45, 0.48, 60, now) // 0.93, 60 min held
	m, ok, reason := PlanMerge(leg, now, "cycle-x")
	if !ok {
		t.Fatalf("expected MERGE trigger, got skip: %s", reason)
	}
	if !approxEqualEngine(m.PaperMergedUnits, 10) {
		t.Errorf("PaperMergedUnits = %.2f, want 10", m.PaperMergedUnits)
	}
	wantCost := 10.0 * 0.93
	if !approxEqualEngine(m.PaperMergedCostUsdc, wantCost) {
		t.Errorf("PaperMergedCostUsdc = %.4f, want %.4f", m.PaperMergedCostUsdc, wantCost)
	}
	if !approxEqualEngine(m.PaperMergedRealizedUsdc, 10.0) {
		t.Errorf("PaperMergedRealizedUsdc = %.4f, want 10.0", m.PaperMergedRealizedUsdc)
	}
	wantPnl := 10.0 - wantCost
	if !approxEqualEngine(m.PaperMergePnlUsdc, wantPnl) {
		t.Errorf("PaperMergePnlUsdc = %.4f, want %.4f", m.PaperMergePnlUsdc, wantPnl)
	}
	if m.PaperMergePnlUsdc <= 0 {
		t.Errorf("PnL should be positive below threshold; got %.4f", m.PaperMergePnlUsdc)
	}
}

func TestMerge_SkipsAboveThreshold(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	leg := mkLegBelowEnvelope(10, 0.49, 0.49, 60, now) // 0.98 > 0.95
	_, ok, reason := PlanMerge(leg, now, "cycle-x")
	if ok {
		t.Error("merge should NOT trigger above 0.95")
	}
	if !strings.Contains(reason, "threshold") {
		t.Errorf("reason = %q; expected to mention threshold", reason)
	}
}

func TestMerge_SkipsBeforeMinHold(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	leg := mkLegBelowEnvelope(10, 0.45, 0.45, 15, now) // 0.90, only 15 min
	_, ok, reason := PlanMerge(leg, now, "cycle-x")
	if ok {
		t.Error("merge should NOT trigger before 30 min hold")
	}
	if !strings.Contains(reason, "hold") {
		t.Errorf("reason = %q; expected to mention hold", reason)
	}
}

func TestMerge_SkipsOneSidedLeg(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	leg := &PaperLeg{
		EventSlug: "e", ConditionID: "c", PaperSharesA: 10,
		PaperWavgA: 0.5, PaperSumWavg: 0.5, FirstFillAt: now.Add(-time.Hour),
	}
	_, ok, _ := PlanMerge(leg, now, "cycle-x")
	if ok {
		t.Error("one-sided leg cannot merge")
	}
}

func TestMerge_NilLeg(t *testing.T) {
	_, ok, _ := PlanMerge(nil, time.Now(), "x")
	if ok {
		t.Error("nil leg cannot merge")
	}
}

func TestMerge_UnitsAreMinOfSides(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	// Unequal sides: A=15, B=8 → merge units = 8
	leg := &PaperLeg{
		EventSlug: "e", ConditionID: "c",
		PaperSharesA: 15, PaperSharesB: 8,
		PaperWavgA: 0.45, PaperWavgB: 0.45,
		PaperSumWavg: 0.90, FirstFillAt: now.Add(-time.Hour),
	}
	m, ok, _ := PlanMerge(leg, now, "x")
	if !ok {
		t.Fatal("expected merge")
	}
	if !approxEqualEngine(m.PaperMergedUnits, 8) {
		t.Errorf("merged units = %.2f, want 8 (min of 15, 8)", m.PaperMergedUnits)
	}
}

// ---------------------------------------------------------------------------
// PlanRedeem
// ---------------------------------------------------------------------------

func TestRedeem_WinningAPaysRemainingA(t *testing.T) {
	now := time.Date(2026, 5, 16, 22, 0, 0, 0, time.UTC)
	leg := &PaperLeg{
		EventSlug: "e", ConditionID: "c",
		PaperSharesA: 10, PaperSharesB: 10,
		PaperWavgA: 0.55, PaperWavgB: 0.50,
	}
	// Pretend MERGE happened on 8 units; remainder 2 on each side.
	r := PlanRedeem(leg, 2, 2, "a", now, "x")
	if r.WinningSide != "a" {
		t.Errorf("winning_side = %q, want a", r.WinningSide)
	}
	if !approxEqualEngine(r.PaperRedeemedShares, 2) {
		t.Errorf("redeemed shares = %.2f, want 2", r.PaperRedeemedShares)
	}
	// Cost basis = 2*0.55 (A) + 2*0.50 (B) = 1.1 + 1.0 = 2.10
	if !approxEqualEngine(r.PaperRedeemedCostUsdc, 2.10) {
		t.Errorf("cost = %.4f, want 2.10", r.PaperRedeemedCostUsdc)
	}
	if !approxEqualEngine(r.PaperRedeemedRealizedUsdc, 2.0) {
		t.Errorf("realized = %.4f, want 2.0", r.PaperRedeemedRealizedUsdc)
	}
	// PnL = 2.0 - 2.1 = -0.1
	if !approxEqualEngine(r.PaperRedeemPnlUsdc, -0.10) {
		t.Errorf("PnL = %.4f, want -0.10", r.PaperRedeemPnlUsdc)
	}
}

func TestRedeem_WinningBPaysRemainingB(t *testing.T) {
	now := time.Date(2026, 5, 16, 22, 0, 0, 0, time.UTC)
	leg := &PaperLeg{
		EventSlug: "e", ConditionID: "c",
		PaperSharesA: 10, PaperSharesB: 10,
		PaperWavgA: 0.55, PaperWavgB: 0.50,
	}
	r := PlanRedeem(leg, 2, 3, "b", now, "x")
	if r.WinningSide != "b" {
		t.Errorf("winning_side = %q, want b", r.WinningSide)
	}
	if !approxEqualEngine(r.PaperRedeemedShares, 3) {
		t.Errorf("redeemed shares = %.2f, want 3 (B-side)", r.PaperRedeemedShares)
	}
	if !approxEqualEngine(r.PaperRedeemedRealizedUsdc, 3.0) {
		t.Errorf("realized = %.4f, want 3.0", r.PaperRedeemedRealizedUsdc)
	}
}

func TestRedeem_DrawPaysZero(t *testing.T) {
	now := time.Date(2026, 5, 16, 22, 0, 0, 0, time.UTC)
	leg := &PaperLeg{
		EventSlug: "e", ConditionID: "c",
		PaperWavgA: 0.5, PaperWavgB: 0.5,
	}
	r := PlanRedeem(leg, 2, 2, "draw", now, "x")
	if r.PaperRedeemedRealizedUsdc != 0 {
		t.Errorf("draw realized = %.4f, want 0", r.PaperRedeemedRealizedUsdc)
	}
	if r.PaperRedeemPnlUsdc >= 0 {
		t.Errorf("draw PnL should be negative (cost paid, no payout); got %.4f", r.PaperRedeemPnlUsdc)
	}
}

func TestRedeem_UnknownDefersPayout(t *testing.T) {
	now := time.Date(2026, 5, 16, 22, 0, 0, 0, time.UTC)
	leg := &PaperLeg{
		EventSlug: "e", ConditionID: "c",
		PaperWavgA: 0.5, PaperWavgB: 0.5,
	}
	r := PlanRedeem(leg, 2, 2, "anything-weird", now, "x")
	if r.WinningSide != "unknown" {
		t.Errorf("unknown side should canonicalize to 'unknown', got %q", r.WinningSide)
	}
	if r.PaperRedeemedRealizedUsdc != 0 {
		t.Errorf("unknown realized = %.4f, want 0", r.PaperRedeemedRealizedUsdc)
	}
}

// ---------------------------------------------------------------------------
// Risk control
// ---------------------------------------------------------------------------

func TestRisk_PerEventStopFiresBelowThreshold(t *testing.T) {
	pos := &PaperPosition{EventSlug: "e", PaperRealizedPnlUsdc: -60}
	r := CheckPerEventStop(pos, DefaultRiskLimits())
	if !r.ShouldStop {
		t.Errorf("expected stop at PnL -60 < -50, got pass")
	}
	if !strings.Contains(r.Reason, "stop-loss") {
		t.Errorf("reason = %q; expected to mention stop-loss", r.Reason)
	}
}

func TestRisk_PerEventStopHoldsAtThreshold(t *testing.T) {
	// At exactly -50, the condition is < -50 (strict), so no stop.
	pos := &PaperPosition{EventSlug: "e", PaperRealizedPnlUsdc: -50}
	r := CheckPerEventStop(pos, DefaultRiskLimits())
	if r.ShouldStop {
		t.Errorf("PnL exactly at -50 should not stop (strict <); got stop")
	}
}

func TestRisk_PerEventStopHoldsAbove(t *testing.T) {
	pos := &PaperPosition{EventSlug: "e", PaperRealizedPnlUsdc: -49}
	r := CheckPerEventStop(pos, DefaultRiskLimits())
	if r.ShouldStop {
		t.Errorf("PnL above -50 should not stop")
	}
}

func TestRisk_PerEventStopHoldsForNil(t *testing.T) {
	r := CheckPerEventStop(nil, DefaultRiskLimits())
	if r.ShouldStop {
		t.Errorf("nil position should not stop")
	}
}

func TestRisk_DailyKillSumsOnly24h(t *testing.T) {
	now := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	old := now.Add(-25 * time.Hour)
	recent := now.Add(-12 * time.Hour)
	merges := []PaperMerge{
		{ObservedAt: old, PaperMergePnlUsdc: -500},     // outside window
		{ObservedAt: recent, PaperMergePnlUsdc: -150},  // counted
	}
	redemptions := []PaperRedemption{
		{ObservedAt: recent, PaperRedeemPnlUsdc: -100}, // counted
	}
	r := CheckDailyKill(merges, redemptions, now, DefaultRiskLimits())
	// Window PnL = -150 + -100 = -250 < -200 → pause
	if !r.ShouldPause {
		t.Errorf("expected pause at -250 < -200, got pass (window PnL=%.2f)", r.WindowPnlUsdc)
	}
	if !approxEqualEngine(r.WindowPnlUsdc, -250) {
		t.Errorf("WindowPnlUsdc = %.2f, want -250", r.WindowPnlUsdc)
	}
}

func TestRisk_DailyKillHoldsAboveThreshold(t *testing.T) {
	now := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	merges := []PaperMerge{
		{ObservedAt: now.Add(-1 * time.Hour), PaperMergePnlUsdc: -100},
	}
	r := CheckDailyKill(merges, nil, now, DefaultRiskLimits())
	if r.ShouldPause {
		t.Errorf("PnL -100 above -200 kill threshold should not pause")
	}
}

func TestRisk_CountActivePositions(t *testing.T) {
	positions := map[string]*PaperPosition{
		"a": {CurrentPhase: PaperOpenB},
		"b": {CurrentPhase: PaperExpand},
		"c": {CurrentPhase: PaperStopped},
		"d": {CurrentPhase: PaperIdle},
		"e": nil,
	}
	if n := CountActivePositions(positions); n != 2 {
		t.Errorf("active count = %d, want 2 (open_b + expand only)", n)
	}
}

func TestRisk_CountNewEntriesIn24h(t *testing.T) {
	now := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	old := now.Add(-30 * time.Hour)
	recent := now.Add(-3 * time.Hour)
	fills := []PaperFill{
		// evt-1: first fill old → does NOT count
		{EventSlug: "evt-1", ObservedAt: old},
		{EventSlug: "evt-1", ObservedAt: recent},
		// evt-2: first fill recent → counts
		{EventSlug: "evt-2", ObservedAt: recent},
		// evt-3: first fill recent → counts
		{EventSlug: "evt-3", ObservedAt: recent.Add(time.Hour)},
	}
	n := CountNewEntriesIn24h(fills, now)
	if n != 2 {
		t.Errorf("new entries = %d, want 2 (evt-2, evt-3)", n)
	}
}

// ---------------------------------------------------------------------------
// Paper ledger: AppendMerge / AppendRedemption + Read variants
// ---------------------------------------------------------------------------

func TestPaperLedger_AppendAndReadMerges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "merges.jsonl")
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		err := AppendMerge(path, PaperMerge{
			ObservedAt: now.Add(time.Duration(i) * time.Minute),
			EventSlug:  "evt", ConditionID: "0xc",
			PaperMergedUnits: 5, PaperMergedCostUsdc: 4.5,
			PaperMergedRealizedUsdc: 5.0, PaperMergePnlUsdc: 0.5,
		})
		if err != nil {
			t.Fatalf("AppendMerge: %v", err)
		}
	}
	got, err := ReadMerges(path)
	if err != nil {
		t.Fatalf("ReadMerges: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d merges, want 3", len(got))
	}
}

func TestPaperLedger_AppendAndReadRedemptions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "redemptions.jsonl")
	now := time.Date(2026, 5, 16, 22, 0, 0, 0, time.UTC)
	err := AppendRedemption(path, PaperRedemption{
		ObservedAt: now, EventSlug: "e", ConditionID: "c",
		WinningSide: "a", PaperRedeemedShares: 2,
	})
	if err != nil {
		t.Fatalf("AppendRedemption: %v", err)
	}
	got, _ := ReadRedemptions(path)
	if len(got) != 1 || got[0].WinningSide != "a" {
		t.Errorf("got %+v", got)
	}
}

func TestPaperLedger_AppendValidation(t *testing.T) {
	dir := t.TempDir()
	mergePath := filepath.Join(dir, "m.jsonl")
	redPath := filepath.Join(dir, "r.jsonl")
	now := time.Now().UTC()

	if err := AppendMerge(mergePath, PaperMerge{EventSlug: "e"}); err == nil {
		t.Error("missing condition_id should fail")
	}
	if err := AppendMerge(mergePath, PaperMerge{EventSlug: "e", ConditionID: "c"}); err == nil {
		t.Error("missing observed_at should fail")
	}
	if err := AppendRedemption(redPath, PaperRedemption{ObservedAt: now}); err == nil {
		t.Error("missing slug should fail")
	}
}

func TestPaperLedger_ReadMissingFile(t *testing.T) {
	tmp := t.TempDir()
	if got, _ := ReadMerges(filepath.Join(tmp, "missing.jsonl")); len(got) != 0 {
		t.Errorf("missing merges file should yield empty, got %d", len(got))
	}
	if got, _ := ReadRedemptions(filepath.Join(tmp, "missing.jsonl")); len(got) != 0 {
		t.Errorf("missing redemptions file should yield empty, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// PnL summary
// ---------------------------------------------------------------------------

func TestPnlSummary_AggregatesEverything(t *testing.T) {
	now := time.Date(2026, 5, 16, 22, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		{EventSlug: "e1", PaperCostUsdc: 5.0},
		{EventSlug: "e1", PaperCostUsdc: 4.9},
		{EventSlug: "e2", PaperCostUsdc: 10.0},
	}
	merges := []PaperMerge{
		{EventSlug: "e1", PaperMergedRealizedUsdc: 5.0, PaperMergePnlUsdc: 0.5},
	}
	redemptions := []PaperRedemption{
		// WinningSide set so summary aggregation counts it (UNKNOWN
		// is deferred -- see TestCycle_UnknownRedemptionDoesNotTriggerStop).
		{EventSlug: "e2", WinningSide: "a", PaperRedeemedRealizedUsdc: 7.5, PaperRedeemPnlUsdc: -2.5},
	}
	positions := map[string]*PaperPosition{
		"e1": {EventSlug: "e1", SportPrefix: "mls", CurrentPhase: PaperOpenB, PaperRealizedPnlUsdc: 0.5},
		"e2": {EventSlug: "e2", SportPrefix: "nba", CurrentPhase: PaperIdle, PaperRealizedPnlUsdc: -2.5},
		"e3": {EventSlug: "e3", SportPrefix: "spl", CurrentPhase: PaperStopped, PaperRealizedPnlUsdc: -55},
	}
	s := BuildPaperPnlSummary(fills, merges, redemptions, positions, now)

	if s.LifetimeFillsCount != 3 || s.LifetimeMergesCount != 1 || s.LifetimeRedemptionsCount != 1 {
		t.Errorf("counts wrong: %+v", s)
	}
	if s.LifetimeEventsTouched != 2 {
		t.Errorf("events touched = %d, want 2", s.LifetimeEventsTouched)
	}
	if !approxEqualEngine(s.PaperTotalCostUsdc, 19.9) {
		t.Errorf("total cost = %.4f, want 19.9", s.PaperTotalCostUsdc)
	}
	if !approxEqualEngine(s.PaperRealizedUsdc, 12.5) {
		t.Errorf("realized = %.4f, want 12.5", s.PaperRealizedUsdc)
	}
	if !approxEqualEngine(s.PaperNetPnlUsdc, -2.0) {
		t.Errorf("net PnL = %.4f, want -2.0", s.PaperNetPnlUsdc)
	}
	if s.OpenPositionsCount != 1 || s.StoppedPositionsCount != 1 || s.IdlePositionsCount != 1 {
		t.Errorf("phase counts wrong: open=%d stopped=%d idle=%d",
			s.OpenPositionsCount, s.StoppedPositionsCount, s.IdlePositionsCount)
	}
	if !approxEqualEngine(s.PaperPnlBySportPrefix["mls"], 0.5) {
		t.Errorf("mls PnL = %.4f, want 0.5", s.PaperPnlBySportPrefix["mls"])
	}
	if !approxEqualEngine(s.PaperPnlBySportPrefix["spl"], -55) {
		t.Errorf("spl PnL = %.4f, want -55", s.PaperPnlBySportPrefix["spl"])
	}
}

func TestPnlSummary_EmptyInputsZero(t *testing.T) {
	s := BuildPaperPnlSummary(nil, nil, nil, nil, time.Now())
	if s.PaperTotalCostUsdc != 0 || s.PaperNetPnlUsdc != 0 || s.PaperReturnPct != 0 {
		t.Errorf("empty inputs should yield zero summary, got %+v", s)
	}
}

func TestPnlSummary_ReturnPctFromCost(t *testing.T) {
	fills := []PaperFill{{EventSlug: "e", PaperCostUsdc: 100.0}}
	merges := []PaperMerge{{EventSlug: "e", PaperMergePnlUsdc: 5.0}}
	s := BuildPaperPnlSummary(fills, merges, nil, nil, time.Now())
	if !approxEqualEngine(s.PaperReturnPct, 5.0) {
		t.Errorf("ReturnPct = %.4f, want 5.0 (5/100)", s.PaperReturnPct)
	}
}

func TestPnlSummary_WriteToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pnl.json")
	s := PaperPnlSummary{
		GeneratedAt:       time.Now().UTC(),
		PaperNetPnlUsdc:   12.3,
	}
	if err := WritePnlSummary(path, s); err != nil {
		t.Fatalf("WritePnlSummary: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(body), "paper_net_pnl_usdc") {
		t.Errorf("missing field in written JSON: %s", body)
	}
}

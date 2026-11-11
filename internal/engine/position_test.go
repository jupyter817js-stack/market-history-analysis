package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SimulatePaperFill
// ---------------------------------------------------------------------------

func TestFillSim_SmallSizeMinimalSlippage(t *testing.T) {
	// Request 5 USDC at best_ask=0.50, depth=200 → available=100
	// requested = 10 shares; 10/100 = 0.1 slippage ratio
	// slippage = 0.1 * 0.02 = 0.002 → avgPrice = 0.50 * 1.002 = 0.501
	// cost = 10 * 0.501 = 5.01; fee = 5.01 * 0.002 = 0.01002; total = 5.02002
	est := SimulatePaperFill(0.50, 200, 5.0)
	if est.PartialFill {
		t.Errorf("expected full fill, got partial")
	}
	if !approxEqualEngine(est.PaperShares, 10.0) {
		t.Errorf("shares = %.4f, want 10.0", est.PaperShares)
	}
	if !approxEqualEngine(est.PaperAvgPrice, 0.501) {
		t.Errorf("avg_price = %.6f, want 0.501", est.PaperAvgPrice)
	}
	wantCost := 10.0 * 0.501 * (1.0 + TakerFeeRate)
	if !approxEqualEngine(est.PaperCostUsdc, wantCost) {
		t.Errorf("cost = %.6f, want %.6f", est.PaperCostUsdc, wantCost)
	}
}

func TestFillSim_LargeSizePartialFill(t *testing.T) {
	// Request 100 USDC at best_ask=0.50, depth=100 → available=50
	// requested = 200 shares; partial. Filled at full slippage.
	// avgPrice = 0.50 * 1.02 = 0.51; shares = 50
	// cost = 50 * 0.51 = 25.5; fee = 0.051; total = 25.551
	est := SimulatePaperFill(0.50, 100, 100.0)
	if !est.PartialFill {
		t.Errorf("expected partial fill, got full")
	}
	if !approxEqualEngine(est.PaperShares, 50.0) {
		t.Errorf("shares = %.4f, want 50.0 (depth-capped)", est.PaperShares)
	}
	if !approxEqualEngine(est.PaperAvgPrice, 0.51) {
		t.Errorf("avg_price = %.6f, want 0.51 (full slippage)", est.PaperAvgPrice)
	}
	wantCost := 50.0 * 0.51 * (1.0 + TakerFeeRate)
	if !approxEqualEngine(est.PaperCostUsdc, wantCost) {
		t.Errorf("cost = %.6f, want %.6f", est.PaperCostUsdc, wantCost)
	}
}

func TestFillSim_ExactBoundaryFill(t *testing.T) {
	// Request exactly the available depth → full slippage but
	// NOT partial (we hit availability exactly, not exceeded).
	// depth=200, available=100; bestAsk=0.50 → 100 shares costs $50
	est := SimulatePaperFill(0.50, 200, 50.0)
	if est.PartialFill {
		t.Errorf("expected NOT partial at exact-fit, got partial=true")
	}
	if !approxEqualEngine(est.PaperShares, 100.0) {
		t.Errorf("shares = %.4f, want 100.0", est.PaperShares)
	}
	// slippage = 100/100 * 0.02 = 0.02 → avgPrice = 0.51
	if !approxEqualEngine(est.PaperAvgPrice, 0.51) {
		t.Errorf("avg_price = %.6f, want 0.51", est.PaperAvgPrice)
	}
}

func TestFillSim_ZeroOrNegativeInputsReturnZero(t *testing.T) {
	for _, c := range []struct {
		name              string
		ask, depth, usdc float64
	}{
		{"zero ask", 0, 100, 10},
		{"negative ask", -0.1, 100, 10},
		{"zero depth", 0.5, 0, 10},
		{"negative depth", 0.5, -100, 10},
		{"zero usdc", 0.5, 100, 0},
		{"negative usdc", 0.5, 100, -5},
	} {
		t.Run(c.name, func(t *testing.T) {
			est := SimulatePaperFill(c.ask, c.depth, c.usdc)
			if est.PaperShares != 0 || est.PaperCostUsdc != 0 {
				t.Errorf("%s: should return zero estimate, got %+v", c.name, est)
			}
		})
	}
}

func TestFillSim_FeeIsCappedAtTakerRate(t *testing.T) {
	// Fee should be exactly TakerFeeRate (0.2%) of the base cost.
	est := SimulatePaperFill(0.5, 1000, 10) // small relative to depth
	baseCost := est.PaperShares * est.PaperAvgPrice
	wantFee := baseCost * TakerFeeRate
	if !approxEqualEngine(est.PaperFeeUsdc, wantFee) {
		t.Errorf("fee = %.6f, want %.6f (TakerFeeRate * base)", est.PaperFeeUsdc, wantFee)
	}
}

func TestFillSim_EstimateBestAskFromSpread(t *testing.T) {
	cases := []struct {
		spread float64
		want   float64
	}{
		{0.0, 0.50},
		{0.02, 0.51},
		{0.10, 0.55},
		{1.5, 1.0},  // clip high
		{-2.0, 0.0}, // clip low
	}
	for _, c := range cases {
		got := EstimateBestAskFromSpread(c.spread)
		if !approxEqualEngine(got, c.want) {
			t.Errorf("EstimateBestAskFromSpread(%.2f) = %.4f, want %.4f",
				c.spread, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// AppendFill / ReadFills + append-only invariant
// ---------------------------------------------------------------------------

func mkFill(slug, cond, side string, at time.Time, shares, avg, cost, fee float64, phase PhaseLabel) PaperFill {
	return PaperFill{
		ObservedAt:    at,
		CycleTs:       at.Format("2006-01-02T15-04-05Z"),
		EventSlug:     slug,
		ConditionID:   cond,
		Side:          side,
		PaperShares:   shares,
		PaperAvgPrice: avg,
		PaperCostUsdc: cost,
		PaperFeeUsdc:  fee,
		Phase:         phase,
	}
}

func TestFillsLedger_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fills.jsonl")
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)

	fills := []PaperFill{
		mkFill("evt-1", "0xc1", "a", now, 10, 0.5, 5.01, 0.01, PaperAccumulateA),
		mkFill("evt-1", "0xc1", "b", now, 10, 0.49, 4.91, 0.0098, PaperOpenB),
		mkFill("evt-2", "0xc2", "a", now.Add(time.Minute), 5, 0.4, 2.0, 0.004, PaperAccumulateA),
	}
	for _, f := range fills {
		if err := AppendFill(path, f); err != nil {
			t.Fatalf("AppendFill: %v", err)
		}
	}
	got, err := ReadFills(path)
	if err != nil {
		t.Fatalf("ReadFills: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d fills, want 3", len(got))
	}
	if got[0].EventSlug != "evt-1" || got[2].EventSlug != "evt-2" {
		t.Errorf("order wrong: %v", []string{got[0].EventSlug, got[2].EventSlug})
	}
}

func TestFillsLedger_AppendOnlyInvariant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fills.jsonl")
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)

	if err := AppendFill(path, mkFill("e", "c", "a", now, 1, 0.5, 0.5, 0.001, PaperAccumulateA)); err != nil {
		t.Fatal(err)
	}
	prefixBytes, _ := os.ReadFile(path)
	prefixHash := sha256.Sum256(prefixBytes)

	if err := AppendFill(path, mkFill("e", "c", "b", now.Add(time.Minute), 1, 0.49, 0.49, 0.001, PaperOpenB)); err != nil {
		t.Fatal(err)
	}
	all, _ := os.ReadFile(path)
	if len(all) < len(prefixBytes) {
		t.Fatal("file shrank")
	}
	for i, b := range prefixBytes {
		if all[i] != b {
			t.Fatalf("byte %d changed: %x → %x", i, b, all[i])
		}
	}
	rehash := sha256.Sum256(all[:len(prefixBytes)])
	if hex.EncodeToString(rehash[:]) != hex.EncodeToString(prefixHash[:]) {
		t.Error("prefix SHA changed")
	}
}

func TestFillsLedger_ChronologyGuard_AcceptsSmallRejectsLarge(t *testing.T) {
	// 2026-05-16 relaxation: small lag (concurrent cycles) ok;
	// large lag (>10 min) is rejected as suspected clock drift.
	dir := t.TempDir()
	path := filepath.Join(dir, "fills.jsonl")
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)

	if err := AppendFill(path, mkFill("e", "c", "a", now, 1, 0.5, 0.5, 0.001, PaperAccumulateA)); err != nil {
		t.Fatal(err)
	}
	// 2-min lag → accepted.
	if err := AppendFill(path, mkFill("e", "c", "b", now.Add(-2*time.Minute), 1, 0.49, 0.49, 0.001, PaperOpenB)); err != nil {
		t.Errorf("2-min lag should be accepted, got: %v", err)
	}
	// 1-hour lag → rejected.
	err := AppendFill(path, mkFill("e", "c", "b", now.Add(-time.Hour), 1, 0.49, 0.49, 0.001, PaperOpenB))
	if err == nil {
		t.Error("expected error for 1-hour lag (>10min)")
	}
}

func TestFillsLedger_AppendValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fills.jsonl")
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		fill PaperFill
	}{
		{"empty slug", PaperFill{ConditionID: "c", Side: "a", ObservedAt: now, Phase: PaperAccumulateA}},
		{"empty cond", PaperFill{EventSlug: "e", Side: "a", ObservedAt: now, Phase: PaperAccumulateA}},
		{"bad side", PaperFill{EventSlug: "e", ConditionID: "c", Side: "x", ObservedAt: now, Phase: PaperAccumulateA}},
		{"zero time", PaperFill{EventSlug: "e", ConditionID: "c", Side: "a", Phase: PaperAccumulateA}},
		{"invalid phase", PaperFill{EventSlug: "e", ConditionID: "c", Side: "a", ObservedAt: now, Phase: "BUY"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := AppendFill(path, c.fill); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

func TestFillsLedger_ReadMissingFile(t *testing.T) {
	got, err := ReadFills(filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil {
		t.Errorf("expected nil error for missing file, got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 fills, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// BuildPaperLegs
// ---------------------------------------------------------------------------

func TestPosition_BuildLegs_SingleTwoSidedFill(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		mkFill("evt", "0xc", "a", now, 10, 0.51, 5.10, 0.01, PaperAccumulateA),
		mkFill("evt", "0xc", "b", now, 10, 0.48, 4.80, 0.01, PaperOpenB),
	}
	legs := BuildPaperLegs(fills)
	if len(legs) != 1 {
		t.Fatalf("got %d legs, want 1", len(legs))
	}
	leg := legs["evt|0xc"]
	if leg.PaperSharesA != 10 || leg.PaperSharesB != 10 {
		t.Errorf("shares = %.1f/%.1f, want 10/10", leg.PaperSharesA, leg.PaperSharesB)
	}
	if !approxEqualEngine(leg.PaperWavgA, 0.51) || !approxEqualEngine(leg.PaperWavgB, 0.48) {
		t.Errorf("WAVG = %.4f/%.4f", leg.PaperWavgA, leg.PaperWavgB)
	}
	if !approxEqualEngine(leg.PaperSumWavg, 0.99) {
		t.Errorf("SumWavg = %.4f, want 0.99", leg.PaperSumWavg)
	}
	if leg.CurrentPhase != PaperOpenB {
		t.Errorf("current phase = %q, want paper_open_b", leg.CurrentPhase)
	}
}

func TestPosition_BuildLegs_OneSideOnly(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		mkFill("evt", "0xc", "a", now, 10, 0.5, 5.0, 0.01, PaperAccumulateA),
	}
	legs := BuildPaperLegs(fills)
	leg := legs["evt|0xc"]
	if leg.CurrentPhase != PaperAccumulateA {
		t.Errorf("phase = %q, want paper_accumulate_a", leg.CurrentPhase)
	}
	if leg.PaperSumWavg != 0 || !leg.MinPaperSumWavgAt.IsZero() {
		t.Errorf("one-sided leg should have zero SumWavg/MinAt, got %.4f / %v",
			leg.PaperSumWavg, leg.MinPaperSumWavgAt)
	}
}

func TestPosition_BuildLegs_RunningMinTracking(t *testing.T) {
	// Three two-sided fills with running SumWavg = 1.0, 0.95, 0.90.
	// MinPaperSumWavg should land at 0.90.
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		mkFill("evt", "0xc", "a", now, 10, 0.55, 5.5, 0.011, PaperAccumulateA),
		mkFill("evt", "0xc", "b", now, 10, 0.45, 4.5, 0.009, PaperOpenB),
		// Now wavg_a=0.55, wavg_b=0.45, sum=1.00
		mkFill("evt", "0xc", "a", now.Add(6*time.Hour), 10, 0.45, 4.5, 0.009, PaperLayer),
		mkFill("evt", "0xc", "b", now.Add(6*time.Hour), 10, 0.40, 4.0, 0.008, PaperLayer),
		// Now wavg_a = (5.5+4.5)/20 = 0.5; wavg_b = (4.5+4.0)/20 = 0.425; sum=0.925
		mkFill("evt", "0xc", "a", now.Add(12*time.Hour), 10, 0.35, 3.5, 0.007, PaperLayer),
		mkFill("evt", "0xc", "b", now.Add(12*time.Hour), 10, 0.35, 3.5, 0.007, PaperLayer),
		// wavg_a = (5.5+4.5+3.5)/30 = 0.45; wavg_b = (4.5+4.0+3.5)/30 = 0.4; sum=0.85
	}
	legs := BuildPaperLegs(fills)
	leg := legs["evt|0xc"]
	if leg.PaperSharesA != 30 || leg.PaperSharesB != 30 {
		t.Errorf("shares = %.1f/%.1f, want 30/30", leg.PaperSharesA, leg.PaperSharesB)
	}
	if !approxEqualEngine(leg.PaperSumWavg, 0.85) {
		t.Errorf("final SumWavg = %.4f, want 0.85", leg.PaperSumWavg)
	}
	if leg.MinPaperSumWavg > 0.86 {
		t.Errorf("MinPaperSumWavg = %.4f, want <= 0.85", leg.MinPaperSumWavg)
	}
}

func TestPosition_BuildLegs_GroupsByCondition(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		mkFill("evt-1", "0xc1", "a", now, 1, 0.5, 0.5, 0.001, PaperAccumulateA),
		mkFill("evt-1", "0xc2", "a", now, 1, 0.5, 0.5, 0.001, PaperAccumulateA),
		mkFill("evt-2", "0xc1", "a", now, 1, 0.5, 0.5, 0.001, PaperAccumulateA),
	}
	legs := BuildPaperLegs(fills)
	if len(legs) != 3 {
		t.Errorf("got %d distinct legs, want 3", len(legs))
	}
}

func TestPosition_BuildLegs_PhaseHistory(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		mkFill("evt", "0xc", "a", now, 5, 0.5, 2.5, 0.005, PaperAccumulateA),
		mkFill("evt", "0xc", "b", now, 5, 0.5, 2.5, 0.005, PaperOpenB),
		mkFill("evt", "0xc", "a", now.Add(6*time.Hour), 5, 0.45, 2.25, 0.0045, PaperLayer),
	}
	legs := BuildPaperLegs(fills)
	leg := legs["evt|0xc"]
	wantHistory := []PhaseLabel{PaperAccumulateA, PaperOpenB, PaperLayer}
	if len(leg.PhaseHistory) != len(wantHistory) {
		t.Fatalf("PhaseHistory len = %d, want %d", len(leg.PhaseHistory), len(wantHistory))
	}
	for i := range wantHistory {
		if leg.PhaseHistory[i] != wantHistory[i] {
			t.Errorf("PhaseHistory[%d] = %q, want %q", i, leg.PhaseHistory[i], wantHistory[i])
		}
	}
}

// ---------------------------------------------------------------------------
// BuildPaperPositions
// ---------------------------------------------------------------------------

func TestPosition_BuildPositions_AggregatesCostAndPhase(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	fills := []PaperFill{
		// evt-1: 2 markets, both two-sided → paper_open_b
		mkFill("evt-1", "0xc1", "a", now, 10, 0.5, 5.0, 0.01, PaperAccumulateA),
		mkFill("evt-1", "0xc1", "b", now, 10, 0.5, 5.0, 0.01, PaperOpenB),
		mkFill("evt-1", "0xc2", "a", now, 5, 0.5, 2.5, 0.005, PaperAccumulateA),
		mkFill("evt-1", "0xc2", "b", now, 5, 0.5, 2.5, 0.005, PaperOpenB),
		// evt-2: 1 market, A-only → paper_accumulate_a
		mkFill("evt-2", "0xc1", "a", now, 3, 0.4, 1.2, 0.003, PaperAccumulateA),
	}
	positions := BuildPaperPositions(fills)
	if len(positions) != 2 {
		t.Fatalf("got %d positions, want 2", len(positions))
	}
	e1 := positions["evt-1"]
	if e1.CurrentPhase != PaperOpenB {
		t.Errorf("evt-1 phase = %q, want paper_open_b", e1.CurrentPhase)
	}
	wantCost := 5.0 + 5.0 + 2.5 + 2.5
	if !approxEqualEngine(e1.PaperTotalCostUsdc, wantCost) {
		t.Errorf("evt-1 cost = %.2f, want %.2f", e1.PaperTotalCostUsdc, wantCost)
	}
	if len(e1.Legs) != 2 {
		t.Errorf("evt-1 legs = %d, want 2", len(e1.Legs))
	}
	e2 := positions["evt-2"]
	if e2.CurrentPhase != PaperAccumulateA {
		t.Errorf("evt-2 phase = %q, want paper_accumulate_a", e2.CurrentPhase)
	}
}

func TestPosition_BuildPositions_PhaseRankUsesMostAdvanced(t *testing.T) {
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	// One leg only entered (A); another fully OPEN_B; another LAYER.
	// Position phase should be PaperLayer (highest rank among the
	// leg phases).
	fills := []PaperFill{
		mkFill("evt", "0xc1", "a", now, 1, 0.5, 0.5, 0.001, PaperAccumulateA),
		mkFill("evt", "0xc2", "a", now, 1, 0.5, 0.5, 0.001, PaperAccumulateA),
		mkFill("evt", "0xc2", "b", now, 1, 0.5, 0.5, 0.001, PaperOpenB),
		mkFill("evt", "0xc3", "a", now, 1, 0.5, 0.5, 0.001, PaperAccumulateA),
		mkFill("evt", "0xc3", "b", now, 1, 0.5, 0.5, 0.001, PaperOpenB),
		mkFill("evt", "0xc3", "a", now.Add(time.Minute), 1, 0.45, 0.45, 0.001, PaperLayer),
		mkFill("evt", "0xc3", "b", now.Add(time.Minute), 1, 0.45, 0.45, 0.001, PaperLayer),
	}
	positions := BuildPaperPositions(fills)
	pos := positions["evt"]
	// c1 is accumulate_a (1); c2 is open_b (2); c3 had a layer
	// fill which appears in PhaseHistory — its CurrentPhase is
	// open_b (derived from filled state), so position-level
	// CurrentPhase is open_b (2), NOT layer. PR-3 will introduce
	// state transitions that mutate CurrentPhase beyond the
	// entry-side derivation.
	if pos.CurrentPhase != PaperOpenB {
		t.Errorf("position phase = %q, want paper_open_b (highest entry-side leg phase in PR-2)", pos.CurrentPhase)
	}
}

func TestPosition_BuildPositions_NoFills(t *testing.T) {
	positions := BuildPaperPositions(nil)
	if len(positions) != 0 {
		t.Errorf("expected 0 positions for nil fills, got %d", len(positions))
	}
}

// ---------------------------------------------------------------------------
// EnsureFillsDir helper
// ---------------------------------------------------------------------------

func TestFillsLedger_EnsureDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nest", "fills.jsonl")
	if err := EnsureFillsDir(path); err != nil {
		t.Errorf("EnsureFillsDir: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("expected dir to exist: %v", err)
	}
}

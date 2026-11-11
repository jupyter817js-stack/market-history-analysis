package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

func writeScanFixture(t *testing.T, dir, name string, cands []scanner.Candidate) string {
	t.Helper()
	body, _ := json.Marshal(cands)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func defaultInput(scanPath, paperDir, cycleTs string, now time.Time) ProcessCycleInput {
	return ProcessCycleInput{
		ScanCandidatesPath: scanPath,
		CycleTs:            cycleTs,
		PaperDir:           paperDir,
		Limits:             DefaultSizingLimits(),
		EntryWindow:        DefaultEntryWindow(),
		RiskLimits:         DefaultRiskLimits(),
		Now:                now,
	}
}

// ---------------------------------------------------------------------------
// ProcessCycle: open path
// ---------------------------------------------------------------------------

func TestCycle_OpensPositionOnFirstScan(t *testing.T) {
	dir := t.TempDir()
	paper := filepath.Join(dir, "paper")
	_ = EnsurePaperDir(paper)

	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt-open")
	scanPath := writeScanFixture(t, dir, "c.json", []scanner.Candidate{c})

	res, err := ProcessCycle(defaultInput(scanPath, paper, "cycle-1", now))
	if err != nil {
		t.Fatalf("ProcessCycle: %v", err)
	}
	if res.EntriesOpened != 1 {
		t.Errorf("EntriesOpened = %d, want 1", res.EntriesOpened)
	}
	if res.FillsAppended != 10 {
		t.Errorf("FillsAppended = %d, want 10 (5 markets × 2 sides)", res.FillsAppended)
	}
	if res.MergesTriggered != 0 || res.RedemptionsTriggered != 0 {
		t.Errorf("first-cycle should have 0 merges/redemptions, got %d/%d",
			res.MergesTriggered, res.RedemptionsTriggered)
	}
	// pnl_summary.json should exist with the 10 fills counted.
	summaryBody, err := os.ReadFile(filepath.Join(paper, "pnl_summary.json"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var s PaperPnlSummary
	_ = json.Unmarshal(summaryBody, &s)
	if s.LifetimeFillsCount != 10 {
		t.Errorf("summary fills = %d, want 10", s.LifetimeFillsCount)
	}
	if s.OpenPositionsCount != 1 {
		t.Errorf("open positions = %d, want 1", s.OpenPositionsCount)
	}
}

// ---------------------------------------------------------------------------
// ProcessCycle: idempotent (no re-opening already-held)
// ---------------------------------------------------------------------------

func TestCycle_DoesNotReOpenExistingPosition(t *testing.T) {
	dir := t.TempDir()
	paper := filepath.Join(dir, "paper")
	_ = EnsurePaperDir(paper)
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt-x")
	scanPath := writeScanFixture(t, dir, "c.json", []scanner.Candidate{c})

	// Cycle 1: open.
	res1, err := ProcessCycle(defaultInput(scanPath, paper, "cycle-1", now))
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if res1.EntriesOpened != 1 {
		t.Fatal("expected open in cycle 1")
	}

	// Cycle 2: same scan; nothing new should open or expand.
	res2, err := ProcessCycle(defaultInput(scanPath, paper, "cycle-2", now.Add(6*time.Hour)))
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if res2.EntriesOpened != 0 {
		t.Errorf("EntriesOpened in cycle 2 = %d, want 0 (idempotent)", res2.EntriesOpened)
	}
	if res2.EntriesExpanded != 0 {
		t.Errorf("EntriesExpanded in cycle 2 = %d, want 0 (no new markets)", res2.EntriesExpanded)
	}
}

// ---------------------------------------------------------------------------
// ProcessCycle: MERGE fires on second cycle when envelope drops
// ---------------------------------------------------------------------------

func TestCycle_MergesWhenEnvelopeFavorable(t *testing.T) {
	dir := t.TempDir()
	paper := filepath.Join(dir, "paper")
	_ = EnsurePaperDir(paper)

	now1 := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	// Cycle 1: open at spread 0.02 → ask≈0.51, sumWavg ≈ 1.02. No MERGE.
	c1 := mkProbeCandidate("evt-merge")
	scanPath1 := writeScanFixture(t, dir, "c1.json", []scanner.Candidate{c1})
	_, err := ProcessCycle(defaultInput(scanPath1, paper, "cycle-1", now1))
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}

	// Cycle 2: 60 min later, same scan -- still no MERGE (envelope
	// stays >= 1.0 because we haven't accumulated additional shares
	// at lower prices yet).
	now2 := now1.Add(60 * time.Minute)
	res2, err := ProcessCycle(defaultInput(scanPath1, paper, "cycle-2", now2))
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if res2.MergesTriggered > 0 {
		t.Errorf("MERGE should not fire when envelope >= threshold; got %d", res2.MergesTriggered)
	}
}

// ---------------------------------------------------------------------------
// ProcessCycle: REDEEM fires when event drops + start passed
// ---------------------------------------------------------------------------

func TestCycle_RedeemsWhenEventResolves(t *testing.T) {
	dir := t.TempDir()
	paper := filepath.Join(dir, "paper")
	_ = EnsurePaperDir(paper)
	now1 := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	startTs := now1.Add(3 * time.Hour) // within window

	c := mkProbeCandidate("evt-x", func(cc *scanner.Candidate) {
		cc.StartTs = &startTs
	})
	scanPath1 := writeScanFixture(t, dir, "c1.json", []scanner.Candidate{c})

	// Cycle 1: open.
	if _, err := ProcessCycle(defaultInput(scanPath1, paper, "cycle-1", now1)); err != nil {
		t.Fatal(err)
	}

	// Cycle 2: 4 hours later, event has dropped from scan AND
	// start (3h after now1 = 1h ago) has passed → REDEEM should fire.
	now2 := now1.Add(4 * time.Hour)
	scanPath2 := writeScanFixture(t, dir, "c2.json", []scanner.Candidate{})
	res, err := ProcessCycle(defaultInput(scanPath2, paper, "cycle-2", now2))
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	// 5 markets → 5 redemption rows.
	if res.RedemptionsTriggered != 5 {
		t.Errorf("RedemptionsTriggered = %d, want 5", res.RedemptionsTriggered)
	}
	// Verify ledger.
	reds, _ := ReadRedemptions(filepath.Join(paper, "redemptions.jsonl"))
	if len(reds) != 5 {
		t.Errorf("redemptions ledger has %d rows, want 5", len(reds))
	}
	for _, r := range reds {
		if r.WinningSide != "unknown" {
			t.Errorf("PR-5 redeem should default to unknown, got %q", r.WinningSide)
		}
	}
}

// ---------------------------------------------------------------------------
// ProcessCycle: stop-loss caps further entries
// ---------------------------------------------------------------------------

func TestCycle_StoppedPositionDoesNotReOpen(t *testing.T) {
	dir := t.TempDir()
	paper := filepath.Join(dir, "paper")
	_ = EnsurePaperDir(paper)

	// Pre-seed a fills + redemptions ledger that puts the position
	// in stop-loss territory (PnL << -50).
	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	c := mkProbeCandidate("evt-stop")
	scanPath := writeScanFixture(t, dir, "c.json", []scanner.Candidate{c})

	// Manually write a fill + a large-loss redemption.
	_ = EnsureFillsDir(filepath.Join(paper, "fills.jsonl"))
	_ = AppendFill(filepath.Join(paper, "fills.jsonl"), PaperFill{
		ObservedAt: now.Add(-24 * time.Hour), CycleTs: "prior",
		EventSlug: "evt-stop", ConditionID: "0xc1",
		Side: "a", PaperShares: 100, PaperAvgPrice: 0.6,
		PaperCostUsdc: 60.06, PaperFeeUsdc: 0.06, Phase: PaperAccumulateA,
	})
	_ = AppendFill(filepath.Join(paper, "fills.jsonl"), PaperFill{
		ObservedAt: now.Add(-24 * time.Hour), CycleTs: "prior",
		EventSlug: "evt-stop", ConditionID: "0xc1",
		Side: "b", PaperShares: 100, PaperAvgPrice: 0.6,
		PaperCostUsdc: 60.06, PaperFeeUsdc: 0.06, Phase: PaperOpenB,
	})
	// Redemption with huge loss → position should be marked stopped.
	_ = AppendRedemption(filepath.Join(paper, "redemptions.jsonl"), PaperRedemption{
		ObservedAt: now.Add(-1 * time.Hour), EventSlug: "evt-stop",
		ConditionID: "0xc1", WinningSide: "draw",
		PaperRedeemedShares: 200, PaperRedeemedCostUsdc: 120.12,
		PaperRedeemedRealizedUsdc: 0, PaperRedeemPnlUsdc: -120.12,
	})

	res, err := ProcessCycle(defaultInput(scanPath, paper, "cycle", now))
	if err != nil {
		t.Fatalf("ProcessCycle: %v", err)
	}
	if res.PositionsStopped < 1 {
		t.Errorf("expected at least 1 position stopped, got %d", res.PositionsStopped)
	}
	// The position should NOT reopen, even though the scanner
	// reports evt-stop as a candidate (it's still in PaperStopped).
	// Note: this candidate is for evt-stop, but the position is
	// in PaperStopped → PlanEntry returns skip. New fills should be 0.
	if res.FillsAppended != 0 {
		t.Errorf("stopped position should not gain new fills, got %d", res.FillsAppended)
	}
}

// ---------------------------------------------------------------------------
// ProcessCycle: daily kill blocks NEW entries
// ---------------------------------------------------------------------------

func TestCycle_DailyKillBlocksNewEntries(t *testing.T) {
	dir := t.TempDir()
	paper := filepath.Join(dir, "paper")
	_ = EnsurePaperDir(paper)

	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	// Pre-seed merges with a huge recent loss.
	_ = AppendMerge(filepath.Join(paper, "merges.jsonl"), PaperMerge{
		ObservedAt: now.Add(-1 * time.Hour),
		EventSlug:  "evt-prior", ConditionID: "0xprior",
		PaperMergedUnits: 10, PaperMergedCostUsdc: 20,
		PaperMergedRealizedUsdc: 10, PaperMergePnlUsdc: -210,
	})

	c := mkProbeCandidate("evt-new")
	scanPath := writeScanFixture(t, dir, "c.json", []scanner.Candidate{c})

	res, err := ProcessCycle(defaultInput(scanPath, paper, "cycle", now))
	if err != nil {
		t.Fatalf("ProcessCycle: %v", err)
	}
	if !res.DailyKillActive {
		t.Errorf("daily kill should be active at -210 < -200")
	}
	if res.EntriesOpened != 0 {
		t.Errorf("EntriesOpened = %d, want 0 (daily kill blocks new entries)", res.EntriesOpened)
	}
}

// ---------------------------------------------------------------------------
// ProcessCycle: respects per-event and daily caps
// ---------------------------------------------------------------------------

func TestCycle_RespectsMaxDailyNewEntries(t *testing.T) {
	dir := t.TempDir()
	paper := filepath.Join(dir, "paper")
	_ = EnsurePaperDir(paper)

	now := time.Date(2026, 5, 16, 8, 0, 0, 0, time.UTC)
	// Generate 15 distinct PROBE-eligible candidates.
	var cands []scanner.Candidate
	for i := 0; i < 15; i++ {
		slug := "evt-" + string(rune('a'+i))
		cands = append(cands, mkProbeCandidate(slug))
	}
	scanPath := writeScanFixture(t, dir, "c.json", cands)

	res, err := ProcessCycle(defaultInput(scanPath, paper, "cycle", now))
	if err != nil {
		t.Fatalf("ProcessCycle: %v", err)
	}
	// Default MaxDailyNewEntries = 10, so we open 10 + skip 5.
	if res.EntriesOpened != 10 {
		t.Errorf("EntriesOpened = %d, want 10 (cap)", res.EntriesOpened)
	}
}

// ---------------------------------------------------------------------------
// ProcessCycle: validates input
// ---------------------------------------------------------------------------

func TestCycle_FailsOnMissingScanPath(t *testing.T) {
	in := ProcessCycleInput{
		ScanCandidatesPath: filepath.Join(t.TempDir(), "missing.json"),
		PaperDir:           t.TempDir(),
		Now:                time.Now().UTC(),
	}
	_, err := ProcessCycle(in)
	if err == nil {
		t.Error("expected error for missing scan path")
	}
}

// TestCycle_UnknownRedemptionDoesNotTriggerStop locks in the
// 2026-05-16 production fix: UNKNOWN-side redemptions must NOT
// reduce realized PnL (or trigger the per-event stop-loss). The
// position transitions to paper_idle but its PnL stays at 0
// until a future resolve-settlements pass supplies the winning
// side.
func TestCycle_UnknownRedemptionDoesNotTriggerStop(t *testing.T) {
	dir := t.TempDir()
	paper := filepath.Join(dir, "paper")
	_ = EnsurePaperDir(paper)

	now := time.Date(2026, 5, 16, 22, 0, 0, 0, time.UTC)
	// Pre-seed fills (cost basis ~ $200 on each side of one
	// market) + an UNKNOWN-side redemption that, under the OLD
	// code, would have booked a -$400 loss and tripped the
	// per-event stop-loss.
	_ = EnsureFillsDir(filepath.Join(paper, "fills.jsonl"))
	_ = AppendFill(filepath.Join(paper, "fills.jsonl"), PaperFill{
		ObservedAt: now.Add(-2 * time.Hour), CycleTs: "prior",
		EventSlug: "evt-unk", ConditionID: "0xc1",
		Side: "a", PaperShares: 400, PaperAvgPrice: 0.5,
		PaperCostUsdc: 200.4, PaperFeeUsdc: 0.4, Phase: PaperAccumulateA,
	})
	_ = AppendFill(filepath.Join(paper, "fills.jsonl"), PaperFill{
		ObservedAt: now.Add(-2 * time.Hour), CycleTs: "prior",
		EventSlug: "evt-unk", ConditionID: "0xc1",
		Side: "b", PaperShares: 400, PaperAvgPrice: 0.5,
		PaperCostUsdc: 200.4, PaperFeeUsdc: 0.4, Phase: PaperOpenB,
	})
	_ = AppendRedemption(filepath.Join(paper, "redemptions.jsonl"), PaperRedemption{
		ObservedAt: now.Add(-30 * time.Minute), EventSlug: "evt-unk",
		ConditionID: "0xc1", WinningSide: "unknown",
		PaperRedeemedShares: 800, PaperRedeemedCostUsdc: 400.8,
		PaperRedeemedRealizedUsdc: 0, PaperRedeemPnlUsdc: -400.8,
	})

	// Empty scan -- triggers no new entries; just re-derives state.
	scanPath := writeScanFixture(t, dir, "c.json", nil)
	res, err := ProcessCycle(defaultInput(scanPath, paper, "cycle", now))
	if err != nil {
		t.Fatalf("ProcessCycle: %v", err)
	}
	// No new stops: UNKNOWN redemption must not have triggered.
	if res.PositionsStopped != 0 {
		t.Errorf("UNKNOWN redemption should NOT trigger stop, got %d stopped", res.PositionsStopped)
	}
	// Position summary should show idle + PnL = 0 (not -400).
	summaryBody, _ := os.ReadFile(filepath.Join(paper, "pnl_summary.json"))
	var s PaperPnlSummary
	_ = json.Unmarshal(summaryBody, &s)
	if s.StoppedPositionsCount != 0 {
		t.Errorf("stopped count = %d, want 0", s.StoppedPositionsCount)
	}
	if s.IdlePositionsCount != 1 {
		t.Errorf("idle count = %d, want 1 (post-redemption)", s.IdlePositionsCount)
	}
	if s.PaperRedeemPnlUsdc != 0 {
		t.Errorf("redeem PnL = %.4f, want 0 (UNKNOWN deferred)", s.PaperRedeemPnlUsdc)
	}
	if s.PaperNetPnlUsdc != 0 {
		t.Errorf("net PnL = %.4f, want 0 (UNKNOWN deferred)", s.PaperNetPnlUsdc)
	}
}

// TestCycle_KnownDrawRedemptionStillCountsAsLoss complements the
// UNKNOWN test: a DRAW redemption is a REAL outcome (event ended
// in a tie, no payout) and SHOULD impact PnL + stop-loss.
func TestCycle_KnownDrawRedemptionStillCountsAsLoss(t *testing.T) {
	dir := t.TempDir()
	paper := filepath.Join(dir, "paper")
	_ = EnsurePaperDir(paper)

	now := time.Date(2026, 5, 16, 22, 0, 0, 0, time.UTC)
	_ = EnsureFillsDir(filepath.Join(paper, "fills.jsonl"))
	_ = AppendFill(filepath.Join(paper, "fills.jsonl"), PaperFill{
		ObservedAt: now.Add(-2 * time.Hour), CycleTs: "prior",
		EventSlug: "evt-draw", ConditionID: "0xc1",
		Side: "a", PaperShares: 200, PaperAvgPrice: 0.5,
		PaperCostUsdc: 100.2, PaperFeeUsdc: 0.2, Phase: PaperAccumulateA,
	})
	_ = AppendFill(filepath.Join(paper, "fills.jsonl"), PaperFill{
		ObservedAt: now.Add(-2 * time.Hour), CycleTs: "prior",
		EventSlug: "evt-draw", ConditionID: "0xc1",
		Side: "b", PaperShares: 200, PaperAvgPrice: 0.5,
		PaperCostUsdc: 100.2, PaperFeeUsdc: 0.2, Phase: PaperOpenB,
	})
	_ = AppendRedemption(filepath.Join(paper, "redemptions.jsonl"), PaperRedemption{
		ObservedAt: now.Add(-30 * time.Minute), EventSlug: "evt-draw",
		ConditionID: "0xc1", WinningSide: "draw",
		PaperRedeemedShares: 400, PaperRedeemedCostUsdc: 200.4,
		PaperRedeemedRealizedUsdc: 0, PaperRedeemPnlUsdc: -200.4,
	})

	scanPath := writeScanFixture(t, dir, "c.json", nil)
	_, err := ProcessCycle(defaultInput(scanPath, paper, "cycle", now))
	if err != nil {
		t.Fatalf("ProcessCycle: %v", err)
	}
	summaryBody, _ := os.ReadFile(filepath.Join(paper, "pnl_summary.json"))
	var s PaperPnlSummary
	_ = json.Unmarshal(summaryBody, &s)
	if s.PaperRedeemPnlUsdc != -200.4 {
		t.Errorf("draw PnL = %.4f, want -200.4 (real loss, not deferred)", s.PaperRedeemPnlUsdc)
	}
	if s.StoppedPositionsCount != 1 {
		t.Errorf("draw should trip per-event stop, got %d stopped", s.StoppedPositionsCount)
	}
}

func TestCycle_EmptyScanProducesNoFills(t *testing.T) {
	dir := t.TempDir()
	paper := filepath.Join(dir, "paper")
	_ = EnsurePaperDir(paper)
	scanPath := writeScanFixture(t, dir, "c.json", []scanner.Candidate{})
	res, err := ProcessCycle(defaultInput(scanPath, paper, "cycle", time.Now().UTC()))
	if err != nil {
		t.Fatalf("ProcessCycle: %v", err)
	}
	if res.FillsAppended != 0 || res.EntriesOpened != 0 {
		t.Errorf("empty scan should produce 0 actions, got %+v", res)
	}
}

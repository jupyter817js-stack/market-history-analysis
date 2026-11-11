package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

// ProcessCycleInput configures one engine tick.
type ProcessCycleInput struct {
	ScanCandidatesPath string
	CycleTs            string
	PaperDir           string // root of .paper/
	Limits             SizingLimits
	EntryWindow        EntryWindow
	RiskLimits         RiskLimits
	Now                time.Time
}

// ProcessCycleResult is the per-call summary printed by the CLI.
type ProcessCycleResult struct {
	CycleTs              string  `json:"cycle_ts"`
	ScanCandidates       int     `json:"scan_candidates"`
	EntriesOpened        int     `json:"entries_opened"`
	EntriesExpanded      int     `json:"entries_expanded"`
	EntriesSkipped       int     `json:"entries_skipped"`
	FillsAppended        int     `json:"fills_appended"`
	MergesTriggered      int     `json:"merges_triggered"`
	RedemptionsTriggered int     `json:"redemptions_triggered"`
	PositionsStopped     int     `json:"positions_stopped"`
	DailyKillActive      bool    `json:"daily_kill_active"`
	PaperNetPnlUsdc      float64 `json:"paper_net_pnl_usdc"`
}

// ProcessCycle is the orchestration entrypoint called once per 6h
// cycle by the CLI. It:
//
//  1. Reads candidates.json from the cycle directory.
//  2. Rebuilds position state from existing fills + merges +
//     redemptions ledgers (each is append-only; positions are
//     derived).
//  3. Enforces daily kill: if rolling 24h paper PnL is below the
//     kill threshold, new entries are blocked (existing positions
//     still MERGE / REDEEM).
//  4. For each candidate, calls PlanEntry and appends accepted
//     fills (respecting the active-positions cap and the
//     daily-new-entries cap).
//  5. For each existing leg not yet merged, calls PlanMerge; if
//     triggered, appends a PaperMerge.
//  6. For each event no longer in scan whose StartTs has passed,
//     calls PlanRedeem with winning_side="unknown" for every leg
//     not yet redeemed; appends PaperRedemption rows.
//  7. Marks positions paper_stopped when per-event PnL crosses the
//     stop-loss threshold (state derived; no separate ledger).
//  8. Rebuilds pnl_summary.json from the fresh ledgers.
//
// All ledger writes are append-only. Idempotent on merges +
// redemptions (won't re-fire once recorded for a given
// event/condition).
func ProcessCycle(in ProcessCycleInput) (ProcessCycleResult, error) {
	res := ProcessCycleResult{CycleTs: in.CycleTs}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}

	// Resolve ledger paths.
	fillsPath := filepath.Join(in.PaperDir, "fills.jsonl")
	mergesPath := filepath.Join(in.PaperDir, "merges.jsonl")
	redemptionsPath := filepath.Join(in.PaperDir, "redemptions.jsonl")
	summaryPath := filepath.Join(in.PaperDir, "pnl_summary.json")

	// Read candidates.
	cands, err := readScanCandidates(in.ScanCandidatesPath)
	if err != nil {
		return res, fmt.Errorf("read candidates: %w", err)
	}
	res.ScanCandidates = len(cands)

	// Read existing ledgers + rebuild position state.
	existingFills, err := ReadFills(fillsPath)
	if err != nil {
		return res, fmt.Errorf("read fills: %w", err)
	}
	existingMerges, err := ReadMerges(mergesPath)
	if err != nil {
		return res, fmt.Errorf("read merges: %w", err)
	}
	existingRedemptions, err := ReadRedemptions(redemptionsPath)
	if err != nil {
		return res, fmt.Errorf("read redemptions: %w", err)
	}

	positions := BuildPaperPositions(existingFills)
	// Apply realized PnL from merges + redemptions to each position.
	applyRealizedPnl(positions, existingMerges, existingRedemptions, in.RiskLimits)

	// Daily kill check.
	dailyKill := CheckDailyKill(existingMerges, existingRedemptions, in.Now, in.RiskLimits)
	res.DailyKillActive = dailyKill.ShouldPause

	// Helper sets: merged legs + redeemed legs (so we don't double-fire).
	mergedKey := func(slug, cond string) string { return slug + "|" + cond }
	mergedSet := map[string]bool{}
	for _, m := range existingMerges {
		mergedSet[mergedKey(m.EventSlug, m.ConditionID)] = true
	}
	redeemedSet := map[string]bool{}
	for _, r := range existingRedemptions {
		redeemedSet[mergedKey(r.EventSlug, r.ConditionID)] = true
	}

	// Track newly-stopped this cycle.
	stoppedSlugs := map[string]bool{}

	// Cycle limits.
	activeCount := CountActivePositions(positions)
	newToday := CountNewEntriesIn24h(existingFills, in.Now)
	candidateBySlug := map[string]*scanner.Candidate{}
	for i := range cands {
		candidateBySlug[cands[i].EventSlug] = &cands[i]
	}

	// 4. Per-candidate entry decisions.
	for i := range cands {
		c := &cands[i]
		existing := positions[c.EventSlug]
		out := PlanEntry(EntryDecisionInput{
			Candidate:        c,
			ExistingPosition: existing,
			Limits:           in.Limits,
			EntryWindow:      in.EntryWindow,
			Now:              in.Now,
			CycleTs:          in.CycleTs,
		})
		switch out.Action {
		case ActionSkip:
			res.EntriesSkipped++
			continue
		case ActionOpen:
			// Caps gate new entries only.
			if dailyKill.ShouldPause {
				res.EntriesSkipped++
				continue
			}
			if activeCount >= in.Limits.MaxActivePaperEvents {
				res.EntriesSkipped++
				continue
			}
			if newToday >= in.Limits.MaxDailyNewEntries {
				res.EntriesSkipped++
				continue
			}
			if err := appendAllFills(fillsPath, out.ProposedFills); err != nil {
				return res, err
			}
			res.EntriesOpened++
			res.FillsAppended += len(out.ProposedFills)
			activeCount++
			newToday++
		case ActionExpand:
			if existing != nil && existing.CurrentPhase == PaperStopped {
				res.EntriesSkipped++
				continue
			}
			if err := appendAllFills(fillsPath, out.ProposedFills); err != nil {
				return res, err
			}
			res.EntriesExpanded++
			res.FillsAppended += len(out.ProposedFills)
		}
	}

	// Rebuild positions with the newly-appended fills.
	if res.FillsAppended > 0 {
		updatedFills, err := ReadFills(fillsPath)
		if err != nil {
			return res, fmt.Errorf("re-read fills: %w", err)
		}
		positions = BuildPaperPositions(updatedFills)
		applyRealizedPnl(positions, existingMerges, existingRedemptions, in.RiskLimits)
	}

	// 5. MERGE pass.
	for slug, pos := range positions {
		if pos.CurrentPhase == PaperIdle {
			continue
		}
		// Note: PaperStopped positions still get MERGE attempts.
		for _, leg := range pos.Legs {
			key := mergedKey(slug, leg.ConditionID)
			if mergedSet[key] {
				continue
			}
			m, ok, _ := PlanMerge(leg, in.Now, in.CycleTs)
			if !ok {
				continue
			}
			if err := AppendMerge(mergesPath, m); err != nil {
				return res, err
			}
			mergedSet[key] = true
			res.MergesTriggered++
			existingMerges = append(existingMerges, m)
		}
	}

	// 6. REDEEM pass for events dropped from scan + start passed.
	for slug, pos := range positions {
		if _, stillInScan := candidateBySlug[slug]; stillInScan {
			continue
		}
		if pos.EventStartTs == nil {
			// Try to recover StartTs from any leg's prior fill (unknown
			// without it). For PR-5 we skip; the orchestrator's caller
			// can re-add slugs with known start times in a follow-up.
			continue
		}
		if pos.EventStartTs.After(in.Now) {
			continue
		}
		// Event has resolved; redeem each leg that hasn't been redeemed.
		for _, leg := range pos.Legs {
			key := mergedKey(slug, leg.ConditionID)
			if redeemedSet[key] {
				continue
			}
			// Compute remaining shares after any prior MERGE.
			merged := mergedUnitsFor(existingMerges, slug, leg.ConditionID)
			remainingA := leg.PaperSharesA - merged
			remainingB := leg.PaperSharesB - merged
			if remainingA < 0 {
				remainingA = 0
			}
			if remainingB < 0 {
				remainingB = 0
			}
			r := PlanRedeem(leg, remainingA, remainingB, "unknown", in.Now, in.CycleTs)
			if err := AppendRedemption(redemptionsPath, r); err != nil {
				return res, err
			}
			redeemedSet[key] = true
			res.RedemptionsTriggered++
			existingRedemptions = append(existingRedemptions, r)
		}
	}

	// 7. Mark positions paper_stopped (state derived from PnL).
	finalFills, _ := ReadFills(fillsPath)
	finalPositions := BuildPaperPositions(finalFills)
	applyRealizedPnl(finalPositions, existingMerges, existingRedemptions, in.RiskLimits)
	for slug, pos := range finalPositions {
		if pos.CurrentPhase == PaperStopped {
			if !stoppedSlugs[slug] {
				stoppedSlugs[slug] = true
				res.PositionsStopped++
			}
		}
	}

	// 8. Rebuild PnL summary.
	summary := BuildPaperPnlSummary(finalFills, existingMerges, existingRedemptions, finalPositions, in.Now)
	if err := WritePnlSummary(summaryPath, summary); err != nil {
		return res, fmt.Errorf("write pnl summary: %w", err)
	}
	res.PaperNetPnlUsdc = summary.PaperNetPnlUsdc

	return res, nil
}

// applyRealizedPnl mutates the supplied positions map to attach
// realized PnL aggregates from the merges + redemptions ledgers,
// and marks positions paper_stopped when realized PnL drops below
// the per-event stop-loss threshold.
//
// UNKNOWN-side redemptions (2026-05-16 production fix): when the
// engine cannot determine the winning side at settlement -- which
// is the Phase F-1 default until a Gamma winningOutcomes resolver
// is added -- the redemption row's PaperRedeemPnlUsdc carries
// -cost (paid for both sides, received 0). Applying that into
// the position's realized PnL would book a spurious -100%-of-cost
// loss for every settled event and trigger the per-event
// stop-loss, corrupting the rolling paper PnL signal.
//
// Fix: skip the position-level PnL aggregation for UNKNOWN
// redemptions. The redemption row is still written to the
// ledger (as a "pending resolution" placeholder); a future
// resolve-settlements pass updates the row in-place once Gamma
// reports the winning side, and that updated row will reapply
// through applyRealizedPnl with correct PnL.
//
// The position still transitions to PaperIdle when ANY redemption
// fires (UNKNOWN or known): we know the event has settled even
// if we don't yet know who won.
//
// "Stopped" is sticky-per-cycle: a position is re-derived from
// realized PnL each cycle, so a position that was stopped under
// the old buggy code will un-stop in the next cycle after this
// fix lands (because UNKNOWN-driven PnL no longer counts).
func applyRealizedPnl(
	positions map[string]*PaperPosition,
	merges []PaperMerge,
	redemptions []PaperRedemption,
	limits RiskLimits,
) {
	for _, m := range merges {
		if pos, ok := positions[m.EventSlug]; ok {
			pos.PaperMergePnlUsdc += m.PaperMergePnlUsdc
			pos.PaperRealizedPnlUsdc += m.PaperMergePnlUsdc
		}
	}
	for _, r := range redemptions {
		pos, ok := positions[r.EventSlug]
		if !ok {
			continue
		}
		// Position has settled (even if we don't yet know the
		// winning side). Mark it idle so no further fills accrue.
		pos.CurrentPhase = PaperIdle

		// UNKNOWN: defer PnL aggregation until a future
		// resolve-settlements pass updates the row with the
		// confirmed winning side.
		if r.WinningSide == "unknown" || r.WinningSide == "" {
			continue
		}
		pos.PaperRedeemPnlUsdc += r.PaperRedeemPnlUsdc
		pos.PaperRealizedPnlUsdc += r.PaperRedeemPnlUsdc
	}
	// Stop-loss derivation (must run after merges + redemptions to
	// see realized PnL).
	for _, pos := range positions {
		if r := CheckPerEventStop(pos, limits); r.ShouldStop {
			pos.CurrentPhase = PaperStopped
		}
	}
}

// mergedUnitsFor returns the total merged units for a given
// (event, condition) pair across all prior PaperMerge records.
// (Currently always 0 or N because we cap at one merge per leg,
// but the helper allows future extensions.)
func mergedUnitsFor(merges []PaperMerge, slug, condID string) float64 {
	for _, m := range merges {
		if m.EventSlug == slug && m.ConditionID == condID {
			return m.PaperMergedUnits
		}
	}
	return 0
}

// appendAllFills loops over a slice of fills and appends each.
// First error short-circuits.
func appendAllFills(path string, fills []PaperFill) error {
	for _, f := range fills {
		if err := AppendFill(path, f); err != nil {
			return fmt.Errorf("append fill %s/%s/%s: %w",
				f.EventSlug, f.ConditionID, f.Side, err)
		}
	}
	return nil
}

// readScanCandidates loads the scanner candidates JSON, BOM-tolerant.
func readScanCandidates(path string) ([]scanner.Candidate, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	if len(body) >= 3 && body[0] == 0xEF && body[1] == 0xBB && body[2] == 0xBF {
		body = body[3:]
	}
	if len(body) == 0 {
		return nil, nil
	}
	var cands []scanner.Candidate
	if err := json.Unmarshal(body, &cands); err != nil {
		return nil, fmt.Errorf("decode %q: %w", path, err)
	}
	return cands, nil
}

// EnsurePaperDir is a small CLI helper that creates the paper-
// engine state directory if it does not yet exist.
func EnsurePaperDir(paperDir string) error {
	if paperDir == "" {
		return nil
	}
	return os.MkdirAll(paperDir, 0755)
}

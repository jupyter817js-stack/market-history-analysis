package engine

import (
	"fmt"
	"strings"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

// EntryWindow defines the time-to-start range during which a
// PROBE candidate is eligible for entry. Defaults match design
// §4.2: open between 2h and 72h before event start.
//
// Operators can tighten or loosen the window via the constructor
// in CycleConfig (PR-5), but the defaults are locked here.
type EntryWindow struct {
	MinHoursBeforeStart float64
	MaxHoursBeforeStart float64
}

// DefaultEntryWindow returns the conservative locked window from
// design §4.2.
func DefaultEntryWindow() EntryWindow {
	return EntryWindow{
		MinHoursBeforeStart: 2,
		MaxHoursBeforeStart: 72,
	}
}

// EntryDecisionInput bundles everything the entry decider needs.
type EntryDecisionInput struct {
	Candidate        *scanner.Candidate
	ExistingPosition *PaperPosition // nil = new entry candidate
	Limits           SizingLimits
	EntryWindow      EntryWindow
	Now              time.Time
	CycleTs          string
}

// EntryActionLabel is a small closed enum naming the engine's
// per-candidate action this cycle. Useful for ledger / report
// rendering; not the same as PhaseLabel (which describes the
// per-leg state).
type EntryActionLabel string

const (
	ActionSkip   EntryActionLabel = "skip"
	ActionOpen   EntryActionLabel = "open"
	ActionExpand EntryActionLabel = "expand"
)

// EntryDecisionOutput is the engine's planned action for one
// scanner candidate in this cycle. ProposedFills are NOT yet
// appended to the ledger -- the caller appends them after
// risk-control passes.
type EntryDecisionOutput struct {
	EventSlug      string             `json:"event_slug"`
	Action         EntryActionLabel   `json:"action"`
	Reason         string             `json:"reason"`
	ProposedFills  []PaperFill        `json:"proposed_fills,omitempty"`
	NewPositionPhase PhaseLabel       `json:"new_position_phase"`
}

// ShouldProbe reports whether a scanner candidate meets the
// PROBE-eligibility gate from design §4.2: very_strong score,
// PASS liquidity, no HUMAN_REVIEW_REQUIRED flag, and event start
// inside the configured entry window. Returns (eligible, reason).
//
// The reason string is human-readable; do not pattern-match it.
func ShouldProbe(c *scanner.Candidate, window EntryWindow, now time.Time) (bool, string) {
	if c == nil {
		return false, "nil candidate"
	}
	// 1. Score band: very_strong only in Phase F-1 (initial cap).
	if c.Score < 10.0 {
		return false, fmt.Sprintf("score %.1f below very_strong floor 10.0", c.Score)
	}
	// 2. Liquidity gate must be PASS.
	if c.LiquidityGate != scanner.LiquidityPass {
		return false, fmt.Sprintf("liquidity gate %q not PASS", c.LiquidityGate)
	}
	// 3. No HUMAN_REVIEW_REQUIRED flag (operator-imposed pause).
	for _, rf := range c.RiskFlags {
		if rf == scanner.RiskHumanReviewRequired {
			return false, "HUMAN_REVIEW_REQUIRED flag present"
		}
	}
	// 4. Event start must be in the entry window.
	if c.StartTs == nil {
		return false, "event has no start_ts"
	}
	hrs := c.StartTs.Sub(now).Hours()
	if hrs < window.MinHoursBeforeStart {
		return false, fmt.Sprintf("event start in %.1fh (less than %.1fh minimum)",
			hrs, window.MinHoursBeforeStart)
	}
	if hrs > window.MaxHoursBeforeStart {
		return false, fmt.Sprintf("event start in %.1fh (more than %.1fh maximum)",
			hrs, window.MaxHoursBeforeStart)
	}
	// 5. Bundle must have >= 2 binary markets (RN1 bundle invariant).
	if c.BinaryCount < 2 {
		return false, fmt.Sprintf("only %d binary markets (need >=2)", c.BinaryCount)
	}
	return true, "eligible: very_strong + PASS + window + bundle"
}

// PlanEntry decides what the engine does for one candidate this
// cycle. Three outcomes:
//
//   - ActionSkip: PROBE check failed, or position already held
//     and no new markets appeared.
//   - ActionOpen: new candidate, PROBE passed, fills for every
//     accepted market in the bundle (both sides per market) are
//     proposed. Position phase becomes paper_open_b.
//   - ActionExpand: existing position; scanner reports markets we
//     do not yet hold. Fills are proposed for the new markets
//     only (both sides). Position phase becomes paper_expand.
//
// LAYER (re-entering a held market at improved price) is NOT in
// PR-3 -- it requires comparing the candidate's per-market prices
// to the position's WAVG and is added in PR-4 alongside
// MERGE / REDEEM logic.
//
// The function does NOT append fills to any ledger; the caller
// is responsible for risk checks + appending.
func PlanEntry(in EntryDecisionInput) EntryDecisionOutput {
	out := EntryDecisionOutput{
		EventSlug:        "",
		Action:           ActionSkip,
		NewPositionPhase: PaperIntent,
	}
	if in.Candidate == nil {
		out.Reason = "nil candidate"
		return out
	}
	out.EventSlug = in.Candidate.EventSlug

	// Stopped position never accepts more entries.
	if in.ExistingPosition != nil && in.ExistingPosition.CurrentPhase == PaperStopped {
		out.Reason = "position is paper_stopped"
		out.NewPositionPhase = PaperStopped
		return out
	}

	// Idle (post-REDEEM) position never accepts new fills.
	if in.ExistingPosition != nil && in.ExistingPosition.CurrentPhase == PaperIdle {
		out.Reason = "position is paper_idle (post-REDEEM)"
		out.NewPositionPhase = PaperIdle
		return out
	}

	// Fresh entry: requires PROBE pass.
	if in.ExistingPosition == nil {
		ok, reason := ShouldProbe(in.Candidate, in.EntryWindow, in.Now)
		if !ok {
			out.Reason = reason
			return out
		}
		out.ProposedFills = planBundleEntryFills(in.Candidate, in.Limits, in.Now, in.CycleTs, PaperOpenB)
		if len(out.ProposedFills) == 0 {
			out.Reason = "PROBE eligible but all markets rejected by sizing"
			return out
		}
		out.Action = ActionOpen
		out.Reason = "open: PROBE passed, " + summarizeFills(out.ProposedFills)
		out.NewPositionPhase = PaperOpenB
		return out
	}

	// Existing position: check for EXPAND opportunity.
	heldConditions := map[string]bool{}
	for _, leg := range in.ExistingPosition.Legs {
		heldConditions[leg.ConditionID] = true
	}
	var newMarketIdxs []int
	for i, pm := range in.Candidate.LiquidityResult.PerMarket {
		if !heldConditions[pm.ConditionID] {
			newMarketIdxs = append(newMarketIdxs, i)
		}
	}
	if len(newMarketIdxs) == 0 {
		out.Reason = "no new markets; LAYER decision deferred to PR-4"
		out.NewPositionPhase = in.ExistingPosition.CurrentPhase
		return out
	}

	out.ProposedFills = planNewMarketFills(in.Candidate, newMarketIdxs,
		in.Limits, in.Now, in.CycleTs, PaperExpand)
	if len(out.ProposedFills) == 0 {
		out.Reason = "EXPAND candidates all rejected by sizing"
		out.NewPositionPhase = in.ExistingPosition.CurrentPhase
		return out
	}
	out.Action = ActionExpand
	out.Reason = fmt.Sprintf("expand: %d new markets, %s",
		len(newMarketIdxs), summarizeFills(out.ProposedFills))
	out.NewPositionPhase = PaperExpand
	return out
}

// planBundleEntryFills generates a fill plan covering every
// accepted market in a candidate's bundle. Two fills per market
// (a + b sides). Used for the OPEN action.
func planBundleEntryFills(
	c *scanner.Candidate, limits SizingLimits, now time.Time, cycleTs string,
	phase PhaseLabel,
) []PaperFill {
	per := c.LiquidityResult.PerMarket
	var fills []PaperFill
	for i := range per {
		fills = append(fills, planMarketFills(c, i, limits, now, cycleTs, phase)...)
	}
	return fills
}

// planNewMarketFills covers only the specified indices. Used for
// EXPAND.
func planNewMarketFills(
	c *scanner.Candidate, idxs []int, limits SizingLimits,
	now time.Time, cycleTs string, phase PhaseLabel,
) []PaperFill {
	var fills []PaperFill
	for _, i := range idxs {
		fills = append(fills, planMarketFills(c, i, limits, now, cycleTs, phase)...)
	}
	return fills
}

// planMarketFills emits 0 or 2 fills for one market based on the
// sizing decision and the conservative fill simulator. Both-sides-
// every-leg invariant: either both sides fill or neither does.
func planMarketFills(
	c *scanner.Candidate, idx int, limits SizingLimits,
	now time.Time, cycleTs string, phase PhaseLabel,
) []PaperFill {
	d := ComputeMarketSize(c, idx, limits)
	if !d.Accepted {
		return nil
	}
	pm := c.LiquidityResult.PerMarket[idx]

	// Approximate best-ask per side from spread.
	askA := EstimateBestAskFromSpread(pm.SpreadA)
	askB := EstimateBestAskFromSpread(pm.SpreadB)

	estA := SimulatePaperFill(askA, pm.TopDepthA, d.PaperSizeUsdc)
	estB := SimulatePaperFill(askB, pm.TopDepthB, d.PaperSizeUsdc)
	if estA.PaperShares == 0 || estB.PaperShares == 0 {
		return nil
	}

	return []PaperFill{
		{
			ObservedAt: now, CycleTs: cycleTs,
			EventSlug: c.EventSlug, ConditionID: pm.ConditionID,
			EventStartTs: c.StartTs,
			Side: "a", PaperShares: estA.PaperShares, PaperAvgPrice: estA.PaperAvgPrice,
			PaperCostUsdc: estA.PaperCostUsdc, PaperFeeUsdc: estA.PaperFeeUsdc,
			Phase: phaseForFirstFill(phase, "a"),
		},
		{
			ObservedAt: now, CycleTs: cycleTs,
			EventSlug: c.EventSlug, ConditionID: pm.ConditionID,
			EventStartTs: c.StartTs,
			Side: "b", PaperShares: estB.PaperShares, PaperAvgPrice: estB.PaperAvgPrice,
			PaperCostUsdc: estB.PaperCostUsdc, PaperFeeUsdc: estB.PaperFeeUsdc,
			Phase: phase,
		},
	}
}

// phaseForFirstFill: when phase=PaperOpenB, the very first A fill
// for a market should still be tagged paper_accumulate_a (to keep
// the entry-side history readable), while the B fill takes the
// PaperOpenB label. When phase is paper_expand or paper_layer, both
// sides take that phase verbatim.
func phaseForFirstFill(phase PhaseLabel, side string) PhaseLabel {
	if phase == PaperOpenB && side == "a" {
		return PaperAccumulateA
	}
	return phase
}

// summarizeFills produces a short human-readable summary line
// such as "3 markets, $50 total". No trading-vocabulary tokens.
func summarizeFills(fills []PaperFill) string {
	conds := map[string]bool{}
	var total float64
	for _, f := range fills {
		conds[f.ConditionID] = true
		total += f.PaperCostUsdc
	}
	return fmt.Sprintf("%d markets, paper_total=%.2f", len(conds), total)
}

// SanityCheckFills runs the basic invariants the engine assumes
// over a list of proposed fills: every fill has a valid phase,
// every market has BOTH sides filled (or neither), and no fill
// has zero shares. Returns the first violation found or nil.
func SanityCheckFills(fills []PaperFill) error {
	if len(fills) == 0 {
		return nil
	}
	bySideCount := map[string]map[string]int{}
	for _, f := range fills {
		if !IsValidPhase(f.Phase) {
			return fmt.Errorf("fill phase %q is not a closed-enum value", f.Phase)
		}
		if f.PaperShares <= 0 {
			return fmt.Errorf("fill on %s/%s has zero shares", f.EventSlug, f.ConditionID)
		}
		key := f.EventSlug + "|" + f.ConditionID
		if bySideCount[key] == nil {
			bySideCount[key] = map[string]int{}
		}
		bySideCount[key][f.Side]++
	}
	for key, sides := range bySideCount {
		if sides["a"] == 0 || sides["b"] == 0 {
			return fmt.Errorf("market %s violates both-sides invariant: a=%d b=%d",
				key, sides["a"], sides["b"])
		}
	}
	return nil
}

// SummarizeEntryAction prints a one-line ledger-friendly version
// of an EntryDecisionOutput. Used by report rendering.
func SummarizeEntryAction(out EntryDecisionOutput) string {
	return fmt.Sprintf("[%s] %s — %s", strings.ToUpper(string(out.Action)),
		out.EventSlug, out.Reason)
}

package engine

import (
	"fmt"
	"math"

	"rn1-strategy-extractor/internal/scanner"
)

// ComputeMarketSize is the per-binary-market PER-SIDE sizing
// decision from docs/rn1-phase2-paper-engine-design.md §5.2 (as
// corrected by the 2026-05-16 production-cycle audit:
// MaxPerEventUsdc is the TOTAL paper notional for the event,
// across BOTH sides of ALL binary markets in the bundle).
//
// The formula (per side, per market):
//
//   base       = limits.MaxPerEventUsdc / (2 * candidate.BinaryCount)
//   adjusted   = base * min(1.0, min_depth / limits.MinTopDepthForFull)
//   reject     = either side's spread > limits.MaxSpreadAcceptable
//   return SizingDecision{Accepted, PaperSizeUsdc=adjusted, ...}
//
// PaperSizeUsdc is the per-side budget; the engine calls
// SimulatePaperFill once per side (a + b) with this exact value,
// so the per-market spend is 2 * PaperSizeUsdc and the per-event
// spend is bounded by MaxPerEventUsdc when every market accepts.
// TestSizing_PerEventCapStaysWithinLimit locks this invariant.
//
// "Conservative" is the default mode: spreads >10% and depth <200
// shares reject the market entirely. Operators can loosen by
// constructing a more permissive SizingLimits, but the test suite
// asserts the DEFAULTS are exactly the design's locked values.
//
// All `Reason` strings are diagnostic; consumers should branch on
// `Accepted`, not on the text.
func ComputeMarketSize(c *scanner.Candidate, marketIdx int, limits SizingLimits) SizingDecision {
	if c == nil {
		return SizingDecision{Accepted: false, Reason: "nil candidate"}
	}
	if c.BinaryCount <= 0 {
		return SizingDecision{Accepted: false, Reason: "no binary markets in candidate"}
	}
	per := c.LiquidityResult.PerMarket
	if marketIdx < 0 || marketIdx >= len(per) {
		return SizingDecision{
			Accepted: false,
			Reason:   fmt.Sprintf("market index %d out of range (have %d)", marketIdx, len(per)),
		}
	}
	pm := per[marketIdx]

	// Per-side budget: half of (event cap / binary count). The
	// engine fills both sides at this size each, so the per-market
	// spend is 2 * base and the per-event spend is bounded by
	// MaxPerEventUsdc.
	base := limits.MaxPerEventUsdc / (2.0 * float64(c.BinaryCount))

	maxSpread := math.Max(pm.SpreadA, pm.SpreadB)
	if maxSpread > limits.MaxSpreadAcceptable {
		return SizingDecision{
			ConditionID:  pm.ConditionID,
			Accepted:     false,
			Reason:       fmt.Sprintf("spread %.3f exceeds limit %.3f", maxSpread, limits.MaxSpreadAcceptable),
			MaxSpread:    maxSpread,
			BaseSizeUsdc: base,
		}
	}

	// Skip markets with no observable two-sided quotes: we cannot
	// even approximate a fill price for either side.
	if !pm.HasTwoSidedQuotesA || !pm.HasTwoSidedQuotesB {
		return SizingDecision{
			ConditionID:  pm.ConditionID,
			Accepted:     false,
			Reason:       "no two-sided quotes on at least one side",
			MaxSpread:    maxSpread,
			BaseSizeUsdc: base,
		}
	}

	minDepth := math.Min(pm.TopDepthA, pm.TopDepthB)
	depthAdjust := 1.0
	if minDepth < limits.MinTopDepthForFull && limits.MinTopDepthForFull > 0 {
		depthAdjust = minDepth / limits.MinTopDepthForFull
	}
	if depthAdjust < 0 {
		depthAdjust = 0
	}
	final := base * depthAdjust

	// Zero size = reject. (Can happen when depth is 0.)
	if final <= 0 {
		return SizingDecision{
			ConditionID:      pm.ConditionID,
			Accepted:         false,
			Reason:           "computed size is zero (no depth)",
			MinDepth:         minDepth,
			MaxSpread:        maxSpread,
			BaseSizeUsdc:     base,
			DepthAdjustment:  depthAdjust,
		}
	}

	return SizingDecision{
		ConditionID:      pm.ConditionID,
		Accepted:         true,
		PaperSizeUsdc:    final,
		Reason:           "ok",
		MinDepth:         minDepth,
		MaxSpread:        maxSpread,
		BaseSizeUsdc:     base,
		DepthAdjustment:  depthAdjust,
	}
}

// ComputeEventSizing wraps ComputeMarketSize across every binary
// market in a candidate and returns the per-market decisions plus
// the per-event paper-notional total. Total is the sum of accepted
// PaperSizeUsdc values, NOT capped (it is the operator's
// indication; the per-event MaxPerEventUsdc cap is already
// distributed across markets via base = max / binary_count).
//
// Used by both the engine's per-cycle entry loop and by the
// `report` subcommand for sizing previews.
func ComputeEventSizing(c *scanner.Candidate, limits SizingLimits) (decisions []SizingDecision, totalPaperUsdc float64) {
	if c == nil {
		return nil, 0
	}
	per := c.LiquidityResult.PerMarket
	decisions = make([]SizingDecision, 0, len(per))
	for i := range per {
		d := ComputeMarketSize(c, i, limits)
		decisions = append(decisions, d)
		if d.Accepted {
			totalPaperUsdc += d.PaperSizeUsdc
		}
	}
	return decisions, totalPaperUsdc
}

// CapAccepts trims a list of SizingDecision rows so the per-event
// accepted notional does not exceed MaxPerEventUsdc. This is a
// belt-and-suspenders guard: ComputeMarketSize's base = max /
// binary_count already enforces the cap by construction when
// every market accepts, but depth-adjustment can leave total
// notional ABOVE the cap if some markets reject and others remain
// at full size. CapAccepts truncates the largest-notional accepts
// first.
//
// Returns the trimmed slice and the final accepted-total.
func CapAccepts(decisions []SizingDecision, capUsdc float64) ([]SizingDecision, float64) {
	if capUsdc <= 0 {
		return decisions, sumAccepted(decisions)
	}
	total := sumAccepted(decisions)
	if total <= capUsdc {
		return decisions, total
	}
	// Sort indices of accepted by descending size.
	type idx struct {
		i    int
		size float64
	}
	var accepted []idx
	for i, d := range decisions {
		if d.Accepted {
			accepted = append(accepted, idx{i, d.PaperSizeUsdc})
		}
	}
	// Trim from largest until under cap.
	for total > capUsdc && len(accepted) > 0 {
		// pick largest
		maxI := 0
		for j := 1; j < len(accepted); j++ {
			if accepted[j].size > accepted[maxI].size {
				maxI = j
			}
		}
		target := accepted[maxI]
		overshoot := total - capUsdc
		if overshoot >= target.size {
			// Reject this market entirely.
			decisions[target.i].Accepted = false
			decisions[target.i].PaperSizeUsdc = 0
			decisions[target.i].Reason = "trimmed to per-event cap"
			total -= target.size
			// remove from accepted slice
			accepted = append(accepted[:maxI], accepted[maxI+1:]...)
		} else {
			// Shrink by overshoot.
			decisions[target.i].PaperSizeUsdc -= overshoot
			accepted[maxI].size -= overshoot
			total = capUsdc
		}
	}
	return decisions, sumAccepted(decisions)
}

func sumAccepted(ds []SizingDecision) float64 {
	s := 0.0
	for _, d := range ds {
		if d.Accepted {
			s += d.PaperSizeUsdc
		}
	}
	return s
}

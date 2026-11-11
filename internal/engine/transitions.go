package engine

import (
	"fmt"
	"time"
)

// EnvelopeMergeThreshold is the SumWavg level below which the
// engine triggers a paper MERGE. 0.95 from design §7.1: a SumWavg
// below 0.95 means a 1A + 1B pair was acquired for under $0.95
// total; merging crystallizes $1.00 of paper USDC per pair, so
// the difference is realized paper profit.
const EnvelopeMergeThreshold = 0.95

// MinHoldDuration is the floor on time-since-first-fill before
// a leg is eligible to MERGE. 30 minutes from design §7.2;
// guards against churn on transient bid/ask wobble.
const MinHoldDuration = 30 * time.Minute

// PaperMerge is one simulated MERGE_HARVEST event for one leg.
// Append-only; written to .paper/merges.jsonl.
//
// All amounts are paper. There is no on-chain MERGE transaction
// and no on-chain interaction of any kind. The realized amount is
// computed as merged_units * 1.0 USDC (Polymarket conditional-
// token merge mechanics: 1A + 1B == 1 USDC).
type PaperMerge struct {
	ObservedAt              time.Time `json:"observed_at"`
	CycleTs                 string    `json:"cycle_ts"`
	EventSlug               string    `json:"event_slug"`
	ConditionID             string    `json:"condition_id"`

	// PaperMergedUnits = min(leg.PaperSharesA, leg.PaperSharesB)
	// at the time of MERGE.
	PaperMergedUnits        float64 `json:"paper_merged_units"`

	// PaperMergedCostUsdc = sum_wavg_at_merge * merged_units --
	// the basis we paid for those merged shares.
	PaperMergedCostUsdc     float64 `json:"paper_merged_cost_usdc"`

	// PaperMergedRealizedUsdc = merged_units * 1.0 USDC.
	PaperMergedRealizedUsdc float64 `json:"paper_merged_realized_usdc"`

	// PaperMergePnlUsdc = realized - cost. Should be positive when
	// triggered (envelope below threshold).
	PaperMergePnlUsdc       float64 `json:"paper_merge_pnl_usdc"`

	// SumWavgAtMerge captures the envelope at MERGE time for
	// audit / report rendering.
	SumWavgAtMerge          float64 `json:"sum_wavg_at_merge"`

	Reason                  string  `json:"reason"`
}

// PaperRedemption is one simulated REDEEM event for one leg.
// Emitted when the event closes / start time passes. The winning
// side's remaining shares are paid 1.0 USDC each; the losing
// side's remainder is worth 0.
type PaperRedemption struct {
	ObservedAt                time.Time `json:"observed_at"`
	CycleTs                   string    `json:"cycle_ts"`
	EventSlug                 string    `json:"event_slug"`
	ConditionID               string    `json:"condition_id"`

	// "a", "b", "draw", "unknown". Phase F-1 defaults to "unknown"
	// when the engine cannot determine the winning side at
	// settlement; a future resolve-settlements pass updates the
	// row when Gamma confirms.
	WinningSide               string  `json:"winning_side"`

	// Shares remaining post-MERGE on the winning side (or both
	// sides if WinningSide=unknown/draw).
	PaperRedeemedShares       float64 `json:"paper_redeemed_shares"`
	PaperRedeemedCostUsdc     float64 `json:"paper_redeemed_cost_usdc"`
	PaperRedeemedRealizedUsdc float64 `json:"paper_redeemed_realized_usdc"`
	PaperRedeemPnlUsdc        float64 `json:"paper_redeem_pnl_usdc"`
}

// PlanMerge decides whether a paper MERGE should fire for the
// given leg this cycle. Returns the PaperMerge row + true if
// triggered; otherwise zero + false with a reason.
//
// Conditions (all must hold):
//   - leg.PaperSharesA > 0 AND leg.PaperSharesB > 0  (both sides held)
//   - leg.PaperSumWavg < EnvelopeMergeThreshold     (envelope below 0.95)
//   - now - leg.FirstFillAt >= MinHoldDuration       (>= 30 min)
//
// Caller is responsible for appending the returned PaperMerge to
// the merges ledger AND for tracking that the merge happened (so
// subsequent cycles don't re-fire). The engine assumes
// "one MERGE per leg lifetime" as a simplifying invariant.
func PlanMerge(leg *PaperLeg, now time.Time, cycleTs string) (PaperMerge, bool, string) {
	if leg == nil {
		return PaperMerge{}, false, "nil leg"
	}
	if leg.PaperSharesA <= 0 || leg.PaperSharesB <= 0 {
		return PaperMerge{}, false, "both sides not held"
	}
	if leg.PaperSumWavg >= EnvelopeMergeThreshold {
		return PaperMerge{}, false, fmt.Sprintf("sum_wavg %.4f not below threshold %.2f",
			leg.PaperSumWavg, EnvelopeMergeThreshold)
	}
	if now.Sub(leg.FirstFillAt) < MinHoldDuration {
		return PaperMerge{}, false, fmt.Sprintf("hold %.0fmin below min %.0fmin",
			now.Sub(leg.FirstFillAt).Minutes(), MinHoldDuration.Minutes())
	}

	units := leg.PaperSharesA
	if leg.PaperSharesB < units {
		units = leg.PaperSharesB
	}
	cost := units * leg.PaperSumWavg
	realized := units * 1.0
	return PaperMerge{
		ObservedAt:              now,
		CycleTs:                 cycleTs,
		EventSlug:               leg.EventSlug,
		ConditionID:             leg.ConditionID,
		PaperMergedUnits:        units,
		PaperMergedCostUsdc:     cost,
		PaperMergedRealizedUsdc: realized,
		PaperMergePnlUsdc:       realized - cost,
		SumWavgAtMerge:          leg.PaperSumWavg,
		Reason: fmt.Sprintf("sum_wavg %.4f < %.2f and hold %.0fmin >= %.0fmin",
			leg.PaperSumWavg, EnvelopeMergeThreshold,
			now.Sub(leg.FirstFillAt).Minutes(), MinHoldDuration.Minutes()),
	}, true, "triggered"
}

// PlanRedeem produces a paper REDEEM for one leg given the
// winning side. Designed to be called once per leg at settlement
// time -- the engine detects "event closed / start passed" at the
// position level and calls PlanRedeem for each leg.
//
// winningSide must be "a", "b", "draw", or "unknown". Behavior:
//   - "a": surviving SharesA pay 1.0 USDC each; SharesB are worthless.
//   - "b": surviving SharesB pay 1.0 USDC each; SharesA are worthless.
//   - "draw": Phase F-1 conservative payout: 0 USDC (a future PR
//     can revise once we have draw-handling data).
//   - "unknown": realized = 0, awaiting resolve-settlements pass.
//
// remainingA / remainingB are the shares left on each side AFTER
// any MERGE that already happened. The caller computes these by
// subtracting merged_units from the original leg shares.
func PlanRedeem(leg *PaperLeg, remainingA, remainingB float64, winningSide string,
	now time.Time, cycleTs string) PaperRedemption {
	r := PaperRedemption{
		ObservedAt:  now,
		CycleTs:     cycleTs,
		EventSlug:   leg.EventSlug,
		ConditionID: leg.ConditionID,
		WinningSide: winningSide,
	}

	// Compute basis cost remaining on each side (proportional to
	// remaining shares). We use the leg's WAVG as the basis since
	// each share was acquired at WAVG on average.
	costA := remainingA * leg.PaperWavgA
	costB := remainingB * leg.PaperWavgB

	switch winningSide {
	case "a":
		r.PaperRedeemedShares = remainingA
		r.PaperRedeemedCostUsdc = costA + costB
		r.PaperRedeemedRealizedUsdc = remainingA * 1.0
	case "b":
		r.PaperRedeemedShares = remainingB
		r.PaperRedeemedCostUsdc = costA + costB
		r.PaperRedeemedRealizedUsdc = remainingB * 1.0
	case "draw":
		r.PaperRedeemedShares = remainingA + remainingB
		r.PaperRedeemedCostUsdc = costA + costB
		r.PaperRedeemedRealizedUsdc = 0
	default:
		r.WinningSide = "unknown"
		r.PaperRedeemedShares = remainingA + remainingB
		r.PaperRedeemedCostUsdc = costA + costB
		r.PaperRedeemedRealizedUsdc = 0
	}
	r.PaperRedeemPnlUsdc = r.PaperRedeemedRealizedUsdc - r.PaperRedeemedCostUsdc
	return r
}

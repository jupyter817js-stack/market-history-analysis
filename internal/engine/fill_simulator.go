package engine

import "math"

// Conservative fill-simulator constants from
// docs/rn1-phase2-paper-engine-design.md §6.
const (
	// Fraction of top-3 depth we assume is available to a taker.
	// Half-depth is intentionally pessimistic: real fills routinely
	// see less.
	AvailableDepthFraction = 0.5

	// Maximum slippage applied at full-fill. Real slippage often
	// exceeds 2%; the simulator caps it here so paper PnL stays
	// proportionally pessimistic on the upside but does not blow up
	// on the downside.
	SlippageFullFill = 0.02

	// Polymarket public taker fee documented at 0.2%.
	TakerFeeRate = 0.002
)

// PaperFillEstimate is the raw output of SimulatePaperFill:
// share count, average execution price, USDC cost net of fee, and
// the fee itself broken out for ledger transparency.
type PaperFillEstimate struct {
	PaperShares       float64
	PaperAvgPrice     float64
	PaperCostUsdc     float64 // includes fee
	PaperFeeUsdc      float64
	PartialFill       bool    // true if requested size exceeded available depth
}

// SimulatePaperFill computes the conservative paper-fill estimate
// for one side of one binary market.
//
// Inputs:
//
//   - bestAsk: the simulator approximates "best ask" from the
//     scanner's spread snapshot (see internal/validation/cycle.go
//     bestAskFor for the same approximation).
//   - topDepth: scanner's TopDepthA or TopDepthB for the side
//     being filled.
//   - sizeUsdc: USDC notional we want to spend (from the sizing
//     decision).
//
// Behavior:
//
//   - availableShares = topDepth * AvailableDepthFraction.
//   - If requestedShares > availableShares: partial fill at full
//     slippage (SlippageFullFill). PartialFill=true.
//   - Else: linear-slippage interpolation
//       slippage = requestedShares/availableShares * SlippageFullFill
//       avgPrice = bestAsk * (1 + slippage)
//   - Cost is always net of TakerFeeRate.
//
// Returns a zero-share estimate when bestAsk <= 0 or topDepth <= 0
// (no fill possible).
func SimulatePaperFill(bestAsk, topDepth, sizeUsdc float64) PaperFillEstimate {
	if bestAsk <= 0 || topDepth <= 0 || sizeUsdc <= 0 {
		return PaperFillEstimate{}
	}
	availableShares := topDepth * AvailableDepthFraction
	if availableShares <= 0 {
		return PaperFillEstimate{}
	}
	requestedShares := sizeUsdc / bestAsk

	var avgPrice, shares float64
	var partial bool
	if requestedShares > availableShares {
		// Cap fill at the available depth; charge full slippage.
		avgPrice = bestAsk * (1.0 + SlippageFullFill)
		shares = availableShares
		partial = true
	} else {
		slippage := (requestedShares / availableShares) * SlippageFullFill
		avgPrice = bestAsk * (1.0 + slippage)
		shares = requestedShares
	}

	// Cost = shares * avgPrice; fee = cost * TakerFeeRate.
	baseCost := shares * avgPrice
	fee := baseCost * TakerFeeRate
	return PaperFillEstimate{
		PaperShares:   shares,
		PaperAvgPrice: avgPrice,
		PaperCostUsdc: baseCost + fee,
		PaperFeeUsdc:  fee,
		PartialFill:   partial,
	}
}

// EstimateBestAskFromSpread mirrors the approximation used by the
// Phase F-0 validation envelope tracker
// (internal/validation/cycle.go bestAskFor). Centralized here so
// F-1 and F-0 agree on the same approximation; if the scanner
// ever exposes a raw best_ask field, both packages update at
// once.
//
// Returns ask = 0.5 + spread/2. Guarantees the value is in
// [0, 1.0]; out-of-range spreads clip.
func EstimateBestAskFromSpread(spread float64) float64 {
	ask := 0.5 + spread/2.0
	if ask < 0 {
		return 0
	}
	if ask > 1.0 {
		return 1.0
	}
	return ask
}

// approxEqualEngine is a 1e-9-tolerance comparator used in tests
// where shared via the test file would be inconvenient. Not
// exported.
func approxEqualEngine(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

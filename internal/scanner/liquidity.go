// Package scanner implements the read-only live candidate scanner.
//
// The scanner reads only public Polymarket endpoints (Gamma /events and
// CLOB /book). It does NOT place orders, sign anything, read wallet
// state, or call any authenticated endpoint. Output never contains
// buy/sell language, sizing, or PnL forecasts.
package scanner

import (
	"rn1-strategy-extractor/internal/polymarket"
)

// LiquidityLabel is the gate output band.
type LiquidityLabel string

const (
	LiquidityPass    LiquidityLabel = "PASS"
	LiquidityWarn    LiquidityLabel = "WARN"
	LiquidityFail    LiquidityLabel = "FAIL"
	LiquidityUnknown LiquidityLabel = "UNKNOWN"
)

// LiquidityConfig holds the gate thresholds. All fields are
// configurable via CLI flags; defaults match the design report.
type LiquidityConfig struct {
	// MinQuoteFraction is the minimum (markets-with-two-side-quotes /
	// markets) ratio required for PASS. Default 0.75.
	MinQuoteFraction float64
	// MaxSpreadPass is the maximum average spread allowed for PASS.
	// Default 0.05.
	MaxSpreadPass float64
	// MaxSpreadWarn is the maximum average spread allowed for WARN.
	// Above this, label is FAIL. Default 0.30.
	MaxSpreadWarn float64
	// MinDepthPass is the minimum top-3 depth (bid_top3 + ask_top3) on
	// each quoted market required for PASS. Default 100 shares.
	MinDepthPass float64
	// MinQuoteFractionFail — below this, label is FAIL. Default 0.25.
	MinQuoteFractionFail float64
}

// DefaultLiquidityConfig returns the conservative defaults documented
// in the design report.
func DefaultLiquidityConfig() LiquidityConfig {
	return LiquidityConfig{
		MinQuoteFraction:     0.75,
		MaxSpreadPass:        0.05,
		MaxSpreadWarn:        0.30,
		MinDepthPass:         100,
		MinQuoteFractionFail: 0.25,
	}
}

// PerMarketLiquidity captures the book-derived metrics for one
// outcome-token pair within a market.
type PerMarketLiquidity struct {
	ConditionID         string
	TokenAID            string
	TokenBID            string
	HasTwoSidedQuotesA  bool
	HasTwoSidedQuotesB  bool
	SpreadA             float64
	SpreadB             float64
	TopDepthA           float64 // bid_top3 + ask_top3 on token A
	TopDepthB           float64
	BookErrorA          string
	BookErrorB          string
}

// LiquidityResult is the per-event gate output.
type LiquidityResult struct {
	Label              LiquidityLabel
	FractionWithQuotes float64
	AvgSpread          float64
	MinTopDepth        float64
	MarketsConsidered  int
	MarketsWithBoth    int
	BookFetchErrors    int
	PerMarket          []PerMarketLiquidity
	Notes              []string
}

// EvaluateLiquidity rolls per-market book results up to a single gate
// label using the conservative threshold defaults.
func EvaluateLiquidity(per []PerMarketLiquidity, cfg LiquidityConfig) LiquidityResult {
	out := LiquidityResult{PerMarket: per, MarketsConsidered: len(per)}
	if len(per) == 0 {
		out.Label = LiquidityUnknown
		out.Notes = append(out.Notes, "no markets to evaluate")
		return out
	}
	var (
		bothCount   int
		spreadSum   float64
		spreadCount int
		minDepth    = -1.0
		errs        int
	)
	for _, m := range per {
		if m.BookErrorA != "" || m.BookErrorB != "" {
			errs++
		}
		marketHasBoth := m.HasTwoSidedQuotesA && m.HasTwoSidedQuotesB
		if marketHasBoth {
			bothCount++
			// average per-market spread = average of A and B spreads
			s := (m.SpreadA + m.SpreadB) / 2.0
			spreadSum += s
			spreadCount++
			d := m.TopDepthA
			if m.TopDepthB < d {
				d = m.TopDepthB
			}
			if minDepth < 0 || d < minDepth {
				minDepth = d
			}
		}
	}
	out.MarketsWithBoth = bothCount
	out.FractionWithQuotes = float64(bothCount) / float64(len(per))
	if spreadCount > 0 {
		out.AvgSpread = spreadSum / float64(spreadCount)
	}
	if minDepth < 0 {
		minDepth = 0
	}
	out.MinTopDepth = minDepth
	out.BookFetchErrors = errs

	// Classification: all-error case is UNKNOWN.
	if errs == len(per) {
		out.Label = LiquidityUnknown
		out.Notes = append(out.Notes, "every book fetch failed")
		return out
	}

	switch {
	case out.FractionWithQuotes >= cfg.MinQuoteFraction &&
		out.AvgSpread > 0 &&
		out.AvgSpread <= cfg.MaxSpreadPass &&
		minDepth >= cfg.MinDepthPass:
		out.Label = LiquidityPass
	case out.FractionWithQuotes < cfg.MinQuoteFractionFail ||
		out.AvgSpread > cfg.MaxSpreadWarn ||
		out.MarketsWithBoth == 0:
		out.Label = LiquidityFail
	default:
		out.Label = LiquidityWarn
	}
	return out
}

// buildPerMarket converts a pair of orderbooks (token A and token B
// of one binary market) into one PerMarketLiquidity record.
func buildPerMarket(conditionID, tokA, tokB string, bookA, bookB *polymarket.Orderbook, errA, errB string) PerMarketLiquidity {
	out := PerMarketLiquidity{
		ConditionID: conditionID,
		TokenAID:    tokA,
		TokenBID:    tokB,
		BookErrorA:  errA,
		BookErrorB:  errB,
	}
	if bookA != nil {
		bb, hasBid := bookA.BestBid()
		ba, hasAsk := bookA.BestAsk()
		if hasBid && hasAsk && ba.Price > bb.Price {
			out.HasTwoSidedQuotesA = true
			out.SpreadA = ba.Price - bb.Price
		}
		out.TopDepthA = bookA.TopNDepth("bid", 3) + bookA.TopNDepth("ask", 3)
	}
	if bookB != nil {
		bb, hasBid := bookB.BestBid()
		ba, hasAsk := bookB.BestAsk()
		if hasBid && hasAsk && ba.Price > bb.Price {
			out.HasTwoSidedQuotesB = true
			out.SpreadB = ba.Price - bb.Price
		}
		out.TopDepthB = bookB.TopNDepth("bid", 3) + bookB.TopNDepth("ask", 3)
	}
	return out
}

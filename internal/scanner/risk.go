package scanner

import (
	"strings"
)

// RiskFlag is one of the canonical risk-flag tokens defined in the
// design report.
type RiskFlag string

const (
	RiskUnknownPrefix       RiskFlag = "UNKNOWN_PREFIX"
	RiskSingleMarket        RiskFlag = "SINGLE_MARKET"
	RiskLowLiquidity        RiskFlag = "LOW_LIQUIDITY"
	RiskWideSpread          RiskFlag = "WIDE_SPREAD"
	RiskStaleMarket         RiskFlag = "STALE_MARKET"
	RiskClosedOrResolved    RiskFlag = "CLOSED_OR_RESOLVED"
	RiskNonSport            RiskFlag = "NON_SPORT"
	RiskMetadataSplit       RiskFlag = "METADATA_SPLIT"
	RiskNoLiveQuotes        RiskFlag = "NO_LIVE_QUOTES"
	RiskHumanReviewRequired RiskFlag = "HUMAN_REVIEW_REQUIRED"
)

// ComputeRiskFlags produces the risk-flag set for one candidate, given
// already-computed selector features, liquidity gate output, and event
// metadata.
//
// The function never reads post-trade data, never decides whether to
// trade, and never emits buy/sell language.
func ComputeRiskFlags(in RiskInput) []RiskFlag {
	var flags []RiskFlag

	if in.SportPrefix == "" {
		flags = append(flags, RiskUnknownPrefix)
	}
	if in.NonSportDetected {
		flags = append(flags, RiskNonSport)
	}
	if in.MarketCount < 2 {
		flags = append(flags, RiskSingleMarket)
	}
	if in.AnyClosedOrResolved {
		flags = append(flags, RiskClosedOrResolved)
	}
	// METADATA_SPLIT fires only when one half of the base / -more-markets
	// pair is actually missing from the scan. A base event with its
	// companion present (the normal split-pair case) does NOT trigger
	// the flag.
	if in.SuffixMoreMarkets && !in.BaseEventSeen {
		flags = append(flags, RiskMetadataSplit)
	}
	if !in.SuffixMoreMarkets && in.CompanionMissing {
		flags = append(flags, RiskMetadataSplit)
	}

	switch in.LiquidityLabel {
	case LiquidityFail:
		// FAIL with no quotes at all → NO_LIVE_QUOTES is the more specific tag.
		if in.MarketsWithBothQuotes == 0 {
			flags = append(flags, RiskNoLiveQuotes)
		} else {
			flags = append(flags, RiskLowLiquidity)
		}
	case LiquidityWarn:
		if in.AvgSpread > 0.15 {
			flags = append(flags, RiskWideSpread)
		}
		if in.MinDepth < 50 {
			flags = append(flags, RiskLowLiquidity)
		}
	case LiquidityUnknown:
		flags = append(flags, RiskNoLiveQuotes)
	}

	if in.BookAgeStale {
		flags = append(flags, RiskStaleMarket)
	}

	// Always set HUMAN_REVIEW_REQUIRED when any other flag is set or
	// when liquidity is not PASS.
	if len(flags) > 0 || in.LiquidityLabel != LiquidityPass {
		flags = appendUnique(flags, RiskHumanReviewRequired)
	}
	return flags
}

// RiskInput is the small set of inputs ComputeRiskFlags needs. Building
// this in the scanner pipeline keeps the risk-flag computation pure
// and easy to unit-test.
type RiskInput struct {
	SportPrefix           string
	NonSportDetected      bool
	MarketCount           int
	AnyClosedOrResolved   bool
	SuffixMoreMarkets     bool
	BaseEventSeen         bool
	// CompanionMissing is true when this event is a base (no
	// `-more-markets` suffix) AND no companion event with that suffix
	// was observed elsewhere in the scan. The earlier field name
	// (CompanionExistsButMissing) and its underlying boolean were
	// inverted; see Live Scanner Patch 1.
	CompanionMissing      bool
	LiquidityLabel        LiquidityLabel
	MarketsWithBothQuotes int
	AvgSpread             float64
	MinDepth              float64
	BookAgeStale          bool
}

func appendUnique(s []RiskFlag, x RiskFlag) []RiskFlag {
	for _, v := range s {
		if v == x {
			return s
		}
	}
	return append(s, x)
}

// FormatRiskFlags joins a slice of RiskFlag for the CLI table output.
func FormatRiskFlags(flags []RiskFlag) string {
	if len(flags) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(flags))
	for _, f := range flags {
		parts = append(parts, string(f))
	}
	return strings.Join(parts, ",")
}

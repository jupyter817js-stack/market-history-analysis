package simulator

import (
	"fmt"
	"strings"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

// EvaluateLabels inspects one scanner.Candidate snapshot and returns
// the set of live-mode labels whose preconditions fire, together with
// a one-line evidence string per label. The function is pure: no
// network, no disk, no side effects.
//
// All labels are independent and may co-fire. A terminal candidate
// (closed or past horizon) returns ONLY LabelIdleOrTerminal, with the
// reason explaining which condition triggered.
func EvaluateLabels(c scanner.Candidate, now time.Time) ([]Label, map[Label]string) {
	reasons := map[Label]string{}

	// Terminal short-circuit. A closed or past-horizon candidate
	// cannot meaningfully carry the observability labels.
	if c.Closed || pastHorizon(c, now) {
		reasons[LabelIdleOrTerminal] = terminalReason(c, now)
		return []Label{LabelIdleOrTerminal}, reasons
	}

	out := []Label{LabelScan}
	reasons[LabelScan] = "candidate present in scanner output"

	// PROBE_ELIGIBLE — structural quality + live-or-stale book.
	if probeEligible(c) {
		out = append(out, LabelProbeEligible)
		reasons[LabelProbeEligible] = fmt.Sprintf(
			"score=%.2f, band=%s, liquidity=%s",
			c.Score, c.Band, c.LiquidityGate,
		)
	}

	// DUAL_SIDE_OBSERVABLE_A / _B — at least one / at least two
	// distinct binary markets with two-sided quotes.
	bothCount := countMarketsWithBothSidedQuotes(c)
	if bothCount >= 1 {
		out = append(out, LabelDualSideObservableA)
		reasons[LabelDualSideObservableA] = fmt.Sprintf(
			"%d/%d binary markets show ask>bid on both tokens",
			bothCount, c.MarketCount,
		)
	}
	if bothCount >= 2 {
		out = append(out, LabelDualSideObservableB)
		reasons[LabelDualSideObservableB] = fmt.Sprintf(
			">=2 distinct binary markets have two-sided quotes (count=%d)",
			bothCount,
		)
	}

	// EXPAND_READY — BinaryCount >= 4 + family + quotes on >=3.
	if expandReady(c, bothCount) {
		out = append(out, LabelExpandReady)
		reasons[LabelExpandReady] = fmt.Sprintf(
			"%d-binary + family:%s + %d markets with two-sided quotes",
			c.BinaryCount,
			strings.Join(c.Families, "+"),
			bothCount,
		)
	}

	// LAYER_STRUCTURE_OBSERVABLE — spread/total offset pattern.
	if layerStructureObservable(c) {
		out = append(out, LabelLayerStructureObservable)
		reasons[LabelLayerStructureObservable] =
			"spread/total offset pattern visible via family:" +
				strings.Join(c.Families, "+")
	}

	// MERGE_STRUCTURE_OBSERVABLE — multi-condition shape that
	// historically PERMITTED merge. Never an action prompt. The
	// reason string MUST contain the disclaimer "if a position
	// existed" — enforced by simulator_test.go.
	if mergeStructureObservable(c) {
		out = append(out, LabelMergeStructureObservable)
		reasons[LabelMergeStructureObservable] = fmt.Sprintf(
			"%d binary markets + family:%s — multi-condition shape would PERMIT merge if a position existed; no position exists and no action is taken",
			c.BinaryCount,
			strings.Join(c.Families, "+"),
		)
	}

	// REDEEM_HORIZON_FUTURE — settlement is in the future. By
	// construction of the live scanner this is always true for
	// non-terminal candidates.
	out = append(out, LabelRedeemHorizonFuture)
	if c.StartTs != nil {
		reasons[LabelRedeemHorizonFuture] = fmt.Sprintf(
			"event end-date %s is in the future (live mode cannot assert real REDEEM)",
			c.StartTs.UTC().Format(time.RFC3339),
		)
	} else {
		reasons[LabelRedeemHorizonFuture] =
			"event end-date is in the future (live mode cannot assert real REDEEM)"
	}
	return out, reasons
}

func pastHorizon(c scanner.Candidate, now time.Time) bool {
	if c.StartTs == nil {
		return false
	}
	return !c.StartTs.UTC().After(now.UTC())
}

func terminalReason(c scanner.Candidate, now time.Time) string {
	switch {
	case c.Closed:
		return "candidate Closed=true; event is terminal"
	case c.StartTs != nil && !c.StartTs.UTC().After(now.UTC()):
		return "event end-date already passed (StartTs <= now)"
	}
	return "candidate is terminal"
}

func probeEligible(c scanner.Candidate) bool {
	if c.Closed {
		return false
	}
	if c.Score < 6 {
		return false
	}
	switch c.Band {
	case "medium", "strong", "very_strong":
	default:
		return false
	}
	switch string(c.LiquidityGate) {
	case "PASS", "WARN":
		return true
	}
	return false
}

func countMarketsWithBothSidedQuotes(c scanner.Candidate) int {
	count := 0
	for _, m := range c.LiquidityResult.PerMarket {
		if m.HasTwoSidedQuotesA && m.HasTwoSidedQuotesB {
			count++
		}
	}
	return count
}

func expandReady(c scanner.Candidate, bothCount int) bool {
	if c.BinaryCount < 4 {
		return false
	}
	if len(c.Families) == 0 {
		return false
	}
	if bothCount < 3 {
		return false
	}
	return true
}

func layerStructureObservable(c scanner.Candidate) bool {
	for _, f := range c.Families {
		switch f {
		case "spread_ml_total_stack":
			return true
		case "expanded_companion_stack":
			if c.BinaryCount >= 5 {
				return true
			}
		}
	}
	return false
}

// mergeStructureObservable mirrors the design report's intent:
// "negRisk + binary >= 3" historically permitted merge. The scanner
// JSON does not currently carry negRisk on Candidate, so the
// simulator uses a structural proxy — the presence of a recognized
// soccer / esports family combined with BinaryCount >= 3. Every
// family below maps to a multi-condition shape Polymarket uses
// negRisk for in practice.
func mergeStructureObservable(c scanner.Candidate) bool {
	if c.BinaryCount < 3 {
		return false
	}
	for _, f := range c.Families {
		switch f {
		case "soccer_3way_moneyline",
			"spread_ml_total_stack",
			"bo3_esports_stack",
			"tennis_set_total_stack",
			"expanded_companion_stack":
			return true
		}
	}
	return false
}

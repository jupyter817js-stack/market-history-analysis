package diff

import (
	"math"
	"sort"

	"rn1-strategy-extractor/internal/scanner"
	"rn1-strategy-extractor/internal/simulator"
)

// snapshotEntry pairs one scanner candidate with its optional
// simulator trace, both representing the same event_slug in one
// snapshot.
type snapshotEntry struct {
	Candidate *scanner.Candidate
	Trace     *simulator.Trace
}

// gateRank maps the four liquidity-gate labels to the lattice
// PASS > WARN > UNKNOWN > FAIL. Higher = better.
func gateRank(gate string) int {
	switch gate {
	case "PASS":
		return 4
	case "WARN":
		return 3
	case "UNKNOWN":
		return 2
	case "FAIL":
		return 1
	}
	return 0
}

// budgetExhaustedCount counts PerMarket entries whose BookErrorA
// or BookErrorB contains the scanner's "book-budget-exhausted"
// marker.
func budgetExhaustedCount(c *scanner.Candidate) int {
	if c == nil {
		return 0
	}
	count := 0
	for _, m := range c.LiquidityResult.PerMarket {
		if m.BookErrorA == "book-budget-exhausted" {
			count++
		}
		if m.BookErrorB == "book-budget-exhausted" {
			count++
		}
	}
	return count
}

// classify returns the set of diff classes that fire for an
// event present in both snapshots. The order in the returned
// slice is deterministic (alphabetical within each category,
// then stable).
//
// If only one side has the simulator trace, label-related
// classes are skipped silently.
func classify(a, b *snapshotEntry, cfg ThresholdConfig) []DiffClass {
	if a == nil || a.Candidate == nil || b == nil || b.Candidate == nil {
		return nil
	}
	ca, cb := a.Candidate, b.Candidate
	classes := map[DiffClass]bool{}

	// --- Rank ---
	if cb.Rank > 0 && ca.Rank > 0 {
		if cb.Rank < ca.Rank {
			classes[ClassRankUp] = true
		} else if cb.Rank > ca.Rank {
			classes[ClassRankDown] = true
		}
	}

	// --- Score / Band ---
	if math.Abs(cb.Score-ca.Score) >= 0.01 {
		classes[ClassScoreChanged] = true
		if cb.Score > ca.Score {
			classes[ClassScoreImproved] = true
		} else {
			classes[ClassScoreDegraded] = true
		}
	}
	if cb.Band != ca.Band {
		classes[ClassBandChanged] = true
	}

	// --- Gate ---
	rankA := gateRank(string(ca.LiquidityGate))
	rankB := gateRank(string(cb.LiquidityGate))
	if rankA > 0 && rankB > 0 && rankA != rankB {
		if rankB > rankA {
			classes[ClassGateImproved] = true
		} else {
			classes[ClassGateDegraded] = true
		}
	}
	switch string(cb.LiquidityGate) {
	case "PASS":
		if string(ca.LiquidityGate) != "PASS" {
			classes[ClassBecamePass] = true
		}
	case "WARN":
		if string(ca.LiquidityGate) != "WARN" {
			classes[ClassBecameWarn] = true
		}
	case "FAIL":
		if string(ca.LiquidityGate) != "FAIL" {
			classes[ClassBecameFail] = true
		}
	case "UNKNOWN":
		if string(ca.LiquidityGate) != "UNKNOWN" {
			classes[ClassBecameUnknown] = true
		}
	}
	if string(ca.LiquidityGate) == "UNKNOWN" && string(cb.LiquidityGate) != "UNKNOWN" {
		classes[ClassUnknownResolved] = true
	}

	// --- Budget ---
	beA := budgetExhaustedCount(ca)
	beB := budgetExhaustedCount(cb)
	if beA > 0 && beB == 0 {
		classes[ClassBudgetExhaustedResolved] = true
	}
	if beA == 0 && beB > 0 {
		classes[ClassBudgetExhaustedNew] = true
	}

	// --- Liquidity numeric (only when both sides quote) ---
	covA := ca.LiquidityResult.FractionWithQuotes
	covB := cb.LiquidityResult.FractionWithQuotes
	if math.Abs(covB-covA) >= cfg.CoverageDelta {
		if covB > covA {
			classes[ClassQuoteCoverageImproved] = true
		} else {
			classes[ClassQuoteCoverageDegraded] = true
		}
	}
	// Only meaningful when both sides have at least some quotes.
	sa := ca.LiquidityResult.AvgSpread
	sb := cb.LiquidityResult.AvgSpread
	if sa > 0 && sb > 0 && math.Abs(sb-sa) >= cfg.SpreadDelta {
		if sb < sa {
			classes[ClassSpreadTightened] = true
		} else {
			classes[ClassSpreadWidened] = true
		}
	}
	da := ca.LiquidityResult.MinTopDepth
	db := cb.LiquidityResult.MinTopDepth
	if da > 0 || db > 0 {
		abs := math.Abs(db - da)
		pct := 0.0
		if da > 0 {
			pct = abs / da
		}
		if abs >= cfg.DepthAbsDelta || pct >= cfg.DepthPctDelta {
			if db > da {
				classes[ClassDepthImproved] = true
			} else if db < da {
				classes[ClassDepthDegraded] = true
			}
		}
	}

	// --- Risk flags (set diff) ---
	added, removed := stringSetDiff(toStringSlice(ca.RiskFlags), toStringSlice(cb.RiskFlags))
	if len(added) > 0 {
		classes[ClassRiskFlagsAdded] = true
	}
	if len(removed) > 0 {
		classes[ClassRiskFlagsRemoved] = true
	}
	// HUMAN_REVIEW_STILL_REQUIRED — both A and B carry it AND no
	// other risk-flag delta is interesting.
	if containsStr(toStringSlice(ca.RiskFlags), "HUMAN_REVIEW_REQUIRED") &&
		containsStr(toStringSlice(cb.RiskFlags), "HUMAN_REVIEW_REQUIRED") &&
		len(added) == 0 && len(removed) == 0 {
		classes[ClassHumanReviewStillReq] = true
	}

	// --- Simulator labels (set diff, only when both sides present) ---
	if a.Trace != nil && b.Trace != nil {
		la := labelStrings(a.Trace.Labels)
		lb := labelStrings(b.Trace.Labels)
		addedL, removedL := stringSetDiff(la, lb)
		if len(addedL) > 0 {
			classes[ClassLabelsAdded] = true
		}
		if len(removedL) > 0 {
			classes[ClassLabelsRemoved] = true
		}
		if len(lb) > len(la) {
			classes[ClassLabelSetGrew] = true
		} else if len(lb) < len(la) {
			classes[ClassLabelSetShrank] = true
		}
		// Terminal: A had REDEEM_HORIZON_FUTURE, B has IDLE_OR_TERMINAL.
		if containsStr(la, string(simulator.LabelRedeemHorizonFuture)) &&
			containsStr(lb, string(simulator.LabelIdleOrTerminal)) {
			classes[ClassBecameTerminal] = true
		}
	}

	// --- Terminal (from scanner fields, independent of trace) ---
	if !ca.Closed && cb.Closed {
		classes[ClassBecameClosed] = true
	}
	if ca.Active && !cb.Active {
		classes[ClassBecameInactive] = true
	}

	// --- Metadata ---
	if ca.Title != cb.Title {
		classes[ClassTitleChanged] = true
	}
	if ca.EventID != cb.EventID {
		classes[ClassEventIDChanged] = true
	}
	famA, famB := append([]string(nil), ca.Families...), append([]string(nil), cb.Families...)
	famAdded, famRemoved := stringSetDiff(famA, famB)
	if len(famAdded) > 0 {
		classes[ClassFamiliesAdded] = true
	}
	if len(famRemoved) > 0 {
		classes[ClassFamiliesRemoved] = true
	}
	if ca.MarketCount != cb.MarketCount {
		classes[ClassMarketsCountChanged] = true
	}
	if ca.ReviewStatus != cb.ReviewStatus {
		classes[ClassReviewStatusChanged] = true
	}

	// --- Quiet fallback ---
	if len(classes) == 0 {
		classes[ClassNoMaterialChange] = true
	}

	return sortClasses(classes)
}

// toStringSlice converts []scanner.RiskFlag to []string for set
// operations.
func toStringSlice(rfs []scanner.RiskFlag) []string {
	out := make([]string, 0, len(rfs))
	for _, r := range rfs {
		out = append(out, string(r))
	}
	return out
}

// labelStrings converts []simulator.Label to []string.
func labelStrings(ls []simulator.Label) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, string(l))
	}
	return out
}

// stringSetDiff returns (in B not in A, in A not in B) for two
// string slices treated as sets.
func stringSetDiff(a, b []string) (added, removed []string) {
	inA := map[string]bool{}
	for _, s := range a {
		inA[s] = true
	}
	inB := map[string]bool{}
	for _, s := range b {
		inB[s] = true
	}
	for _, s := range b {
		if !inA[s] {
			added = append(added, s)
		}
	}
	for _, s := range a {
		if !inB[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func containsStr(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

// sortClasses returns the closed-enum classes in a deterministic
// order matching their declaration in AllDiffClasses.
func sortClasses(m map[DiffClass]bool) []DiffClass {
	out := make([]DiffClass, 0, len(m))
	for _, c := range AllDiffClasses {
		if m[c] {
			out = append(out, c)
		}
	}
	return out
}

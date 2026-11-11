package validation

import (
	"fmt"
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

// likenessWeights are the per-axis multipliers in the design's
// weighted-sum formula. They must sum to 1.0 (asserted in tests).
// Changing a weight here REQUIRES updating
// docs/rn1-phase-f0-cross-validation-design.md §3.1 and
// re-validating the 5-cluster sanity test in this package.
const (
	wSportPrefix = 0.20
	wFamily      = 0.25
	wBundleSize  = 0.15
	wLiquidity   = 0.20
	wScoreBand   = 0.10
	wHorizon     = 0.10
)

// rn1KnownFamilies is the closed set the B axis tests against.
// These are the derivative-family detectors that selection.DetectFamilies
// can emit AND that have a documented RN1 footprint (per forensic
// integrated report §3-§4).
var rn1KnownFamilies = map[string]bool{
	"soccer_3way_moneyline":    true,
	"spread_ml_total_stack":    true,
	"bo3_esports_stack":        true,
	"tennis_set_total_stack":   true,
	"expanded_companion_stack": true,
}

// LikenessInput is the small, scanner-agnostic input bundle for
// ScoreLikeness. Two convenience adapters (LikenessInputFromCandidate /
// LikenessInputFromPrototype) build this from the two real callers.
type LikenessInput struct {
	SportPrefix   string
	Families      []string
	BinaryCount   int
	LiquidityGate string // "PASS" | "WARN" | "FAIL" | "UNKNOWN" | ""
	Score         float64
	TimeToStart   time.Duration
}

// LikenessInputFromCandidate adapts a scanner.Candidate to the
// scoring input bundle. It is the live-path adapter (called per
// candidate per tick).
func LikenessInputFromCandidate(c *scanner.Candidate) LikenessInput {
	return LikenessInput{
		SportPrefix:   c.SportPrefix,
		Families:      append([]string(nil), c.Families...),
		BinaryCount:   c.BinaryCount,
		LiquidityGate: string(c.LiquidityGate),
		Score:         c.Score,
		TimeToStart:   c.TimeToStart,
	}
}

// LikenessInputFromPrototype adapts a Layer5Prototype fixture to the
// scoring input bundle. Used by the 5-cluster regression test.
func LikenessInputFromPrototype(p Layer5Prototype) LikenessInput {
	return LikenessInput{
		SportPrefix:   p.SportPrefix,
		Families:      append([]string(nil), p.Families...),
		BinaryCount:   p.BinaryCount,
		LiquidityGate: p.LiquidityGate,
		Score:         p.Score,
		TimeToStart:   time.Duration(p.HorizonHours) * time.Hour,
	}
}

// ScoreLikeness applies the design's 6-axis weighted-sum formula
// against the given historical profile. Returns a fully populated
// LikenessScore including per-axis components, the weighted total,
// the classified band, and a short list of diagnostic reasons.
//
// The function is pure: same inputs always yield the same output.
// No clock reads, no random source, no disk or network access.
func ScoreLikeness(in LikenessInput, profile *HistoricalProfile) LikenessScore {
	if profile == nil {
		// A nil profile is a programming error; return all-zeros.
		// The CLI should fail earlier with a clearer message.
		return LikenessScore{Band: BandNonRn1}
	}

	a := scoreSportPrefix(in.SportPrefix, profile)
	b := scoreFamily(in.Families)
	c := scoreBundleSize(in.BinaryCount)
	d := scoreLiquidity(in.LiquidityGate)
	e := scoreScoreBand(in.Score)
	f := scoreHorizon(in.TimeToStart)

	total := wSportPrefix*a + wFamily*b + wBundleSize*c +
		wLiquidity*d + wScoreBand*e + wHorizon*f

	score := LikenessScore{
		SportPrefixComponent: a,
		FamilyComponent:      b,
		BundleSizeComponent:  c,
		LiquidityComponent:   d,
		ScoreBandComponent:   e,
		HorizonComponent:     f,
		Total:                total,
		Band:                 classifyBand(total),
		Reasons:              buildReasons(in, a, b, c, d, e, f),
	}
	return score
}

// --- per-axis component functions ----------------------------------

// A axis: sport prefix recognition.
func scoreSportPrefix(prefix string, profile *HistoricalProfile) float64 {
	if prefix == "" {
		return 0.0
	}
	if profile.HasKnownPrefix(prefix) {
		return 1.0
	}
	return 0.0
}

// B axis: at least one recognized RN1 derivative family.
func scoreFamily(families []string) float64 {
	for _, f := range families {
		if rn1KnownFamilies[f] {
			return 1.0
		}
	}
	return 0.0
}

// C axis: bundle size saturating at 10 binary markets.
func scoreBundleSize(binaryCount int) float64 {
	if binaryCount <= 0 {
		return 0.0
	}
	if binaryCount >= 10 {
		return 1.0
	}
	return float64(binaryCount) / 10.0
}

// D axis: liquidity gate quality.
func scoreLiquidity(gate string) float64 {
	switch gate {
	case "PASS":
		return 1.0
	case "WARN":
		return 0.6
	default: // FAIL, UNKNOWN, "", anything else
		return 0.0
	}
}

// E axis: selector score band relative to RN1's documented zone.
func scoreScoreBand(score float64) float64 {
	switch {
	case score >= 5.0 && score <= 9.5:
		return 1.0
	case score >= 4.0 && score < 5.0:
		return 0.5
	case score > 9.5 && score <= 12.0:
		return 0.5
	default:
		return 0.0
	}
}

// F axis: event horizon relative to RN1's typical trading window.
func scoreHorizon(tts time.Duration) float64 {
	hours := tts.Hours()
	switch {
	case hours >= 6 && hours <= 72:
		return 1.0
	case hours >= 2 && hours < 6:
		return 0.5
	case hours > 72 && hours <= 168:
		return 0.5
	default:
		return 0.0
	}
}

// --- diagnostic reason builder ---------------------------------

// buildReasons emits a short, human-readable list explaining the
// non-trivial axis contributions. Used for review-queue rendering;
// never consumed by downstream scoring or evaluation.
func buildReasons(in LikenessInput, a, b, c, d, e, f float64) []string {
	var rs []string
	if a == 0 {
		if in.SportPrefix == "" {
			rs = append(rs, "no sport prefix detected")
		} else {
			rs = append(rs, fmt.Sprintf("sport prefix %q not in RN1 known set", in.SportPrefix))
		}
	}
	if b == 0 {
		rs = append(rs, "no recognized RN1 derivative family")
	}
	if c < 0.3 {
		rs = append(rs, fmt.Sprintf("small bundle (binary_count=%d)", in.BinaryCount))
	}
	if d < 1.0 {
		rs = append(rs, fmt.Sprintf("liquidity gate %q", in.LiquidityGate))
	}
	if e < 1.0 {
		rs = append(rs, fmt.Sprintf("score %.1f outside RN1 core band", in.Score))
	}
	if f < 1.0 {
		rs = append(rs, fmt.Sprintf("horizon %.1fh outside RN1 typical range", in.TimeToStart.Hours()))
	}
	// Positive consolidation when everything's strong.
	if len(rs) == 0 {
		rs = append(rs, "strong match across all axes")
	}
	return rs
}

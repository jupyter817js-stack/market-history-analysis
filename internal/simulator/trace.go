package simulator

import (
	"time"

	"rn1-strategy-extractor/internal/scanner"
)

// Trace is one descriptive observability record per candidate. The
// fields are passed through from the scanner unchanged plus the
// simulator-derived Labels / LabelReasons / boolean flags.
//
// Trace deliberately does NOT carry:
//   - any "expected return" / "expected pnl" / "forecast" field,
//   - any "position size" / "stake" / "units" / "lots" field,
//   - any "wavg" / "avg_price" / "realized pnl" field,
//   - any "arbitrage" / "execute" / "place order" field,
//   - any bare 9-state name (ACCUMULATE_A / OPEN_B / MERGE_HARVEST /
//     REDEEM) — only the closed-set Labels from labels.go are valid.
type Trace struct {
	Rank          int
	EventSlug     string
	Title         string
	SportPrefix   string
	SelectorScore float64
	ScoreBand     string
	LiquidityGate string

	Labels       []Label
	LabelReasons map[Label]string

	ObservedFamilies      []string
	ActiveMarkets         int
	BinaryMarkets         int
	MarketsWithBothQuotes int
	QuoteCoverage         float64

	TwoSideQuoteObservable   bool
	ExpansionReady           bool
	MergeStructureObservable bool
	RedeemHorizonFuture      bool

	RiskFlags         []string
	HumanReviewStatus string

	ScannedAt   time.Time
	StartTs     *time.Time
	TimeToStart time.Duration
}

// BuildTrace turns one scanner.Candidate into one descriptive
// Trace record. `now` should be the operator's reference time
// (typically the scanner's ScannedAt for trace consistency, or
// time.Now().UTC()).
func BuildTrace(c scanner.Candidate, now time.Time) Trace {
	labels, reasons := EvaluateLabels(c, now)

	bothCount := countMarketsWithBothSidedQuotes(c)
	coverage := 0.0
	if c.MarketCount > 0 {
		coverage = float64(bothCount) / float64(c.MarketCount)
	}

	risk := make([]string, 0, len(c.RiskFlags))
	for _, r := range c.RiskFlags {
		risk = append(risk, string(r))
	}

	review := "clean"
	if string(c.LiquidityGate) != "PASS" || len(c.RiskFlags) > 0 {
		review = "review"
	}

	t := Trace{
		Rank:                  c.Rank,
		EventSlug:             c.EventSlug,
		Title:                 c.Title,
		SportPrefix:           c.SportPrefix,
		SelectorScore:         c.Score,
		ScoreBand:             c.Band,
		LiquidityGate:         string(c.LiquidityGate),
		Labels:                labels,
		LabelReasons:          reasons,
		ObservedFamilies:      append([]string(nil), c.Families...),
		ActiveMarkets:         c.MarketCount,
		BinaryMarkets:         c.BinaryCount,
		MarketsWithBothQuotes: bothCount,
		QuoteCoverage:         coverage,
		RiskFlags:             risk,
		HumanReviewStatus:     review,
		ScannedAt:             c.ScannedAt,
		StartTs:               c.StartTs,
		TimeToStart:           c.TimeToStart,
	}

	for _, l := range labels {
		switch l {
		case LabelDualSideObservableA:
			t.TwoSideQuoteObservable = true
		case LabelExpandReady:
			t.ExpansionReady = true
		case LabelMergeStructureObservable:
			t.MergeStructureObservable = true
		case LabelRedeemHorizonFuture:
			t.RedeemHorizonFuture = true
		}
	}
	return t
}

// BuildTraces is the batch helper used by the CLI.
func BuildTraces(cs []scanner.Candidate, now time.Time) []Trace {
	out := make([]Trace, 0, len(cs))
	for _, c := range cs {
		out = append(out, BuildTrace(c, now))
	}
	return out
}

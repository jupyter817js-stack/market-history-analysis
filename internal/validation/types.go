// Package validation implements the read-only Phase F-0 Live <->
// Historical Cross-Validation step. It scores each live scanner
// candidate against an RN1 historical-cohort profile, tracks
// SumWavg-based envelope opportunity windows over multiple scan
// cycles, observes settlement outcomes, and computes hypothetical
// MERGE / REDEEM PnL.
//
// The phrase "hypothetical" is load-bearing. No paper position is
// opened, no paper money is risked, and no real money exists in
// this package or any of its outputs. The only computation is
// "what would have happened if a 1-unit-per-tick observer had been
// passively accumulating both sides of every high-likeness
// candidate." That number is purely retrospective and exists only
// to feed the Phase F-0 evaluation rubric documented in
// docs/rn1-phase-f0-cross-validation-design.md.
//
// Hard constraints (CI-enforced by validation_test.go):
//
//   - No network calls. Package does not import net/http, net/url,
//     websocket libraries, or any RPC client. The historical
//     profile is loaded from a JSON file written by a separate
//     `build-historical-profile` command (which has DB access).
//
//   - No execution path. No signer, no keystore, no private key,
//     no wallet, no Polygon RPC, no order placement, no
//     authenticated CLOB endpoint.
//
//   - No paper / real / live money vocabulary. The validation
//     package emits ONLY fields prefixed `hypo_`. The words
//     "paper", "real", and "live" never appear in any output
//     field name or JSON value.
//
//   - No bare 9-state simulator names. ACCUMULATE_A / OPEN_B /
//     MERGE_HARVEST / REDEEM never appear in output.
//
//   - No buy/sell/enter/exit/arbitrage/stake/PnL/position
//     vocabulary in output.
//
//   - Closed-enum LikenessBand. Only RN1_PRIME / RN1_PLAUSIBLE /
//     RN1_ADJACENT / NON_RN1 are valid. Emitting another value is
//     a programming error and is statically forbidden.
//
//   - Append-only ledgers. Once a JSONL record is written, it is
//     not modified. Validated via SHA256 byte-prefix in
//     PR-3 / PR-4 / PR-5.
//
// Phase F-0 is the prerequisite to Phase F-1 (paper engine, see
// docs/rn1-phase2-paper-engine-design.md). Implementation of F-1
// is gated on the F-0 evaluation rubric's 8 pass criteria over
// 4-6 weeks of live operation.
package validation

import "time"

// LikenessBand is the closed-enum classification of how RN1-like a
// scanner candidate is, derived from a LikenessScore total.
//
// The static-check test TestValidation_ClosedEnumLikenessBand
// asserts no value outside AllLikenessBands escapes into output.
type LikenessBand string

const (
	// BandRn1Prime: total >= 0.80. Likely matches RN1's historical
	// cohort distribution. Envelope tracker is started for every
	// candidate in this band.
	BandRn1Prime LikenessBand = "RN1_PRIME"

	// BandRn1Plausible: 0.60 <= total < 0.80. Plausibly RN1-shaped
	// but not a top match. Envelope tracker still started so we
	// can later compare prime vs plausible envelope yield.
	BandRn1Plausible LikenessBand = "RN1_PLAUSIBLE"

	// BandRn1Adjacent: 0.40 <= total < 0.60. Some RN1 signals
	// present but unlikely to match. Likeness recorded; envelope
	// tracker NOT started (would dilute the signal).
	BandRn1Adjacent LikenessBand = "RN1_ADJACENT"

	// BandNonRn1: total < 0.40. Almost certainly not RN1-shaped.
	// Likeness recorded for distribution analysis; no tracker.
	BandNonRn1 LikenessBand = "NON_RN1"
)

// AllLikenessBands is the closed set of every band emitted by this
// package. The CLI rejects bands not in this slice, and the static
// safety test asserts no other value escapes into output.
var AllLikenessBands = []LikenessBand{
	BandRn1Prime,
	BandRn1Plausible,
	BandRn1Adjacent,
	BandNonRn1,
}

// IsValidBand reports whether b is one of the closed-enum bands.
func IsValidBand(b LikenessBand) bool {
	for _, x := range AllLikenessBands {
		if x == b {
			return true
		}
	}
	return false
}

// classifyBand maps a likeness total to its band. This is the
// canonical thresholding; consumers should NOT re-implement the
// thresholds.
func classifyBand(total float64) LikenessBand {
	switch {
	case total >= 0.80:
		return BandRn1Prime
	case total >= 0.60:
		return BandRn1Plausible
	case total >= 0.40:
		return BandRn1Adjacent
	default:
		return BandNonRn1
	}
}

// LikenessScore is the per-candidate, per-tick output of the
// likeness scorer. Each per-axis component is in [0, 1] and is
// weighted by the constants in likeness.go. The Total field is the
// authoritative weighted sum.
//
// The Band field is the closed-enum classification derived from
// Total via classifyBand and is included for ledger convenience.
//
// Reasons holds a short, human-readable justification for each
// non-trivial axis. It is bounded in size and contains no
// trading-vocabulary tokens.
type LikenessScore struct {
	// Per-axis components, each in [0, 1].
	SportPrefixComponent float64 `json:"sport_prefix_component"`
	FamilyComponent      float64 `json:"family_component"`
	BundleSizeComponent  float64 `json:"bundle_size_component"`
	LiquidityComponent   float64 `json:"liquidity_component"`
	ScoreBandComponent   float64 `json:"score_band_component"`
	HorizonComponent     float64 `json:"horizon_component"`

	// Weighted total in [0, 1]; classification.
	Total float64      `json:"total"`
	Band  LikenessBand `json:"band"`

	// Diagnostic reasons (max ~6 entries). Used for review-queue
	// table rendering only; not consumed by downstream code.
	Reasons []string `json:"reasons,omitempty"`
}

// TickObservation is one snapshot tick of one binary market within
// one high-likeness candidate. The envelope tracker (PR-3) appends
// one TickObservation per (event_slug, condition_id) per scan
// cycle.
//
// All price fields are mid-quote-relative (best_ask and best_bid
// from the scanner's LiquidityResult.PerMarket). The tracker
// computes hypothetical SumWavg from these prices ONLY; it does
// not consult any external price source.
type TickObservation struct {
	ObservedAt  time.Time `json:"observed_at"`
	CycleTs     string    `json:"cycle_ts"`     // matches .dryrun/server/<ts>/
	EventSlug   string    `json:"event_slug"`
	ConditionID string    `json:"condition_id"`

	// EventStartTs is the candidate's StartTs at the time of
	// observation. Carried into the tick so the settlement
	// detector can determine "event has passed start time"
	// without re-reading scanner artifacts.
	EventStartTs *time.Time `json:"event_start_ts,omitempty"`

	// Per-side observed top-of-book prices and depths. "A" and "B"
	// refer to the two clob token IDs of a single binary market;
	// no semantic side mapping (YES/NO) is asserted here.
	AskA      float64 `json:"ask_a"`
	AskB      float64 `json:"ask_b"`
	BidA      float64 `json:"bid_a"`
	BidB      float64 `json:"bid_b"`
	SpreadA   float64 `json:"spread_a"`
	SpreadB   float64 `json:"spread_b"`
	TopDepthA float64 `json:"top_depth_a"`
	TopDepthB float64 `json:"top_depth_b"`

	// Likeness score at observation time. Carried in every tick
	// so the envelope tracker can be filtered without joining
	// against the likeness ledger.
	Likeness float64      `json:"likeness"`
	Band     LikenessBand `json:"band"`
}

// LikenessRecord is one per-candidate, per-cycle row in the
// likeness ledger. Every scan candidate gets one LikenessRecord
// every cycle, regardless of band. The envelope tracker only
// records ticks for high-likeness candidates, but the full
// distribution lives here for the evaluation rubric's KS-test
// (criterion 7) and the report subcommand's distribution view.
type LikenessRecord struct {
	ObservedAt  time.Time     `json:"observed_at"`
	CycleTs     string        `json:"cycle_ts"`
	EventSlug   string        `json:"event_slug"`
	SportPrefix string        `json:"sport_prefix"`
	Score       LikenessScore `json:"score"`
}

// HypotheticalLeg is the running-aggregate per-condition view that
// the envelope tracker maintains. It is rebuilt deterministically
// from the appended TickObservation rows for that
// (event_slug, condition_id) pair.
//
// "Hypothetical" because no actual position is opened: the leg
// assumes a passive observer accumulating one unit per side per
// tick at the observed best_ask. No sizing decision, no actual
// fill, no actual cost.
type HypotheticalLeg struct {
	EventSlug   string `json:"event_slug"`
	ConditionID string `json:"condition_id"`

	// Aggregated counters. CumCost = sum(AskA), CumShares = number
	// of ticks (since we accumulate 1 share per tick by
	// definition). HypoWavgA = CumCostA / CumSharesA.
	TickCount     int     `json:"tick_count"`
	HypoSharesA   float64 `json:"hypo_shares_a"`
	HypoSharesB   float64 `json:"hypo_shares_b"`
	HypoCumCostA  float64 `json:"hypo_cum_cost_a"`
	HypoCumCostB  float64 `json:"hypo_cum_cost_b"`
	HypoWavgA     float64 `json:"hypo_wavg_a"`
	HypoWavgB     float64 `json:"hypo_wavg_b"`
	HypoSumWavg   float64 `json:"hypo_sum_wavg"` // = HypoWavgA + HypoWavgB

	// Window summary so far.
	FirstObservedAt   time.Time `json:"first_observed_at"`
	LastObservedAt    time.Time `json:"last_observed_at"`
	MinHypoSumWavg    float64   `json:"min_hypo_sum_wavg"`
	MinObservedAt     time.Time `json:"min_observed_at,omitempty"`
	TicksBelow1_0     int       `json:"ticks_below_1_0"`
	TicksBelow0_95    int       `json:"ticks_below_0_95"`
}

// EnvelopeWindow is the immutable per-(event, condition) summary
// emitted when the envelope tracker decides the window is closed
// (event resolved, or N ticks elapsed without resolution). It is
// what the evaluation rubric consumes.
type EnvelopeWindow struct {
	EventSlug         string    `json:"event_slug"`
	ConditionID       string    `json:"condition_id"`
	Likeness          float64   `json:"likeness"`
	Band              LikenessBand `json:"band"`
	FirstObservedAt   time.Time `json:"first_observed_at"`
	LastObservedAt    time.Time `json:"last_observed_at"`
	TickCount         int       `json:"tick_count"`
	MinHypoSumWavg    float64   `json:"min_hypo_sum_wavg"`
	MinHypoSumWavgAt  time.Time `json:"min_hypo_sum_wavg_at"`
	HoursBelow1_0     float64   `json:"hours_below_1_0"`
	HoursBelow0_95    float64   `json:"hours_below_0_95"`
	WindowClosure     string    `json:"window_closure"` // "RESOLVED" / "TIMEOUT" / "DELISTED"
}

// SettlementOutcome is the per-event resolution record emitted when
// a scanner candidate that was being tracked by the envelope
// tracker is detected to have closed. It includes the per-condition
// hypothetical MERGE + REDEEM PnL that a passive 1-unit-per-tick
// observer would have realized.
//
// All amount fields are hypothetical units of pUSD (1.0 = 1 USDC
// notionally) but the validation package never resolves them
// against any wallet or real balance. They exist only as inputs
// to the evaluation rubric.
type SettlementOutcome struct {
	EventSlug       string                  `json:"event_slug"`
	ResolvedAt      time.Time               `json:"resolved_at"`
	WinningOutcomes []string                `json:"winning_outcomes"`
	PerCondition    []ConditionResolution   `json:"per_condition"`

	// Aggregated hypothetical PnL across all tracked conditions
	// of this event.
	HypoTotalCost     float64 `json:"hypo_total_cost"`
	HypoMergePnl      float64 `json:"hypo_merge_pnl"`
	HypoRedeemPnl     float64 `json:"hypo_redeem_pnl"`
	HypoNetPnl        float64 `json:"hypo_net_pnl"`
	HypoReturnPct     float64 `json:"hypo_return_pct"`
}

// ConditionResolution is the per-condition outcome record.
type ConditionResolution struct {
	ConditionID   string  `json:"condition_id"`
	WinningSide   string  `json:"winning_side"` // "A" | "B" | "DRAW" | "UNKNOWN"

	// Hypothetical position at MERGE decision point. MergeAt is
	// the tick with the lowest observed HypoSumWavg (if below
	// 0.95) -- the deepest envelope opportunity.
	MergeAt           time.Time `json:"merge_at,omitempty"`
	HypoMergeUnits    float64   `json:"hypo_merge_units"`
	HypoMergeCost     float64   `json:"hypo_merge_cost"`
	HypoMergeRealized float64   `json:"hypo_merge_realized"`
	HypoMergePnl      float64   `json:"hypo_merge_pnl"`

	// Remainder after MERGE: |HypoSharesA - HypoSharesB| at the
	// LAST observed tick. Winning side is paid 1.0 USDC per share
	// hypothetically.
	HypoRedeemSide      string  `json:"hypo_redeem_side"`
	HypoRedeemUnits     float64 `json:"hypo_redeem_units"`
	HypoRedeemCost      float64 `json:"hypo_redeem_cost"`
	HypoRedeemRealized  float64 `json:"hypo_redeem_realized"`
	HypoRedeemPnl       float64 `json:"hypo_redeem_pnl"`

	// Sum.
	HypoConditionPnl float64 `json:"hypo_condition_pnl"`
}

// EvaluationSnapshot is the rolling rubric output that the
// `rn1-validate evaluate` subcommand writes. The 8 criteria from
// the design doc §7.2 are evaluated against the appended ledgers
// and reported here.
type EvaluationSnapshot struct {
	GeneratedAt              time.Time `json:"generated_at"`
	WindowStartAt            time.Time `json:"window_start_at"`
	WindowEndAt              time.Time `json:"window_end_at"`
	TotalCandidatesScored    int       `json:"total_candidates_scored"`
	TotalEnvelopeWindows     int       `json:"total_envelope_windows"`
	TotalSettlements         int       `json:"total_settlements"`

	// 8 criteria from §7.2.
	EnvelopeOccurrenceRate   float64           `json:"envelope_occurrence_rate"`   // criterion 1
	MedianMinHypoSumWavg     float64           `json:"median_min_hypo_sum_wavg"`   // criterion 2
	HypoNetPnlTotal          float64           `json:"hypo_net_pnl_total"`         // criterion 3
	FamilyPositiveCount      int               `json:"family_positive_count"`      // criterion 4
	SportPrefixPositiveCount int               `json:"sport_prefix_positive_count"` // criterion 5
	SettlementCoverageRate   float64           `json:"settlement_coverage_rate"`   // criterion 6
	LikenessKsTestPValue     float64           `json:"likeness_ks_test_p_value"`   // criterion 7
	CiInvariantViolations    int               `json:"ci_invariant_violations"`    // criterion 8

	// Diagnostic breakdowns.
	HypoPnlByFamily          map[string]float64 `json:"hypo_pnl_by_family,omitempty"`
	HypoPnlBySportPrefix     map[string]float64 `json:"hypo_pnl_by_sport_prefix,omitempty"`
	HypoSumWavgHistogram     map[string]int     `json:"hypo_sum_wavg_histogram,omitempty"`
}

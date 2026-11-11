// Package shadow implements the read-only Paper / Shadow Mode
// (Phase 1). It tracks operator-selected candidates (WATCH,
// NEEDS_LIQUIDITY_REVIEW, NEEDS_RESCAN, etc.) over multiple
// rn1-live-scan / rn1-live-sim / rn1-live-diff observation ticks
// and appends one JSONL record per tick to a per-day shadow log.
//
// Hard constraints, enforced by static-check tests in
// shadow_test.go:
//
//   - No network calls. The package does not import net/http,
//     net/url, websocket libraries, or any RPC client.
//   - No execution path: no signer, keystore, private key,
//     Polygon RPC, order placement, wallet, or authenticated
//     CLOB endpoint.
//   - No real or simulated PnL, no stake, no position size, no
//     order id, no execution id, no fill data, no WAVG.
//   - No buy/sell/enter/exit recommendation in any output.
//   - No arbitrage / edge / mispricing label.
//   - No automatic decision promotion. The single exception is
//     CLOSED_EVENT (data-driven, see update.go) — every other
//     transition requires an explicit operator-supplied
//     --decision flag.
//
// The shadow tool is the longitudinal extension of the existing
// read-only operating stack. It is NOT a trading bot. See
// docs/rn1-paper-shadow-mode-design.md §K for the honest framing
// vs a hypothetical future execution work-package.
package shadow

import "time"

// ShadowDecision is one closed-enum operator decision label. The
// closed set is documented in
// docs/rn1-paper-shadow-mode-design.md §E. Emitting a value
// outside AllShadowDecisions is a programming error and is
// statically forbidden in output.
type ShadowDecision string

const (
	// First observation tick after the operator marks the
	// candidate. Typically promoted to WATCHING on the next tick.
	DecisionDiscovered ShadowDecision = "DISCOVERED"

	// Candidate is being tracked across snapshots. Equivalent to
	// the review template's WATCH label, used over time.
	DecisionWatching ShadowDecision = "WATCHING"

	// The last observation reported UNKNOWN + book-budget-
	// exhausted, or the operator wants a closer-to-start re-scan
	// before re-classifying.
	DecisionNeedsRescan ShadowDecision = "NEEDS_RESCAN"

	// WARN gate with high spread or low depth and the candidate
	// is still structurally strong — manual liquidity review is
	// required before further classification.
	DecisionNeedsLiquidityReview ShadowDecision = "NEEDS_LIQUIDITY_REVIEW"

	// The operator has stopped tracking this candidate. Often
	// terminal-ish; auto-archived on event-close.
	DecisionRejected ShadowDecision = "REJECTED"

	// The event has passed its horizon, closed, or been resolved.
	// This is the ONLY automatic transition the tool performs —
	// every other decision change must be explicit.
	DecisionClosedEvent ShadowDecision = "CLOSED_EVENT"

	// Terminal manual state. Operator-driven post-event archive
	// for record retention.
	DecisionArchived ShadowDecision = "ARCHIVED"
)

// AllShadowDecisions is the closed set of every decision label
// the shadow tool may emit. The static-check test
// TestShadow_ClosedEnumShadowDecision asserts that no value
// outside this slice escapes into output, and the CLI rejects
// --decision values not in this set.
var AllShadowDecisions = []ShadowDecision{
	DecisionDiscovered,
	DecisionWatching,
	DecisionNeedsRescan,
	DecisionNeedsLiquidityReview,
	DecisionRejected,
	DecisionClosedEvent,
	DecisionArchived,
}

// IsValidDecision reports whether d is one of the closed-enum
// decisions. The CLI uses this to validate operator input.
func IsValidDecision(d ShadowDecision) bool {
	for _, x := range AllShadowDecisions {
		if x == d {
			return true
		}
	}
	return false
}

// ShadowRecord is one JSONL line — one observation tick for one
// shadow_id. The schema deliberately does NOT contain any field
// that would imply a real or simulated trading state. The
// forbidden field-name list lives in shadow_test.go
// (TestShadow_NoForbiddenSchemaFields).
type ShadowRecord struct {
	// --- identity ---
	ShadowID    string `json:"shadow_id"`
	EventSlug   string `json:"event_slug"`
	Title       string `json:"title"`
	SportPrefix string `json:"sport_prefix"`

	// --- timestamps ---
	FirstSeenAt   time.Time  `json:"first_seen_at"`
	ObservationAt time.Time  `json:"observation_at"`
	LastSeenAt    time.Time  `json:"last_seen_at"`
	EventStartTs  *time.Time `json:"event_start_ts,omitempty"`

	// --- current closed-enum state (operator-set unless
	// CLOSED_EVENT auto-transition) ---
	ShadowDecision      ShadowDecision `json:"shadow_decision"`
	CurrentReviewStatus string         `json:"current_review_status,omitempty"`

	// --- latest observation snapshot (sourced from scanner) ---
	SelectorScore    float64  `json:"selector_score"`
	ScoreBand        string   `json:"score_band"`
	LiquidityGate    string   `json:"liquidity_gate"`
	QuoteCoverage    float64  `json:"quote_coverage"`
	AvgSpread        float64  `json:"avg_spread"`
	MinTopDepth      float64  `json:"min_top_depth"`
	BinaryCount      int      `json:"binary_count"`
	MarketCount      int      `json:"market_count"`
	ObservedFamilies []string `json:"observed_families,omitempty"`

	// --- latest simulator labels + scanner risk flags ---
	Labels    []string `json:"labels,omitempty"`
	RiskFlags []string `json:"risk_flags,omitempty"`

	// --- diff classes since the previous tick (empty on first) ---
	DiffClassesSincePrev []string `json:"diff_classes_since_prev,omitempty"`

	// --- cumulative history (extends by one element each tick) ---
	SelectorScoreHistory  []float64  `json:"selector_score_history,omitempty"`
	LiquidityGateHistory  []string   `json:"liquidity_gate_history,omitempty"`
	SimulatorLabelHistory [][]string `json:"simulator_label_history,omitempty"`
	RiskFlagHistory       [][]string `json:"risk_flag_history,omitempty"`
	DiffClassHistory      [][]string `json:"diff_class_history,omitempty"`
	DecisionHistory       []string   `json:"decision_history,omitempty"`

	// --- reviewer-supplied note ---
	ReviewerNote string `json:"reviewer_note,omitempty"`

	// --- append-only audit ---
	AppendedAt   time.Time `json:"appended_at"`
	AppendSource string    `json:"append_source,omitempty"`
}

// ShadowSummary is the per-shadow_id summary returned by the
// report subcommand. It is built from the LATEST record for
// that shadow_id in the log and exposes only observational
// fields — no trading semantics.
type ShadowSummary struct {
	ShadowID             string         `json:"shadow_id"`
	EventSlug            string         `json:"event_slug"`
	Title                string         `json:"title"`
	SportPrefix          string         `json:"sport_prefix"`
	CurrentDecision      ShadowDecision `json:"current_decision"`
	FirstSeenAt          time.Time      `json:"first_seen_at"`
	LastSeenAt           time.Time      `json:"last_seen_at"`
	TickCount            int            `json:"tick_count"`
	LatestSelectorScore  float64        `json:"latest_selector_score"`
	LatestLiquidityGate  string         `json:"latest_liquidity_gate"`
	LatestLabels         []string       `json:"latest_labels,omitempty"`
	LatestRiskFlags      []string       `json:"latest_risk_flags,omitempty"`
	DiffClassesSincePrev []string       `json:"diff_classes_since_prev,omitempty"`
	LatestReviewerNote   string         `json:"latest_reviewer_note,omitempty"`
	DecisionHistory      []string       `json:"decision_history,omitempty"`
}

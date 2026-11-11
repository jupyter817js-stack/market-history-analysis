// Package engine implements the Phase F-1 paper strategy engine.
//
// "Paper" is load-bearing. This package does NOT and will NOT:
//
//   - Open or hold any real position.
//   - Call any authenticated CLOB endpoint.
//   - Read, write, sign, or transmit any wallet, private key, or
//     keystore material.
//   - Submit, cancel, or amend any on-chain transaction.
//   - Touch any Polygon RPC, eth_*, or signed-order endpoint.
//
// The package's sole job is to forward-simulate what would have
// happened if a passive accumulator with the documented sizing
// rules had been operating on the live scanner output. All
// positions are paper, all fills are paper, all PnL is paper, and
// every output field name uses the `paper_` prefix to make this
// crystal clear to downstream consumers.
//
// Real execution lives in a separate Phase F-2 project (not in
// this repository) and requires its own design, audit, and
// authorization.
//
// CI-enforced invariants (engine_test.go):
//
//   - No network imports.
//   - No store / ingest / summary / replay imports (forward-only
//     consumer of scanner + simulator + diff JSON output).
//   - No wallet / signer / private-key / keystore / order-placement
//     tokens in source.
//   - No real-PnL / real-balance / live-PnL vocabulary in output;
//     every monetary field must be `paper_*`.
//   - Closed-enum PhaseLabel; emitting another value is a static
//     error.
//   - Append-only ledgers (fills / merges / redemptions): SHA256
//     byte-prefix invariant.
//
// See docs/rn1-phase2-paper-engine-design.md (renamed to
// "Phase F-1") for the full design and the criteria that gate
// Phase F-2 (live execution).
package engine

import "time"

// PhaseLabel is the closed-enum state-machine phase that a single
// PaperPosition occupies at any given tick. The labels mirror the
// 9-state machine documented in docs/rn1-construction-simulator-
// live-mode-design.md, but every value here is `paper_`-prefixed
// to make the boundary unmistakable in output: a `paper_merge_harvest`
// row is a paper simulation, never a real on-chain MERGE.
type PhaseLabel string

const (
	// PaperIntent: the engine has decided this candidate qualifies
	// for entry, but has NOT yet recorded any paper fill. Used as
	// the initial state during a single tick before the
	// accumulation phase runs.
	PaperIntent PhaseLabel = "paper_intent"

	// PaperAccumulateA: at least one paper fill recorded on side A.
	PaperAccumulateA PhaseLabel = "paper_accumulate_a"

	// PaperOpenB: at least one paper fill recorded on side B
	// (post-A). This is the "both sides observable" state.
	PaperOpenB PhaseLabel = "paper_open_b"

	// PaperExpand: paper fills extended into a second or later
	// derivative market within the same event bundle.
	PaperExpand PhaseLabel = "paper_expand"

	// PaperLayer: additional paper fills on a market already held,
	// triggered by improved envelope or price laddering.
	PaperLayer PhaseLabel = "paper_layer"

	// PaperMergeHarvest: a paper MERGE was simulated -- min(shares_a,
	// shares_b) units crystallized into paper USDC at 1.0 each.
	PaperMergeHarvest PhaseLabel = "paper_merge_harvest"

	// PaperRedeem: the event resolved and the position's remaining
	// shares were redeemed against the resolution outcome.
	PaperRedeem PhaseLabel = "paper_redeem"

	// PaperIdle: terminal state after PaperRedeem completes.
	PaperIdle PhaseLabel = "paper_idle"

	// PaperStopped: terminal state entered by a risk control
	// (per-event stop-loss, daily kill, liquidity degradation
	// fallback). No further fills accepted.
	PaperStopped PhaseLabel = "paper_stopped"
)

// AllPhaseLabels is the closed set of labels emitted by this
// package. The static-check test TestEngine_ClosedEnumPhaseLabel
// asserts that no value outside this slice escapes into output.
var AllPhaseLabels = []PhaseLabel{
	PaperIntent,
	PaperAccumulateA,
	PaperOpenB,
	PaperExpand,
	PaperLayer,
	PaperMergeHarvest,
	PaperRedeem,
	PaperIdle,
	PaperStopped,
}

// IsValidPhase reports whether p is one of the closed-enum
// PhaseLabels.
func IsValidPhase(p PhaseLabel) bool {
	for _, x := range AllPhaseLabels {
		if x == p {
			return true
		}
	}
	return false
}

// SizingDecision is the output of ComputeMarketSize for one binary
// market within one candidate. Either Accepted=true and PaperSizeUsdc
// is set, or Accepted=false and Reason explains why.
//
// The Reason field is human-readable; downstream code should not
// pattern-match on its content (use Accepted instead).
type SizingDecision struct {
	ConditionID      string  `json:"condition_id"`
	Accepted         bool    `json:"accepted"`
	PaperSizeUsdc    float64 `json:"paper_size_usdc"`
	Reason           string  `json:"reason"`

	// Diagnostic carry-throughs for ledger rendering.
	MinDepth         float64 `json:"min_depth"`
	MaxSpread        float64 `json:"max_spread"`
	BaseSizeUsdc     float64 `json:"base_size_usdc"`
	DepthAdjustment  float64 `json:"depth_adjustment"`
}

// PaperFill is one simulated taker fill on one side of one binary
// market. Append-only; written to .paper/fills.jsonl.
//
// "Paper" means: no order was actually placed, no maker matched on
// the other side, and the price + slippage are simulated against
// the scanner's observed top-of-book snapshot.
type PaperFill struct {
	ObservedAt       time.Time `json:"observed_at"`
	CycleTs          string    `json:"cycle_ts"`
	EventSlug        string    `json:"event_slug"`
	ConditionID      string    `json:"condition_id"`

	// EventStartTs is the candidate's StartTs at fill time. Carried
	// in every fill so the redemption detector (PR-4) can ask "has
	// this event passed its start?" without re-reading scanner
	// artifacts. Optional; settlement detection skips legs without
	// it.
	EventStartTs *time.Time `json:"event_start_ts,omitempty"`

	// "a" or "b" -- which of the two clob token IDs in the binary
	// market this fill was on. The engine fills both sides for any
	// market it enters (both-sides-every-leg invariant).
	Side             string    `json:"side"`

	// Simulated execution. paper_shares is the share count we
	// "received"; paper_avg_price is the slippage-adjusted ask;
	// paper_cost_usdc is shares * avg_price + fee.
	PaperShares      float64   `json:"paper_shares"`
	PaperAvgPrice    float64   `json:"paper_avg_price"`
	PaperCostUsdc    float64   `json:"paper_cost_usdc"`
	PaperFeeUsdc     float64   `json:"paper_fee_usdc"`

	// State machine label this fill belongs to.
	Phase            PhaseLabel `json:"phase"`
}

// PaperLeg is the per-(event, condition) running aggregate of
// paper fills. It is rebuilt deterministically from the PaperFill
// rows for the same (event_slug, condition_id) pair. Unlike Phase
// F-0's HypotheticalLeg (uniform 1 unit per tick), a PaperLeg's
// shares come from actual sizing decisions and can be 0 if the
// market never accepted entry.
type PaperLeg struct {
	EventSlug          string  `json:"event_slug"`
	ConditionID        string  `json:"condition_id"`

	PaperSharesA       float64 `json:"paper_shares_a"`
	PaperSharesB       float64 `json:"paper_shares_b"`
	PaperCumCostA      float64 `json:"paper_cum_cost_a"`
	PaperCumCostB      float64 `json:"paper_cum_cost_b"`
	PaperCumFeeUsdc    float64 `json:"paper_cum_fee_usdc"`

	// Cached derived metrics. WAVG = CumCost / Shares.
	PaperWavgA         float64 `json:"paper_wavg_a"`
	PaperWavgB         float64 `json:"paper_wavg_b"`
	PaperSumWavg       float64 `json:"paper_sum_wavg"`

	// Window summary so far (over the leg's lifetime in this
	// ledger).
	FirstFillAt        time.Time  `json:"first_fill_at"`
	LastFillAt         time.Time  `json:"last_fill_at"`
	MinPaperSumWavg    float64    `json:"min_paper_sum_wavg"`
	MinPaperSumWavgAt  time.Time  `json:"min_paper_sum_wavg_at,omitempty"`

	// Current state machine label and the ordered history of
	// labels this leg has cycled through.
	CurrentPhase       PhaseLabel    `json:"current_phase"`
	PhaseHistory       []PhaseLabel  `json:"phase_history,omitempty"`
}

// PaperPosition is the per-event view -- one PaperPosition per
// scanner candidate the engine has decided to enter. It holds zero
// or more PaperLeg entries (one per binary market) and tracks
// position-level aggregates: total paper cost, total paper PnL
// (post-MERGE + post-REDEEM), and the position-level phase.
type PaperPosition struct {
	EventSlug          string                  `json:"event_slug"`
	SportPrefix        string                  `json:"sport_prefix"`
	Title              string                  `json:"title"`

	OpenedAt           time.Time               `json:"opened_at"`
	LastUpdatedAt      time.Time               `json:"last_updated_at"`
	ClosedAt           *time.Time              `json:"closed_at,omitempty"`

	EventStartTs       *time.Time              `json:"event_start_ts,omitempty"`

	Legs               map[string]*PaperLeg    `json:"legs,omitempty"`

	// Per-event paper PnL aggregates. Realized = MERGE + REDEEM
	// gains net of cost. Unrealized = 0 in Phase F-1 (no mark-to-
	// market against current quotes; only realized cashflow).
	PaperTotalCostUsdc     float64    `json:"paper_total_cost_usdc"`
	PaperMergePnlUsdc      float64    `json:"paper_merge_pnl_usdc"`
	PaperRedeemPnlUsdc     float64    `json:"paper_redeem_pnl_usdc"`
	PaperRealizedPnlUsdc   float64    `json:"paper_realized_pnl_usdc"`

	// Position-level phase label. Position transitions to
	// PaperStopped if a risk control fires anywhere within.
	CurrentPhase           PhaseLabel `json:"current_phase"`

	// Reviewer-style status note (free text); never trade-action
	// verbs.
	StatusNote             string     `json:"status_note,omitempty"`
}

// SizingLimits bundles the per-event and per-day caps that the
// sizing module enforces. Defaults match the Phase F-1 design §5
// (MAX_PER_EVENT_USDC=100, MAX_ACTIVE_PAPER_EVENTS=30,
// MAX_DAILY_NEW_ENTRIES=10).
//
// Operators can override at construction time but the defaults
// are chosen to keep one cycle's hypothetical paper notional
// bounded at $250 worst case (10 new entries × $100 / 4 markets/
// event ÷ 1 unit/market = ~$250) which is the operator-visible
// "what's at risk" budget number.
type SizingLimits struct {
	MaxPerEventUsdc      float64
	MaxActivePaperEvents int
	MaxDailyNewEntries   int
	MinTopDepthForFull   float64 // depth at or above this → no adjustment
	MaxSpreadAcceptable  float64 // either side above this → reject entry
}

// DefaultSizingLimits returns the conservative defaults locked by
// docs/rn1-phase2-paper-engine-design.md §5.
func DefaultSizingLimits() SizingLimits {
	return SizingLimits{
		MaxPerEventUsdc:      100.0,
		MaxActivePaperEvents: 30,
		MaxDailyNewEntries:   10,
		MinTopDepthForFull:   200.0,
		MaxSpreadAcceptable:  0.10,
	}
}

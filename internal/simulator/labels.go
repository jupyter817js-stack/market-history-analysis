// Package simulator implements the read-only Construction Simulator
// (live mode). It consumes the public output of rn1-live-scan and
// produces a *descriptive observability trace* against the RN1 9-state
// model, with every state name re-interpreted as a label that
// describes the public-data shape of a candidate event — NEVER as a
// position-state, an action, or a recommendation.
//
// Hard constraints, enforced by static-check tests in
// simulator_test.go:
//
//   - No execution, no order placement, no order cancellation.
//   - No signer, no keystore, no private key, no Polygon RPC.
//   - No authenticated Polymarket endpoint.
//   - No buy/sell/enter/exit language anywhere in output.
//   - No PnL forecast, no expected return, no position sizing.
//   - No arbitrage label.
//   - No automatic MERGE trigger; MERGE shows up only as the
//     observability label MERGE_STRUCTURE_OBSERVABLE.
//   - No bare 9-state names (ACCUMULATE_A / OPEN_B / MERGE_HARVEST /
//     REDEEM) in output. Every live-mode label carries a suffix
//     (_OBSERVABLE / _FUTURE / _PLACEHOLDER / _ELIGIBLE / _READY /
//     _OR_TERMINAL) that distinguishes it from the historical replay
//     engine's authoritative state assertions.
//
// The simulator does not import internal/store, internal/ingest,
// internal/summary, or internal/replay. It may import scanner types
// since the scanner already meets the same guardrails.
package simulator

// Label is a live-mode observability label. The set of valid label
// values is a closed enum; any bare string outside this set is a
// programming error and statically forbidden in output.
type Label string

const (
	// Default. The event passed the scanner's eligibility rules and
	// is being looked at by the simulator, but no further structural
	// preconditions have fired.
	LabelScan Label = "SCAN"

	// Score >= 6, band ∈ {medium, strong, very_strong}, liquidity
	// gate ∈ {PASS, WARN}, and the event is not closed. The event
	// structurally resembles what RN1 historically promoted into
	// PROBE. NOT an assertion that any probing occurred.
	LabelProbeEligible Label = "PROBE_ELIGIBLE"

	// At least one binary market in the event has both a non-zero
	// best bid and best ask with ask > bid. Observation only — no
	// fills, no positions.
	LabelDualSideObservableA Label = "DUAL_SIDE_OBSERVABLE_A"

	// At least two DISTINCT binary markets satisfy
	// DUAL_SIDE_OBSERVABLE_A. The bundle's second side is also
	// observable. No second-side fill is implied.
	LabelDualSideObservableB Label = "DUAL_SIDE_OBSERVABLE_B"

	// BinaryCount >= 4 AND at least one recognized derivative
	// family AND MarketsWithBothQuotes >= 3. The bundle has the
	// structural fingerprint that historically preceded EXPAND.
	LabelExpandReady Label = "EXPAND_READY"

	// The bundle exposes the spread + total offset pattern
	// (spread_ml_total_stack or expanded_companion_stack family
	// signal). The structure is visible; no layering occurred.
	LabelLayerStructureObservable Label = "LAYER_STRUCTURE_OBSERVABLE"

	// The bundle has the multi-condition shape that historically
	// permitted MERGE — i.e. at least 3 binary markets and a
	// recognized soccer/esports family. **NEVER an action prompt.**
	// MERGE cannot be triggered by the simulator and no position
	// exists to merge.
	LabelMergeStructureObservable Label = "MERGE_STRUCTURE_OBSERVABLE"

	// Settlement is in the future. The bare REDEEM state from
	// historical replay can never be "current" in live mode because
	// the event is, by definition, unresolved. This label is a
	// placeholder for the future settlement boundary.
	LabelRedeemHorizonFuture Label = "REDEEM_HORIZON_FUTURE"

	// The event is closed, resolved, or past the operator-defined
	// horizon. When this label fires, it is the only label returned;
	// all other labels are suppressed.
	LabelIdleOrTerminal Label = "IDLE_OR_TERMINAL"
)

// AllLabels lists every label the simulator may emit. Used by the
// static-check tests to assert no bare 9-state name escapes into
// output and that every emitted label is from this closed set.
var AllLabels = []Label{
	LabelScan,
	LabelProbeEligible,
	LabelDualSideObservableA,
	LabelDualSideObservableB,
	LabelExpandReady,
	LabelLayerStructureObservable,
	LabelMergeStructureObservable,
	LabelRedeemHorizonFuture,
	LabelIdleOrTerminal,
}

// ForbiddenBareLabels lists the historical-replay state names that
// MUST NOT appear in simulator output as bare strings. Each one is
// reserved for the authoritative historical-replay engine in
// internal/replay; the simulator uses the _OBSERVABLE / _FUTURE /
// _PLACEHOLDER suffixed variants from AllLabels.
var ForbiddenBareLabels = []string{
	"ACCUMULATE_A",
	"OPEN_B",
	"MERGE_HARVEST",
	"REDEEM",
}

// ForbiddenTradeVocabulary is the closed list of strings the
// simulator's output is grepped for at test time. Any hit fails CI.
// This includes both classic trade-action verbs and the
// trade-economics nouns (PnL / sizing) the simulator must never
// surface.
var ForbiddenTradeVocabulary = []string{
	// action verbs
	"buy",
	"sell",
	"enter long",
	"enter short",
	"exit long",
	"exit short",
	"execute",
	"place order",
	"submit order",
	"take profit",
	"stop loss",
	// economics
	"stake",
	"position size",
	"expected profit",
	"expected return",
	"expected pnl",
	"forecast",
	"arbitrage",
	"arb opportunity",
}

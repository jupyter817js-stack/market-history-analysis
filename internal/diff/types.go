// Package diff implements the read-only Cross-Scan Diff (M5
// Phase 1). It compares two rn1-live-scan JSON snapshots (and
// optional rn1-live-sim trace snapshots) and emits per-candidate
// observational diff records.
//
// Hard constraints, enforced by static-check tests in
// diff_test.go:
//
//   - No network calls: internal/diff and cmd/rn1-live-diff must
//     not import net/http, net/url, websockets, or any RPC client.
//     The diff tool is offline by construction.
//   - No execution path: no signer, keystore, private key,
//     Polygon RPC, order placement, or buy/sell language.
//   - No authentication / wallet / position state.
//   - No automatic decision emission: the diff produces classes
//     and notes but never assigns a review template decision
//     label (WATCH / REJECT / etc.) — that remains a manual
//     human step routed through the existing review template.
//   - DiffClass is a closed enum; review_note is generated from a
//     closed list of templates (see notes.go); both are
//     statically checked by diff_test.go.
//
// The diff package may import internal/scanner and
// internal/simulator (for their public Candidate / Trace / Label
// types) since both already pass equivalent guardrails. It does
// NOT import internal/store, internal/ingest, internal/summary,
// or internal/replay.
package diff

import (
	"time"
)

// DiffClass is one descriptive label for how a candidate moved
// between two snapshots. The full set is closed; emitting a class
// outside AllDiffClasses is a programming error and statically
// forbidden in output.
type DiffClass string

const (
	// Membership
	ClassNewCandidate     DiffClass = "NEW_CANDIDATE"
	ClassDroppedCandidate DiffClass = "DROPPED_CANDIDATE"

	// Rank
	ClassRankUp   DiffClass = "RANK_UP"
	ClassRankDown DiffClass = "RANK_DOWN"

	// Score
	ClassScoreChanged  DiffClass = "SCORE_CHANGED"
	ClassScoreImproved DiffClass = "SCORE_IMPROVED"
	ClassScoreDegraded DiffClass = "SCORE_DEGRADED"
	ClassBandChanged   DiffClass = "BAND_CHANGED"

	// Gate
	ClassGateImproved     DiffClass = "GATE_IMPROVED"
	ClassGateDegraded     DiffClass = "GATE_DEGRADED"
	ClassBecamePass       DiffClass = "BECAME_PASS"
	ClassBecameWarn       DiffClass = "BECAME_WARN"
	ClassBecameFail       DiffClass = "BECAME_FAIL"
	ClassBecameUnknown    DiffClass = "BECAME_UNKNOWN"
	ClassUnknownResolved  DiffClass = "UNKNOWN_RESOLVED"

	// Budget
	ClassBudgetExhaustedResolved DiffClass = "BUDGET_EXHAUSTED_RESOLVED"
	ClassBudgetExhaustedNew      DiffClass = "BUDGET_EXHAUSTED_NEW"

	// Liquidity
	ClassQuoteCoverageImproved DiffClass = "QUOTE_COVERAGE_IMPROVED"
	ClassQuoteCoverageDegraded DiffClass = "QUOTE_COVERAGE_DEGRADED"
	ClassSpreadTightened       DiffClass = "SPREAD_TIGHTENED"
	ClassSpreadWidened         DiffClass = "SPREAD_WIDENED"
	ClassDepthImproved         DiffClass = "DEPTH_IMPROVED"
	ClassDepthDegraded         DiffClass = "DEPTH_DEGRADED"

	// Risk
	ClassRiskFlagsAdded         DiffClass = "RISK_FLAGS_ADDED"
	ClassRiskFlagsRemoved       DiffClass = "RISK_FLAGS_REMOVED"
	ClassHumanReviewStillReq    DiffClass = "HUMAN_REVIEW_STILL_REQUIRED"

	// Simulator labels
	ClassLabelsAdded    DiffClass = "LABELS_ADDED"
	ClassLabelsRemoved  DiffClass = "LABELS_REMOVED"
	ClassLabelSetGrew   DiffClass = "LABEL_SET_GREW"
	ClassLabelSetShrank DiffClass = "LABEL_SET_SHRANK"

	// Terminal
	ClassBecameTerminal DiffClass = "BECAME_TERMINAL"
	ClassBecameClosed   DiffClass = "BECAME_CLOSED"
	ClassBecameInactive DiffClass = "BECAME_INACTIVE"

	// Metadata
	ClassTitleChanged        DiffClass = "TITLE_CHANGED"
	ClassEventIDChanged      DiffClass = "EVENT_ID_CHANGED"
	ClassFamiliesAdded       DiffClass = "FAMILIES_ADDED"
	ClassFamiliesRemoved     DiffClass = "FAMILIES_REMOVED"
	ClassMarketsCountChanged DiffClass = "MARKETS_COUNT_CHANGED"
	ClassReviewStatusChanged DiffClass = "REVIEW_STATUS_CHANGED"

	// Quiet
	ClassNoMaterialChange DiffClass = "NO_MATERIAL_CHANGE"
)

// AllDiffClasses is the closed set of every class the diff may
// emit. The static-check test in diff_test.go asserts that no
// class outside this slice escapes into output.
var AllDiffClasses = []DiffClass{
	ClassNewCandidate, ClassDroppedCandidate,
	ClassRankUp, ClassRankDown,
	ClassScoreChanged, ClassScoreImproved, ClassScoreDegraded, ClassBandChanged,
	ClassGateImproved, ClassGateDegraded,
	ClassBecamePass, ClassBecameWarn, ClassBecameFail, ClassBecameUnknown,
	ClassUnknownResolved,
	ClassBudgetExhaustedResolved, ClassBudgetExhaustedNew,
	ClassQuoteCoverageImproved, ClassQuoteCoverageDegraded,
	ClassSpreadTightened, ClassSpreadWidened,
	ClassDepthImproved, ClassDepthDegraded,
	ClassRiskFlagsAdded, ClassRiskFlagsRemoved, ClassHumanReviewStillReq,
	ClassLabelsAdded, ClassLabelsRemoved, ClassLabelSetGrew, ClassLabelSetShrank,
	ClassBecameTerminal, ClassBecameClosed, ClassBecameInactive,
	ClassTitleChanged, ClassEventIDChanged,
	ClassFamiliesAdded, ClassFamiliesRemoved,
	ClassMarketsCountChanged, ClassReviewStatusChanged,
	ClassNoMaterialChange,
}

// ThresholdConfig holds the noise-suppression thresholds for the
// liquidity diff classes. All fields are operator-configurable
// via CLI flags; defaults match the design report §F.3.
type ThresholdConfig struct {
	// SpreadDelta — minimum absolute change in avg spread that
	// fires SPREAD_TIGHTENED / SPREAD_WIDENED. Default 0.02.
	SpreadDelta float64
	// CoverageDelta — minimum absolute change in
	// FractionWithQuotes that fires QUOTE_COVERAGE_IMPROVED /
	// QUOTE_COVERAGE_DEGRADED. Default 0.10.
	CoverageDelta float64
	// DepthAbsDelta — minimum absolute change in MinTopDepth.
	// Either this OR DepthPctDelta triggers DEPTH_IMPROVED /
	// DEPTH_DEGRADED, whichever is more permissive. Default 100.
	DepthAbsDelta float64
	// DepthPctDelta — minimum relative change in MinTopDepth
	// (fraction of A's value). Default 0.25.
	DepthPctDelta float64
}

// DefaultThresholdConfig returns the conservative defaults from
// the design report.
func DefaultThresholdConfig() ThresholdConfig {
	return ThresholdConfig{
		SpreadDelta:   0.02,
		CoverageDelta: 0.10,
		DepthAbsDelta: 100,
		DepthPctDelta: 0.25,
	}
}

// SnapshotMeta is the per-side identifier metadata. ScannedAt
// must be present; the diff tool uses it for freshness checks
// (B must be later than A; max-age warning thresholds).
type SnapshotMeta struct {
	Label     string    // "A" or "B" or "morning" etc. — display only
	Path      string    // source path on disk
	ScannedAt time.Time // earliest ScannedAt across the records
}

// DiffRecord is one per-candidate output row. Fields with the _A
// / _B suffix carry the corresponding scan's value, or the zero
// value when the candidate is missing from that side (e.g.
// NEW_CANDIDATE has RankA == 0).
type DiffRecord struct {
	EventSlug string

	TitleA string
	TitleB string

	ScanATs time.Time
	ScanBTs time.Time

	RankA     int
	RankB     int
	RankDelta int

	ScoreA float64
	ScoreB float64

	BandA string
	BandB string

	GateA string
	GateB string

	QuoteCoverageA float64
	QuoteCoverageB float64

	AvgSpreadA float64
	AvgSpreadB float64

	MinTopDepthA float64
	MinTopDepthB float64

	BookBudgetExhaustedA int // count of PerMarket entries with the error in scan A
	BookBudgetExhaustedB int

	LabelsA []string
	LabelsB []string

	LabelsAdded   []string
	LabelsRemoved []string

	RiskFlagsA []string
	RiskFlagsB []string

	RiskFlagsAdded   []string
	RiskFlagsRemoved []string

	DiffClasses []DiffClass

	ReviewNote string
}

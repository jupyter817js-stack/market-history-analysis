package shadow

import (
	"fmt"
	"strings"
	"time"

	"rn1-strategy-extractor/internal/scanner"
	"rn1-strategy-extractor/internal/simulator"
)

// Observation bundles one scan tick's data for a single
// event_slug. It is the input to AddRecord / UpdateRecord and is
// derived from existing scanner / simulator / diff JSON
// outputs.
type Observation struct {
	Candidate        *scanner.Candidate
	Trace            *simulator.Trace // optional
	DiffClassesSince []string         // optional; from rn1-live-diff
	Now              time.Time        // reference time for CLOSED_EVENT logic
	AppendSource     string           // CLI command line, for audit
}

// AddInput is the per-call input to the `add` subcommand.
type AddInput struct {
	LogPath      string
	EventSlug    string
	Decision     ShadowDecision
	ReviewerNote string
	Obs          Observation
}

// UpdateInput is the per-call input to the `update` subcommand.
// Decision may be empty — in that case the prior decision is
// kept, unless the data-driven CLOSED_EVENT rule fires.
type UpdateInput struct {
	LogPath      string
	EventSlug    string
	Decision     ShadowDecision // optional; empty means "keep prior or auto-CLOSED_EVENT"
	ReviewerNote string
	Obs          Observation
}

// Add appends an initial ShadowRecord for a previously-untracked
// event_slug. Returns an error if the slug already exists in
// the log (the caller should use Update instead).
func Add(in AddInput) (ShadowRecord, error) {
	if in.LogPath == "" {
		return ShadowRecord{}, fmt.Errorf("shadow log path required")
	}
	if in.EventSlug == "" {
		return ShadowRecord{}, fmt.Errorf("--event-slug required")
	}
	if in.Decision == "" {
		return ShadowRecord{}, fmt.Errorf("--decision required for add (one of: %s)",
			joinDecisions(AllShadowDecisions))
	}
	if !IsValidDecision(in.Decision) {
		return ShadowRecord{}, fmt.Errorf("invalid --decision %q; must be one of: %s",
			in.Decision, joinDecisions(AllShadowDecisions))
	}
	if in.Obs.Candidate == nil {
		return ShadowRecord{}, fmt.Errorf("scan input required (Observation.Candidate is nil)")
	}
	if in.Obs.Candidate.EventSlug != in.EventSlug {
		return ShadowRecord{}, fmt.Errorf(
			"event-slug mismatch: --event-slug=%q but scan candidate is %q",
			in.EventSlug, in.Obs.Candidate.EventSlug)
	}

	existing, err := ReadLog(in.LogPath)
	if err != nil {
		return ShadowRecord{}, err
	}
	for _, r := range existing {
		if r.EventSlug == in.EventSlug {
			return ShadowRecord{}, fmt.Errorf(
				"event_slug %q already tracked in this log (shadow_id=%s) — use `update` subcommand instead",
				in.EventSlug, r.ShadowID)
		}
	}

	now := in.Obs.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rec := buildRecord(in.Obs, in.EventSlug, in.Decision, in.ReviewerNote, nil, now)
	rec.FirstSeenAt = rec.ObservationAt
	if err := AppendLog(in.LogPath, rec); err != nil {
		return ShadowRecord{}, err
	}
	return rec, nil
}

// Update appends a new tick for an existing shadow_id. The
// Decision field may be empty:
//
//   - If empty AND the data-driven CLOSED_EVENT rule fires
//     (event is Closed=true OR past start time), the new tick
//     auto-promotes to CLOSED_EVENT.
//   - If empty AND the rule does NOT fire, the prior decision
//     is carried forward unchanged.
//
// Update returns an error when the slug is not in the log
// (operator should `add` first).
func Update(in UpdateInput) (ShadowRecord, error) {
	if in.LogPath == "" {
		return ShadowRecord{}, fmt.Errorf("shadow log path required")
	}
	if in.EventSlug == "" {
		return ShadowRecord{}, fmt.Errorf("--event-slug required")
	}
	if in.Decision != "" && !IsValidDecision(in.Decision) {
		return ShadowRecord{}, fmt.Errorf("invalid --decision %q; must be one of: %s",
			in.Decision, joinDecisions(AllShadowDecisions))
	}

	existing, err := ReadLog(in.LogPath)
	if err != nil {
		return ShadowRecord{}, err
	}
	var prior *ShadowRecord
	for i := range existing {
		r := existing[i]
		if r.EventSlug == in.EventSlug {
			prior = &r // keep advancing — last write wins
		}
	}
	if prior == nil {
		return ShadowRecord{}, fmt.Errorf(
			"event_slug %q not yet tracked in this log — use `add` subcommand first",
			in.EventSlug)
	}

	now := in.Obs.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	// Determine final decision. Operator-supplied decision wins.
	// If absent, apply the CLOSED_EVENT auto-rule, else keep prior.
	finalDecision := in.Decision
	if finalDecision == "" {
		finalDecision = prior.ShadowDecision
		if shouldAutoCloseEvent(in.Obs.Candidate, now) {
			finalDecision = DecisionClosedEvent
		}
	}

	rec := buildRecord(in.Obs, in.EventSlug, finalDecision, in.ReviewerNote, prior, now)
	if err := AppendLog(in.LogPath, rec); err != nil {
		return ShadowRecord{}, err
	}
	return rec, nil
}

// shouldAutoCloseEvent implements the ONLY data-driven automatic
// transition: a scan with Candidate.Closed=true OR an event whose
// StartTs has passed triggers CLOSED_EVENT on the next update.
// When the candidate is nil (slug not in the scan), the rule
// does NOT fire — ambiguity is not a closure signal.
func shouldAutoCloseEvent(c *scanner.Candidate, now time.Time) bool {
	if c == nil {
		return false
	}
	if c.Closed {
		return true
	}
	if c.StartTs != nil && !c.StartTs.UTC().After(now.UTC()) {
		return true
	}
	return false
}

func buildRecord(obs Observation, slug string, decision ShadowDecision, note string, prior *ShadowRecord, now time.Time) ShadowRecord {
	rec := ShadowRecord{
		ShadowID:       shadowIDFor(slug),
		EventSlug:      slug,
		ShadowDecision: decision,
		ObservationAt:  now,
		LastSeenAt:     now,
		AppendedAt:     now,
		AppendSource:   obs.AppendSource,
		ReviewerNote:   note,
	}
	if obs.Candidate != nil {
		c := obs.Candidate
		rec.Title = c.Title
		rec.SportPrefix = c.SportPrefix
		rec.SelectorScore = c.Score
		rec.ScoreBand = c.Band
		rec.LiquidityGate = string(c.LiquidityGate)
		rec.QuoteCoverage = c.LiquidityResult.FractionWithQuotes
		rec.AvgSpread = c.LiquidityResult.AvgSpread
		rec.MinTopDepth = c.LiquidityResult.MinTopDepth
		rec.BinaryCount = c.BinaryCount
		rec.MarketCount = c.MarketCount
		rec.ObservedFamilies = append([]string(nil), c.Families...)
		rec.RiskFlags = riskFlagStrings(c.RiskFlags)
		rec.CurrentReviewStatus = c.ReviewStatus
		if c.StartTs != nil {
			ts := *c.StartTs
			rec.EventStartTs = &ts
		}
	}
	if obs.Trace != nil {
		rec.Labels = labelStrings(obs.Trace.Labels)
	}
	rec.DiffClassesSincePrev = append([]string(nil), obs.DiffClassesSince...)

	// History extension: start fresh for first tick, otherwise
	// extend prior's history by exactly one element.
	if prior != nil {
		rec.FirstSeenAt = prior.FirstSeenAt
		rec.SelectorScoreHistory = append(append([]float64(nil), prior.SelectorScoreHistory...), rec.SelectorScore)
		rec.LiquidityGateHistory = append(append([]string(nil), prior.LiquidityGateHistory...), rec.LiquidityGate)
		rec.SimulatorLabelHistory = append(append([][]string(nil), prior.SimulatorLabelHistory...), append([]string(nil), rec.Labels...))
		rec.RiskFlagHistory = append(append([][]string(nil), prior.RiskFlagHistory...), append([]string(nil), rec.RiskFlags...))
		rec.DiffClassHistory = append(append([][]string(nil), prior.DiffClassHistory...), append([]string(nil), rec.DiffClassesSincePrev...))
		rec.DecisionHistory = append(append([]string(nil), prior.DecisionHistory...), string(rec.ShadowDecision))
	} else {
		rec.FirstSeenAt = now
		rec.SelectorScoreHistory = []float64{rec.SelectorScore}
		rec.LiquidityGateHistory = []string{rec.LiquidityGate}
		rec.SimulatorLabelHistory = [][]string{append([]string(nil), rec.Labels...)}
		rec.RiskFlagHistory = [][]string{append([]string(nil), rec.RiskFlags...)}
		rec.DiffClassHistory = [][]string{append([]string(nil), rec.DiffClassesSincePrev...)}
		rec.DecisionHistory = []string{string(rec.ShadowDecision)}
	}
	return rec
}

func shadowIDFor(slug string) string {
	return "sh-" + slug
}

func riskFlagStrings(rfs []scanner.RiskFlag) []string {
	out := make([]string, 0, len(rfs))
	for _, r := range rfs {
		out = append(out, string(r))
	}
	return out
}

func labelStrings(ls []simulator.Label) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, string(l))
	}
	return out
}

func joinDecisions(ds []ShadowDecision) string {
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		parts = append(parts, string(d))
	}
	return strings.Join(parts, " | ")
}

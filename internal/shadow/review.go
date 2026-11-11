package shadow

import (
	"sort"
	"time"
)

// ReviewQueueFilter restricts a shadow log to the records the operator
// likely needs to look at next. The default queue (zero-valued filter)
// surfaces DISCOVERED / NEEDS_RESCAN / NEEDS_LIQUIDITY_REVIEW — i.e.
// every state that explicitly waits on operator input.
//
// WATCHING is NOT included by default because it is the steady-state
// label for "I've decided to track this, no action needed right now".
// Operators who want the watch-list use the existing report subcommand
// with --decision=WATCHING.
type ReviewQueueFilter struct {
	// Decisions, when non-empty, replaces the default decision set
	// with an explicit one. Records whose latest decision is not in
	// the set are excluded.
	Decisions []ShadowDecision

	// StartingWithin restricts to events whose EventStartTs is within
	// this duration from Now. Zero = no time filter.
	StartingWithin time.Duration

	// MinScore restricts to records whose latest SelectorScore is at
	// least this value. 0 = no floor.
	MinScore float64

	// Now is the reference time for the StartingWithin filter. Zero =
	// time.Now().UTC().
	Now time.Time
}

// ReviewQueue returns the per-shadow_id summaries that match the
// filter, sorted by event start time ascending (events starting soonest
// come first). Records without an EventStartTs sort to the end.
func ReviewQueue(records []ShadowRecord, f ReviewQueueFilter) []ShadowSummary {
	now := f.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	decisionSet := map[ShadowDecision]bool{}
	if len(f.Decisions) > 0 {
		for _, d := range f.Decisions {
			decisionSet[d] = true
		}
	} else {
		decisionSet[DecisionDiscovered] = true
		decisionSet[DecisionNeedsRescan] = true
		decisionSet[DecisionNeedsLiquidityReview] = true
	}

	// Build the full report first; the existing Report() helper
	// already deduplicates to latest tick per shadow_id.
	full := Report(records, ReportFilter{})

	// We also need per-record EventStartTs, which Report() does not
	// expose. Index latest record per shadow_id from the input log.
	latest := map[string]ShadowRecord{}
	for _, r := range records {
		prev, ok := latest[r.ShadowID]
		if !ok || r.ObservationAt.After(prev.ObservationAt) {
			latest[r.ShadowID] = r
		}
	}

	out := make([]ShadowSummary, 0, len(full))
	for _, s := range full {
		if !decisionSet[s.CurrentDecision] {
			continue
		}
		rec := latest[s.ShadowID]
		if f.StartingWithin > 0 {
			if rec.EventStartTs == nil {
				continue
			}
			d := rec.EventStartTs.Sub(now)
			if d < 0 || d > f.StartingWithin {
				continue
			}
		}
		if f.MinScore > 0 && s.LatestSelectorScore < f.MinScore {
			continue
		}
		out = append(out, s)
	}

	// Sort by event start time asc (closest to start first); records
	// without a start time sink to the end.
	sort.SliceStable(out, func(i, j int) bool {
		ri := latest[out[i].ShadowID]
		rj := latest[out[j].ShadowID]
		switch {
		case ri.EventStartTs == nil && rj.EventStartTs == nil:
			return out[i].EventSlug < out[j].EventSlug
		case ri.EventStartTs == nil:
			return false
		case rj.EventStartTs == nil:
			return true
		default:
			return ri.EventStartTs.Before(*rj.EventStartTs)
		}
	})
	return out
}

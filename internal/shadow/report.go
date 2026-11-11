package shadow

import (
	"sort"
)

// ReportFilter narrows which shadow_ids the report covers. All
// fields are optional; zero-valued fields mean "no filter".
type ReportFilter struct {
	Decision  ShadowDecision
	EventSlug string
}

// Report builds the per-shadow_id summaries from a JSONL log
// (already loaded via ReadLog). The function makes no IO, no
// network call, and does not mutate the input slice.
func Report(records []ShadowRecord, f ReportFilter) []ShadowSummary {
	if len(records) == 0 {
		return nil
	}
	// Tick count per shadow_id, last record retained.
	type slot struct {
		latest ShadowRecord
		count  int
	}
	bySlug := map[string]*slot{}
	for _, r := range records {
		s, ok := bySlug[r.ShadowID]
		if !ok {
			s = &slot{}
			bySlug[r.ShadowID] = s
		}
		s.latest = r
		s.count++
	}

	out := make([]ShadowSummary, 0, len(bySlug))
	for _, s := range bySlug {
		r := s.latest
		if f.Decision != "" && r.ShadowDecision != f.Decision {
			continue
		}
		if f.EventSlug != "" && r.EventSlug != f.EventSlug {
			continue
		}
		out = append(out, ShadowSummary{
			ShadowID:             r.ShadowID,
			EventSlug:            r.EventSlug,
			Title:                r.Title,
			SportPrefix:          r.SportPrefix,
			CurrentDecision:      r.ShadowDecision,
			FirstSeenAt:          r.FirstSeenAt,
			LastSeenAt:           r.LastSeenAt,
			TickCount:            s.count,
			LatestSelectorScore:  r.SelectorScore,
			LatestLiquidityGate:  r.LiquidityGate,
			LatestLabels:         append([]string(nil), r.Labels...),
			LatestRiskFlags:      append([]string(nil), r.RiskFlags...),
			DiffClassesSincePrev: append([]string(nil), r.DiffClassesSincePrev...),
			LatestReviewerNote:   r.ReviewerNote,
			DecisionHistory:      append([]string(nil), r.DecisionHistory...),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		// Active states first, terminal states last; within each
		// group sort by latest_seen_at descending.
		gi, gj := decisionGroup(out[i].CurrentDecision), decisionGroup(out[j].CurrentDecision)
		if gi != gj {
			return gi < gj
		}
		if !out[i].LastSeenAt.Equal(out[j].LastSeenAt) {
			return out[i].LastSeenAt.After(out[j].LastSeenAt)
		}
		return out[i].EventSlug < out[j].EventSlug
	})
	return out
}

func decisionGroup(d ShadowDecision) int {
	switch d {
	case DecisionWatching:
		return 1
	case DecisionNeedsRescan, DecisionNeedsLiquidityReview:
		return 2
	case DecisionDiscovered:
		return 3
	case DecisionRejected:
		return 4
	case DecisionClosedEvent:
		return 5
	case DecisionArchived:
		return 6
	}
	return 7
}

package engine

import (
	"sort"
	"time"
)

// BuildPaperLegs groups PaperFill rows by (event_slug, condition_id)
// and builds the running PaperLeg aggregate for each. Within each
// leg the fills are sorted chronologically before aggregation so
// the running WAVG is computed in temporal order.
//
// CurrentPhase is set from the simplest signal:
//   - A fills only         -> paper_accumulate_a
//   - A and B fills        -> paper_open_b
//
// More sophisticated phase transitions (paper_expand,
// paper_layer, paper_merge_harvest, paper_redeem, paper_stopped)
// are layered on by PR-3 / PR-4 via separate post-processing
// passes. PR-2 only emits the entry-side phases.
//
// PhaseHistory contains the entry-side phases in the order they
// were first observed for this leg.
//
// The map key is event_slug + "|" + condition_id (same convention
// as F-0's BuildLegStates).
func BuildPaperLegs(fills []PaperFill) map[string]*PaperLeg {
	groups := map[string][]PaperFill{}
	for _, f := range fills {
		key := f.EventSlug + "|" + f.ConditionID
		groups[key] = append(groups[key], f)
	}

	out := make(map[string]*PaperLeg, len(groups))
	for key, fs := range groups {
		sort.SliceStable(fs, func(i, j int) bool {
			return fs[i].ObservedAt.Before(fs[j].ObservedAt)
		})
		leg := &PaperLeg{
			EventSlug:       fs[0].EventSlug,
			ConditionID:     fs[0].ConditionID,
			FirstFillAt:     fs[0].ObservedAt,
			MinPaperSumWavg: 2.0, // upper bound; both-sides sum tops at 2
			CurrentPhase:    PaperIntent,
		}
		phaseSeen := map[PhaseLabel]bool{}
		for _, f := range fs {
			leg.PaperCumFeeUsdc += f.PaperFeeUsdc
			switch f.Side {
			case "a":
				leg.PaperSharesA += f.PaperShares
				leg.PaperCumCostA += f.PaperCostUsdc
			case "b":
				leg.PaperSharesB += f.PaperShares
				leg.PaperCumCostB += f.PaperCostUsdc
			}
			leg.PaperWavgA = safeWavg(leg.PaperCumCostA, leg.PaperSharesA)
			leg.PaperWavgB = safeWavg(leg.PaperCumCostB, leg.PaperSharesB)
			if leg.PaperSharesA > 0 && leg.PaperSharesB > 0 {
				leg.PaperSumWavg = leg.PaperWavgA + leg.PaperWavgB
				if leg.PaperSumWavg < leg.MinPaperSumWavg {
					leg.MinPaperSumWavg = leg.PaperSumWavg
					leg.MinPaperSumWavgAt = f.ObservedAt
				}
			}
			leg.LastFillAt = f.ObservedAt

			// Record phase if this fill's phase is a new entry-side
			// label we haven't recorded yet.
			if !phaseSeen[f.Phase] {
				phaseSeen[f.Phase] = true
				leg.PhaseHistory = append(leg.PhaseHistory, f.Phase)
			}
		}

		// Derive CurrentPhase from the leg's filled state. The
		// entry-side rule:
		//   both sides filled  -> paper_open_b
		//   side a only         -> paper_accumulate_a
		//   no fills (unusual)  -> paper_intent
		switch {
		case leg.PaperSharesA > 0 && leg.PaperSharesB > 0:
			leg.CurrentPhase = PaperOpenB
		case leg.PaperSharesA > 0:
			leg.CurrentPhase = PaperAccumulateA
		default:
			leg.CurrentPhase = PaperIntent
		}

		// If the leg never had both sides filled, MinPaperSumWavg
		// stays at the upper bound (2.0). Reset to 0 in that case
		// so consumers can use "0 means never two-sided" cleanly.
		if leg.PaperSharesA == 0 || leg.PaperSharesB == 0 {
			leg.MinPaperSumWavg = 0
			leg.MinPaperSumWavgAt = time.Time{}
		}

		out[key] = leg
	}
	return out
}

// BuildPaperPositions reduces a slice of PaperFill rows into a
// per-event PaperPosition map. Each position aggregates its leg
// states + position-level cost / PnL totals.
//
// PR-2 reports only PaperTotalCostUsdc (sum of leg cum-costs).
// MERGE / REDEEM PnL are zero until PR-3 / PR-4 add the phase
// transition logic. Position-level CurrentPhase is the
// rightmost-progressed phase across its legs.
func BuildPaperPositions(fills []PaperFill) map[string]*PaperPosition {
	legs := BuildPaperLegs(fills)
	positions := map[string]*PaperPosition{}
	for _, leg := range legs {
		pos, ok := positions[leg.EventSlug]
		if !ok {
			pos = &PaperPosition{
				EventSlug:    leg.EventSlug,
				Legs:         map[string]*PaperLeg{},
				CurrentPhase: PaperIntent,
				OpenedAt:     leg.FirstFillAt,
			}
			positions[leg.EventSlug] = pos
		}
		pos.Legs[leg.ConditionID] = leg
		pos.PaperTotalCostUsdc += leg.PaperCumCostA + leg.PaperCumCostB
		if leg.FirstFillAt.Before(pos.OpenedAt) {
			pos.OpenedAt = leg.FirstFillAt
		}
		if leg.LastFillAt.After(pos.LastUpdatedAt) {
			pos.LastUpdatedAt = leg.LastFillAt
		}
		// Carry the most recent EventStartTs seen across this
		// event's fills (any leg will do; they're all the same
		// event).
		if ts := latestEventStartTs(fills, leg.EventSlug); ts != nil {
			startCopy := *ts
			pos.EventStartTs = &startCopy
		}
		// Position-level phase is the most-advanced leg phase
		// per a closed ordering.
		if phaseRank(leg.CurrentPhase) > phaseRank(pos.CurrentPhase) {
			pos.CurrentPhase = leg.CurrentPhase
		}
	}
	return positions
}

// phaseRank gives an integer ordering over PhaseLabel so we can
// pick the "most advanced" phase across a position's legs.
// Higher = more progressed.
func phaseRank(p PhaseLabel) int {
	switch p {
	case PaperIntent:
		return 0
	case PaperAccumulateA:
		return 1
	case PaperOpenB:
		return 2
	case PaperExpand:
		return 3
	case PaperLayer:
		return 4
	case PaperMergeHarvest:
		return 5
	case PaperRedeem:
		return 6
	case PaperIdle:
		return 7
	case PaperStopped:
		return 8 // terminal, ranks above idle so the label sticks
	default:
		return -1
	}
}

func safeWavg(cost, shares float64) float64 {
	if shares == 0 {
		return 0
	}
	return cost / shares
}

// latestEventStartTs scans all fills for the given event_slug and
// returns the most recently-observed EventStartTs (the latest
// ObservedAt that carried a non-nil EventStartTs). Returns nil if
// no fill for this event ever carried a StartTs.
func latestEventStartTs(fills []PaperFill, eventSlug string) *time.Time {
	var bestAt time.Time
	var bestTs *time.Time
	for i := range fills {
		f := &fills[i]
		if f.EventSlug != eventSlug || f.EventStartTs == nil {
			continue
		}
		if bestTs == nil || f.ObservedAt.After(bestAt) {
			bestAt = f.ObservedAt
			ts := *f.EventStartTs
			bestTs = &ts
		}
	}
	return bestTs
}

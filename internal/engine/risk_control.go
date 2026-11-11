package engine

import (
	"fmt"
	"time"
)

// RiskLimits bundles the risk-control thresholds. Defaults from
// design §8.
type RiskLimits struct {
	// PerEventStopLossUsdc: when a position's realized PnL drops
	// below this value, the engine marks the position as
	// paper_stopped and accepts no further fills for it.
	PerEventStopLossUsdc float64

	// DailyDrawdownKillUsdc: when the sum of realized PnL across
	// all positions over the last 24h drops below this value,
	// the engine pauses new entries (existing positions still
	// MERGE / REDEEM normally).
	DailyDrawdownKillUsdc float64
}

// DefaultRiskLimits returns the conservative locked defaults from
// design §8.1-§8.2.
func DefaultRiskLimits() RiskLimits {
	return RiskLimits{
		PerEventStopLossUsdc:  -50.0,
		DailyDrawdownKillUsdc: -200.0,
	}
}

// RiskCheckResult is the verdict of a per-event risk check.
type RiskCheckResult struct {
	ShouldStop bool
	Reason     string
}

// CheckPerEventStop returns ShouldStop=true if the position's
// realized PnL has fallen below the per-event stop-loss threshold.
// The position should be marked paper_stopped and no further
// fills accepted.
func CheckPerEventStop(pos *PaperPosition, limits RiskLimits) RiskCheckResult {
	if pos == nil {
		return RiskCheckResult{}
	}
	if pos.PaperRealizedPnlUsdc < limits.PerEventStopLossUsdc {
		return RiskCheckResult{
			ShouldStop: true,
			Reason: fmt.Sprintf("realized PnL %.2f below stop-loss %.2f",
				pos.PaperRealizedPnlUsdc, limits.PerEventStopLossUsdc),
		}
	}
	return RiskCheckResult{}
}

// DailyKillResult is the verdict of the rolling-24h drawdown
// check.
type DailyKillResult struct {
	ShouldPause      bool
	Reason           string
	WindowPnlUsdc    float64
	WindowStartedAt  time.Time
}

// CheckDailyKill computes the rolling 24h paper PnL across the
// supplied merges + redemptions and reports whether new entries
// should be paused.
//
// The 24h window is anchored at `now - 24h`. Records older than
// that are ignored. The function does NOT modify the records
// slice.
//
// Existing positions are unaffected: their MERGE / REDEEM logic
// continues to run normally. Only NEW entries are blocked.
func CheckDailyKill(merges []PaperMerge, redemptions []PaperRedemption,
	now time.Time, limits RiskLimits) DailyKillResult {
	windowStart := now.Add(-24 * time.Hour)
	pnl := 0.0
	for _, m := range merges {
		if m.ObservedAt.After(windowStart) {
			pnl += m.PaperMergePnlUsdc
		}
	}
	for _, r := range redemptions {
		if r.ObservedAt.After(windowStart) {
			pnl += r.PaperRedeemPnlUsdc
		}
	}
	res := DailyKillResult{
		WindowPnlUsdc:   pnl,
		WindowStartedAt: windowStart,
	}
	if pnl < limits.DailyDrawdownKillUsdc {
		res.ShouldPause = true
		res.Reason = fmt.Sprintf("rolling-24h paper PnL %.2f below kill threshold %.2f",
			pnl, limits.DailyDrawdownKillUsdc)
	}
	return res
}

// CountActivePositions returns the number of positions whose
// CurrentPhase is something other than paper_idle or paper_stopped.
// Used by the entry decider to enforce SizingLimits.MaxActivePaperEvents.
func CountActivePositions(positions map[string]*PaperPosition) int {
	n := 0
	for _, p := range positions {
		if p == nil {
			continue
		}
		switch p.CurrentPhase {
		case PaperIdle, PaperStopped:
			continue
		default:
			n++
		}
	}
	return n
}

// CountNewEntriesIn24h scans the fills ledger and counts the
// number of distinct event_slugs whose first fill is within the
// last 24h. Used by the entry decider to enforce
// SizingLimits.MaxDailyNewEntries.
func CountNewEntriesIn24h(fills []PaperFill, now time.Time) int {
	windowStart := now.Add(-24 * time.Hour)
	// Find first-fill time per event_slug.
	firstFillBySlug := map[string]time.Time{}
	for _, f := range fills {
		if t, ok := firstFillBySlug[f.EventSlug]; !ok || f.ObservedAt.Before(t) {
			firstFillBySlug[f.EventSlug] = f.ObservedAt
		}
	}
	n := 0
	for _, t := range firstFillBySlug {
		if t.After(windowStart) {
			n++
		}
	}
	return n
}

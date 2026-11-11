package validation

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// MERGE_THRESHOLD is the SumWavg level below which a passive
// accumulator would have had a profitable MERGE opportunity. Below
// this level, MERGE units * 1.0 USDC > MERGE cost. The 0.95 value
// is the conservative envelope threshold from
// docs/rn1-phase-f0-cross-validation-design.md §7.2 criterion 2.
const MERGE_THRESHOLD = 0.95

// ComputeHypoPnL produces one ConditionResolution row from the
// hypothetical-accumulator model:
//
//   - Walk ticks chronologically, finding the index K where the
//     running SumWavg was at its minimum.
//   - If that minimum was below MERGE_THRESHOLD, simulate a MERGE
//     at tick K: merge K+1 units (1 per tick from 0..K inclusive),
//     spending cumA[K] + cumB[K], receiving (K+1) USDC.
//   - Post-MERGE: continue accumulating ticks K+1..N-1 (one per
//     tick on each side). At settlement, the winning side pays
//     1.0 USDC per remaining share; losing side pays 0.
//
// winningSide must be "A", "B", "DRAW", or "UNKNOWN":
//
//   - "A" / "B": REDEEM the post-merge remainder on that side.
//   - "DRAW": REDEEM realized is 0 (conservative; some markets
//     pay 0.5/0.5 but Phase F-0 does not assume this).
//   - "UNKNOWN": REDEEM realized is 0 and HypoRedeemSide is set
//     to "UNKNOWN"; downstream consumers should treat the row as
//     awaiting resolution.
//
// The returned ConditionResolution has all hypothetical fields
// populated. ConditionID is copied from the leg.
func ComputeHypoPnL(leg *HypotheticalLeg, ticks []TickObservation, winningSide string) ConditionResolution {
	if leg == nil || leg.TickCount == 0 || len(ticks) == 0 {
		return ConditionResolution{
			ConditionID: legCondID(leg),
			WinningSide: winningSide,
		}
	}

	// Defensive chronological sort.
	sorted := append([]TickObservation(nil), ticks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ObservedAt.Before(sorted[j].ObservedAt)
	})

	// Walk to find the optimal MERGE point and accumulate
	// final cum costs in one pass.
	var (
		cumA, cumB        float64
		minIdx            = -1
		minSumWavg        = 2.0 // upper bound
		cumAAtMin         float64
		cumBAtMin         float64
		minObservedAtCalc time.Time
	)
	for i, t := range sorted {
		cumA += t.AskA
		cumB += t.AskB
		n := float64(i + 1)
		sw := (cumA / n) + (cumB / n)
		if sw < minSumWavg {
			minSumWavg = sw
			minIdx = i
			cumAAtMin = cumA
			cumBAtMin = cumB
			minObservedAtCalc = t.ObservedAt
		}
	}
	totalCumA := cumA
	totalCumB := cumB
	totalShares := float64(len(sorted))

	res := ConditionResolution{
		ConditionID: legCondID(leg),
		WinningSide: winningSide,
	}

	// MERGE simulation (only if envelope crossed threshold).
	var mergeUnits, mergeCost, mergeRealized float64
	if minIdx >= 0 && minSumWavg < MERGE_THRESHOLD {
		mergeUnits = float64(minIdx + 1)
		mergeCost = cumAAtMin + cumBAtMin
		mergeRealized = mergeUnits * 1.0 // Polymarket merge: 1A + 1B = 1 USDC
		res.MergeAt = minObservedAtCalc
		res.HypoMergeUnits = mergeUnits
		res.HypoMergeCost = mergeCost
		res.HypoMergeRealized = mergeRealized
		res.HypoMergePnl = mergeRealized - mergeCost
	}

	// REDEEM simulation on the post-MERGE remainder.
	remainderShares := totalShares - mergeUnits
	remainderCostA := totalCumA - cumAAtMin // post-merge cumulative on A only
	remainderCostB := totalCumB - cumBAtMin
	remainderCost := remainderCostA + remainderCostB

	switch winningSide {
	case "A", "B":
		res.HypoRedeemSide = winningSide
		res.HypoRedeemUnits = remainderShares
		res.HypoRedeemCost = remainderCost
		res.HypoRedeemRealized = remainderShares * 1.0
	case "DRAW":
		res.HypoRedeemSide = "DRAW"
		res.HypoRedeemUnits = remainderShares
		res.HypoRedeemCost = remainderCost
		res.HypoRedeemRealized = 0
	default:
		res.HypoRedeemSide = "UNKNOWN"
		res.HypoRedeemUnits = remainderShares
		res.HypoRedeemCost = remainderCost
		res.HypoRedeemRealized = 0
	}
	res.HypoRedeemPnl = res.HypoRedeemRealized - res.HypoRedeemCost
	res.HypoConditionPnl = res.HypoMergePnl + res.HypoRedeemPnl

	return res
}

func legCondID(leg *HypotheticalLeg) string {
	if leg == nil {
		return ""
	}
	return leg.ConditionID
}

// BuildSettlementOutcome aggregates a list of ConditionResolution
// rows into one SettlementOutcome record per event. The caller
// supplies the per-condition resolutions (one per binary market in
// the event) and the winning-outcomes set (from Gamma, optional).
//
// All aggregate fields are pure sums of the per-condition fields.
// HypoReturnPct is HypoNetPnl / HypoTotalCost when total cost > 0.
func BuildSettlementOutcome(
	eventSlug string,
	resolvedAt time.Time,
	winningOutcomes []string,
	perCondition []ConditionResolution,
) SettlementOutcome {
	out := SettlementOutcome{
		EventSlug:       eventSlug,
		ResolvedAt:      resolvedAt,
		WinningOutcomes: append([]string(nil), winningOutcomes...),
		PerCondition:    append([]ConditionResolution(nil), perCondition...),
	}
	for _, r := range perCondition {
		out.HypoMergePnl += r.HypoMergePnl
		out.HypoRedeemPnl += r.HypoRedeemPnl
		out.HypoTotalCost += r.HypoMergeCost + r.HypoRedeemCost
	}
	out.HypoNetPnl = out.HypoMergePnl + out.HypoRedeemPnl
	if out.HypoTotalCost > 0 {
		out.HypoReturnPct = 100.0 * out.HypoNetPnl / out.HypoTotalCost
	}
	return out
}

// AppendSettlement writes a single SettlementOutcome to the
// settlements JSONL ledger. The ledger is append-only; existing
// rows are never modified. Multiple SettlementOutcome rows for the
// same event_slug are permitted (e.g., later WinningOutcomes
// resolution after an initial UNKNOWN-side placeholder), and the
// reader should take the LAST row as authoritative.
func AppendSettlement(ledgerPath string, outcome SettlementOutcome) error {
	if ledgerPath == "" {
		return fmt.Errorf("ledger path required")
	}
	if outcome.EventSlug == "" {
		return fmt.Errorf("settlement requires event_slug")
	}
	if outcome.ResolvedAt.IsZero() {
		return fmt.Errorf("settlement requires resolved_at")
	}
	body, err := json.Marshal(outcome)
	if err != nil {
		return fmt.Errorf("marshal settlement: %w", err)
	}
	f, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open ledger %q: %w", ledgerPath, err)
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("append settlement: %w", err)
	}
	return nil
}

// ReadSettlements loads every SettlementOutcome from the ledger in
// append order. Missing or empty file yields a nil slice.
func ReadSettlements(ledgerPath string) ([]SettlementOutcome, error) {
	if ledgerPath == "" {
		return nil, fmt.Errorf("ledger path required")
	}
	body, err := os.ReadFile(ledgerPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ledger %q: %w", ledgerPath, err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	if len(body) >= 3 && body[0] == 0xEF && body[1] == 0xBB && body[2] == 0xBF {
		body = body[3:]
	}
	var out []SettlementOutcome
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var s SettlementOutcome
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			return nil, fmt.Errorf("decode settlement line %q: %w", line, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// LatestSettlementByEvent returns the most recently-appended
// SettlementOutcome for each event_slug. Useful for the evaluation
// rubric, which only cares about the latest known resolution.
func LatestSettlementByEvent(records []SettlementOutcome) map[string]SettlementOutcome {
	out := map[string]SettlementOutcome{}
	for _, r := range records {
		out[r.EventSlug] = r // last-write-wins
	}
	return out
}

package replay

import (
	"testing"
	"time"
)

func mkTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestReplay_StateMachineProgression(t *testing.T) {
	actions := []Action{
		// Probe order on side A of leg 1
		{TimestampUTC: mkTime("2026-05-10T23:30:13Z"), ActionType: "TRADE", Side: "BUY",
			ConditionID: "c1", TokenID: "a", Price: 0.50, Size: 2.44, UsdcSize: 1.22},
		// Bigger fill on side A of leg 1 → ACCUMULATE_A
		{TimestampUTC: mkTime("2026-05-10T23:37:32Z"), ActionType: "TRADE", Side: "BUY",
			ConditionID: "c1", TokenID: "a", Price: 0.35, Size: 1163.43, UsdcSize: 407.20},
		// Side B opens on leg 1 → OPEN_B
		{TimestampUTC: mkTime("2026-05-10T23:43:42Z"), ActionType: "TRADE", Side: "BUY",
			ConditionID: "c1", TokenID: "b", Price: 0.51, Size: 3.92, UsdcSize: 2.00},
		// New condition opens → EXPAND
		{TimestampUTC: mkTime("2026-05-10T23:43:52Z"), ActionType: "TRADE", Side: "BUY",
			ConditionID: "c2", TokenID: "x", Price: 0.51, Size: 2.08, UsdcSize: 1.06},
		// Later fill → LAYER
		{TimestampUTC: mkTime("2026-05-10T23:44:25Z"), ActionType: "TRADE", Side: "BUY",
			ConditionID: "c1", TokenID: "a", Price: 0.33, Size: 604.68, UsdcSize: 199.54},
		// REDEEM resolves cluster
		{TimestampUTC: mkTime("2026-05-11T03:00:00Z"), ActionType: "REDEEM",
			ConditionID: "c1", Size: 1770, UsdcSize: 1770},
	}
	c := Replay("nba-test", actions)
	want := []State{StateScan, StateProbe, StateAccumulateA, StateOpenB, StateExpand, StateLayer, StateRedeem, StateIdle}
	if len(c.StatesVisited) != len(want) {
		t.Fatalf("states visited = %v, want %v", c.StatesVisited, want)
	}
	for i, s := range want {
		if c.StatesVisited[i] != s {
			t.Errorf("state[%d] = %v, want %v", i, c.StatesVisited[i], s)
		}
	}
	if c.TotalRedeemUsdc != 1770 {
		t.Errorf("redeem usdc = %v, want 1770", c.TotalRedeemUsdc)
	}
	if len(c.LegOrder) != 2 || c.LegOrder[0] != "c1" || c.LegOrder[1] != "c2" {
		t.Errorf("leg order = %v", c.LegOrder)
	}
}

func TestReplay_MergeHarvest(t *testing.T) {
	actions := []Action{
		// Build both sides on leg 1
		{TimestampUTC: mkTime("2026-05-02T13:30:36Z"), ActionType: "TRADE", Side: "BUY",
			ConditionID: "c1", TokenID: "a", Price: 0.48, Size: 200, UsdcSize: 96.0},
		{TimestampUTC: mkTime("2026-05-02T13:35:00Z"), ActionType: "TRADE", Side: "BUY",
			ConditionID: "c1", TokenID: "b", Price: 0.18, Size: 250, UsdcSize: 45.0},
		// MERGE harvests 100 pair-units → both shares drop by 100
		{TimestampUTC: mkTime("2026-05-02T13:58:44Z"), ActionType: "MERGE",
			ConditionID: "c1", Size: 100, UsdcSize: 100},
	}
	c := Replay("bun-test", actions)
	foundMerge := false
	for _, s := range c.StatesVisited {
		if s == StateMergeHarvest {
			foundMerge = true
		}
	}
	if !foundMerge {
		t.Errorf("expected MERGE_HARVEST in states, got %v", c.StatesVisited)
	}
	leg := c.Legs["c1"]
	if leg == nil {
		t.Fatal("leg c1 missing")
	}
	if leg.SharesA != 100 || leg.SharesB != 150 {
		t.Errorf("after merge: sharesA=%v sharesB=%v, want 100/150", leg.SharesA, leg.SharesB)
	}
	if leg.MergeUnits != 100 {
		t.Errorf("merge units = %v, want 100", leg.MergeUnits)
	}
}

func TestReplay_EnvelopeFlagAndPositiveLegs(t *testing.T) {
	// Side A 100 sh at $0.30 (WAVG 0.30); Side B 100 sh at $0.50 (WAVG 0.50). Sum 0.80 < 1.
	actions := []Action{
		{TimestampUTC: mkTime("2026-05-02T13:30:00Z"), ActionType: "TRADE", Side: "BUY",
			ConditionID: "c1", TokenID: "a", Price: 0.30, Size: 100, UsdcSize: 30.0},
		{TimestampUTC: mkTime("2026-05-02T13:31:00Z"), ActionType: "TRADE", Side: "BUY",
			ConditionID: "c1", TokenID: "b", Price: 0.50, Size: 100, UsdcSize: 50.0},
	}
	c := Replay("bun-test", actions)
	leg := c.Legs["c1"]
	if !leg.EnvelopeAt() {
		t.Errorf("expected envelope true, got sum_wavg=%v", leg.SumWavg())
	}
	if len(c.PositiveEnvelopeLegs) != 1 || c.PositiveEnvelopeLegs[0] != "c1" {
		t.Errorf("positive envelope legs = %v, want [c1]", c.PositiveEnvelopeLegs)
	}
}

func TestReplay_EmptyActions(t *testing.T) {
	c := Replay("none", nil)
	if len(c.Legs) != 0 {
		t.Errorf("expected 0 legs, got %d", len(c.Legs))
	}
	if len(c.StatesVisited) == 0 {
		t.Errorf("expected at least SCAN/IDLE in visited states")
	}
}

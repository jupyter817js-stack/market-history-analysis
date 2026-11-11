// Package replay implements the historical event-cluster replay engine.
//
// The engine reproduces the sequential state machine from the
// integrated forensic report (S0 SCAN → S1 PROBE → S2 ACCUMULATE_A →
// S3 OPEN_B → S4 EXPAND → S5 LAYER → S6 MERGE_HARVEST → S7 REDEEM)
// against the actual account_actions ledger for one event_slug. It
// does NOT place orders, and it never reads any post-trade summary
// view; it consumes raw action rows in timestamp order.
package replay

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// State enumerates the documented RN1 event-cluster state machine.
type State string

const (
	StateScan         State = "SCAN"
	StateProbe        State = "PROBE"
	StateAccumulateA  State = "ACCUMULATE_A"
	StateOpenB        State = "OPEN_B"
	StateExpand       State = "EXPAND"
	StateLayer        State = "LAYER"
	StateMergeHarvest State = "MERGE_HARVEST"
	StateRedeem       State = "REDEEM"
	StateIdle         State = "IDLE"
)

// Action is one normalized row from account_actions filtered to a
// single event_slug. The caller is responsible for filtering and sort.
type Action struct {
	TimestampUTC time.Time
	ActionType   string  // TRADE / MERGE / REDEEM / MAKER_REBATE / REWARD / CONVERSION
	Side         string  // BUY / SELL (lowercased internally); empty for non-TRADE
	ConditionID  string
	TokenID      string  // asset; empty on MERGE/REDEEM
	MarketSlug   string
	Title        string
	Outcome      string
	Price        float64
	Size         float64
	UsdcSize     float64
	TxHash       string
}

// Step is one state-machine transition or recorded event in the
// replay trace.
type Step struct {
	TimestampUTC time.Time
	State        State
	LegOrder     int     // 1-based entry order of the leg, if applicable
	ConditionID  string  // empty when event-level
	MarketSlug   string  // empty when event-level
	TokenID      string  // empty when MERGE/REDEEM/event-level
	Side         string  // BUY/SELL on TRADE steps
	Price        float64
	Size         float64
	UsdcSize     float64
	SharesA      float64 // running shares on side A of this leg after step
	SharesB      float64
	WavgA        float64 // running WAVG on side A of this leg after step
	WavgB        float64
	SumWavg      float64 // 0 if either side is 0
	EnvelopeAt   bool    // both sides held AND sum-of-WAVG strictly < 1.0
	Note         string
}

// LegState is the per-(condition_id) running state.
type LegState struct {
	ConditionID    string
	MarketSlug     string
	Title          string
	TokenA         string
	TokenB         string
	OutcomeA       string
	OutcomeB       string
	EntryOrder     int
	FirstFillTs    time.Time
	BuysA          int
	BuysB          int
	SellsA         int
	SellsB         int
	SharesA        float64
	SharesB        float64
	CumBuySizeA    float64
	CumBuySizeB    float64
	CumBuyUsdcA    float64
	CumBuyUsdcB    float64
	MergeUnits     float64
	RedeemUsdc     float64
	FirstSideA     bool // whether side A was the first one opened on this leg
}

func (l *LegState) WavgA() float64 {
	if l.CumBuySizeA <= 0 {
		return 0
	}
	return l.CumBuyUsdcA / l.CumBuySizeA
}
func (l *LegState) WavgB() float64 {
	if l.CumBuySizeB <= 0 {
		return 0
	}
	return l.CumBuyUsdcB / l.CumBuySizeB
}
func (l *LegState) SumWavg() float64 {
	if l.CumBuySizeA <= 0 || l.CumBuySizeB <= 0 {
		return 0
	}
	return l.WavgA() + l.WavgB()
}
func (l *LegState) BothHeld() bool {
	return l.SharesA > 1e-9 && l.SharesB > 1e-9
}
func (l *LegState) EnvelopeAt() bool {
	return l.BothHeld() && l.SumWavg() > 0 && l.SumWavg() < 1.0
}

// Cluster is the full replay result for one event_slug.
type Cluster struct {
	EventSlug          string
	StartTs            time.Time
	EndTs              time.Time
	StatesVisited      []State
	Steps              []Step
	Legs               map[string]*LegState // keyed by condition_id
	LegOrder           []string             // condition_ids in entry order
	TotalBuyUsdc       float64
	TotalSellUsdc      float64
	TotalMergeUsdc     float64
	TotalRedeemUsdc    float64
	MergeEvents        int
	RedeemEvents       int
	PositiveEnvelopeLegs []string // condition_ids where envelope was ever true
}

// Replay runs the state machine over actions for one event_slug. The
// actions are sorted by timestamp; deterministic tie-breaks use
// (condition_id, asset, txhash).
func Replay(eventSlug string, actions []Action) *Cluster {
	c := &Cluster{
		EventSlug: eventSlug,
		Legs:      map[string]*LegState{},
	}
	if len(actions) == 0 {
		c.StatesVisited = []State{StateScan, StateIdle}
		return c
	}
	sortActions(actions)
	c.StartTs = actions[0].TimestampUTC
	c.EndTs = actions[len(actions)-1].TimestampUTC

	currentState := StateScan
	visit := func(s State) {
		if len(c.StatesVisited) == 0 || c.StatesVisited[len(c.StatesVisited)-1] != s {
			c.StatesVisited = append(c.StatesVisited, s)
		}
	}
	visit(currentState)

	for _, a := range actions {
		switch a.ActionType {
		case "TRADE":
			c.processTrade(a, &currentState, visit)
		case "MERGE":
			c.processMerge(a, &currentState, visit)
		case "REDEEM":
			c.processRedeem(a, &currentState, visit)
		default:
			// MAKER_REBATE / REWARD / CONVERSION: record as informational step.
			c.Steps = append(c.Steps, Step{
				TimestampUTC: a.TimestampUTC,
				State:        currentState,
				ConditionID:  a.ConditionID,
				MarketSlug:   a.MarketSlug,
				Side:         strings.ToUpper(a.Side),
				Price:        a.Price,
				Size:         a.Size,
				UsdcSize:     a.UsdcSize,
				Note:         "informational: " + a.ActionType,
			})
		}
	}

	// Collect legs where envelope was ever true.
	for _, leg := range c.Legs {
		if leg.SharesA > 1e-9 && leg.SharesB > 1e-9 && leg.SumWavg() > 0 && leg.SumWavg() < 1.0 {
			c.PositiveEnvelopeLegs = append(c.PositiveEnvelopeLegs, leg.ConditionID)
		}
	}
	sort.Strings(c.PositiveEnvelopeLegs)

	// Final state: IDLE after all actions consumed.
	visit(StateIdle)
	return c
}

func (c *Cluster) leg(a Action) *LegState {
	leg, ok := c.Legs[a.ConditionID]
	if !ok {
		leg = &LegState{
			ConditionID: a.ConditionID,
			MarketSlug:  a.MarketSlug,
			Title:       a.Title,
			TokenA:      a.TokenID, // first-seen token; side B is whatever is the other one
			OutcomeA:    a.Outcome,
			FirstFillTs: a.TimestampUTC,
			EntryOrder:  len(c.LegOrder) + 1,
			FirstSideA:  true,
		}
		c.Legs[a.ConditionID] = leg
		c.LegOrder = append(c.LegOrder, a.ConditionID)
	}
	return leg
}

func (c *Cluster) processTrade(a Action, currentState *State, visit func(State)) {
	leg := c.leg(a)
	isA := false
	isB := false
	switch a.TokenID {
	case "":
		// nothing
	case leg.TokenA:
		isA = true
	default:
		if leg.TokenB == "" {
			// first time we see the complementary token: pin it.
			leg.TokenB = a.TokenID
			leg.OutcomeB = a.Outcome
		}
		if a.TokenID == leg.TokenB {
			isB = true
		}
	}

	side := strings.ToLower(a.Side)
	switch {
	case side == "buy" && isA:
		leg.BuysA++
		leg.SharesA += a.Size
		leg.CumBuySizeA += a.Size
		leg.CumBuyUsdcA += a.UsdcSize
	case side == "sell" && isA:
		leg.SellsA++
		leg.SharesA -= a.Size
	case side == "buy" && isB:
		leg.BuysB++
		leg.SharesB += a.Size
		leg.CumBuySizeB += a.Size
		leg.CumBuyUsdcB += a.UsdcSize
	case side == "sell" && isB:
		leg.SellsB++
		leg.SharesB -= a.Size
	}
	if side == "buy" {
		c.TotalBuyUsdc += a.UsdcSize
	} else if side == "sell" {
		c.TotalSellUsdc += a.UsdcSize
	}

	// Determine next state transition for this step.
	nextState := *currentState
	switch *currentState {
	case StateScan:
		nextState = StateProbe
	case StateProbe:
		// promote to ACCUMULATE_A once any single fill on the first
		// side accumulates beyond a small threshold (>=100 shares is
		// well above the documented 2-10 share probe sizes).
		if isA && leg.SharesA >= 100 {
			nextState = StateAccumulateA
		}
	case StateAccumulateA:
		if isB && leg.SharesB > 1e-9 {
			nextState = StateOpenB
		} else if len(c.LegOrder) >= 2 {
			nextState = StateExpand
		}
	case StateOpenB:
		if len(c.LegOrder) >= 2 {
			nextState = StateExpand
		}
	case StateExpand:
		nextState = StateLayer
	case StateLayer:
		nextState = StateLayer
	}
	if nextState != *currentState {
		*currentState = nextState
		visit(nextState)
	}

	c.Steps = append(c.Steps, Step{
		TimestampUTC: a.TimestampUTC,
		State:        *currentState,
		LegOrder:     leg.EntryOrder,
		ConditionID:  a.ConditionID,
		MarketSlug:   a.MarketSlug,
		TokenID:      a.TokenID,
		Side:         strings.ToUpper(a.Side),
		Price:        a.Price,
		Size:         a.Size,
		UsdcSize:     a.UsdcSize,
		SharesA:      leg.SharesA,
		SharesB:      leg.SharesB,
		WavgA:        leg.WavgA(),
		WavgB:        leg.WavgB(),
		SumWavg:      leg.SumWavg(),
		EnvelopeAt:   leg.EnvelopeAt(),
	})
}

func (c *Cluster) processMerge(a Action, currentState *State, visit func(State)) {
	leg := c.leg(a)
	leg.MergeUnits += a.Size
	leg.SharesA -= a.Size
	leg.SharesB -= a.Size
	c.TotalMergeUsdc += a.UsdcSize
	c.MergeEvents++
	*currentState = StateMergeHarvest
	visit(StateMergeHarvest)
	c.Steps = append(c.Steps, Step{
		TimestampUTC: a.TimestampUTC,
		State:        *currentState,
		LegOrder:     leg.EntryOrder,
		ConditionID:  a.ConditionID,
		MarketSlug:   a.MarketSlug,
		Side:         "MERGE",
		Size:         a.Size,
		UsdcSize:     a.UsdcSize,
		SharesA:      leg.SharesA,
		SharesB:      leg.SharesB,
		WavgA:        leg.WavgA(),
		WavgB:        leg.WavgB(),
		SumWavg:      leg.SumWavg(),
	})
}

func (c *Cluster) processRedeem(a Action, currentState *State, visit func(State)) {
	leg := c.leg(a)
	leg.RedeemUsdc += a.UsdcSize
	c.TotalRedeemUsdc += a.UsdcSize
	c.RedeemEvents++
	*currentState = StateRedeem
	visit(StateRedeem)
	c.Steps = append(c.Steps, Step{
		TimestampUTC: a.TimestampUTC,
		State:        *currentState,
		LegOrder:     leg.EntryOrder,
		ConditionID:  a.ConditionID,
		MarketSlug:   a.MarketSlug,
		Side:         "REDEEM",
		Size:         a.Size,
		UsdcSize:     a.UsdcSize,
	})
}

// Summary returns a one-line headline describing the replay.
func (c *Cluster) Summary() string {
	if c == nil {
		return "(nil)"
	}
	return fmt.Sprintf("event=%s legs=%d buy=$%.2f merge=$%.2f redeem=$%.2f states=%v envelope_legs=%d",
		c.EventSlug, len(c.Legs), c.TotalBuyUsdc, c.TotalMergeUsdc, c.TotalRedeemUsdc,
		c.StatesVisited, len(c.PositiveEnvelopeLegs))
}

func sortActions(actions []Action) {
	sort.SliceStable(actions, func(i, j int) bool {
		a, b := actions[i], actions[j]
		if !a.TimestampUTC.Equal(b.TimestampUTC) {
			return a.TimestampUTC.Before(b.TimestampUTC)
		}
		if a.ConditionID != b.ConditionID {
			return a.ConditionID < b.ConditionID
		}
		if a.TokenID != b.TokenID {
			return a.TokenID < b.TokenID
		}
		return a.TxHash < b.TxHash
	})
}

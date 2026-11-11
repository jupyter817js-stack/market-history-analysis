// Package selection implements the RN1 market-selection filter.
//
// Inputs are pre-trade only: markets_basic rows for an event, plus the
// snapshot time. The filter never reads account_actions or any
// post-trade view. The intent is that this filter can be re-applied to
// a fresh Gamma /events response and produce the same scoring.
package selection

import (
	"encoding/json"
	"strings"
	"time"
)

// MarketMeta is the slice of markets_basic that the selector needs.
// It is a strict subset of pre-trade information.
type MarketMeta struct {
	ConditionID   string
	MarketSlug    string
	EventSlug     string
	EventID       string
	EventTitle    string
	Title         string
	Category      string
	EndDate       *time.Time
	Active        *bool
	Closed        *bool
	NegativeRisk  *bool
	ClobTokenIDs  string // JSON-encoded array string, e.g. `["1234","5678"]`
}

// EventFeatures is the per-event feature vector derived from the
// pre-trade market metadata. It contains nothing derived from
// account_actions.
type EventFeatures struct {
	EventSlug          string
	EventID            string
	EventTitle         string
	MarketCount        int
	BinaryMarketCount  int      // markets with exactly 2 clob token IDs
	AllActive          bool
	AnyClosed          bool
	MinEndDate         *time.Time
	MaxEndDate         *time.Time
	SportPrefix        string   // detected sport tag, "" if unknown
	Families           []string // detected derivative families
	HorizonSeconds     int64    // max(0, MinEndDate - now). 0 if MinEndDate already past or nil.
}

// FilterConfig holds the tunable thresholds. Defaults map to the
// confirmed rules from the locked forensic report.
type FilterConfig struct {
	// MaxHorizonSeconds — events with MinEndDate further out than this
	// are not preferred. Default 7 days.
	MaxHorizonSeconds int64
	// MinMarketsPerEvent — events with fewer markets than this are
	// rejected at the bundle-preference level. The all-time RN1 mix
	// includes 10,738 single-market events too, but bundle preference
	// is the documented behavior.
	MinMarketsPerEvent int
	// MaxScoreMarketsCap — diminishing returns above this many markets.
	MaxScoreMarketsCap int
	// IgnoreResolutionState — when true, the eligibility check skips
	// the active/closed flags and the "end_date in the future" check.
	// Used for historical back-evaluation against markets_basic
	// snapshots where every event has long since resolved.
	IgnoreResolutionState bool
}

func DefaultConfig() FilterConfig {
	return FilterConfig{
		MaxHorizonSeconds:  7 * 24 * 3600,
		MinMarketsPerEvent: 2,
		MaxScoreMarketsCap: 7,
	}
}

// Decision is the per-event output.
type Decision struct {
	Features        EventFeatures
	Eligible        bool
	Score           float64
	Reasons         []string // "eligible: …" or "reject: …" reasons
}

// SportPrefixes is the canonical RN1-visible sport-slug prefix list.
// New prefixes that appear in markets_basic should be added here.
//
// The list was extended in Hardening Patch 1 (2026-05-12) after
// Validation Pass 1 surfaced 15+ historical events with high RN1 buy
// notional that were being scored 0 purely because their slug prefix
// was not yet recognized. All additions are sport-event prefixes
// (soccer leagues / cups), not non-sport categories.
var SportPrefixes = []string{
	"nba", "nfl", "mlb", "nhl",
	"epl", "ucl", "lal", "sea", "bun", "cbb", "cfb",
	"atp", "wta",
	"cs2", "cs", "csgo", "lol", "val", "dota",
	"wnba", "fifa", "mls",
	// Hardening Patch 1 (2026-05-12) additions:
	"elc",  // English League Cup (EFL Cup)
	"lib",  // Copa Libertadores
	"uef",  // UEFA Nations League / qualifiers
	"uel",  // UEFA Europa League
	"tur",  // Turkish Süper Lig
	"spl",  // Saudi Pro League
	"itsb", // Italian Serie B
	"bra",  // Brazilian Série A
	"fl1",  // French Ligue 1
	"ere",  // Dutch Eredivisie
	"cdr",  // Spanish Copa del Rey
	"efa",  // English FA / FA Cup
	"cze1", // Czech First League
	// Hardening Patch 2 (2026-05-12) additions: prefixes flagged by
	// the residual-anomaly investigation. All correspond to RN1-active
	// sport competitions; collectively they covered ~1,515 events and
	// ~$10.8M of buy notional that had been scoring 0.
	"acn", // Africa Cup of Nations
	"col", // UEFA Conference League (Rayo Vallecano vs AEK Athens etc.)
	"por", // Primeira Liga (Portugal)
	"efl", // English Football League / EFL Cup
	"fif", // FIFA international (qualifiers / friendlies)
	"mex", // Liga MX (Mexico)
	"bl2", // 2. Bundesliga (Germany 2nd tier)
	"fr2", // Ligue 2 (France 2nd tier)
	// Hardening Patch 3 (2026-05-12) additions: prefixes flagged
	// after Patch 2. Combined RN1 footprint ~1,519 events,
	// ~$5.76M buy notional. All four are unambiguous sport namespaces:
	// three soccer competitions plus the Dota 2 esports analog of cs2.
	"arg",   // Argentine Primera División
	"es2",   // Spanish La Liga 2 (Segunda División)
	"dota2", // Dota 2 esports (BO3/BO5 series)
	"scop",  // Scottish Premiership
	// Live Scanner Prefix Patch 2 (2026-05-12) additions: prefixes
	// surfaced by the live-scanner dry-run anomaly side-channel. All
	// six are confirmed soccer competitions per Gamma description and
	// the official association resolutionSource domain; no family
	// detector changes were needed (each follows the existing
	// soccer_3way_moneyline + expanded_companion_stack shape).
	"brco",  // Copa do Brasil (cbf.com.br)
	"egy1",  // Egyptian Premier League (efa.com.eg)
	"kor",   // K-League — Korean 1st division (kleague.com)
	"bol1",  // Bolivia LFPB — Liga de Fútbol Profesional (lfpb.com.bo)
	"j1100", // J1 100 Year Vision League — Japan J1 (jleague.jp)
	"nwsl",  // NWSL — US women's 1st division (nwslsoccer.com)
	// Live Scanner Prefix Patch 3 (2026-05-12) additions: 8 soccer
	// prefixes surfaced by the 72h / 96h wider-horizon dry-run
	// anomaly side-channel. All confirmed against Gamma description
	// and the official association resolutionSource. Cricket (crint)
	// was investigated alongside these but is deliberately excluded —
	// its side-market slug structure (-team-top-batter, -toss-match-
	// double, -most-sixes) diverges from the soccer/esports family
	// shape and requires a separate policy decision before adding.
	"j2100", // J2 100 Year Vision League — Japan J2 (jleague.jp)
	"col1",  // Colombia Primera A (dimayor.com.co) — distinct from `col` (UEFA Conference League)
	"ukr1",  // Ukraine Premier Liha (upl.ua)
	"per1",  // Peru Liga 1 (liga1.pe)
	"chi1",  // Chile Primera (anfp.cl)
	"aus",   // Australia A-League (a-league.com.au) — 3-char prefix, monitor for future non-sport `aus-` slugs
	"el1",   // English EFL League One (efl.com)
	"el2",   // English EFL League Two (efl.com)
}

// DetectSportPrefix returns the SportPrefixes entry that matches the
// start of the event_slug (delimited by `-`), or empty string.
func DetectSportPrefix(eventSlug string) string {
	if eventSlug == "" {
		return ""
	}
	lower := strings.ToLower(eventSlug)
	for _, p := range SportPrefixes {
		if strings.HasPrefix(lower, p+"-") || lower == p {
			return p
		}
	}
	return ""
}

// IsBinary returns true if the clobTokenIDs JSON array contains exactly
// 2 entries. RN1's confirmed building block is binary YES/NO markets.
func IsBinary(clobTokenIDs string) bool {
	clobTokenIDs = strings.TrimSpace(clobTokenIDs)
	if clobTokenIDs == "" {
		return false
	}
	var arr []any
	if err := json.Unmarshal([]byte(clobTokenIDs), &arr); err != nil {
		return false
	}
	return len(arr) == 2
}

// DetectFamilies inspects the set of market titles in an event and
// flags the recurring derivative families that the RN1 forensic
// reports name: soccer 3-way moneyline, NBA/NFL spread+ML+total
// stack, BO3 esports stack, tennis set+total stack, and the
// "-more-markets" companion expansion. The flags are heuristic over
// market titles, market slugs, and the event slug.
func DetectFamilies(markets []MarketMeta) []string {
	var (
		hasWin         bool
		hasOpponentWin bool
		hasDraw        bool
		hasSpread      bool
		hasTotal       bool
		hasMoneyline   bool
		hasMap1        bool
		hasMap2        bool
		hasSeriesBO3   bool
		hasHandicap    bool
		hasMapTotal    bool
		hasSetWinner   bool
		hasMatchTotal  bool
	)
	teamsWithWinQuestion := map[string]bool{}
	eventSlug := ""
	if len(markets) > 0 {
		eventSlug = strings.ToLower(markets[0].EventSlug)
	}
	binaryCount := 0
	for _, m := range markets {
		if IsBinary(m.ClobTokenIDs) {
			binaryCount++
		}
		t := strings.ToLower(m.Title)
		ms := strings.ToLower(m.MarketSlug)
		if strings.Contains(t, "win on ") || strings.HasPrefix(t, "will ") && strings.Contains(t, " win") {
			hasWin = true
			teamsWithWinQuestion[t] = true
		}
		if strings.Contains(t, "draw") {
			hasDraw = true
		}
		if strings.HasPrefix(t, "spread:") || strings.Contains(t, "spread") {
			hasSpread = true
		}
		if strings.Contains(t, "o/u") || strings.Contains(t, "over/under") || strings.Contains(t, "total:") {
			hasTotal = true
		}
		if !hasMoneyline && (strings.Contains(t, "moneyline") ||
			(strings.Contains(t, " vs.") && !hasSpread && !hasTotal && !hasDraw)) {
			hasMoneyline = true
		}
		if strings.Contains(t, "map 1 winner") || strings.Contains(ms, "game1") || strings.Contains(ms, "map-1") {
			hasMap1 = true
		}
		if strings.Contains(t, "map 2 winner") || strings.Contains(ms, "game2") || strings.Contains(ms, "map-2") {
			hasMap2 = true
		}
		if strings.Contains(t, "bo3") || strings.Contains(ms, "bo3") {
			hasSeriesBO3 = true
		}
		if strings.Contains(t, "handicap") || strings.Contains(ms, "handicap") {
			hasHandicap = true
		}
		if strings.Contains(t, "games total") || strings.Contains(t, "maps total") || strings.Contains(ms, "total-games") || strings.Contains(ms, "total-maps") {
			hasMapTotal = true
		}
		if strings.Contains(t, "set winner") || strings.Contains(ms, "set-winner") ||
			strings.Contains(t, "first set winner") || strings.Contains(ms, "first-set-winner") {
			hasSetWinner = true
		}
		if strings.Contains(t, "match total") || strings.Contains(ms, "match-total") {
			hasMatchTotal = true
		}
	}
	if hasWin && len(teamsWithWinQuestion) >= 2 {
		hasOpponentWin = true
	}

	out := []string{}
	// Soccer 3-way moneyline: 2+ team-win questions + draw market.
	if hasOpponentWin && hasDraw {
		out = append(out, "soccer_3way_moneyline")
	}
	// NBA / NFL multi-leg: moneyline-like + spread + total.
	if hasSpread && hasTotal && (hasMoneyline || hasOpponentWin) {
		out = append(out, "spread_ml_total_stack")
	}
	// BO3 esports: series + map 1 + map 2 + handicap or total games.
	if hasMap1 && hasMap2 && (hasSeriesBO3 || hasHandicap || hasMapTotal) {
		out = append(out, "bo3_esports_stack")
	}
	// Tennis set + match total stack: first-set-winner + match-total.
	// Triggered when both a set-winner-style market and a match-total
	// market appear in the same event.
	if hasSetWinner && hasMatchTotal {
		out = append(out, "tennis_set_total_stack")
	}
	// "-more-markets" companion event slugs are a Gamma data shape
	// where the same matchup is split across two event_slug rows:
	//   <event>            (3 base markets: win-A / draw / win-B)
	//   <event>-more-markets  (5-9 derivative markets: spreads, totals,
	//                          BTTS, handicaps)
	// The companion is the same event from a strategy point of view,
	// so we award the same kind of structural bonus when the slug
	// suffix is present and the event has multiple binary markets.
	if strings.HasSuffix(eventSlug, "-more-markets") && binaryCount >= 5 {
		out = append(out, "expanded_companion_stack")
	}
	return out
}

// ExtractFeatures collapses markets_basic rows of one event into a
// single EventFeatures record.
func ExtractFeatures(markets []MarketMeta, now time.Time) EventFeatures {
	if len(markets) == 0 {
		return EventFeatures{}
	}
	f := EventFeatures{
		EventSlug:    markets[0].EventSlug,
		EventID:      markets[0].EventID,
		EventTitle:   markets[0].EventTitle,
		MarketCount:  len(markets),
		SportPrefix:  DetectSportPrefix(markets[0].EventSlug),
		AllActive:    true,
	}
	for _, m := range markets {
		if IsBinary(m.ClobTokenIDs) {
			f.BinaryMarketCount++
		}
		if m.Active != nil && !*m.Active {
			f.AllActive = false
		}
		if m.Closed != nil && *m.Closed {
			f.AnyClosed = true
		}
		if m.EndDate != nil {
			if f.MinEndDate == nil || m.EndDate.Before(*f.MinEndDate) {
				ed := *m.EndDate
				f.MinEndDate = &ed
			}
			if f.MaxEndDate == nil || m.EndDate.After(*f.MaxEndDate) {
				ed := *m.EndDate
				f.MaxEndDate = &ed
			}
		}
	}
	if f.MinEndDate != nil && f.MinEndDate.After(now) {
		f.HorizonSeconds = int64(f.MinEndDate.Sub(now).Seconds())
	}
	f.Families = DetectFamilies(markets)
	return f
}

// Decide applies the RN1-style eligibility + scoring rules to one
// event's features, using only pre-trade information.
//
// Eligibility (all must hold):
//   - AnyClosed == false
//   - AllActive == true
//   - MarketCount >= MinMarketsPerEvent
//   - SportPrefix is recognized
//   - At least one market is binary (BinaryMarketCount > 0)
//   - MinEndDate is in the future and within MaxHorizonSeconds
//
// Score (higher = more preferred):
//   - +1 per binary market in the event, capped at MaxScoreMarketsCap.
//   - +2 per recognized derivative family.
//   - +1 if event is "near-term" (HorizonSeconds <= 2 days).
//   - +0.5 if MarketCount >= 4 (mid-or-large bundle territory).
func Decide(f EventFeatures, cfg FilterConfig, now time.Time) Decision {
	d := Decision{Features: f}

	rejectIf := func(cond bool, reason string) bool {
		if cond {
			d.Reasons = append(d.Reasons, "reject: "+reason)
			return true
		}
		return false
	}

	rejected := false
	if !cfg.IgnoreResolutionState {
		rejected = rejectIf(f.AnyClosed, "event has at least one closed market") || rejected
		rejected = rejectIf(!f.AllActive, "event has at least one inactive market") || rejected
	}
	rejected = rejectIf(f.MarketCount < cfg.MinMarketsPerEvent, "event has fewer markets than MinMarketsPerEvent") || rejected
	rejected = rejectIf(f.SportPrefix == "", "event slug has no recognized sport prefix") || rejected
	rejected = rejectIf(f.BinaryMarketCount == 0, "no binary outcome markets detected") || rejected

	if !cfg.IgnoreResolutionState {
		if f.MinEndDate == nil {
			rejected = rejectIf(true, "event has no end_date") || rejected
		} else if !f.MinEndDate.After(now) {
			rejected = rejectIf(true, "event MinEndDate already past") || rejected
		} else if cfg.MaxHorizonSeconds > 0 && f.HorizonSeconds > cfg.MaxHorizonSeconds {
			rejected = rejectIf(true, "event farther out than MaxHorizonSeconds") || rejected
		}
	}

	if rejected {
		d.Eligible = false
		return d
	}

	d.Eligible = true
	binaryWeight := float64(f.BinaryMarketCount)
	if cfg.MaxScoreMarketsCap > 0 && binaryWeight > float64(cfg.MaxScoreMarketsCap) {
		binaryWeight = float64(cfg.MaxScoreMarketsCap)
	}
	score := binaryWeight + 2.0*float64(len(f.Families))
	if f.HorizonSeconds <= 2*24*3600 {
		score += 1.0
	}
	if f.MarketCount >= 4 {
		score += 0.5
	}
	d.Score = score
	d.Reasons = append(d.Reasons, "eligible: pre-trade rules pass")
	return d
}

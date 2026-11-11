package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"rn1-strategy-extractor/internal/normalize"
	"rn1-strategy-extractor/internal/polymarket"
	"rn1-strategy-extractor/internal/selection"
)

// Config wires the scanner together. All knobs are CLI-visible.
type Config struct {
	Horizon                time.Duration
	Limit                  int
	MaxEvents              int
	MaxMarketsPerEvent     int
	MaxBookRequests        int
	Concurrency            int
	BookCacheTTL           time.Duration
	IncludeSingleMarket    bool
	IncludeNonSport        bool
	SportFilter            map[string]bool // empty == "all"
	MinScore               float64
	LiquidityFilter        string // "any" | "pass" | "warn"
	Selector               selection.FilterConfig
	Liquidity              LiquidityConfig
	StaleBookSeconds       int64
}

func DefaultConfig() Config {
	return Config{
		Horizon:            48 * time.Hour,
		Limit:              50,
		MaxEvents:          400,
		MaxMarketsPerEvent: 24,
		MaxBookRequests:    500,
		Concurrency:        6,
		BookCacheTTL:       30 * time.Second,
		IncludeSingleMarket: false,
		IncludeNonSport:    false,
		SportFilter:        nil,
		MinScore:           0,
		LiquidityFilter:    "any",
		Selector:           selection.DefaultConfig(),
		Liquidity:          DefaultLiquidityConfig(),
		StaleBookSeconds:   1800,
	}
}

// Candidate is one ranked entry in the scanner output.
type Candidate struct {
	Rank              int
	EventSlug         string
	EventID           string
	Title             string
	SportPrefix       string
	Score             float64
	Band              string
	SelectorReasons   []string
	LiquidityGate     LiquidityLabel
	LiquidityResult   LiquidityResult
	MarketCount       int
	BinaryCount       int
	Families          []string
	StartTs           *time.Time
	TimeToStart       time.Duration
	Active            bool
	Closed            bool
	ReasonSummary     string
	RiskFlags         []RiskFlag
	ReviewStatus      string
	ScannedAt         time.Time
}

// Scanner runs the screening pipeline.
type Scanner struct {
	Client *polymarket.Client
	Now    func() time.Time

	// bookCache short-lived cache to avoid re-fetching the same token
	// within a single scan or within BookCacheTTL.
	mu        sync.Mutex
	bookCache map[string]bookEntry
}

type bookEntry struct {
	book      *polymarket.Orderbook
	fetchedAt time.Time
	err       string
}

// New constructs a Scanner with sensible defaults. Network is reached
// only when Run is called.
func New(client *polymarket.Client) *Scanner {
	return &Scanner{
		Client:    client,
		Now:       func() time.Time { return time.Now().UTC() },
		bookCache: map[string]bookEntry{},
	}
}

// AnomalyEvent is one row of the side-channel JSONL log.
type AnomalyEvent struct {
	ScannedAt time.Time `json:"scanned_at"`
	EventSlug string    `json:"event_slug"`
	Reason    string    `json:"reason"`
	Detail    string    `json:"detail,omitempty"`
}

// Run executes one scan and returns the ranked candidate list plus any
// anomalies that should land in the side-channel log.
func (s *Scanner) Run(ctx context.Context, cfg Config) ([]Candidate, []AnomalyEvent, error) {
	now := s.Now()
	startUnix := now.Unix()
	endUnix := now.Add(cfg.Horizon).Unix()

	allEvents, err := s.fetchAllEvents(ctx, startUnix, endUnix, cfg)
	if err != nil {
		return nil, nil, err
	}

	// Build event groupings + anomaly side-channel.
	type eventBundle struct {
		slug      string
		raw       map[string]any
		markets   []selection.MarketMeta
	}
	bundles := make([]eventBundle, 0, len(allEvents))
	slugsSeen := map[string]bool{}
	var anomalies []AnomalyEvent
	for _, ev := range allEvents {
		eventSlug := normalize.AsString(ev["slug"])
		if eventSlug == "" {
			continue
		}
		slugsSeen[eventSlug] = true
		mm, err := normalize.MarketMetasFromEvent(ev)
		if err != nil {
			anomalies = append(anomalies, AnomalyEvent{
				ScannedAt: now,
				EventSlug: eventSlug,
				Reason:    "MARKETS_DECODE_ERROR",
				Detail:    err.Error(),
			})
			continue
		}
		if cfg.MaxMarketsPerEvent > 0 && len(mm) > cfg.MaxMarketsPerEvent {
			mm = mm[:cfg.MaxMarketsPerEvent]
		}
		bundles = append(bundles, eventBundle{slug: eventSlug, raw: ev, markets: mm})
	}

	// Build candidates.
	candidates := make([]Candidate, 0, len(bundles))
	bookBudget := cfg.MaxBookRequests
	for _, b := range bundles {
		feat := selection.ExtractFeatures(b.markets, now)
		dec := selection.Decide(feat, cfg.Selector, now)

		// Sport filter.
		if len(cfg.SportFilter) > 0 && !cfg.SportFilter[feat.SportPrefix] {
			continue
		}
		// Sport namespace gates.
		if !cfg.IncludeNonSport && feat.SportPrefix == "" {
			anomalies = append(anomalies, AnomalyEvent{
				ScannedAt: now,
				EventSlug: b.slug,
				Reason:    "UNKNOWN_PREFIX",
			})
			continue
		}
		// Client-side horizon enforcement. Gamma's end_date_max query
		// is best-effort; the server occasionally returns events past
		// the bound. Re-check here so the scanner output is always
		// inside the requested window.
		if cfg.Horizon > 0 && feat.MinEndDate != nil {
			horizonEnd := now.Add(cfg.Horizon)
			if feat.MinEndDate.After(horizonEnd) {
				continue
			}
		}
		// Single-market gate.
		if !cfg.IncludeSingleMarket && feat.MarketCount < cfg.Selector.MinMarketsPerEvent {
			continue
		}
		if dec.Score < cfg.MinScore {
			continue
		}

		// Liquidity gate.
		perMarket, gate := s.evaluateEventLiquidity(ctx, b.markets, cfg, &bookBudget)
		_ = perMarket

		// Apply liquidity filter.
		switch strings.ToLower(cfg.LiquidityFilter) {
		case "pass":
			if gate.Label != LiquidityPass {
				continue
			}
		case "warn":
			if gate.Label == LiquidityFail {
				continue
			}
		}

		// Risk flags. Companion-missing semantics: a base event flags
		// METADATA_SPLIT only when its `-more-markets` companion is
		// genuinely absent from the scan results. The previous
		// implementation passed the inverse boolean (true when the
		// companion was present), which incorrectly tagged every
		// well-paired base event.
		anyClosed := feat.AnyClosed
		lowerSlug := strings.ToLower(b.slug)
		suffix := strings.HasSuffix(lowerSlug, "-more-markets")
		baseSlug := strings.TrimSuffix(lowerSlug, "-more-markets")
		baseSeen := slugsSeen[baseSlug] || baseSlug == lowerSlug
		companionMissing := !suffix && !slugsSeen[lowerSlug+"-more-markets"]
		bookStale := false
		risk := RiskInput{
			SportPrefix:           feat.SportPrefix,
			NonSportDetected:      feat.SportPrefix == "",
			MarketCount:           feat.MarketCount,
			AnyClosedOrResolved:   anyClosed,
			SuffixMoreMarkets:     suffix,
			BaseEventSeen:         baseSeen,
			CompanionMissing:      companionMissing,
			LiquidityLabel:        gate.Label,
			MarketsWithBothQuotes: gate.MarketsWithBoth,
			AvgSpread:             gate.AvgSpread,
			MinDepth:              gate.MinTopDepth,
			BookAgeStale:          bookStale,
		}
		flags := ComputeRiskFlags(risk)

		// Build candidate.
		c := Candidate{
			EventSlug:       b.slug,
			EventID:         feat.EventID,
			Title:           feat.EventTitle,
			SportPrefix:     feat.SportPrefix,
			Score:           dec.Score,
			Band:            ScoreBand(dec.Score),
			SelectorReasons: dec.Reasons,
			LiquidityGate:   gate.Label,
			LiquidityResult: gate,
			MarketCount:     feat.MarketCount,
			BinaryCount:     feat.BinaryMarketCount,
			Families:        feat.Families,
			StartTs:         feat.MinEndDate,
			Active:          feat.AllActive,
			Closed:          feat.AnyClosed,
			ReasonSummary:   reasonSummary(feat, dec),
			RiskFlags:       flags,
			ReviewStatus:    reviewStatus(gate.Label, flags),
			ScannedAt:       now,
		}
		if feat.HorizonSeconds > 0 {
			c.TimeToStart = time.Duration(feat.HorizonSeconds) * time.Second
		}
		candidates = append(candidates, c)
	}

	rankCandidates(candidates)
	if cfg.Limit > 0 && len(candidates) > cfg.Limit {
		candidates = candidates[:cfg.Limit]
	}
	return candidates, anomalies, nil
}

func (s *Scanner) fetchAllEvents(ctx context.Context, startUnix, endUnix int64, cfg Config) ([]map[string]any, error) {
	var out []map[string]any
	offset := 0
	pageSize := 100
	for offset < cfg.MaxEvents {
		page, err := s.Client.FetchActiveEvents(ctx, polymarket.FetchActiveEventsRequest{
			StartUnix: startUnix,
			EndUnix:   endUnix,
			Limit:     pageSize,
			Offset:    offset,
		})
		if err != nil {
			return nil, fmt.Errorf("fetch events offset=%d: %w", offset, err)
		}
		if len(page.Rows) == 0 {
			break
		}
		out = append(out, page.Rows...)
		offset += pageSize
		if len(page.Rows) < pageSize {
			break
		}
		if cfg.MaxEvents > 0 && len(out) >= cfg.MaxEvents {
			break
		}
	}
	if cfg.MaxEvents > 0 && len(out) > cfg.MaxEvents {
		out = out[:cfg.MaxEvents]
	}
	return out, nil
}

func (s *Scanner) evaluateEventLiquidity(ctx context.Context, markets []selection.MarketMeta, cfg Config, budget *int) ([]PerMarketLiquidity, LiquidityResult) {
	per := make([]PerMarketLiquidity, 0, len(markets))
	for _, m := range markets {
		if budget != nil && *budget <= 0 {
			// no budget left; treat remaining markets as unknown
			per = append(per, PerMarketLiquidity{
				ConditionID: m.ConditionID,
				BookErrorA:  "book-budget-exhausted",
				BookErrorB:  "book-budget-exhausted",
			})
			continue
		}
		tokA, tokB, ok := parseBinaryTokens(m.ClobTokenIDs)
		if !ok {
			per = append(per, PerMarketLiquidity{
				ConditionID: m.ConditionID,
				BookErrorA:  "non-binary-or-no-tokens",
				BookErrorB:  "non-binary-or-no-tokens",
			})
			continue
		}
		bookA, errA := s.fetchBookCached(ctx, tokA, cfg)
		if budget != nil {
			*budget--
		}
		bookB, errB := s.fetchBookCached(ctx, tokB, cfg)
		if budget != nil {
			*budget--
		}
		per = append(per, buildPerMarket(m.ConditionID, tokA, tokB, bookA, bookB, errA, errB))
	}
	return per, EvaluateLiquidity(per, cfg.Liquidity)
}

func (s *Scanner) fetchBookCached(ctx context.Context, tokenID string, cfg Config) (*polymarket.Orderbook, string) {
	if tokenID == "" {
		return nil, "empty-token"
	}
	s.mu.Lock()
	if e, ok := s.bookCache[tokenID]; ok {
		if time.Since(e.fetchedAt) < cfg.BookCacheTTL {
			s.mu.Unlock()
			return e.book, e.err
		}
	}
	s.mu.Unlock()
	book, err := s.Client.GetBook(ctx, tokenID)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	s.mu.Lock()
	s.bookCache[tokenID] = bookEntry{book: book, fetchedAt: time.Now(), err: errStr}
	s.mu.Unlock()
	return book, errStr
}

func parseBinaryTokens(clobTokenIDs string) (string, string, bool) {
	v := strings.TrimSpace(clobTokenIDs)
	if v == "" {
		return "", "", false
	}
	var arr []string
	if err := json.Unmarshal([]byte(v), &arr); err == nil {
		if len(arr) == 2 {
			return arr[0], arr[1], true
		}
	}
	// Try alternative: array of any with string conversion.
	var any2 []any
	if err := json.Unmarshal([]byte(v), &any2); err == nil && len(any2) == 2 {
		a, ok1 := any2[0].(string)
		b, ok2 := any2[1].(string)
		if ok1 && ok2 {
			return a, b, true
		}
	}
	return "", "", false
}

// ScoreBand maps a numeric selector score to the band name documented
// in the Selector Evaluation Report.
func ScoreBand(score float64) string {
	switch {
	case score >= 10:
		return "very_strong"
	case score >= 8:
		return "strong"
	case score >= 6:
		return "medium"
	case score >= 4:
		return "basic"
	default:
		return "low"
	}
}

func reasonSummary(f selection.EventFeatures, d selection.Decision) string {
	if !d.Eligible {
		return "ineligible"
	}
	parts := []string{}
	if f.BinaryMarketCount > 0 {
		parts = append(parts, fmt.Sprintf("%d-binary", f.BinaryMarketCount))
	}
	if len(f.Families) > 0 {
		parts = append(parts, "family:"+strings.Join(f.Families, "+"))
	}
	if f.HorizonSeconds > 0 && f.HorizonSeconds <= 2*24*3600 {
		parts = append(parts, "near-term")
	}
	if f.MarketCount >= 4 {
		parts = append(parts, "mid-or-large-bundle")
	}
	if len(parts) == 0 {
		return "eligible"
	}
	return strings.Join(parts, "+")
}

func reviewStatus(gate LiquidityLabel, flags []RiskFlag) string {
	if gate == LiquidityPass && len(flags) == 0 {
		return ""
	}
	return "review"
}

func rankCandidates(c []Candidate) {
	sort.SliceStable(c, func(i, j int) bool {
		if c[i].Score != c[j].Score {
			return c[i].Score > c[j].Score
		}
		// liquidity tie-break: PASS > WARN > UNKNOWN > FAIL
		li := liquidityRank(c[i].LiquidityGate)
		lj := liquidityRank(c[j].LiquidityGate)
		if li != lj {
			return li > lj
		}
		// horizon tie-break: closer first
		if c[i].TimeToStart != c[j].TimeToStart {
			if c[i].TimeToStart == 0 {
				return false
			}
			if c[j].TimeToStart == 0 {
				return true
			}
			return c[i].TimeToStart < c[j].TimeToStart
		}
		return c[i].EventSlug < c[j].EventSlug
	})
	for i := range c {
		c[i].Rank = i + 1
	}
}

func liquidityRank(l LiquidityLabel) int {
	switch l {
	case LiquidityPass:
		return 4
	case LiquidityWarn:
		return 3
	case LiquidityUnknown:
		return 2
	case LiquidityFail:
		return 1
	}
	return 0
}


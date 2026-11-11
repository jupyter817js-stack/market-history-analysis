package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// OrderbookLevel is one (price, size) entry in either the bid or ask
// side of the CLOB book.
type OrderbookLevel struct {
	Price float64
	Size  float64
}

// Orderbook is the public CLOB book snapshot for one outcome token.
// No private fields, no authentication, no signer.
type Orderbook struct {
	TokenID string
	Bids    []OrderbookLevel // sorted highest price first
	Asks    []OrderbookLevel // sorted lowest price first
	Raw     *RawResponse
}

// BestBid returns the highest bid or (0, false) if none.
func (b *Orderbook) BestBid() (OrderbookLevel, bool) {
	if b == nil || len(b.Bids) == 0 {
		return OrderbookLevel{}, false
	}
	return b.Bids[0], true
}

// BestAsk returns the lowest ask or (0, false) if none.
func (b *Orderbook) BestAsk() (OrderbookLevel, bool) {
	if b == nil || len(b.Asks) == 0 {
		return OrderbookLevel{}, false
	}
	return b.Asks[0], true
}

// TopNDepth sums the size of the first N levels on a side. Pass "bid"
// or "ask".
func (b *Orderbook) TopNDepth(side string, n int) float64 {
	if b == nil || n <= 0 {
		return 0
	}
	var levels []OrderbookLevel
	switch side {
	case "bid", "bids":
		levels = b.Bids
	case "ask", "asks":
		levels = b.Asks
	default:
		return 0
	}
	sum := 0.0
	for i, lv := range levels {
		if i >= n {
			break
		}
		sum += lv.Size
	}
	return sum
}

// GetBook fetches the public CLOB orderbook for one outcome token.
// Read-only; uses no auth, no signer, no wallet.
func (c *Client) GetBook(ctx context.Context, tokenID string) (*Orderbook, error) {
	if tokenID == "" {
		return nil, fmt.Errorf("token_id required")
	}
	full := buildURL(c.clobBase, "/book", map[string]string{"token_id": tokenID})
	raw, err := c.getJSON(ctx, "clob", full)
	if err != nil {
		return nil, err
	}
	return parseBook(tokenID, raw)
}

func parseBook(tokenID string, raw *RawResponse) (*Orderbook, error) {
	if raw == nil || len(raw.Body) == 0 {
		return &Orderbook{TokenID: tokenID, Raw: raw}, nil
	}
	var env map[string]any
	if err := json.Unmarshal(raw.Body, &env); err != nil {
		return nil, fmt.Errorf("decode /book body: %w", err)
	}
	out := &Orderbook{TokenID: tokenID, Raw: raw}
	out.Bids = sortBidsDesc(extractLevels(env["bids"]))
	out.Asks = sortAsksAsc(extractLevels(env["asks"]))
	return out, nil
}

func extractLevels(v any) []OrderbookLevel {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []OrderbookLevel
	for _, raw := range arr {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var lv OrderbookLevel
		lv.Price = asFloat(m["price"])
		lv.Size = asFloat(m["size"])
		if lv.Price <= 0 || lv.Size <= 0 {
			continue
		}
		out = append(out, lv)
	}
	return out
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}

func sortBidsDesc(levels []OrderbookLevel) []OrderbookLevel {
	// insertion sort; book pages are short
	for i := 1; i < len(levels); i++ {
		for j := i; j > 0 && levels[j].Price > levels[j-1].Price; j-- {
			levels[j], levels[j-1] = levels[j-1], levels[j]
		}
	}
	return levels
}

func sortAsksAsc(levels []OrderbookLevel) []OrderbookLevel {
	for i := 1; i < len(levels); i++ {
		for j := i; j > 0 && levels[j].Price < levels[j-1].Price; j-- {
			levels[j], levels[j-1] = levels[j-1], levels[j]
		}
	}
	return levels
}

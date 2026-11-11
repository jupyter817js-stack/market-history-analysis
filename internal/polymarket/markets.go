package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type KeysetResult struct {
	Raw        *RawResponse
	Rows       []map[string]any
	NextCursor string
	Endpoint   string
	Params     map[string]string
}

type MarketsKeysetRequest struct {
	ConditionIDs []string
	Slugs        []string
	ClobTokenIDs []string
	Limit        int
	AfterCursor  string
}

func (c *Client) GetMarketsKeyset(ctx context.Context, req MarketsKeysetRequest) (*KeysetResult, error) {
	if req.Limit <= 0 {
		req.Limit = 100
	}
	params := map[string]string{
		"limit": strconv.Itoa(req.Limit),
	}
	if len(req.ConditionIDs) > 0 {
		params["condition_ids"] = strings.Join(req.ConditionIDs, ",")
	}
	if len(req.Slugs) > 0 {
		params["slug"] = strings.Join(req.Slugs, ",")
	}
	if len(req.ClobTokenIDs) > 0 {
		params["clob_token_ids"] = strings.Join(req.ClobTokenIDs, ",")
	}
	if req.AfterCursor != "" {
		params["after_cursor"] = req.AfterCursor
	}
	full := buildURL(c.gammaBase, "/markets/keyset", params)
	raw, err := c.getJSON(ctx, "gamma", full)
	if err != nil {
		return nil, err
	}
	rows, next, err := decodeKeyset(raw.Body, "data")
	if err != nil {
		return nil, fmt.Errorf("decode markets keyset: %w", err)
	}
	return &KeysetResult{
		Raw:        raw,
		Rows:       rows,
		NextCursor: next,
		Endpoint:   "/markets/keyset",
		Params:     params,
	}, nil
}

type EventsKeysetRequest struct {
	Slugs       []string
	Limit       int
	AfterCursor string
}

func (c *Client) GetEventsKeyset(ctx context.Context, req EventsKeysetRequest) (*KeysetResult, error) {
	if req.Limit <= 0 {
		req.Limit = 100
	}
	params := map[string]string{
		"limit": strconv.Itoa(req.Limit),
	}
	if len(req.Slugs) > 0 {
		params["slug"] = strings.Join(req.Slugs, ",")
	}
	if req.AfterCursor != "" {
		params["after_cursor"] = req.AfterCursor
	}
	full := buildURL(c.gammaBase, "/events/keyset", params)
	raw, err := c.getJSON(ctx, "gamma", full)
	if err != nil {
		return nil, err
	}
	rows, next, err := decodeKeyset(raw.Body, "data")
	if err != nil {
		return nil, fmt.Errorf("decode events keyset: %w", err)
	}
	return &KeysetResult{
		Raw:        raw,
		Rows:       rows,
		NextCursor: next,
		Endpoint:   "/events/keyset",
		Params:     params,
	}, nil
}

func decodeKeyset(body []byte, dataKey string) ([]map[string]any, string, error) {
	if len(body) == 0 {
		return nil, "", nil
	}
	var asArray []map[string]any
	if err := json.Unmarshal(body, &asArray); err == nil {
		return asArray, "", nil
	}
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, "", err
	}
	rows := []map[string]any{}
	if v, ok := env[dataKey]; ok {
		if arr, ok := v.([]any); ok {
			for _, r := range arr {
				if m, ok := r.(map[string]any); ok {
					rows = append(rows, m)
				}
			}
		}
	}
	if len(rows) == 0 {
		for _, key := range []string{"results", "rows", "markets", "events"} {
			if v, ok := env[key]; ok {
				if arr, ok := v.([]any); ok {
					for _, r := range arr {
						if m, ok := r.(map[string]any); ok {
							rows = append(rows, m)
						}
					}
				}
			}
		}
	}
	next := ""
	if v, ok := env["next_cursor"].(string); ok {
		next = v
	} else if v, ok := env["nextCursor"].(string); ok {
		next = v
	}
	return rows, next, nil
}

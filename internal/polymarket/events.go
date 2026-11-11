package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

type EventsResult struct {
	Raw      *RawResponse
	Rows     []map[string]any
	Endpoint string
	Params   map[string]string
}

// GetEventsByMultiSlug calls Gamma /events with the multi-value slug parameter
// pattern (?slug=A&slug=B&slug=C...). This is the form that actually filters
// server-side; the keyset endpoint and CSV-style slug=A,B,C both return empty.
//
// limit defaults to 100.
func (c *Client) GetEventsByMultiSlug(ctx context.Context, slugs []string, limit int) (*EventsResult, error) {
	if len(slugs) == 0 {
		return nil, fmt.Errorf("at least one slug is required")
	}
	if limit <= 0 {
		limit = 100
	}
	u, err := url.Parse(c.gammaBase + "/events")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for _, s := range slugs {
		if s == "" {
			continue
		}
		q.Add("slug", s)
	}
	q.Set("limit", strconv.Itoa(limit))
	u.RawQuery = q.Encode()

	raw, err := c.getJSON(ctx, "gamma", u.String())
	if err != nil {
		return nil, err
	}
	rows, err := decodeEventsBody(raw.Body)
	if err != nil {
		return nil, fmt.Errorf("decode /events body: %w", err)
	}
	params := map[string]string{
		"slug_count": strconv.Itoa(len(slugs)),
		"limit":      strconv.Itoa(limit),
	}
	return &EventsResult{
		Raw:      raw,
		Rows:     rows,
		Endpoint: "/events",
		Params:   params,
	}, nil
}

func decodeEventsBody(body []byte) ([]map[string]any, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var asArray []map[string]any
	if err := json.Unmarshal(body, &asArray); err == nil {
		return asArray, nil
	}
	var env map[string]any
	if err := json.Unmarshal(body, &env); err == nil {
		for _, key := range []string{"events", "data", "results"} {
			if v, ok := env[key]; ok {
				if arr, ok := v.([]any); ok {
					out := make([]map[string]any, 0, len(arr))
					for _, r := range arr {
						if m, ok := r.(map[string]any); ok {
							out = append(out, m)
						}
					}
					return out, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("unexpected /events body shape")
}

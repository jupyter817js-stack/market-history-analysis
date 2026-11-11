package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

type ActivityResult struct {
	Raw      *RawResponse
	Rows     []map[string]any
	Endpoint string
	Params   map[string]string
}

type ActivityRequest struct {
	User          string
	StartUnix     int64
	EndUnix       int64
	Limit         int
	Offset        int
	SortBy        string
	SortDirection string
	Type          string
}

func (c *Client) GetActivity(ctx context.Context, req ActivityRequest) (*ActivityResult, error) {
	if req.User == "" {
		return nil, fmt.Errorf("user required")
	}
	if req.Limit <= 0 {
		req.Limit = 500
	}
	if req.SortBy == "" {
		req.SortBy = "TIMESTAMP"
	}
	if req.SortDirection == "" {
		req.SortDirection = "ASC"
	}
	params := map[string]string{
		"user":          req.User,
		"limit":         strconv.Itoa(req.Limit),
		"offset":        strconv.Itoa(req.Offset),
		"sortBy":        req.SortBy,
		"sortDirection": req.SortDirection,
	}
	if req.StartUnix > 0 {
		params["start"] = strconv.FormatInt(req.StartUnix, 10)
	}
	if req.EndUnix > 0 {
		params["end"] = strconv.FormatInt(req.EndUnix, 10)
	}
	if req.Type != "" {
		params["type"] = req.Type
	}

	full := buildURL(c.dataBase, "/activity", params)
	raw, err := c.getJSON(ctx, "data", full)
	if err != nil {
		return nil, err
	}
	rows, err := decodeRowsFlexible(raw.Body)
	if err != nil {
		return nil, fmt.Errorf("decode activity rows: %w", err)
	}
	return &ActivityResult{
		Raw:      raw,
		Rows:     rows,
		Endpoint: "/activity",
		Params:   params,
	}, nil
}

func decodeRowsFlexible(body []byte) ([]map[string]any, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var asArray []map[string]any
	if err := json.Unmarshal(body, &asArray); err == nil {
		return asArray, nil
	}
	var asObject map[string]any
	if err := json.Unmarshal(body, &asObject); err == nil {
		for _, key := range []string{"data", "results", "rows", "items", "activity"} {
			if v, ok := asObject[key]; ok {
				if rows, ok := v.([]any); ok {
					out := make([]map[string]any, 0, len(rows))
					for _, r := range rows {
						if m, ok := r.(map[string]any); ok {
							out = append(out, m)
						}
					}
					return out, nil
				}
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("response is neither array nor object envelope")
}

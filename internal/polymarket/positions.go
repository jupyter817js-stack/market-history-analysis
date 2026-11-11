package polymarket

import (
	"context"
	"fmt"
	"strconv"
)

type PositionsResult struct {
	Raw      *RawResponse
	Rows     []map[string]any
	Endpoint string
	Params   map[string]string
}

type PositionsRequest struct {
	User          string
	SizeThreshold *float64
	Limit         int
	Offset        int
	SortBy        string
	SortDirection string
}

func (c *Client) GetPositions(ctx context.Context, req PositionsRequest) (*PositionsResult, error) {
	if req.User == "" {
		return nil, fmt.Errorf("user required")
	}
	if req.Limit <= 0 {
		req.Limit = 500
	}
	if req.SortBy == "" {
		req.SortBy = "TITLE"
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
	if req.SizeThreshold != nil {
		params["sizeThreshold"] = strconv.FormatFloat(*req.SizeThreshold, 'f', -1, 64)
	}
	full := buildURL(c.dataBase, "/positions", params)
	raw, err := c.getJSON(ctx, "data", full)
	if err != nil {
		return nil, err
	}
	rows, err := decodeRowsFlexible(raw.Body)
	if err != nil {
		return nil, fmt.Errorf("decode positions rows: %w", err)
	}
	return &PositionsResult{
		Raw:      raw,
		Rows:     rows,
		Endpoint: "/positions",
		Params:   params,
	}, nil
}

type ClosedPositionsRequest struct {
	User          string
	Limit         int
	Offset        int
	SortBy        string
	SortDirection string
}

func (c *Client) GetClosedPositions(ctx context.Context, req ClosedPositionsRequest) (*PositionsResult, error) {
	if req.User == "" {
		return nil, fmt.Errorf("user required")
	}
	if req.Limit <= 0 {
		req.Limit = 50
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
	full := buildURL(c.dataBase, "/closed-positions", params)
	raw, err := c.getJSON(ctx, "data", full)
	if err != nil {
		return nil, err
	}
	rows, err := decodeRowsFlexible(raw.Body)
	if err != nil {
		return nil, fmt.Errorf("decode closed-positions rows: %w", err)
	}
	return &PositionsResult{
		Raw:      raw,
		Rows:     rows,
		Endpoint: "/closed-positions",
		Params:   params,
	}, nil
}

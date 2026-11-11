package polymarket

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// FetchActiveEventsRequest narrows the public Gamma /events endpoint to
// the current set of active, near-term sports events the live scanner
// needs. All fields are pre-trade only; no account or position data is
// involved.
type FetchActiveEventsRequest struct {
	// StartUnix and EndUnix bound the event end_date window in UTC.
	StartUnix int64
	EndUnix   int64
	// Limit per page. Defaults to 100 if zero. Polymarket's docs state
	// /events can serve larger pages but we stay conservative.
	Limit int
	// Offset for pagination. Defaults to 0.
	Offset int
}

// FetchActiveEvents calls Gamma /events with active=true & closed=false
// and end_date bounded to the supplied window. The response contains
// event objects with nested markets[] arrays (the same shape used by
// FetchEventsBySlug). Read-only, no auth.
func (c *Client) FetchActiveEvents(ctx context.Context, req FetchActiveEventsRequest) (*EventsResult, error) {
	if req.Limit <= 0 {
		req.Limit = 100
	}
	u, err := url.Parse(c.gammaBase + "/events")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("active", "true")
	q.Set("closed", "false")
	q.Set("limit", strconv.Itoa(req.Limit))
	if req.Offset > 0 {
		q.Set("offset", strconv.Itoa(req.Offset))
	}
	if req.StartUnix > 0 {
		q.Set("end_date_min", time.Unix(req.StartUnix, 0).UTC().Format(time.RFC3339))
	}
	if req.EndUnix > 0 {
		q.Set("end_date_max", time.Unix(req.EndUnix, 0).UTC().Format(time.RFC3339))
	}
	u.RawQuery = q.Encode()
	raw, err := c.getJSON(ctx, "gamma", u.String())
	if err != nil {
		return nil, err
	}
	rows, err := decodeEventsBody(raw.Body)
	if err != nil {
		return nil, fmt.Errorf("decode active events: %w", err)
	}
	return &EventsResult{
		Raw:      raw,
		Rows:     rows,
		Endpoint: "/events",
		Params: map[string]string{
			"active":       "true",
			"closed":       "false",
			"limit":        strconv.Itoa(req.Limit),
			"offset":       strconv.Itoa(req.Offset),
			"end_date_min": strconv.FormatInt(req.StartUnix, 10),
			"end_date_max": strconv.FormatInt(req.EndUnix, 10),
		},
	}, nil
}

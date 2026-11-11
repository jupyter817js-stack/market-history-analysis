package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type InsertRawResponse struct {
	SourceRunID   int64
	SourceName    string
	Endpoint      string
	RequestURL    string
	RequestParams map[string]string
	ResponseBody  []byte
	ResponseHash  string
	HTTPStatus    int
	FetchedAt     time.Time
	PageOffset    *int
	Cursor        *string
	WindowStart   *time.Time
	WindowEnd     *time.Time
}

func (s *Store) InsertRawResponse(ctx context.Context, in InsertRawResponse) (int64, error) {
	paramsJSON, err := json.Marshal(in.RequestParams)
	if err != nil {
		return 0, err
	}
	if len(in.ResponseBody) == 0 {
		in.ResponseBody = []byte("null")
	}
	var pageOffset sql.NullInt64
	if in.PageOffset != nil {
		pageOffset = sql.NullInt64{Int64: int64(*in.PageOffset), Valid: true}
	}
	var cursor sql.NullString
	if in.Cursor != nil && *in.Cursor != "" {
		cursor = sql.NullString{String: *in.Cursor, Valid: true}
	}
	var ws, we sql.NullTime
	if in.WindowStart != nil && !in.WindowStart.IsZero() {
		ws = sql.NullTime{Time: in.WindowStart.UTC(), Valid: true}
	}
	if in.WindowEnd != nil && !in.WindowEnd.IsZero() {
		we = sql.NullTime{Time: in.WindowEnd.UTC(), Valid: true}
	}
	var id int64
	err = s.DB.QueryRowContext(ctx,
		`insert into raw_api_responses
		(source_run_id, source_name, endpoint, request_url, request_params,
		 response_body, response_hash, http_status, fetched_at,
		 page_offset, cursor, time_window_start, time_window_end)
		 values ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$8,$9,$10,$11,$12,$13)
		 returning id`,
		in.SourceRunID, in.SourceName, in.Endpoint, in.RequestURL, string(paramsJSON),
		string(in.ResponseBody), in.ResponseHash, in.HTTPStatus, in.FetchedAt.UTC(),
		pageOffset, cursor, ws, we,
	).Scan(&id)
	return id, err
}

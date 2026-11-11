package store

import (
	"database/sql"
	"time"
)

type SourceRun struct {
	ID             int64
	SourceName     string
	RunType        string
	AccountAddress string
	ProxyWallet    sql.NullString
	StartedAt      time.Time
	FinishedAt     sql.NullTime
	Status         string
	ErrorMessage   sql.NullString
}

type RawResponseRow struct {
	ID             int64
	SourceRunID    int64
	SourceName     string
	Endpoint       string
	RequestURL     string
	RequestParams  []byte
	ResponseBody   []byte
	ResponseHash   string
	HTTPStatus     int
	FetchedAt      time.Time
	PageOffset     sql.NullInt64
	Cursor         sql.NullString
	WindowStart    sql.NullTime
	WindowEnd      sql.NullTime
}

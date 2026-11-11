package store

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) StartSourceRun(ctx context.Context, sourceName, runType, accountAddress string, proxyWallet string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx,
		`insert into source_runs(source_name, run_type, account_address, proxy_wallet, status)
		 values ($1,$2,$3,nullif($4,''),'running') returning id`,
		sourceName, runType, accountAddress, proxyWallet,
	).Scan(&id)
	return id, err
}

func (s *Store) FinishSourceRun(ctx context.Context, runID int64, status string, errMsg string, reqCount, rawCount, normCount int) error {
	_, err := s.DB.ExecContext(ctx,
		`update source_runs
		 set finished_at = $1,
		     status = $2,
		     error_message = nullif($3,''),
		     request_count = $4,
		     raw_response_count = $5,
		     normalized_count = $6
		 where id = $7`,
		time.Now().UTC(), status, errMsg, reqCount, rawCount, normCount, runID,
	)
	return err
}

func (s *Store) RecentRuns(ctx context.Context, limit int) ([]SourceRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.DB.QueryContext(ctx,
		`select id, source_name, run_type, account_address, proxy_wallet, started_at, finished_at, status, error_message
		 from source_runs
		 order by started_at desc
		 limit $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceRun
	for rows.Next() {
		var r SourceRun
		var pw, em sql.NullString
		var fin sql.NullTime
		if err := rows.Scan(&r.ID, &r.SourceName, &r.RunType, &r.AccountAddress, &pw, &r.StartedAt, &fin, &r.Status, &em); err != nil {
			return nil, err
		}
		r.ProxyWallet = pw
		r.FinishedAt = fin
		r.ErrorMessage = em
		out = append(out, r)
	}
	return out, rows.Err()
}

package store

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) UpsertAccount(ctx context.Context, accountAddress, proxyWallet, displayName string, createdAtSource *time.Time, profileRaw []byte) error {
	if len(profileRaw) == 0 {
		profileRaw = []byte("null")
	}
	var pw sql.NullString
	if proxyWallet != "" {
		pw = sql.NullString{String: proxyWallet, Valid: true}
	}
	var dn sql.NullString
	if displayName != "" {
		dn = sql.NullString{String: displayName, Valid: true}
	}
	var ca sql.NullTime
	if createdAtSource != nil && !createdAtSource.IsZero() {
		ca = sql.NullTime{Time: createdAtSource.UTC(), Valid: true}
	}
	_, err := s.DB.ExecContext(ctx,
		`insert into accounts(account_address, proxy_wallet, display_name, created_at_source, profile_raw, updated_at)
		 values ($1,$2,$3,$4,$5::jsonb, now())
		 on conflict (account_address) do update
		   set proxy_wallet      = excluded.proxy_wallet,
		       display_name      = excluded.display_name,
		       created_at_source = excluded.created_at_source,
		       profile_raw       = excluded.profile_raw,
		       updated_at        = excluded.updated_at`,
		accountAddress, pw, dn, ca, string(profileRaw),
	)
	return err
}

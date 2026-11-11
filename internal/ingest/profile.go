package ingest

import (
	"context"
	"fmt"
	"time"

	"rn1-strategy-extractor/internal/polymarket"
	"rn1-strategy-extractor/internal/store"
	"rn1-strategy-extractor/internal/util"
)

func (o *Orchestrator) IngestProfile(ctx context.Context, accountAddress string) (AccountIdentity, error) {
	if err := ValidateAccount(accountAddress); err != nil {
		return AccountIdentity{}, err
	}
	o.resetCounters()

	runID, err := o.Store.StartSourceRun(ctx, "polymarket", "profile", accountAddress, "")
	if err != nil {
		return AccountIdentity{}, fmt.Errorf("start source run: %w", err)
	}

	identity := AccountIdentity{Input: accountAddress}
	runErr := func() error {
		o.bumpRequest()
		res, err := o.Client.GetPublicProfile(ctx, accountAddress)
		if err != nil {
			return fmt.Errorf("get public profile: %w", err)
		}
		if o.DryRun {
			o.logger().Info("dry-run: would store profile", "url", res.Raw.URL, "bytes", len(res.Raw.Body))
			identity.ProxyWallet = polymarket.ProxyWalletFromProfile(res.Parsed)
			return nil
		}
		_, err = o.Store.InsertRawResponse(ctx, store.InsertRawResponse{
			SourceRunID:   runID,
			SourceName:    "polymarket",
			Endpoint:      res.Endpoint,
			RequestURL:    res.Raw.URL,
			RequestParams: res.Params,
			ResponseBody:  res.Raw.Body,
			ResponseHash:  res.Raw.Hash,
			HTTPStatus:    res.Raw.StatusCode,
			FetchedAt:     res.Raw.FetchedAt,
		})
		if err != nil {
			return fmt.Errorf("insert raw profile: %w", err)
		}
		o.bumpRaw()

		identity.ProxyWallet = polymarket.ProxyWalletFromProfile(res.Parsed)
		displayName := polymarket.DisplayNameFromProfile(res.Parsed)
		createdAtStr := polymarket.CreatedAtSourceFromProfile(res.Parsed)

		var createdAt *time.Time
		if t, err := util.ParseFlexibleTime(createdAtStr); err == nil && !t.IsZero() {
			tt := t
			createdAt = &tt
		}

		if identity.ProxyWallet == "" {
			o.logger().Warn("proxyWallet is null on profile; storing input address only", "address", accountAddress)
		}

		if err := o.Store.UpsertAccount(ctx, accountAddress, identity.ProxyWallet, displayName, createdAt, res.Raw.Body); err != nil {
			return fmt.Errorf("upsert account: %w", err)
		}
		return nil
	}()

	if ferr := o.finish(ctx, runID, runErr); ferr != nil && runErr == nil {
		return identity, ferr
	}
	return identity, runErr
}

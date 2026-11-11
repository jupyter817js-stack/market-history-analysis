package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"rn1-strategy-extractor/internal/polymarket"
)

type Orchestrator struct {
	Client          *polymarket.Client
	Store           Sink
	Logger          *slog.Logger
	Location        *time.Location
	PageSize        int
	MaxOffsetSafety int
	MinWindow       time.Duration
	DryRun          bool

	// Per-run counters
	requestCount    int
	rawCount        int
	normalizedCount int
	partial         bool
}

func (o *Orchestrator) resetCounters() {
	o.requestCount = 0
	o.rawCount = 0
	o.normalizedCount = 0
	o.partial = false
}

func (o *Orchestrator) bumpRequest()    { o.requestCount++ }
func (o *Orchestrator) bumpRaw()        { o.rawCount++ }
func (o *Orchestrator) bumpNormalized() { o.normalizedCount++ }
func (o *Orchestrator) markPartial()    { o.partial = true }

func (o *Orchestrator) finish(ctx context.Context, runID int64, runErr error) error {
	status := "success"
	errMsg := ""
	if runErr != nil {
		status = "failed"
		errMsg = runErr.Error()
	} else if o.partial {
		status = "partial"
	}
	return o.Store.FinishSourceRun(ctx, runID, status, errMsg, o.requestCount, o.rawCount, o.normalizedCount)
}

func (o *Orchestrator) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

type AccountIdentity struct {
	Input       string
	ProxyWallet string
}

func (a AccountIdentity) Effective() string {
	if a.ProxyWallet != "" {
		return a.ProxyWallet
	}
	return a.Input
}

func ValidateAccount(addr string) error {
	if addr == "" {
		return fmt.Errorf("--account is required")
	}
	return nil
}

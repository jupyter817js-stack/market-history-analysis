package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rn1-strategy-extractor/internal/config"
	"rn1-strategy-extractor/internal/ingest"
	"rn1-strategy-extractor/internal/polymarket"
	"rn1-strategy-extractor/internal/store"
	"rn1-strategy-extractor/internal/util"
)

type commonFlags struct {
	Account         string
	DatabaseURL     string
	Start           string
	End             string
	Timezone        string
	HTTPTimeout     time.Duration
	PageSize        int
	MaxOffsetSafety int
	MinWindow       time.Duration
	DryRun          bool
}

func bind(fs *flag.FlagSet, def config.Config) *commonFlags {
	cf := &commonFlags{}
	fs.StringVar(&cf.Account, "account", "", "input wallet address (RN1)")
	fs.StringVar(&cf.DatabaseURL, "database-url", def.DatabaseURL, "PostgreSQL DSN")
	fs.StringVar(&cf.Start, "start", "2024-12-01T00:00:00Z", "ingest window start (RFC3339)")
	fs.StringVar(&cf.End, "end", time.Now().UTC().Format(time.RFC3339), "ingest window end (RFC3339)")
	fs.StringVar(&cf.Timezone, "timezone", def.Timezone, "timezone for KST-style columns")
	fs.DurationVar(&cf.HTTPTimeout, "http-timeout", def.HTTPTimeout, "HTTP client timeout")
	fs.IntVar(&cf.PageSize, "page-size", def.PageSize, "page size for /activity")
	fs.IntVar(&cf.MaxOffsetSafety, "max-offset-safety", def.MaxOffsetSafety, "max safe offset before splitting window")
	fs.DurationVar(&cf.MinWindow, "min-window", def.MinWindow, "smallest split window")
	fs.BoolVar(&cf.DryRun, "dry-run", false, "do not commit normalized inserts")
	return cf
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	def := config.Default()

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cf := bind(fs, def)
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if err := run(cmd, cf, def); err != nil {
		slog.Error("ingest failed", "command", cmd, "err", err)
		os.Exit(1)
	}
}

func run(cmd string, cf *commonFlags, def config.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := util.LoadEnvFile(".env"); err != nil {
		slog.Warn("could not load .env", "err", err)
	}

	cfg := def
	cfg.DatabaseURL = cf.DatabaseURL
	cfg.HTTPTimeout = cf.HTTPTimeout
	cfg.Timezone = cf.Timezone
	cfg.PageSize = cf.PageSize
	cfg.MaxOffsetSafety = cf.MaxOffsetSafety
	cfg.MinWindow = cf.MinWindow
	if err := cfg.Validate(); err != nil {
		return err
	}

	loc, err := util.LoadLocation(cfg.Timezone)
	if err != nil {
		return err
	}

	start, err := util.ParseFlexibleTime(cf.Start)
	if err != nil {
		return fmt.Errorf("parse start: %w", err)
	}
	end, err := util.ParseFlexibleTime(cf.End)
	if err != nil {
		return fmt.Errorf("parse end: %w", err)
	}

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	client := polymarket.NewClient(polymarket.ClientOptions{
		GammaBaseURL: cfg.GammaBaseURL,
		DataBaseURL:  cfg.DataBaseURL,
		HTTPTimeout:  cfg.HTTPTimeout,
		GammaRPS:     cfg.GammaRPS,
		DataRPS:      cfg.DataRPS,
		RetryMax:     cfg.RetryMax,
	})
	defer client.Close()

	orch := &ingest.Orchestrator{
		Client:          client,
		Store:           st,
		Logger:          slog.Default(),
		Location:        loc,
		PageSize:        cfg.PageSize,
		MaxOffsetSafety: cfg.MaxOffsetSafety,
		MinWindow:       cfg.MinWindow,
		DryRun:          cf.DryRun,
	}

	switch cmd {
	case "profile":
		identity, err := orch.IngestProfile(ctx, cf.Account)
		if err != nil {
			return err
		}
		fmt.Printf("profile ingested; proxy_wallet=%s\n", identity.ProxyWallet)
		return nil
	case "activity":
		identity, err := orch.IngestProfile(ctx, cf.Account)
		if err != nil {
			return err
		}
		return orch.IngestActivity(ctx, identity, start, end)
	case "positions":
		identity, err := orch.IngestProfile(ctx, cf.Account)
		if err != nil {
			return err
		}
		if err := orch.IngestPositions(ctx, identity); err != nil {
			return err
		}
		return orch.IngestClosedPositions(ctx, identity)
	case "metadata":
		identity, err := orch.IngestProfile(ctx, cf.Account)
		if err != nil {
			return err
		}
		return orch.IngestMetadata(ctx, identity)
	case "all":
		return orch.IngestAll(ctx, cf.Account, start, end)
	default:
		usage()
		return fmt.Errorf("unknown subcommand: %q", cmd)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rn1-ingest <profile|activity|positions|metadata|all> [flags]")
	fmt.Fprintln(os.Stderr, "flags: --account --database-url --start --end --timezone --http-timeout --page-size --max-offset-safety --min-window --dry-run")
}

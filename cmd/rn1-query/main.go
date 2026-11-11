package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"rn1-strategy-extractor/internal/config"
	"rn1-strategy-extractor/internal/store"
	"rn1-strategy-extractor/internal/summary"
	"rn1-strategy-extractor/internal/util"
)

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
	var account, dbURL string
	fs.StringVar(&account, "account", "", "input wallet address")
	fs.StringVar(&dbURL, "database-url", def.DatabaseURL, "PostgreSQL DSN")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if cmd != "summary" {
		usage()
		os.Exit(2)
	}
	if account == "" {
		fmt.Fprintln(os.Stderr, "--account is required")
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := util.LoadEnvFile(".env"); err != nil {
		slog.Warn("could not load .env", "err", err)
	}

	st, err := store.Open(ctx, dbURL)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	report, err := summary.Build(ctx, st, account)
	if err != nil {
		slog.Error("build summary", "err", err)
		os.Exit(1)
	}
	report.WriteText(os.Stdout)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rn1-query summary --account <addr> [--database-url <dsn>]")
}

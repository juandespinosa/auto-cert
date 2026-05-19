// Binary monitor is the local CLI entry point. It wires config + secrets +
// dependencies and delegates execution to internal/runner. For Lambda use
// cmd/lambda instead — both share the same runner.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"auto-certs/internal/config"
	"auto-certs/internal/runner"
	"auto-certs/internal/secrets"
)

func main() {
	var (
		configPath string
		dryRun     bool
	)
	flag.StringVar(&configPath, "config", "configs/config.yaml", "path to YAML config")
	flag.BoolVar(&dryRun, "dry-run", false, "render email to stdout/HTML file and skip state save")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	ctx := context.Background()

	// Secrets first (so config's ${VAR} expansion sees them). In Lambda the
	// SSM loader would run instead, but the CLI is local-dev only — always env.
	if err := secrets.LoadDotenv(""); err != nil {
		slog.Warn(".env load failed", "err", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	slog.Info("auto-certs initialized",
		"thresholds_days", cfg.Thresholds,
		"tls_workers", cfg.TLS.Workers,
		"tls_timeout", cfg.TLS.Timeout,
		"tls_default_port", cfg.TLS.DefaultPort,
		"notifier_backend", cfg.Notifier.Backend,
		"state_backend", cfg.State.Backend,
		"inventory_backend", cfg.Inventory.Backend,
		"dry_run", dryRun,
	)

	deps, err := runner.BuildDeps(ctx, cfg, dryRun)
	if err != nil {
		slog.Error("build deps failed", "err", err)
		os.Exit(1)
	}

	if err := runner.Run(ctx, deps); err != nil {
		slog.Error("run failed", "err", err)
		os.Exit(1)
	}
}

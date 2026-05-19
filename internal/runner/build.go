package runner

import (
	"context"
	"fmt"
	"os"

	"auto-certs/internal/config"
	"auto-certs/internal/inventory"
	"auto-certs/internal/notify"
	"auto-certs/internal/rdap"
	"auto-certs/internal/state"
)

// BuildDeps materializes State / Inventory / Notifier / RDAPCache from cfg.
// forceDryRun short-circuits the notifier to DryRun regardless of
// cfg.Notifier.Backend — used by the CLI -dry-run flag.
func BuildDeps(ctx context.Context, cfg *config.Config, forceDryRun bool) (Deps, error) {
	deps := Deps{Config: cfg, DryRunSkipsStateSave: forceDryRun}

	store, err := buildState(ctx, cfg)
	if err != nil {
		return deps, fmt.Errorf("build state: %w", err)
	}
	deps.State = store

	sink, err := buildInventory(ctx, cfg)
	if err != nil {
		return deps, fmt.Errorf("build inventory: %w", err)
	}
	deps.Inventory = sink

	n, err := buildNotifier(ctx, cfg, forceDryRun)
	if err != nil {
		return deps, fmt.Errorf("build notifier: %w", err)
	}
	deps.Notifier = n

	cache, err := buildRDAPCache(ctx, cfg)
	if err != nil {
		return deps, fmt.Errorf("build rdap cache: %w", err)
	}
	deps.RDAPCache = cache

	return deps, nil
}

func buildRDAPCache(ctx context.Context, cfg *config.Config) (rdap.Cache, error) {
	switch cfg.RDAP.Cache.Backend {
	case "", "none":
		return rdap.NoopCache{}, nil
	case "file":
		path := cfg.RDAP.Cache.Path
		if path == "" {
			path = "state/rdap-cache.json"
		}
		return rdap.NewFileCache(path, cfg.RDAP.Cache.TTL), nil
	case "s3":
		return rdap.NewS3Cache(ctx,
			cfg.RDAP.Cache.S3.Bucket,
			cfg.RDAP.Cache.S3.Key,
			cfg.RDAP.Cache.S3.Region,
			cfg.RDAP.Cache.TTL)
	default:
		return nil, fmt.Errorf("rdap.cache.backend %q: must be none|file|s3", cfg.RDAP.Cache.Backend)
	}
}

func buildState(ctx context.Context, cfg *config.Config) (state.Store, error) {
	switch cfg.State.Backend {
	case "file":
		return state.NewFileStore(cfg.State.Path), nil
	case "s3":
		return state.NewS3Store(ctx, cfg.State.S3.Bucket, cfg.State.S3.Key, cfg.State.S3.Region)
	default:
		return nil, fmt.Errorf("state.backend %q: must be file|s3", cfg.State.Backend)
	}
}

func buildInventory(ctx context.Context, cfg *config.Config) (inventory.Sink, error) {
	switch cfg.Inventory.Backend {
	case "file":
		return inventory.NewFileSink(cfg.Inventory.Path), nil
	case "s3":
		return inventory.NewS3Sink(ctx, cfg.Inventory.S3.Bucket, cfg.Inventory.S3.KeyPrefix, cfg.Inventory.S3.Region)
	default:
		return nil, fmt.Errorf("inventory.backend %q: must be file|s3", cfg.Inventory.Backend)
	}
}

func buildNotifier(ctx context.Context, cfg *config.Config, forceDryRun bool) (notify.Notifier, error) {
	backend := cfg.Notifier.Backend
	if forceDryRun {
		backend = "dryrun"
	}
	switch backend {
	case "dryrun":
		return &notify.DryRun{W: os.Stdout, HTMLPath: cfg.Notifier.DryRun.HTMLPath}, nil
	case "smtp":
		s := cfg.Notifier.SMTP
		if s.Host == "" || s.From == "" || len(s.To) == 0 {
			return nil, fmt.Errorf("smtp: host, from, and at least one to are required")
		}
		return &notify.SMTP{
			Host: s.Host, Port: s.Port,
			Username: s.Username, Password: s.Password,
			From: s.From, To: s.To,
		}, nil
	case "ses":
		return notify.NewSES(ctx, cfg.Notifier.SES.From, cfg.Notifier.SES.To, cfg.Notifier.SES.Region)
	default:
		return nil, fmt.Errorf("notifier.backend %q: must be dryrun|smtp|ses", backend)
	}
}

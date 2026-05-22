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

	store, err := buildState(cfg)
	if err != nil {
		return deps, fmt.Errorf("build state: %w", err)
	}
	deps.State = store

	sink, err := buildInventory(cfg)
	if err != nil {
		return deps, fmt.Errorf("build inventory: %w", err)
	}
	deps.Inventory = sink

	n, err := buildNotifier(cfg, forceDryRun)
	if err != nil {
		return deps, fmt.Errorf("build notifier: %w", err)
	}
	deps.Notifier = n

	cache, err := buildRDAPCache(cfg)
	if err != nil {
		return deps, fmt.Errorf("build rdap cache: %w", err)
	}
	deps.RDAPCache = cache
	_ = ctx

	return deps, nil
}

func buildRDAPCache(cfg *config.Config) (rdap.Cache, error) {
	switch cfg.RDAP.Cache.Backend {
	case "", "none":
		return rdap.NoopCache{}, nil
	case "file":
		path := cfg.RDAP.Cache.Path
		if path == "" {
			path = "state/rdap-cache.json"
		}
		return rdap.NewFileCache(path, cfg.RDAP.Cache.TTL), nil
	default:
		return nil, fmt.Errorf("rdap.cache.backend %q: must be none|file", cfg.RDAP.Cache.Backend)
	}
}

func buildState(cfg *config.Config) (state.Store, error) {
	switch cfg.State.Backend {
	case "file":
		return state.NewFileStore(cfg.State.Path), nil
	default:
		return nil, fmt.Errorf("state.backend %q: must be file", cfg.State.Backend)
	}
}

func buildInventory(cfg *config.Config) (inventory.Sink, error) {
	switch cfg.Inventory.Backend {
	case "file":
		return inventory.NewFileSink(cfg.Inventory.Path), nil
	default:
		return nil, fmt.Errorf("inventory.backend %q: must be file", cfg.Inventory.Backend)
	}
}

func buildNotifier(cfg *config.Config, forceDryRun bool) (notify.Notifier, error) {
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
	default:
		return nil, fmt.Errorf("notifier.backend %q: must be dryrun|smtp", backend)
	}
}

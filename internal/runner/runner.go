// Package runner is the shared pipeline: discovery → RDAP → TLS → alert →
// state filter → notify → state save. Both cmd/monitor (CLI) and cmd/lambda
// (Lambda handler) import this so the business logic lives in one place.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"auto-certs/internal/alert"
	"auto-certs/internal/config"
	"auto-certs/internal/discovery"
	"auto-certs/internal/healthcheck"
	"auto-certs/internal/inventory"
	"auto-certs/internal/model"
	"auto-certs/internal/notify"
	"auto-certs/internal/rdap"
	"auto-certs/internal/state"
	"auto-certs/internal/tlscheck"
)

// Deps bundles the pluggable dependencies. main() builds these from config
// (file/S3 storage, SMTP/SES/DryRun notifier, optional RDAP cache) and hands
// them to Run.
type Deps struct {
	Config    *config.Config
	State     state.Store
	Inventory inventory.Sink
	Notifier  notify.Notifier
	RDAPCache rdap.Cache
	// DryRunSkipsStateSave: when true, the run won't persist state — useful
	// for `-dry-run` so the next run still sees the same alerts.
	DryRunSkipsStateSave bool
}

// Run executes the full pipeline. Returns a non-nil error only on fatal
// failures (config-shape problems, all-storage-broken); per-domain errors
// are logged and aggregated into the inventory but don't abort the run.
// A healthcheck Success ping happens on clean exit; Failure on hard error.
func Run(ctx context.Context, deps Deps) (err error) {
	cfg := deps.Config
	pinger := healthcheck.New(cfg.Healthcheck.URL, cfg.Healthcheck.Timeout)
	defer func() {
		if err != nil {
			pinger.Failure(ctx, cfg.Healthcheck.FailureURL)
		} else {
			pinger.Success(ctx)
		}
	}()

	discoverers := buildDiscoverers(cfg)
	if len(discoverers) == 0 {
		slog.Warn("no discoverers enabled; nothing to do")
		return nil
	}

	result := discovery.Aggregate(ctx, cfg.TLS.DefaultPort, discoverers...)
	slog.Info("discovery complete",
		"total_domains", len(result.Domains),
		"discoverer_errors", len(result.Errors),
	)

	var looker rdap.Looker = rdap.NewClient()
	if deps.RDAPCache != nil {
		looker = rdap.NewCachedLooker(looker, deps.RDAPCache)
	}
	infos := rdap.Enrich(ctx, looker, result.Domains, rdap.Options{
		Workers:               cfg.RDAP.EnrichWorkers,
		MismatchToleranceDays: cfg.RDAP.MismatchToleranceDays,
	})
	logEnrichment(infos)
	// Persist any cache mutations now — even if a later step fails, fresh
	// lookups for this run shouldn't be lost.
	if deps.RDAPCache != nil {
		if err := deps.RDAPCache.Flush(ctx); err != nil {
			slog.Warn("rdap cache flush failed", "err", err)
		}
	}

	checker := tlscheck.New(cfg.TLS.Timeout, cfg.TLS.Workers)
	certs := checker.CheckAll(ctx, result.Domains)
	logCerts(certs)

	now := time.Now().UTC()
	alerts := alert.Evaluate(now, result.Domains, certs, infos, cfg.Thresholds)
	summary := notify.BuildSummary(now, result.Domains, certs, alerts)
	slog.Info("alerts evaluated", "total", len(alerts))
	slog.Info("inventory summary",
		"total", summary.Total,
		"apex", summary.Apex,
		"subdomain", summary.Subdomain,
		"healthy", summary.Healthy,
		"alerted", summary.Alerted,
		"no_cert", summary.NoCertData,
	)

	snap := inventory.Build(now, result.Domains, infos, certs, alerts)
	if err := deps.Inventory.Save(snap); err != nil {
		slog.Error("inventory save failed", "err", err)
		// continue: an inventory failure shouldn't block alert delivery
	} else {
		slog.Info("inventory saved", "entries", snap.Total)
	}

	fresh, err := deps.State.Filter(alerts)
	if err != nil {
		return fmt.Errorf("state filter: %w", err)
	}
	slog.Info("alerts after dedup", "fresh", len(fresh), "suppressed", len(alerts)-len(fresh))

	if len(fresh) == 0 {
		slog.Info("no fresh alerts; nothing to send")
		return nil
	}

	if err := deps.Notifier.Notify(fresh, summary); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	slog.Info("alerts delivered", "count", len(fresh))

	if !deps.DryRunSkipsStateSave {
		if err := deps.State.Save(fresh); err != nil {
			return fmt.Errorf("state save: %w", err)
		}
	}
	return nil
}

func buildDiscoverers(cfg *config.Config) []discovery.Discoverer {
	var ds []discovery.Discoverer
	if cfg.Discovery.Static.Enabled {
		ds = append(ds, discovery.NewStatic(cfg.Discovery.Static.Path))
	}
	if cfg.Discovery.Route53.Enabled {
		ds = append(ds, discovery.NewRoute53(
			cfg.Discovery.Route53.Region,
			cfg.Discovery.Route53.Profile,
			cfg.Discovery.Route53.ExcludeZones,
		))
	}
	if cfg.Discovery.NameCom.Enabled {
		ds = append(ds, discovery.NewNameCom(
			cfg.Discovery.NameCom.Username,
			cfg.Discovery.NameCom.Token,
			cfg.Discovery.NameCom.BaseURL,
		))
	}
	return ds
}

func logEnrichment(infos []model.DomainInfo) {
	for _, info := range infos {
		if info.Err != nil {
			slog.Error("domain expiry lookup failed",
				"domain", info.Domain,
				"err", info.Err,
			)
			continue
		}
		slog.Debug("domain expiry",
			"domain", info.Domain,
			"source", info.Source,
			"expires_at", info.ExpiresAt.Format(time.RFC3339),
		)
	}
}

func logCerts(certs []model.CertInfo) {
	for _, ci := range certs {
		if ci.Err != nil {
			slog.Error("tls check failed",
				"domain", ci.Domain,
				"port", ci.Port,
				"err", ci.Err,
			)
			continue
		}
		slog.Debug("tls cert",
			"domain", ci.Domain,
			"port", ci.Port,
			"not_after", ci.NotAfter.Format(time.RFC3339),
		)
	}
}

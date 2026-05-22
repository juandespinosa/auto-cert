// Package healthcheck pings a deadman-switch URL at the end of every
// successful run. The remote service (healthchecks.io, cronitor.io, a
// self-hosted endpoint) raises an alert if the ping doesn't arrive on
// schedule — that catches "el cron dejó de disparar" o "el server se
// reinició y nadie reactivó el cron" cases donde el monitor se queda mudo.
//
// The package is intentionally tiny: one GET (or POST for /fail). No retries,
// no exotic options. If the ping itself fails we log a warning but don't
// abort the caller — a missed ping is not worth crashing the whole run.
package healthcheck

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

const defaultTimeout = 5 * time.Second

// Pinger sends success/failure signals. Methods are no-ops when URL is empty,
// so callers can wire it unconditionally and let config decide.
type Pinger struct {
	URL     string
	Timeout time.Duration
	client  *http.Client
}

// New returns a Pinger. An empty URL disables all pings (useful for local
// dev where the deadman is not configured).
func New(url string, timeout time.Duration) *Pinger {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Pinger{
		URL:     url,
		Timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

// Success pings the configured URL. Healthchecks.io / cronitor.io expect a
// plain GET; the request is best-effort and never returns an error to the
// caller — a missed ping is a soft failure that the deadman will surface
// elsewhere on its own (by NOT alerting too soon).
func (p *Pinger) Success(ctx context.Context) {
	p.ping(ctx, p.URL, "success")
}

// Failure pings the failure variant. For healthchecks.io that's the same URL
// with "/fail" appended; for other services it may differ — callers can pass
// a fully-formed FailureURL via config if needed. If FailureURL is empty we
// derive it from URL.
func (p *Pinger) Failure(ctx context.Context, failureURL string) {
	if failureURL == "" && p.URL != "" {
		failureURL = p.URL + "/fail"
	}
	p.ping(ctx, failureURL, "failure")
}

func (p *Pinger) ping(ctx context.Context, url, kind string) {
	if url == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn("healthcheck request build failed", "kind", kind, "err", err)
		return
	}
	resp, err := p.client.Do(req)
	if err != nil {
		slog.Warn("healthcheck ping failed", "kind", kind, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		slog.Warn("healthcheck ping non-2xx", "kind", kind, "status", resp.StatusCode)
		return
	}
	slog.Info("healthcheck ping ok", "kind", kind, "status", resp.StatusCode)
}

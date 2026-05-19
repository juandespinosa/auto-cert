// Package rdap looks up domain registry expiration via the IANA RDAP
// bootstrap registry and per-TLD RDAP endpoints. Only the "expiration" event
// is consumed; everything else in the RDAP response is ignored.
package rdap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"auto-certs/internal/model"
)

const (
	bootstrapURL    = "https://data.iana.org/rdap/dns.json"
	rdapAccept      = "application/rdap+json"
	defaultTimeout  = 15 * time.Second
	expirationEvent = "expiration"

	// Defensive limits on HTTP response sizes. The IANA bootstrap is ~80KB;
	// per-domain RDAP responses are well under 100KB. Anything larger is
	// either a misconfigured endpoint or a tarpit — fail fast instead of
	// reading megabytes into memory.
	maxBootstrapBytes = 5 << 20  // 5MB
	maxRDAPBodyBytes  = 1 << 20  // 1MB
)

// Client resolves TLD → RDAP endpoint and fetches per-domain expiration.
// Safe for concurrent use.
type Client struct {
	http *http.Client

	bootstrapOnce sync.Once
	bootstrapErr  error
	tldToEndpoint map[string]string // TLD (lowercase, no trailing dot) → endpoint URL with trailing slash
}

// NewClient returns a Client with a default-configured http.Client.
func NewClient() *Client {
	return &Client{
		http: &http.Client{Timeout: defaultTimeout},
	}
}

// bootstrapResponse mirrors the shape of https://data.iana.org/rdap/dns.json.
// services is an array of 2-element tuples: [tlds[], endpoints[]].
type bootstrapResponse struct {
	Services [][][]string `json:"services"`
}

func (c *Client) loadBootstrap(ctx context.Context) error {
	c.bootstrapOnce.Do(func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, bootstrapURL, nil)
		if err != nil {
			c.bootstrapErr = fmt.Errorf("rdap bootstrap request: %w", err)
			return
		}
		resp, err := c.http.Do(req)
		if err != nil {
			c.bootstrapErr = fmt.Errorf("rdap bootstrap fetch: %w", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			c.bootstrapErr = fmt.Errorf("rdap bootstrap status %d", resp.StatusCode)
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBootstrapBytes))
		if err != nil {
			c.bootstrapErr = fmt.Errorf("rdap bootstrap read: %w", err)
			return
		}
		var br bootstrapResponse
		if err := json.Unmarshal(body, &br); err != nil {
			c.bootstrapErr = fmt.Errorf("rdap bootstrap parse: %w", err)
			return
		}
		m := make(map[string]string, 1500)
		for _, svc := range br.Services {
			if len(svc) < 2 {
				continue
			}
			tlds, endpoints := svc[0], svc[1]
			if len(endpoints) == 0 {
				continue
			}
			// Prefer https endpoint when present.
			endpoint := endpoints[0]
			for _, e := range endpoints {
				if strings.HasPrefix(e, "https://") {
					endpoint = e
					break
				}
			}
			if !strings.HasSuffix(endpoint, "/") {
				endpoint += "/"
			}
			for _, tld := range tlds {
				m[strings.ToLower(strings.TrimSuffix(tld, "."))] = endpoint
			}
		}
		c.tldToEndpoint = m
	})
	return c.bootstrapErr
}

// endpointFor resolves the longest-matching TLD suffix to its RDAP endpoint.
// For "algo.com.co" it tries "com.co" first, then "co".
func (c *Client) endpointFor(domain string) (string, error) {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if d == "" {
		return "", errors.New("empty domain")
	}
	labels := strings.Split(d, ".")
	for i := 0; i < len(labels); i++ {
		suffix := strings.Join(labels[i:], ".")
		if ep, ok := c.tldToEndpoint[suffix]; ok {
			return ep, nil
		}
	}
	return "", fmt.Errorf("no RDAP endpoint for %q", domain)
}

// rdapDomainResponse is the subset of an RDAP domain object we read.
type rdapDomainResponse struct {
	Events []struct {
		EventAction string `json:"eventAction"`
		EventDate   string `json:"eventDate"`
	} `json:"events"`
}

// Lookup returns the registry expiration for domain via RDAP. The returned
// DomainInfo always has Domain and CheckedAt set; on failure Err is populated
// and ExpiresAt is zero.
func (c *Client) Lookup(ctx context.Context, domain string) *model.DomainInfo {
	info := &model.DomainInfo{
		Domain:    domain,
		CheckedAt: time.Now().UTC(),
	}
	if err := c.loadBootstrap(ctx); err != nil {
		info.Err = err
		return info
	}
	endpoint, err := c.endpointFor(domain)
	if err != nil {
		info.Err = err
		return info
	}
	url := endpoint + "domain/" + strings.ToLower(strings.TrimSpace(domain))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		info.Err = fmt.Errorf("rdap request: %w", err)
		return info
	}
	req.Header.Set("Accept", rdapAccept)
	resp, err := c.http.Do(req)
	if err != nil {
		info.Err = fmt.Errorf("rdap fetch %s: %w", url, err)
		return info
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		info.Err = fmt.Errorf("rdap %s: status %d", url, resp.StatusCode)
		return info
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRDAPBodyBytes))
	if err != nil {
		info.Err = fmt.Errorf("rdap read %s: %w", url, err)
		return info
	}
	var dr rdapDomainResponse
	if err := json.Unmarshal(body, &dr); err != nil {
		info.Err = fmt.Errorf("rdap parse %s: %w", url, err)
		return info
	}
	for _, ev := range dr.Events {
		if strings.EqualFold(ev.EventAction, expirationEvent) {
			t, err := time.Parse(time.RFC3339, ev.EventDate)
			if err != nil {
				info.Err = fmt.Errorf("rdap expiration date %q: %w", ev.EventDate, err)
				return info
			}
			info.ExpiresAt = t
			info.Source = "rdap"
			return info
		}
	}
	info.Err = fmt.Errorf("rdap %s: no expiration event in response", url)
	return info
}

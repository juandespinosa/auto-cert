package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"auto-certs/internal/model"
)

const (
	namecomDefaultBaseURL = "https://api.dev.name.com"
	namecomTimeout        = 20 * time.Second
	// Defensive cap on response bodies. Name.com pages are typically <100KB;
	// 5MB is two orders of magnitude beyond what we expect to see.
	namecomMaxBodyBytes = 5 << 20
)

// NameCom discovers registered domains (and their DNS records, when name.com
// hosts the DNS) via the v1 Core API. Auth is HTTP Basic with username+token;
// dev sandbox lives at api.dev.name.com with a separate token.
type NameCom struct {
	Username string
	Token    string
	BaseURL  string
	http     *http.Client
}

func NewNameCom(username, token, baseURL string) *NameCom {
	if baseURL == "" {
		baseURL = namecomDefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &NameCom{
		Username: username,
		Token:    token,
		BaseURL:  baseURL,
		http:     &http.Client{Timeout: namecomTimeout},
	}
}

func (n *NameCom) Name() string { return "namecom" }

type namecomDomainsResponse struct {
	Domains  []namecomDomain `json:"domains"`
	NextPage int             `json:"nextPage"`
}

type namecomDomain struct {
	DomainName string `json:"domainName"`
}

type namecomRecordsResponse struct {
	Records  []namecomRecord `json:"records"`
	NextPage int             `json:"nextPage"`
}

type namecomRecord struct {
	Type string `json:"type"`
	Host string `json:"host"`
	Fqdn string `json:"fqdn"`
}

func (n *NameCom) Discover(ctx context.Context) ([]model.Domain, error) {
	if n.Username == "" || n.Token == "" {
		return nil, errors.New("namecom: missing username or token")
	}

	seen := map[string]struct{}{}
	var out []model.Domain
	add := func(raw string) {
		h := normalizeHost(raw)
		if h == "" || !isMonitorableHost(h) {
			return
		}
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		out = append(out, model.Domain{Name: h, Source: n.Name()})
	}

	domains, err := n.listDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("namecom list domains: %w", err)
	}
	for _, d := range domains {
		add(d)
		// Record enumeration is best-effort: a failure on one zone (e.g., DNS
		// hosted elsewhere) shouldn't kill the rest.
		records, err := n.listRecords(ctx, d)
		if err != nil {
			slog.Warn("namecom list records failed", "domain", d, "err", err)
			continue
		}
		for _, r := range records {
			add(r)
		}
	}
	return out, nil
}

func (n *NameCom) listDomains(ctx context.Context) ([]string, error) {
	var all []string
	page := 1
	for {
		endpoint := fmt.Sprintf("%s/core/v1/domains?page=%d", n.BaseURL, page)
		var resp namecomDomainsResponse
		rawSize, err := n.getJSONWithSize(ctx, endpoint, &resp)
		if err != nil {
			return nil, err
		}
		slog.Info("namecom listDomains page",
			"base_url", n.BaseURL,
			"page", page,
			"response_bytes", rawSize,
			"domains_in_page", len(resp.Domains),
			"next_page", resp.NextPage,
		)
		for _, d := range resp.Domains {
			if d.DomainName != "" {
				all = append(all, d.DomainName)
			}
		}
		if resp.NextPage == 0 || resp.NextPage <= page {
			break
		}
		page = resp.NextPage
	}
	return all, nil
}

func (n *NameCom) listRecords(ctx context.Context, domain string) ([]string, error) {
	var hosts []string
	page := 1
	for {
		endpoint := fmt.Sprintf("%s/core/v1/domains/%s/records?page=%d",
			n.BaseURL, url.PathEscape(domain), page)
		var resp namecomRecordsResponse
		if err := n.getJSON(ctx, endpoint, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Records {
			switch strings.ToUpper(r.Type) {
			case "A", "AAAA", "CNAME":
			default:
				continue
			}
			name := r.Fqdn
			if name == "" {
				if r.Host == "" || r.Host == "@" {
					name = domain
				} else {
					name = r.Host + "." + domain
				}
			}
			hosts = append(hosts, name)
		}
		if resp.NextPage == 0 || resp.NextPage <= page {
			break
		}
		page = resp.NextPage
	}
	return hosts, nil
}

func (n *NameCom) getJSON(ctx context.Context, endpoint string, out any) error {
	_, err := n.getJSONWithSize(ctx, endpoint, out)
	return err
}

// getJSONWithSize is getJSON but also reports the raw response body length —
// useful when the API returns 200 with an unexpected shape and the parsed
// struct ends up empty.
func (n *NameCom) getJSONWithSize(ctx context.Context, endpoint string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(n.Username, n.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, namecomMaxBodyBytes))
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		return len(body), fmt.Errorf("status %d on %s: %s", resp.StatusCode, endpoint, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return len(body), fmt.Errorf("parse %s: %w", endpoint, err)
	}
	return len(body), nil
}

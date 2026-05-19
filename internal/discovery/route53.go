package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/route53domains"

	"auto-certs/internal/model"
)

// Route53 discovers TLS-monitorable hostnames from two AWS APIs:
//   - route53domains.ListDomains: domains registered through AWS.
//   - route53.ListHostedZones + ListResourceRecordSets: A/AAAA/CNAME records in
//     each PUBLIC hosted zone (strategy "A + B" per project_overview).
//
// Private zones are skipped by default — they typically hold internal-only
// hostnames that won't resolve from outside and would just flood the error log.
// ExcludeZones lets the user opt out of specific zones by suffix match (e.g.,
// "dev.bodytech.co" skips that zone AND any record under it found in a parent).
type Route53 struct {
	Region       string
	Profile      string
	ExcludeZones []string
}

func NewRoute53(region, profile string, excludeZones []string) *Route53 {
	return &Route53{
		Region:       region,
		Profile:      profile,
		ExcludeZones: excludeZones,
	}
}

// isExcluded reports whether host is or is a subdomain of any ExcludeZones
// entry. Comparison is case-insensitive and dot-suffix anchored: an exclude
// of "dev.bodytech.co" matches "dev.bodytech.co" and "foo.dev.bodytech.co",
// but NOT "notdev.bodytech.co".
func (r *Route53) isExcluded(host string) bool {
	if len(r.ExcludeZones) == 0 {
		return false
	}
	h := normalizeHost(host)
	for _, ex := range r.ExcludeZones {
		e := normalizeHost(ex)
		if e == "" {
			continue
		}
		if h == e || strings.HasSuffix(h, "."+e) {
			return true
		}
	}
	return false
}

func (r *Route53) Name() string { return "route53" }

func (r *Route53) Discover(ctx context.Context) ([]model.Domain, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if r.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(r.Region))
	}
	if r.Profile != "" {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(r.Profile))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}

	seen := map[string]struct{}{}
	var out []model.Domain
	add := func(raw string) {
		n := normalizeHost(raw)
		if n == "" {
			return
		}
		if r.isExcluded(n) {
			return
		}
		if _, ok := seen[n]; ok {
			return
		}
		seen[n] = struct{}{}
		out = append(out, model.Domain{Name: n, Source: r.Name()})
	}

	domErr := r.collectRegisteredDomains(ctx, awsCfg, add)
	zoneErr := r.collectHostedZones(ctx, awsCfg, add)

	if domErr != nil && zoneErr != nil && len(out) == 0 {
		return nil, fmt.Errorf("route53 both APIs failed: ListDomains=%v; ListHostedZones=%v", domErr, zoneErr)
	}
	return out, nil
}

func (r *Route53) collectRegisteredDomains(ctx context.Context, cfg aws.Config, add func(string)) error {
	client := route53domains.NewFromConfig(cfg)
	p := route53domains.NewListDomainsPaginator(client, &route53domains.ListDomainsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			slog.Warn("route53 ListDomains failed", "err", err)
			return err
		}
		for _, d := range page.Domains {
			if d.DomainName == nil {
				continue
			}
			add(*d.DomainName)
		}
	}
	return nil
}

func (r *Route53) collectHostedZones(ctx context.Context, cfg aws.Config, add func(string)) error {
	client := route53.NewFromConfig(cfg)
	zp := route53.NewListHostedZonesPaginator(client, &route53.ListHostedZonesInput{})
	var firstErr error
	for zp.HasMorePages() {
		page, err := zp.NextPage(ctx)
		if err != nil {
			slog.Warn("route53 ListHostedZones failed", "err", err)
			return err
		}
		for _, z := range page.HostedZones {
			if z.Config != nil && z.Config.PrivateZone {
				continue
			}
			zoneName := ""
			if z.Name != nil {
				zoneName = *z.Name
			}
			if r.isExcluded(zoneName) {
				slog.Info("route53 zone excluded", "zone", normalizeHost(zoneName))
				continue
			}
			add(zoneName)

			if z.Id == nil {
				continue
			}
			if err := r.enumerateRecords(ctx, client, *z.Id, zoneName, add); err != nil {
				slog.Warn("route53 ListResourceRecordSets failed", "zone", zoneName, "err", err)
				if firstErr == nil {
					firstErr = err
				}
				// keep going with other zones
			}
		}
	}
	return firstErr
}

func (r *Route53) enumerateRecords(ctx context.Context, client *route53.Client, zoneID, zoneName string, add func(string)) error {
	rp := route53.NewListResourceRecordSetsPaginator(client, &route53.ListResourceRecordSetsInput{
		HostedZoneId: &zoneID,
	})
	for rp.HasMorePages() {
		page, err := rp.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, rec := range page.ResourceRecordSets {
			switch rec.Type {
			case r53types.RRTypeA, r53types.RRTypeAaaa, r53types.RRTypeCname:
			default:
				continue
			}
			if rec.Name == nil {
				continue
			}
			name := *rec.Name
			if !isMonitorableHost(name) {
				continue
			}
			add(name)
		}
	}
	return nil
}

// normalizeHost lowercases, trims whitespace, and strips the trailing dot
// Route53 returns on FQDN values ("example.com." → "example.com").
func normalizeHost(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		return ""
	}
	return strings.ToLower(s)
}

// isMonitorableHost rejects DNS records that aren't real TLS-serving hosts:
//   - wildcards (we don't know which concrete subdomain to check)
//   - underscore-prefixed labels (DKIM, _acme-challenge, ACM validation,
//     _dmarc, SPF helpers — all metadata, never a TLS host)
//   - names with backslash escapes (Route53 returns "\NNN" for unusual chars;
//     these almost always come from underscore/space-prefixed metadata too)
func isMonitorableHost(name string) bool {
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, "*.") || strings.HasPrefix(name, `\052.`) {
		return false
	}
	if strings.Contains(name, `\`) {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if strings.HasPrefix(label, "_") {
			return false
		}
	}
	return true
}


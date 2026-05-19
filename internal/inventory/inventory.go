// Package inventory writes a snapshot of every monitored domain plus its
// cert + registry data + computed alerts. Storage is pluggable (FileSink for
// local dev, S3Sink for Lambda) so the snapshot can be archived for
// historical comparison or consumed by downstream scripts.
package inventory

import (
	"sort"
	"time"

	"auto-certs/internal/model"
)

type Snapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	Total       int       `json:"total"`
	Entries     []Entry   `json:"entries"`
}

type Entry struct {
	Name           string         `json:"name"`
	Port           int            `json:"port"`
	Source         string         `json:"source"`
	ExpiryFallback *time.Time     `json:"expiry_fallback,omitempty"`
	Cert           *CertSection   `json:"cert,omitempty"`
	Domain         *DomainSection `json:"domain,omitempty"`
	Alerts         []AlertEntry   `json:"alerts,omitempty"`
}

type CertSection struct {
	NotAfter  time.Time `json:"not_after,omitempty"`
	Issuer    string    `json:"issuer,omitempty"`
	Subject   string    `json:"subject,omitempty"`
	DNSNames  []string  `json:"dns_names,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

type DomainSection struct {
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Source    string    `json:"source,omitempty"` // "rdap" or "fallback"
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

type AlertEntry struct {
	Kind      model.AlertKind `json:"kind"`
	Threshold int             `json:"threshold"`
	DaysLeft  int             `json:"days_left"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// Sink persists a Snapshot. Implementations choose where (file, S3, ...).
type Sink interface {
	Save(s Snapshot) error
}

// Build assembles the snapshot. Cert and DomainInfo are matched to Domain by
// name (case-insensitive). Alerts are bucketed per domain.
func Build(now time.Time, domains []model.Domain, infos []model.DomainInfo, certs []model.CertInfo, alerts []model.Alert) Snapshot {
	infoByName := map[string]model.DomainInfo{}
	for _, i := range infos {
		infoByName[i.Domain] = i
	}
	certByName := map[string]model.CertInfo{}
	for _, c := range certs {
		certByName[c.Domain] = c
	}
	alertsByDomain := map[string][]model.Alert{}
	for _, a := range alerts {
		alertsByDomain[a.Domain] = append(alertsByDomain[a.Domain], a)
	}

	entries := make([]Entry, 0, len(domains))
	for _, d := range domains {
		e := Entry{
			Name:           d.Name,
			Port:           d.Port,
			Source:         d.Source,
			ExpiryFallback: d.ExpiryFallback,
		}
		if c, ok := certByName[d.Name]; ok {
			e.Cert = &CertSection{
				NotAfter:  c.NotAfter,
				Issuer:    c.Issuer,
				Subject:   c.Subject,
				DNSNames:  c.DNSNames,
				CheckedAt: c.CheckedAt,
				Error:     errStr(c.Err),
			}
		}
		if i, ok := infoByName[d.Name]; ok {
			e.Domain = &DomainSection{
				ExpiresAt: i.ExpiresAt,
				Source:    i.Source,
				CheckedAt: i.CheckedAt,
				Error:     errStr(i.Err),
			}
		}
		for _, a := range alertsByDomain[d.Name] {
			e.Alerts = append(e.Alerts, AlertEntry{
				Kind:      a.Kind,
				Threshold: a.Threshold,
				DaysLeft:  a.DaysLeft,
				ExpiresAt: a.ExpiresAt,
			})
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return Snapshot{
		GeneratedAt: now.UTC(),
		Total:       len(entries),
		Entries:     entries,
	}
}

func errStr(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

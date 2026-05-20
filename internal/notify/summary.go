package notify

import (
	"sort"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"auto-certs/internal/model"
)

// Summary aggregates inventory-wide counts for the email header. Each entry
// (FQDN) lands in exactly one health bucket: healthy / alerted / nocert.
// Apex vs subdomain is decided via the public suffix list so .com.co and
// .co.uk are handled correctly.
type Summary struct {
	GeneratedAt time.Time

	Total     int
	Apex      int
	Subdomain int

	Healthy    int // no error AND no alert
	Alerted    int // >= 1 alert
	NoCertData int // no alert AND TLS check failed (we lost coverage)

	BySource []SourceStat
}

// SourceStat is the per-discoverer slice of Summary.
type SourceStat struct {
	Source     string
	Total      int
	Apex       int
	Subdomain  int
	Healthy    int
	Alerted    int
	NoCertData int
}

// BuildSummary categorizes every Domain into a health bucket and a topology
// bucket (apex vs subdomain). alerts is the full alert list (pre-dedup),
// because the inventory view should reflect "anything in alert", regardless of
// whether the state store filtered it out for this email.
func BuildSummary(now time.Time, domains []model.Domain, certs []model.CertInfo, alerts []model.Alert) Summary {
	alertedSet := make(map[string]bool, len(alerts))
	for _, a := range alerts {
		alertedSet[strings.ToLower(a.Domain)] = true
	}
	certByName := make(map[string]model.CertInfo, len(certs))
	for _, c := range certs {
		certByName[strings.ToLower(c.Domain)] = c
	}

	s := Summary{GeneratedAt: now, Total: len(domains)}
	bySource := make(map[string]*SourceStat)

	for _, d := range domains {
		name := strings.ToLower(d.Name)
		apex := isApex(name)
		bucket := classify(name, certByName, alertedSet)

		if apex {
			s.Apex++
		} else {
			s.Subdomain++
		}
		switch bucket {
		case "healthy":
			s.Healthy++
		case "alerted":
			s.Alerted++
		case "nocert":
			s.NoCertData++
		}

		ss, ok := bySource[d.Source]
		if !ok {
			ss = &SourceStat{Source: d.Source}
			bySource[d.Source] = ss
		}
		ss.Total++
		if apex {
			ss.Apex++
		} else {
			ss.Subdomain++
		}
		switch bucket {
		case "healthy":
			ss.Healthy++
		case "alerted":
			ss.Alerted++
		case "nocert":
			ss.NoCertData++
		}
	}

	sources := make([]SourceStat, 0, len(bySource))
	for _, ss := range bySource {
		sources = append(sources, *ss)
	}
	// Total desc, alfabético como desempate: el lector escanea primero los
	// orígenes más grandes (los que más cobertura aportan).
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Total != sources[j].Total {
			return sources[i].Total > sources[j].Total
		}
		return sources[i].Source < sources[j].Source
	})
	s.BySource = sources
	return s
}

func classify(name string, certs map[string]model.CertInfo, alerted map[string]bool) string {
	if alerted[name] {
		return "alerted"
	}
	c, ok := certs[name]
	if !ok || c.Err != nil {
		return "nocert"
	}
	return "healthy"
}

// isApex returns true when name equals its eTLD+1 (registrable domain).
// Anything else — including unknown TLDs that publicsuffix can't classify —
// is treated as a subdomain conservatively.
func isApex(name string) bool {
	etld1, err := publicsuffix.EffectiveTLDPlusOne(name)
	if err != nil {
		return false
	}
	return strings.EqualFold(etld1, name)
}

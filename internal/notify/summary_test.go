package notify

import (
	"errors"
	"testing"
	"time"

	"auto-certs/internal/model"
)

func tDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestIsApex(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{"plain apex .com", "foo.com", true},
		{"plain apex .org", "example.org", true},
		{"www subdomain", "www.foo.com", false},
		{"deeper subdomain", "api.dev.foo.com", false},
		{"apex .com.co (managed TLD)", "bodytech.com.co", true},
		{"sub of .com.co", "www.bodytech.com.co", false},
		{"apex .co.uk", "example.co.uk", true},
		{"sub of .co.uk", "api.example.co.uk", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isApex(tt.host); got != tt.want {
				t.Errorf("isApex(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestBuildSummary_Empty(t *testing.T) {
	s := BuildSummary(tDate("2026-05-19"), nil, nil, nil)
	if s.Total != 0 || s.Healthy != 0 || s.Alerted != 0 || s.NoCertData != 0 {
		t.Errorf("empty inputs should yield all zeros, got %+v", s)
	}
}

func TestBuildSummary_BucketsExhaustiveAndSumToTotal(t *testing.T) {
	now := tDate("2026-05-19")
	domains := []model.Domain{
		{Name: "healthy.com", Source: "static"},
		{Name: "alerted.com", Source: "static"},
		{Name: "nocert.com", Source: "static"},
	}
	certs := []model.CertInfo{
		{Domain: "healthy.com", NotAfter: tDate("2027-12-01")},                  // OK
		{Domain: "alerted.com", NotAfter: tDate("2026-05-25")},                  // soon → alert
		{Domain: "nocert.com", Err: errors.New("dial timeout")},                 // errored
	}
	alerts := []model.Alert{
		{Domain: "alerted.com", Kind: model.AlertCertExpiring, Threshold: 30},
	}

	s := BuildSummary(now, domains, certs, alerts)

	if s.Total != 3 {
		t.Errorf("Total = %d, want 3", s.Total)
	}
	if s.Healthy != 1 {
		t.Errorf("Healthy = %d, want 1", s.Healthy)
	}
	if s.Alerted != 1 {
		t.Errorf("Alerted = %d, want 1", s.Alerted)
	}
	if s.NoCertData != 1 {
		t.Errorf("NoCertData = %d, want 1", s.NoCertData)
	}
	if s.Healthy+s.Alerted+s.NoCertData != s.Total {
		t.Errorf("buckets must sum to Total: %d + %d + %d != %d",
			s.Healthy, s.Alerted, s.NoCertData, s.Total)
	}
}

func TestBuildSummary_ApexSubdomainSumToTotal(t *testing.T) {
	now := tDate("2026-05-19")
	domains := []model.Domain{
		{Name: "foo.com", Source: "static"},     // apex
		{Name: "www.foo.com", Source: "static"}, // sub
		{Name: "api.foo.com", Source: "static"}, // sub
	}
	s := BuildSummary(now, domains, nil, nil)
	if s.Apex+s.Subdomain != s.Total {
		t.Errorf("apex(%d) + sub(%d) != total(%d)", s.Apex, s.Subdomain, s.Total)
	}
	if s.Apex != 1 || s.Subdomain != 2 {
		t.Errorf("Apex/Sub = %d/%d, want 1/2", s.Apex, s.Subdomain)
	}
}

func TestBuildSummary_PerSourceAggregates(t *testing.T) {
	now := tDate("2026-05-19")
	domains := []model.Domain{
		{Name: "a.com", Source: "mi.com.co"},
		{Name: "b.com", Source: "mi.com.co"},
		{Name: "c.com", Source: "marcaria.com"},
		{Name: "d.com", Source: "route53"},
	}
	certs := []model.CertInfo{
		{Domain: "a.com", NotAfter: tDate("2027-12-01")},
		{Domain: "b.com", Err: errors.New("timeout")},
		{Domain: "c.com", NotAfter: tDate("2026-05-25")},
		{Domain: "d.com", NotAfter: tDate("2027-12-01")},
	}
	alerts := []model.Alert{
		{Domain: "c.com", Kind: model.AlertCertExpiring, Threshold: 30},
	}

	s := BuildSummary(now, domains, certs, alerts)
	if len(s.BySource) != 3 {
		t.Fatalf("expected 3 sources in BySource, got %d", len(s.BySource))
	}

	bySource := map[string]SourceStat{}
	for _, ss := range s.BySource {
		bySource[ss.Source] = ss
	}

	if mi := bySource["mi.com.co"]; mi.Total != 2 || mi.Healthy != 1 || mi.NoCertData != 1 {
		t.Errorf("mi.com.co stats wrong: %+v", mi)
	}
	if mar := bySource["marcaria.com"]; mar.Total != 1 || mar.Alerted != 1 {
		t.Errorf("marcaria.com stats wrong: %+v", mar)
	}
	if r53 := bySource["route53"]; r53.Total != 1 || r53.Healthy != 1 {
		t.Errorf("route53 stats wrong: %+v", r53)
	}
}

func TestBuildSummary_AlertedTakesPriorityOverErrored(t *testing.T) {
	// If a domain has BOTH a cert error AND an alert (e.g. from RDAP fallback),
	// it counts as Alerted, not NoCertData.
	now := tDate("2026-05-19")
	domains := []model.Domain{{Name: "foo.com", Source: "static"}}
	certs := []model.CertInfo{{Domain: "foo.com", Err: errors.New("timeout")}}
	alerts := []model.Alert{
		{Domain: "foo.com", Kind: model.AlertDomainExpiring, Threshold: 30},
	}

	s := BuildSummary(now, domains, certs, alerts)
	if s.Alerted != 1 || s.NoCertData != 0 {
		t.Errorf("Alerted(%d)/NoCertData(%d) — alerts must win over cert errors",
			s.Alerted, s.NoCertData)
	}
}

func TestBuildSummary_BySourceSorted(t *testing.T) {
	now := tDate("2026-05-19")
	domains := []model.Domain{
		{Name: "z.com", Source: "zzz"},
		{Name: "a.com", Source: "aaa"},
		{Name: "m.com", Source: "mmm"},
	}
	s := BuildSummary(now, domains, nil, nil)
	want := []string{"aaa", "mmm", "zzz"}
	for i, ss := range s.BySource {
		if ss.Source != want[i] {
			t.Errorf("BySource[%d] = %q, want %q (must be alphabetical)",
				i, ss.Source, want[i])
		}
	}
}

package rdap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"auto-certs/internal/model"
)

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// mockLooker records every Lookup call and returns scripted responses.
type mockLooker struct {
	mu        sync.Mutex
	calls     []string
	responses map[string]*model.DomainInfo
}

func (m *mockLooker) Lookup(_ context.Context, domain string) *model.DomainInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, domain)
	if r, ok := m.responses[domain]; ok {
		// return a copy so callers can mutate Domain without affecting subsequent reads
		cp := *r
		return &cp
	}
	return &model.DomainInfo{
		Domain: domain,
		Err:    errors.New("no mock for " + domain),
	}
}

func (m *mockLooker) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func TestRegistrableDomain(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"foo.com", "foo.com"},
		{"www.foo.com", "foo.com"},
		{"api.dev.foo.com", "foo.com"},
		{"bodytech.com.co", "bodytech.com.co"},
		{"www.bodytech.com.co", "bodytech.com.co"},
		{"FOO.COM", "foo.com"},
		{"foo.com.", "foo.com"},  // trailing dot
		{"", ""},
	}
	for _, tt := range tests {
		if got := registrableDomain(tt.in); got != tt.want {
			t.Errorf("registrableDomain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestGroupByApex_BasicGrouping(t *testing.T) {
	domains := []model.Domain{
		{Name: "foo.com"},
		{Name: "www.foo.com"},
		{Name: "api.foo.com"},
		{Name: "bar.com"},
	}
	groups := groupByApex(domains)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups (foo.com, bar.com), got %d", len(groups))
	}
	// foo.com group: 3 members, apexMemberIdx=0
	var foo apexGroup
	for _, g := range groups {
		if g.apex == "foo.com" {
			foo = g
		}
	}
	if len(foo.members) != 3 || foo.apexMemberIdx != 0 {
		t.Errorf("foo.com group wrong: members=%v apexMemberIdx=%d", foo.members, foo.apexMemberIdx)
	}
}

func TestGroupByApex_ApexAbsent(t *testing.T) {
	// User only has subdomain in YAML, apex isn't a Domain entry.
	domains := []model.Domain{
		{Name: "www.foo.com"},
		{Name: "api.foo.com"},
	}
	groups := groupByApex(domains)
	if len(groups) != 1 || groups[0].apexMemberIdx != -1 {
		t.Errorf("apexMemberIdx should be -1 when apex not in input; got %+v", groups[0])
	}
}

func TestGroupByApex_FallbackOnApex(t *testing.T) {
	fb := mustDate("2027-01-01")
	domains := []model.Domain{
		{Name: "foo.com", ExpiryFallback: &fb},
		{Name: "www.foo.com"},  // no fallback (typical static loader output)
	}
	groups := groupByApex(domains)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].fallback == nil || !groups[0].fallback.Equal(fb) {
		t.Errorf("apex fallback should propagate to group; got %v", groups[0].fallback)
	}
}

func TestEnrich_OneLookupPerApex(t *testing.T) {
	rdapDate := mustDate("2027-01-01")
	mock := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"foo.com": {Domain: "foo.com", ExpiresAt: rdapDate, Source: "rdap"},
			"bar.com": {Domain: "bar.com", ExpiresAt: rdapDate, Source: "rdap"},
		},
	}
	domains := []model.Domain{
		{Name: "foo.com"},
		{Name: "www.foo.com"},
		{Name: "api.foo.com"},
		{Name: "bar.com"},
	}
	infos := Enrich(context.Background(), mock, domains, Options{Workers: 2})

	if mock.callCount() != 2 {
		t.Errorf("expected exactly 2 RDAP calls (one per apex), got %d (calls: %v)",
			mock.callCount(), mock.calls)
	}
	if len(infos) != 4 {
		t.Fatalf("expected 4 infos (one per domain), got %d", len(infos))
	}
	// Each info should carry the apex's data with its own Domain.
	for i, info := range infos {
		if !info.ExpiresAt.Equal(rdapDate) {
			t.Errorf("infos[%d].ExpiresAt = %v, want %v", i, info.ExpiresAt, rdapDate)
		}
		if info.Domain != domains[i].Name {
			t.Errorf("infos[%d].Domain = %q, want %q", i, info.Domain, domains[i].Name)
		}
	}
}

func TestEnrich_OrderPreserved(t *testing.T) {
	mock := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"foo.com": {Domain: "foo.com", ExpiresAt: mustDate("2027-01-01"), Source: "rdap"},
			"bar.com": {Domain: "bar.com", ExpiresAt: mustDate("2027-06-01"), Source: "rdap"},
		},
	}
	domains := []model.Domain{
		{Name: "www.bar.com"},
		{Name: "foo.com"},
		{Name: "www.foo.com"},
	}
	infos := Enrich(context.Background(), mock, domains, Options{Workers: 1})

	want := []string{"www.bar.com", "foo.com", "www.foo.com"}
	for i, n := range want {
		if infos[i].Domain != n {
			t.Errorf("infos[%d].Domain = %q, want %q (order must mirror input)",
				i, infos[i].Domain, n)
		}
	}
}

func TestEnrich_MismatchAttachedOnceToApex(t *testing.T) {
	rdapDate := mustDate("2027-01-01")
	fb := mustDate("2026-12-01")    // 31 days off
	mock := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"foo.com": {Domain: "foo.com", ExpiresAt: rdapDate, Source: "rdap"},
		},
	}
	domains := []model.Domain{
		{Name: "foo.com", ExpiryFallback: &fb},
		{Name: "www.foo.com"},
		{Name: "api.foo.com"},
	}
	infos := Enrich(context.Background(), mock, domains, Options{Workers: 1, MismatchToleranceDays: 0})

	flagged := 0
	for _, info := range infos {
		if !info.FallbackExpected.IsZero() {
			flagged++
			if info.Domain != "foo.com" {
				t.Errorf("FallbackExpected should be on apex (foo.com), got %q", info.Domain)
			}
		}
	}
	if flagged != 1 {
		t.Errorf("expected exactly 1 info with FallbackExpected (the apex), got %d", flagged)
	}
}

func TestEnrich_ToleranceSilencesSmallDiff(t *testing.T) {
	rdapDate := mustDate("2026-07-01")
	fb := mustDate("2026-06-30") // 1 day off — typical marcaria.com pattern
	mock := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"marcaria.com": {Domain: "marcaria.com", ExpiresAt: rdapDate, Source: "rdap"},
		},
	}
	domains := []model.Domain{
		{Name: "marcaria.com", ExpiryFallback: &fb},
	}

	// Strict: should fire.
	infosStrict := Enrich(context.Background(), mock, domains, Options{Workers: 1, MismatchToleranceDays: 0})
	if infosStrict[0].FallbackExpected.IsZero() {
		t.Error("with tolerance=0, 1-day diff should trigger mismatch")
	}

	// Tolerant: should silence.
	infosTol := Enrich(context.Background(), mock, domains, Options{Workers: 1, MismatchToleranceDays: 1})
	if !infosTol[0].FallbackExpected.IsZero() {
		t.Error("with tolerance=1, 1-day diff should NOT trigger mismatch")
	}
}

func TestEnrich_RDAPFailWithFallback_UsesFallback(t *testing.T) {
	fb := mustDate("2027-08-15")
	mock := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"foo.com.co": {Domain: "foo.com.co", Err: errors.New("no rdap for .co")},
		},
	}
	domains := []model.Domain{
		{Name: "foo.com.co", ExpiryFallback: &fb},
		{Name: "www.foo.com.co"},
	}
	infos := Enrich(context.Background(), mock, domains, Options{Workers: 1})
	for i, info := range infos {
		if info.Err != nil {
			t.Errorf("infos[%d].Err = %v; fallback should have replaced the RDAP error", i, info.Err)
		}
		if info.Source != "fallback" {
			t.Errorf("infos[%d].Source = %q, want fallback", i, info.Source)
		}
		if !info.ExpiresAt.Equal(fb) {
			t.Errorf("infos[%d].ExpiresAt = %v, want %v (fallback)", i, info.ExpiresAt, fb)
		}
	}
}

func TestEnrich_RDAPFailNoFallback_ErrorPropagates(t *testing.T) {
	mock := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"foo.com.co": {Domain: "foo.com.co", Err: errors.New("no rdap for .co")},
		},
	}
	domains := []model.Domain{
		{Name: "foo.com.co"},
		{Name: "www.foo.com.co"},
	}
	infos := Enrich(context.Background(), mock, domains, Options{Workers: 1})
	for i, info := range infos {
		if info.Err == nil {
			t.Errorf("infos[%d] should carry an error when RDAP fails and no fallback exists", i)
		}
	}
}

func TestEnrich_IsApexSetCorrectly(t *testing.T) {
	mock := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"foo.com": {Domain: "foo.com", ExpiresAt: mustDate("2027-01-01"), Source: "rdap"},
		},
	}
	domains := []model.Domain{
		{Name: "www.foo.com"}, // subdomain first
		{Name: "foo.com"},     // apex second
		{Name: "api.foo.com"}, // another sub
	}
	infos := Enrich(context.Background(), mock, domains, Options{Workers: 1})

	for _, info := range infos {
		want := info.Domain == "foo.com"
		if info.IsApex != want {
			t.Errorf("infos[Domain=%s].IsApex = %v, want %v", info.Domain, info.IsApex, want)
		}
	}
}

func TestEnrich_IsApexFallsBackToFirstWhenApexAbsent(t *testing.T) {
	// User only has subdomains in YAML; no apex entry. The first member
	// should be promoted to apex-representative so domain alerts still fire.
	mock := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"foo.com": {Domain: "foo.com", ExpiresAt: mustDate("2027-01-01"), Source: "rdap"},
		},
	}
	domains := []model.Domain{
		{Name: "www.foo.com"},
		{Name: "api.foo.com"},
	}
	infos := Enrich(context.Background(), mock, domains, Options{Workers: 1})

	apexCount := 0
	for _, info := range infos {
		if info.IsApex {
			apexCount++
		}
	}
	if apexCount != 1 {
		t.Errorf("exactly 1 member should be marked IsApex; got %d", apexCount)
	}
}

func TestEnrich_EmptyDomains(t *testing.T) {
	mock := &mockLooker{}
	infos := Enrich(context.Background(), mock, nil, Options{Workers: 5})
	if len(infos) != 0 {
		t.Errorf("expected 0 infos for empty input, got %d", len(infos))
	}
	if mock.callCount() != 0 {
		t.Errorf("expected 0 RDAP calls for empty input, got %d", mock.callCount())
	}
}

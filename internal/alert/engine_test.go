package alert

import (
	"errors"
	"testing"
	"time"

	"auto-certs/internal/model"
)

// mustDate parses YYYY-MM-DD into a UTC midnight time. Test helper only.
func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestDaysBetween(t *testing.T) {
	tests := []struct {
		name   string
		now    time.Time
		future time.Time
		want   int
	}{
		{"same day", mustDate("2026-05-19"), mustDate("2026-05-19"), 0},
		{"one day forward", mustDate("2026-05-19"), mustDate("2026-05-20"), 1},
		{"one day backward", mustDate("2026-05-20"), mustDate("2026-05-19"), -1},
		{"30 days forward", mustDate("2026-05-19"), mustDate("2026-06-18"), 30},
		{"31 days forward (just over threshold)", mustDate("2026-05-19"), mustDate("2026-06-19"), 31},
		{"crosses year", mustDate("2026-12-15"), mustDate("2027-01-15"), 31},
		// Time-of-day on different days still counts as 1 day apart at date granularity.
		{
			"time-of-day collapses to date diff",
			time.Date(2026, 5, 19, 23, 59, 59, 0, time.UTC),
			time.Date(2026, 5, 20, 0, 0, 1, 0, time.UTC),
			1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := daysBetween(tt.now, tt.future); got != tt.want {
				t.Errorf("daysBetween(%v, %v) = %d, want %d", tt.now, tt.future, got, tt.want)
			}
		})
	}
}

func TestEvaluate_EmptyInputs(t *testing.T) {
	now := mustDate("2026-05-19")
	got := Evaluate(now, nil, nil, nil, []int{30, 15, 7, 3})
	if len(got) != 0 {
		t.Errorf("expected 0 alerts, got %d", len(got))
	}
}

func TestEvaluate_SkipsRecordsWithErrors(t *testing.T) {
	now := mustDate("2026-05-19")
	certs := []model.CertInfo{
		{Domain: "broken.com", Err: errors.New("dial timeout")},
	}
	infos := []model.DomainInfo{
		{Domain: "broken.com", Err: errors.New("rdap 404")},
	}
	alerts := Evaluate(now, nil, certs, infos, []int{30})
	if len(alerts) != 0 {
		t.Errorf("error records must not produce alerts; got %d", len(alerts))
	}
}

func TestEvaluate_CertFarFuture_NoAlert(t *testing.T) {
	now := mustDate("2026-05-19")
	certs := []model.CertInfo{
		{Domain: "iana.org", NotAfter: mustDate("2027-12-08")},
	}
	got := Evaluate(now, nil, certs, nil, []int{30, 15, 7, 3})
	if len(got) != 0 {
		t.Errorf("cert >30d in future should not alert; got %d alerts", len(got))
	}
}

func TestEvaluate_CertExpiring_MultipleThresholdsMatch(t *testing.T) {
	now := mustDate("2026-05-19")
	// NotAfter in 5 days → triggers thresholds 30, 15, 7 (5 <= each), NOT 3.
	certs := []model.CertInfo{
		{Domain: "soon.com", NotAfter: mustDate("2026-05-24")},
	}
	got := Evaluate(now, nil, certs, nil, []int{30, 15, 7, 3})
	if len(got) != 3 {
		t.Fatalf("expected 3 expiring alerts (30,15,7), got %d", len(got))
	}
	wantThresholds := map[int]bool{30: false, 15: false, 7: false}
	for _, a := range got {
		if a.Kind != model.AlertCertExpiring {
			t.Errorf("expected AlertCertExpiring, got %s", a.Kind)
		}
		if a.DaysLeft != 5 {
			t.Errorf("expected DaysLeft=5, got %d", a.DaysLeft)
		}
		wantThresholds[a.Threshold] = true
	}
	for th, seen := range wantThresholds {
		if !seen {
			t.Errorf("missing threshold %d in output", th)
		}
	}
}

func TestEvaluate_CertExpired(t *testing.T) {
	now := mustDate("2026-05-19")
	certs := []model.CertInfo{
		{Domain: "old.com", NotAfter: mustDate("2025-01-01")},
	}
	got := Evaluate(now, nil, certs, nil, []int{30})
	if len(got) != 1 {
		t.Fatalf("expected 1 expired alert, got %d", len(got))
	}
	a := got[0]
	if a.Kind != model.AlertCertExpired {
		t.Errorf("kind = %s, want AlertCertExpired", a.Kind)
	}
	if a.DaysLeft >= 0 {
		t.Errorf("DaysLeft must be negative for expired cert, got %d", a.DaysLeft)
	}
	if a.Threshold != 0 {
		t.Errorf("expired alerts must have Threshold=0, got %d", a.Threshold)
	}
}

func TestEvaluate_DomainAlerts(t *testing.T) {
	now := mustDate("2026-05-19")
	infos := []model.DomainInfo{
		{Domain: "expiring.com", ExpiresAt: mustDate("2026-06-01"), IsApex: true}, // 13d → threshold 30 + 15
		{Domain: "expired.com", ExpiresAt: mustDate("2025-01-01"), IsApex: true},  // expired
	}
	got := Evaluate(now, nil, nil, infos, []int{30, 15, 7, 3})

	var expiringCount, expiredCount int
	for _, a := range got {
		switch a.Kind {
		case model.AlertDomainExpiring:
			expiringCount++
		case model.AlertDomainExpired:
			expiredCount++
		}
	}
	if expiringCount != 2 {
		t.Errorf("expected 2 expiring alerts (thresholds 30 and 15), got %d", expiringCount)
	}
	if expiredCount != 1 {
		t.Errorf("expected 1 expired alert, got %d", expiredCount)
	}
}

func TestEvaluate_MismatchAlertGenerated(t *testing.T) {
	now := mustDate("2026-05-19")
	infos := []model.DomainInfo{
		{
			Domain:           "marcaria.com",
			ExpiresAt:        mustDate("2027-07-01"),
			FallbackExpected: mustDate("2027-06-30"), // 1 day off
			IsApex:           true,
		},
	}
	got := Evaluate(now, nil, nil, infos, []int{30})

	var found *model.Alert
	for i := range got {
		if got[i].Kind == model.AlertDomainMismatch {
			found = &got[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected AlertDomainMismatch, not emitted")
	}
	if found.DaysLeft != -1 {
		t.Errorf("DaysLeft for mismatch (fallback - rdap) = %d, want -1", found.DaysLeft)
	}
	if !found.FallbackExpected.Equal(mustDate("2027-06-30")) {
		t.Errorf("FallbackExpected not propagated: %v", found.FallbackExpected)
	}
}

func TestEvaluate_NoMismatchWhenFallbackZero(t *testing.T) {
	now := mustDate("2026-05-19")
	infos := []model.DomainInfo{
		{Domain: "ok.com", ExpiresAt: mustDate("2027-01-01"), IsApex: true}, // no FallbackExpected set
	}
	got := Evaluate(now, nil, nil, infos, []int{30})
	for _, a := range got {
		if a.Kind == model.AlertDomainMismatch {
			t.Errorf("mismatch alert emitted without FallbackExpected set")
		}
	}
}

func TestEvaluate_DomainAlertsOnlyFromApex(t *testing.T) {
	// Apex + 2 subdomains all carrying the same ExpiresAt (the apex's). The
	// engine should emit alerts ONCE (for the apex), not three times.
	now := mustDate("2026-05-19")
	expires := mustDate("2026-06-01") // 13d → threshold 30 + 15
	infos := []model.DomainInfo{
		{Domain: "foo.com", ExpiresAt: expires, IsApex: true},
		{Domain: "www.foo.com", ExpiresAt: expires, IsApex: false},
		{Domain: "api.foo.com", ExpiresAt: expires, IsApex: false},
	}
	got := Evaluate(now, nil, nil, infos, []int{30, 15, 7, 3})

	// Expect exactly 2 alerts: one for threshold 30, one for 15. Both for foo.com.
	if len(got) != 2 {
		t.Fatalf("expected 2 alerts (apex × 2 thresholds), got %d: %+v", len(got), got)
	}
	for _, a := range got {
		if a.Domain != "foo.com" {
			t.Errorf("alert emitted for non-apex domain %q", a.Domain)
		}
	}
}

func TestEvaluate_SourceAttribution(t *testing.T) {
	now := mustDate("2026-05-19")
	domains := []model.Domain{
		{Name: "soon.com", Source: "mi.com.co"},
	}
	certs := []model.CertInfo{
		{Domain: "soon.com", NotAfter: mustDate("2026-05-24")},
	}
	got := Evaluate(now, domains, certs, nil, []int{30})
	if len(got) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(got))
	}
	if got[0].Source != "mi.com.co" {
		t.Errorf("Source = %q, want mi.com.co", got[0].Source)
	}
}

func TestEvaluate_SourceAttributionCaseInsensitive(t *testing.T) {
	now := mustDate("2026-05-19")
	domains := []model.Domain{
		{Name: "FOO.COM", Source: "static"},
	}
	certs := []model.CertInfo{
		{Domain: "foo.com", NotAfter: mustDate("2026-05-24")}, // lowercase
	}
	got := Evaluate(now, domains, certs, nil, []int{30})
	if len(got) == 0 || got[0].Source != "static" {
		t.Errorf("Source lookup must be case-insensitive; got %q", got[0].Source)
	}
}

func TestEvaluate_BoundaryThreshold(t *testing.T) {
	now := mustDate("2026-05-19")
	// Exactly 30 days out → SHOULD match threshold 30 (daysLeft <= t).
	certs := []model.CertInfo{
		{Domain: "boundary.com", NotAfter: mustDate("2026-06-18")},
	}
	got := Evaluate(now, nil, certs, nil, []int{30, 15, 7, 3})
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 alert at boundary, got %d", len(got))
	}
	if got[0].Threshold != 30 {
		t.Errorf("threshold = %d, want 30", got[0].Threshold)
	}
}

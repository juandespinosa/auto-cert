// Package alert turns TLS and registry expiration data into actionable Alerts.
// Pure: no I/O, no time.Now() — the caller injects "now" so the engine is
// trivially testable and deterministic.
package alert

import (
	"sort"
	"strings"
	"time"

	"auto-certs/internal/model"
)

// Evaluate emits one Alert per (domain, kind, threshold) match. Records with
// non-nil Err are skipped (no data → no alert). For each cert/domain:
//   - already expired (NotAfter/ExpiresAt < now) → one *Expired alert with
//     Threshold=0 and a negative DaysLeft.
//   - not yet expired → one *Expiring alert per threshold T such that
//     daysLeft <= T. Multiple thresholds can match the same record at once;
//     state-layer dedup prevents repeated emails.
//
// domains is used only to attach the discoverer Source to each Alert (case-
// insensitive name match). Domains not in the list get empty Source.
func Evaluate(now time.Time, domains []model.Domain, certs []model.CertInfo, infos []model.DomainInfo, thresholds []int) []model.Alert {
	// Stable threshold order (ascending) so output is deterministic.
	thr := append([]int(nil), thresholds...)
	sort.Ints(thr)

	sourceByName := make(map[string]string, len(domains))
	for _, d := range domains {
		sourceByName[strings.ToLower(d.Name)] = d.Source
	}

	var alerts []model.Alert
	for _, c := range certs {
		if c.Err != nil {
			continue
		}
		certAlerts := evalOne(now, c.Domain, c.NotAfter, model.AlertCertExpiring, model.AlertCertExpired, thr)
		for i := range certAlerts {
			certAlerts[i].Issuer = c.Issuer
		}
		alerts = append(alerts, certAlerts...)
	}
	for _, i := range infos {
		if i.Err != nil {
			continue
		}
		// Domain registration is per-apex: only the apex info emits domain
		// alerts. Subdomain infos carry the same ExpiresAt for the inventory
		// snapshot but would just duplicate the alert otherwise.
		if !i.IsApex {
			continue
		}
		alerts = append(alerts, evalOne(now, i.Domain, i.ExpiresAt, model.AlertDomainExpiring, model.AlertDomainExpired, thr)...)
		if !i.FallbackExpected.IsZero() {
			alerts = append(alerts, model.Alert{
				Domain:           i.Domain,
				Kind:             model.AlertDomainMismatch,
				ExpiresAt:        i.ExpiresAt,
				FallbackExpected: i.FallbackExpected,
				DaysLeft:         daysBetween(i.ExpiresAt, i.FallbackExpected),
				Threshold:        0,
			})
		}
	}

	for i := range alerts {
		alerts[i].Source = sourceByName[strings.ToLower(alerts[i].Domain)]
	}
	return alerts
}

func evalOne(now time.Time, domain string, expiresAt time.Time, expiring, expired model.AlertKind, thresholds []int) []model.Alert {
	if expiresAt.IsZero() {
		return nil
	}
	if expiresAt.Before(now) {
		return []model.Alert{{
			Domain:    domain,
			Kind:      expired,
			ExpiresAt: expiresAt,
			DaysLeft:  daysBetween(now, expiresAt),
			Threshold: 0,
		}}
	}
	days := daysBetween(now, expiresAt)
	var out []model.Alert
	for _, t := range thresholds {
		if days <= t {
			out = append(out, model.Alert{
				Domain:    domain,
				Kind:      expiring,
				ExpiresAt: expiresAt,
				DaysLeft:  days,
				Threshold: t,
			})
		}
	}
	return out
}

// daysBetween returns whole-day difference (future - now), using UTC date
// boundaries. Positive when future is later, negative when already past.
// Ignoring time-of-day matches how humans think about expiry deadlines.
func daysBetween(now, future time.Time) int {
	n := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	f := time.Date(future.Year(), future.Month(), future.Day(), 0, 0, 0, 0, time.UTC)
	return int(f.Sub(n).Hours() / 24)
}

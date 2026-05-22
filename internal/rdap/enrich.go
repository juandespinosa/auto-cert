package rdap

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"auto-certs/internal/model"
)

// Looker is the minimal Client surface Enrich depends on (eases testing).
type Looker interface {
	Lookup(ctx context.Context, domain string) *model.DomainInfo
}

// Options tunes Enrich. Zero values fall back to sane defaults.
type Options struct {
	Workers int
	// MismatchToleranceDays controls when RDAP vs YAML fallback disagreement
	// triggers an AlertDomainMismatch. The check fires when
	// |rdap_date - yaml_date| > tolerance. Zero (default) means strict: even
	// 1 day off is reported. Set to 1 to silence the common "registrar UI
	// shows local-tz date, registry returns UTC" off-by-one.
	MismatchToleranceDays int
}

const defaultEnrichWorkers = 5

// Enrich resolves each domain's registry expiration via RDAP. Subdomains are
// grouped by their registrable domain (eTLD+1, via publicsuffix) and exactly
// ONE RDAP lookup is made per apex — saves ~80% of HTTP calls on typical
// route53 / namecom inventories where most entries are subdomains.
//
// Each returned DomainInfo carries the apex's expiration data with Domain set
// to the original FQDN. Only one info per group has FallbackExpected set —
// the apex member if present, else the first member — so AlertDomainMismatch
// fires exactly once per apex when RDAP and YAML disagree by more than
// opts.MismatchToleranceDays.
//
// The output slice mirrors the input order.
func Enrich(ctx context.Context, c Looker, domains []model.Domain, opts Options) []model.DomainInfo {
	workers := opts.Workers
	if workers <= 0 {
		workers = defaultEnrichWorkers
	}
	out := make([]model.DomainInfo, len(domains))
	if len(domains) == 0 {
		return out
	}

	groups := groupByApex(domains)
	if len(groups) < workers {
		workers = len(groups)
	}

	jobs := make(chan apexGroup)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for g := range jobs {
				processGroup(ctx, c, g, domains, out, opts.MismatchToleranceDays)
			}
		}()
	}
	// Producer respects ctx.Done() so a SIGINT / context cancel no deja
	// deadlock cuando los workers están atascados en Lookups lentos.
	for _, g := range groups {
		select {
		case jobs <- g:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return out
		}
	}
	close(jobs)
	wg.Wait()
	return out
}

type apexGroup struct {
	apex          string
	members       []int      // indices into the original domains slice
	fallback      *time.Time // from the apex member's ExpiryFallback (if any)
	apexMemberIdx int        // index of the member whose Name == apex; -1 if absent
}

func groupByApex(domains []model.Domain) []apexGroup {
	byApex := map[string]*apexGroup{}
	for i, d := range domains {
		apex := registrableDomain(d.Name)
		g, ok := byApex[apex]
		if !ok {
			g = &apexGroup{apex: apex, apexMemberIdx: -1}
			byApex[apex] = g
		}
		g.members = append(g.members, i)
		if strings.EqualFold(d.Name, apex) {
			g.apexMemberIdx = i
			if d.ExpiryFallback != nil {
				g.fallback = d.ExpiryFallback
			}
		} else if g.fallback == nil && d.ExpiryFallback != nil {
			// Subdomain happens to carry a fallback (uncommon — the static
			// loader normally puts ExpiryFallback only on the apex entry).
			// Use it as a backup so the comparison still runs.
			g.fallback = d.ExpiryFallback
		}
	}
	out := make([]apexGroup, 0, len(byApex))
	for _, g := range byApex {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].apex < out[j].apex })
	return out
}

// registrableDomain returns eTLD+1 (lowercased) for name. Falls back to the
// literal lowercased name when publicsuffix can't classify it (e.g., unknown
// TLDs, or single-label inputs that can't have a public suffix).
func registrableDomain(name string) string {
	n := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if n == "" {
		return ""
	}
	if etld1, err := publicsuffix.EffectiveTLDPlusOne(n); err == nil {
		return strings.ToLower(etld1)
	}
	return n
}

func processGroup(ctx context.Context, c Looker, g apexGroup, domains []model.Domain, out []model.DomainInfo, toleranceDays int) {
	apexInfo := c.Lookup(ctx, g.apex)

	// RDAP failed: substitute fallback if YAML provided one.
	if apexInfo.Err != nil && g.fallback != nil {
		slog.Warn("rdap failed, using YAML fallback",
			"apex", g.apex, "err", apexInfo.Err)
		apexInfo = &model.DomainInfo{
			Domain:    g.apex,
			ExpiresAt: *g.fallback,
			Source:    "fallback",
			CheckedAt: time.Now().UTC(),
		}
	}

	// Mismatch detection: RDAP succeeded AND fallback exists AND dates differ
	// by MORE than toleranceDays.
	mismatch := false
	if apexInfo.Err == nil && g.fallback != nil {
		diff := absDayDiff(apexInfo.ExpiresAt, *g.fallback)
		if diff > toleranceDays {
			slog.Warn("rdap/fallback date mismatch",
				"apex", g.apex,
				"rdap", apexInfo.ExpiresAt.Format("2006-01-02"),
				"fallback", g.fallback.Format("2006-01-02"),
				"diff_days", diff,
				"tolerance_days", toleranceDays,
			)
			mismatch = true
		}
	}

	// Pick the "representative" member for emit-once alerts: the apex member
	// if it's in the input, else the first member. Domain registration is
	// per-apex, so we want a single alert per registration even if multiple
	// FQDNs share it.
	apexIdx := g.apexMemberIdx
	if apexIdx == -1 && len(g.members) > 0 {
		apexIdx = g.members[0]
	}

	for _, idx := range g.members {
		info := *apexInfo
		info.Domain = domains[idx].Name
		info.IsApex = (idx == apexIdx)
		info.FallbackExpected = time.Time{} // clean default; set below only on apex
		if mismatch && idx == apexIdx {
			info.FallbackExpected = *g.fallback
		}
		// Disjoint slice indices → safe to write without locking.
		out[idx] = info
	}
}

func dayDiff(a, b time.Time) int {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	aDay := time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)
	bDay := time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC)
	return int(bDay.Sub(aDay).Hours() / 24)
}

func absDayDiff(a, b time.Time) int {
	d := dayDiff(a, b)
	if d < 0 {
		return -d
	}
	return d
}

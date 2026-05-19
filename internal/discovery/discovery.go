package discovery

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"auto-certs/internal/model"
)

// Discoverer enumerates monitorable domains from one source.
// Implementations must be safe to call concurrently with other Discoverers.
type Discoverer interface {
	Name() string
	Discover(ctx context.Context) ([]model.Domain, error)
}

// Result aggregates the deduplicated domain list and any per-discoverer errors.
type Result struct {
	Domains []model.Domain
	Errors  map[string]error
}

// discovererPriority controls which discoverer's entry "wins" Source when the
// same FQDN comes from multiple discoverers. Lower number = higher priority.
// Rationale: `static` carries the human-curated registrar grouping (mi.com.co,
// marcaria.com, etc.), which is more informative than knowing where the DNS
// is hosted. `namecom` is also a registrar API (knows ownership). `route53`
// and `cloudflare` are DNS providers — they tell you who hosts records, not
// who owns the domain.
func discovererPriority(name string) int {
	switch name {
	case "static":
		return 0
	case "namecom":
		return 1
	case "route53", "cloudflare":
		return 2
	default:
		return 99
	}
}

// Aggregate runs every Discoverer in parallel, applies defaultPort to entries
// that omit it, and deduplicates by (lowercase name, port). When the same
// (name, port) appears in multiple sources, the higher-priority discoverer
// wins for Source; a non-nil ExpiryFallback from any source is preserved on
// the kept entry. ExpiryFallback is only consulted if RDAP fails for the
// domain.
func Aggregate(ctx context.Context, defaultPort int, discoverers ...Discoverer) Result {
	out := Result{Errors: map[string]error{}}
	if len(discoverers) == 0 {
		return out
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	perDiscoverer := make(map[string][]model.Domain, len(discoverers))

	for _, d := range discoverers {
		wg.Add(1)
		go func(d Discoverer) {
			defer wg.Done()
			domains, err := d.Discover(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.Errors[d.Name()] = err
				slog.Warn("discoverer failed", "name", d.Name(), "err", err)
				return
			}
			perDiscoverer[d.Name()] = domains
			slog.Info("discoverer finished", "name", d.Name(), "count", len(domains))
		}(d)
	}
	wg.Wait()

	out.Domains = dedup(perDiscoverer, defaultPort)
	return out
}

func dedup(perDiscoverer map[string][]model.Domain, defaultPort int) []model.Domain {
	type key struct {
		name string
		port int
	}
	seen := map[key]*model.Domain{}

	// Higher-priority discoverers go first so first-write-wins yields the
	// most informative Source. Ties break alphabetically.
	names := make([]string, 0, len(perDiscoverer))
	for n := range perDiscoverer {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		pi, pj := discovererPriority(names[i]), discovererPriority(names[j])
		if pi != pj {
			return pi < pj
		}
		return names[i] < names[j]
	})

	for _, n := range names {
		for _, d := range perDiscoverer[n] {
			if d.Port == 0 {
				d.Port = defaultPort
			}
			k := key{strings.ToLower(d.Name), d.Port}
			if existing, ok := seen[k]; ok {
				if existing.ExpiryFallback == nil && d.ExpiryFallback != nil {
					existing.ExpiryFallback = d.ExpiryFallback
				}
				slog.Debug("deduped domain", "name", d.Name, "kept_source", existing.Source, "dropped_source", d.Source)
				continue
			}
			cp := d
			seen[k] = &cp
		}
	}

	list := make([]model.Domain, 0, len(seen))
	for _, d := range seen {
		list = append(list, *d)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].Port < list[j].Port
	})
	return list
}

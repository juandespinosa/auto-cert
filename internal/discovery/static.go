package discovery

import (
	"context"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"auto-certs/internal/model"
)

type Static struct {
	Path string
}

func NewStatic(path string) *Static {
	return &Static{Path: path}
}

func (s *Static) Name() string { return "static" }

// staticFile supports two layouts simultaneously: a flat `domains:` list
// (every entry's Source defaults to "static") and a grouped `groups:` block
// where every entry inherits the group's `source:`. Use groups to track who
// the registrar is per domain (mi.com.co, marcaria.com, etc.) — that becomes
// the per-source breakdown in the email.
type staticFile struct {
	Groups  []staticGroup `yaml:"groups"`
	Domains []staticEntry `yaml:"domains"`
}

type staticGroup struct {
	Source  string        `yaml:"source"`
	Domains []staticEntry `yaml:"domains"`
}

type staticEntry struct {
	Name           string   `yaml:"name"`
	Port           int      `yaml:"port"`
	ExpiryFallback flexDate `yaml:"expiry_fallback"`
	// Subdomains is sugar to avoid repeating the apex. Each value is the label
	// prefix (e.g., "www", "api", "dev.admin") and gets expanded to a separate
	// Domain entry with full FQDN. Subdomains inherit Port and Source but NOT
	// ExpiryFallback — they share the apex's registration so RDAP errors on
	// them are expected and silent.
	Subdomains []string `yaml:"subdomains"`
}

// flexDate accepts both ISO 8601 (YYYY-MM-DD) and the DD-MM-YYYY format the
// user commonly types from registrar control panels. Other formats fail
// loudly so a typo doesn't silently drop the fallback.
type flexDate struct {
	t   time.Time
	set bool
}

func (d *flexDate) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Tag == "!!null" || value.Value == "" {
		return nil
	}
	formats := []string{
		"2006-01-02",
		"02-01-2006",
		time.RFC3339,
		"2006-01-02T15:04:05Z",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, value.Value); err == nil {
			d.t = t.UTC()
			d.set = true
			return nil
		}
	}
	return fmt.Errorf("expiry_fallback %q at line %d: not a recognized date (try YYYY-MM-DD or DD-MM-YYYY)",
		value.Value, value.Line)
}

func (d flexDate) toPtr() *time.Time {
	if !d.set {
		return nil
	}
	t := d.t
	return &t
}

func (s *Static) Discover(_ context.Context) ([]model.Domain, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.Path, err)
	}
	var f staticFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.Path, err)
	}

	var out []model.Domain
	expand(&out, "static", f.Domains)
	for _, g := range f.Groups {
		src := g.Source
		if src == "" {
			src = "static"
		}
		expand(&out, src, g.Domains)
	}
	return out, nil
}

func expand(out *[]model.Domain, source string, entries []staticEntry) {
	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		*out = append(*out, model.Domain{
			Name:           e.Name,
			Port:           e.Port,
			Source:         source,
			ExpiryFallback: e.ExpiryFallback.toPtr(),
		})
		for _, sub := range e.Subdomains {
			if sub == "" {
				continue
			}
			*out = append(*out, model.Domain{
				Name:   sub + "." + e.Name,
				Port:   e.Port,
				Source: source,
			})
		}
	}
}

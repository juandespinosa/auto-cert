package rdap

import (
	"context"
	"strings"
	"sync"
	"time"

	"auto-certs/internal/model"
)

// Cache stores successful RDAP lookups keyed by lowercased domain. Errors
// are never cached — a transient failure shouldn't persist for the TTL.
// Implementations decide where the cached values live (none, filesystem, S3)
// and when to load/flush them.
type Cache interface {
	Get(domain string) (*model.DomainInfo, bool)
	Set(domain string, info *model.DomainInfo)
	// Flush persists the in-memory state to the backing store. Called once
	// at the end of a run by the caller.
	Flush(ctx context.Context) error
}

// CachedLooker wraps an inner Looker with a Cache. Hits within TTL return
// instantly without network. Misses fall through to the inner Looker and
// populate the cache on success.
type CachedLooker struct {
	Inner Looker
	Cache Cache
}

func NewCachedLooker(inner Looker, cache Cache) *CachedLooker {
	return &CachedLooker{Inner: inner, Cache: cache}
}

func (c *CachedLooker) Lookup(ctx context.Context, domain string) *model.DomainInfo {
	if info, ok := c.Cache.Get(domain); ok {
		// Preserve cached values; the enricher post-processes (IsApex,
		// FallbackExpected) based on the current run's input.
		return info
	}
	info := c.Inner.Lookup(ctx, domain)
	c.Cache.Set(domain, info)
	return info
}

// NoopCache satisfies the Cache interface but never stores anything. Useful
// when cache is disabled in config.
type NoopCache struct{}

func (NoopCache) Get(string) (*model.DomainInfo, bool)        { return nil, false }
func (NoopCache) Set(string, *model.DomainInfo)               {}
func (NoopCache) Flush(context.Context) error                 { return nil }

// cachedEntry is the on-disk / on-S3 representation. We do NOT store Err
// (cache misses already model that) and we omit IsApex / FallbackExpected
// since those are recomputed per-run.
type cachedEntry struct {
	Domain    string    `json:"domain"`
	ExpiresAt time.Time `json:"expires_at"`
	Source    string    `json:"source"`
	CheckedAt time.Time `json:"checked_at"`
	CachedAt  time.Time `json:"cached_at"`
}

func (e cachedEntry) toInfo() *model.DomainInfo {
	return &model.DomainInfo{
		Domain:    e.Domain,
		ExpiresAt: e.ExpiresAt,
		Source:    e.Source,
		CheckedAt: e.CheckedAt,
	}
}

// cacheFile is the JSON shape on disk / in S3.
type cacheFile struct {
	Entries []cachedEntry `json:"entries"`
}

// memBackend is the shared in-process map used by FileCache and S3Cache.
// It tracks whether anything changed since the last load so Flush can skip
// the write when nothing happened.
type memBackend struct {
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[string]cachedEntry
	dirty   bool
}

func newMemBackend(ttl time.Duration) *memBackend {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return &memBackend{
		ttl:     ttl,
		entries: make(map[string]cachedEntry),
	}
}

// Get satisfies the Cache interface for any backend that embeds *memBackend.
func (m *memBackend) Get(domain string) (*model.DomainInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[strings.ToLower(domain)]
	if !ok {
		return nil, false
	}
	if time.Since(e.CachedAt) > m.ttl {
		return nil, false
	}
	return e.toInfo(), true
}

// Set satisfies the Cache interface; same caveat as Get.
func (m *memBackend) Set(domain string, info *model.DomainInfo) {
	if info == nil || info.Err != nil || info.ExpiresAt.IsZero() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[strings.ToLower(domain)] = cachedEntry{
		Domain:    info.Domain,
		ExpiresAt: info.ExpiresAt,
		Source:    info.Source,
		CheckedAt: info.CheckedAt,
		CachedAt:  time.Now().UTC(),
	}
	m.dirty = true
}

// snapshot returns the entries sorted by domain — stable output for diff-
// friendly serialization. Caller must NOT mutate the returned slice.
func (m *memBackend) snapshot() []cachedEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]cachedEntry, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e)
	}
	return out
}

func (m *memBackend) isDirty() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dirty
}

func (m *memBackend) markClean() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirty = false
}

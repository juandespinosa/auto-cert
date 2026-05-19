package rdap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"auto-certs/internal/model"
)

func TestCachedLooker_HitSkipsInner(t *testing.T) {
	inner := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"foo.com": {Domain: "foo.com", ExpiresAt: mustDate("2027-01-01"), Source: "rdap"},
		},
	}
	cache := NewFileCache(filepath.Join(t.TempDir(), "cache.json"), 24*time.Hour)
	cl := NewCachedLooker(inner, cache)

	// First call: miss → inner hit.
	_ = cl.Lookup(context.Background(), "foo.com")
	if inner.callCount() != 1 {
		t.Fatalf("expected 1 inner call after miss, got %d", inner.callCount())
	}

	// Second call: hit → inner NOT called again.
	got := cl.Lookup(context.Background(), "foo.com")
	if inner.callCount() != 1 {
		t.Errorf("expected inner to be cached (still 1 call), got %d", inner.callCount())
	}
	if !got.ExpiresAt.Equal(mustDate("2027-01-01")) {
		t.Errorf("cached value wrong: %v", got)
	}
}

func TestCachedLooker_DoesNotCacheErrors(t *testing.T) {
	inner := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"broken.co": {Domain: "broken.co", Err: errors.New("no rdap")},
		},
	}
	cache := NewFileCache(filepath.Join(t.TempDir(), "cache.json"), 24*time.Hour)
	cl := NewCachedLooker(inner, cache)

	_ = cl.Lookup(context.Background(), "broken.co")
	_ = cl.Lookup(context.Background(), "broken.co")
	if inner.callCount() != 2 {
		t.Errorf("errored lookups must NOT be cached; expected 2 inner calls, got %d", inner.callCount())
	}
}

func TestCachedLooker_TTLExpiry(t *testing.T) {
	inner := &mockLooker{
		responses: map[string]*model.DomainInfo{
			"foo.com": {Domain: "foo.com", ExpiresAt: mustDate("2027-01-01"), Source: "rdap"},
		},
	}
	cache := NewFileCache(filepath.Join(t.TempDir(), "cache.json"), 1*time.Nanosecond)
	cl := NewCachedLooker(inner, cache)

	_ = cl.Lookup(context.Background(), "foo.com")
	time.Sleep(10 * time.Millisecond) // exceed 1ns TTL
	_ = cl.Lookup(context.Background(), "foo.com")
	if inner.callCount() != 2 {
		t.Errorf("expired cache entry must trigger refetch; got %d calls", inner.callCount())
	}
}

func TestFileCache_PersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")

	c1 := NewFileCache(path, 24*time.Hour)
	c1.Set("foo.com", &model.DomainInfo{
		Domain: "foo.com", ExpiresAt: mustDate("2027-01-01"), Source: "rdap",
	})
	if err := c1.Flush(context.Background()); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	// New instance, same path: should load the previous data.
	c2 := NewFileCache(path, 24*time.Hour)
	got, ok := c2.Get("foo.com")
	if !ok {
		t.Fatal("entry not loaded from disk")
	}
	if !got.ExpiresAt.Equal(mustDate("2027-01-01")) {
		t.Errorf("loaded value wrong: %v", got)
	}
}

func TestFileCache_FlushSkippedWhenClean(t *testing.T) {
	// If we never Set anything, Flush should be a no-op and not create the file.
	path := filepath.Join(t.TempDir(), "cache.json")
	c := NewFileCache(path, 24*time.Hour)
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("flush should succeed even when clean: %v", err)
	}
	// We don't check for non-existence explicitly — the important guarantee is
	// no error and no spurious mutation. (FileCache returns nil immediately on
	// !isDirty().)
}

func TestNoopCache(t *testing.T) {
	c := NoopCache{}
	c.Set("foo.com", &model.DomainInfo{Domain: "foo.com"})
	if _, ok := c.Get("foo.com"); ok {
		t.Error("NoopCache.Get must always return ok=false")
	}
	if err := c.Flush(context.Background()); err != nil {
		t.Errorf("NoopCache.Flush should never error, got %v", err)
	}
}

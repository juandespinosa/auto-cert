package rdap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FileCache persists the RDAP cache as a single JSON file. Load happens
// eagerly in NewFileCache; Flush writes the file atomically (temp + rename).
// Inherits Get/Set from memBackend via embedding.
type FileCache struct {
	Path string
	*memBackend
}

// NewFileCache loads any existing cache from path and returns a ready-to-use
// FileCache. A missing file or parse error logs a warning and starts empty
// — losing the cache is recoverable, not fatal.
func NewFileCache(path string, ttl time.Duration) *FileCache {
	c := &FileCache{
		Path:       path,
		memBackend: newMemBackend(ttl),
	}
	c.load()
	return c
}

func (c *FileCache) load() {
	data, err := os.ReadFile(c.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		slog.Warn("rdap cache load failed", "path", c.Path, "err", err)
		return
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		slog.Warn("rdap cache parse failed", "path", c.Path, "err", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range cf.Entries {
		c.entries[e.Domain] = e
	}
	slog.Info("rdap cache loaded", "path", c.Path, "entries", len(c.entries))
}

// Flush writes the cache to disk atomically if anything changed since load.
func (c *FileCache) Flush(_ context.Context) error {
	if !c.isDirty() {
		return nil
	}
	entries := c.snapshot()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Domain < entries[j].Domain })
	data, err := json.MarshalIndent(cacheFile{Entries: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("rdap cache marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return fmt.Errorf("rdap cache mkdir: %w", err)
	}
	tmp := c.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("rdap cache write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, c.Path); err != nil {
		return fmt.Errorf("rdap cache rename: %w", err)
	}
	c.markClean()
	slog.Info("rdap cache flushed", "path", c.Path, "entries", len(entries))
	return nil
}

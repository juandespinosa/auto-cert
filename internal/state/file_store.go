package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"auto-certs/internal/model"
)

// FileStore reads and writes the state JSON atomically (write-temp + rename).
// Not goroutine-safe; the monitor runs Filter and Save sequentially.
type FileStore struct {
	Path string
}

func NewFileStore(path string) *FileStore {
	return &FileStore{Path: path}
}

func (s *FileStore) load() (map[string]Record, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state read %s: %w", s.Path, err)
	}
	return decode(data)
}

func (s *FileStore) Filter(alerts []model.Alert) ([]model.Alert, error) {
	existing, err := s.load()
	if err != nil {
		return nil, err
	}
	return filterAgainst(existing, alerts), nil
}

func (s *FileStore) Save(sent []model.Alert) error {
	existing, err := s.load()
	if err != nil {
		return err
	}
	recs := upsert(existing, sent)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("state mkdir: %w", err)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return fmt.Errorf("state marshal: %w", err)
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("state write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		return fmt.Errorf("state rename %s: %w", s.Path, err)
	}
	return nil
}

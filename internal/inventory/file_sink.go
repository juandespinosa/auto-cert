package inventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileSink writes the snapshot atomically (temp file + rename) so a partial
// write never leaves a corrupt JSON on disk.
type FileSink struct {
	Path string
}

func NewFileSink(path string) *FileSink {
	return &FileSink{Path: path}
}

func (f *FileSink) Save(s Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return fmt.Errorf("inventory mkdir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("inventory marshal: %w", err)
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("inventory write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, f.Path); err != nil {
		return fmt.Errorf("inventory rename %s: %w", f.Path, err)
	}
	return nil
}

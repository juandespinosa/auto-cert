package inventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileSink writes the snapshot atomically (temp file + rename) so a partial
// write never leaves a corrupt JSON on disk. Also emite un XLSX hermano para
// consumo no-técnico (abrir en Excel / Sheets) — los mismos bytes se usan
// para el adjunto del correo.
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
	if err := writeAtomic(f.Path, data); err != nil {
		return err
	}

	xlsxData, err := MarshalXLSX(s)
	if err != nil {
		return fmt.Errorf("inventory xlsx: %w", err)
	}
	if err := writeAtomic(xlsxPathFor(f.Path), xlsxData); err != nil {
		return err
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("inventory write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("inventory rename %s: %w", path, err)
	}
	return nil
}

// xlsxPathFor reemplaza el sufijo .json por .xlsx. Si Path no termina en
// .json, simplemente añade .xlsx para no inventar nombres raros.
func xlsxPathFor(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		return path[:len(path)-len(".json")] + ".xlsx"
	}
	return path + ".xlsx"
}

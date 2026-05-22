// Package secrets loads secret values into the process environment so el
// mecanismo de placeholders ${VAR} en configs/config.yaml siga funcionando.
// Backend único: dotenv. (El soporte para AWS SSM se removió cuando el
// proyecto se quedó on-prem; vive en el git history si hay que rescatarlo.)
package secrets

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/joho/godotenv"
)

// LoadDotenv reads .env (if present) into os.Environ. Missing file is not an
// error — útil para dev/test donde el user puede exportar las vars desde el
// shell sin depender del archivo.
func LoadDotenv(path string) error {
	if path == "" {
		path = ".env"
	}
	if err := godotenv.Load(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("dotenv load %s: %w", path, err)
	}
	return nil
}

#!/usr/bin/env bash
# Wrapper para ejecutar auto-certs vía cron durante el período de testing
# local (antes del deploy a Lambda).
#
# Garantías:
#   - CWD correcto (lee configs/, .env y escribe state/ con paths relativos).
#   - Sin runs concurrentes (flock).
#   - Log dateado en state/cron-logs/YYYY-MM-DD.log con timestamps.
#   - Exit code propagado para que cron pueda decidir si manda mail de fallo.
#
# Pre-requisito: el binario debe estar compilado en la raíz del proyecto.
# Construir con:  make local-build && mv $(go env GOPATH)/bin/... 2>/dev/null || go build -o auto-certs ./cmd/monitor
# (o más simple: `go build -o auto-certs ./cmd/monitor` desde el root)

set -uo pipefail

# Project root = directorio padre de donde vive este script. Permite mover el
# proyecto sin tocar crontab.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

LOG_DIR="$ROOT/state/cron-logs"
mkdir -p "$LOG_DIR"
LOG="$LOG_DIR/$(date +%Y-%m-%d).log"

# Lock: si una corrida anterior está colgada y se viene otra, esta sale.
LOCK="$ROOT/state/cron.lock"
exec 9>"$LOCK"
if ! flock -n 9; then
    echo "$(date -Iseconds) | otro run en progreso; salteando" >> "$LOG"
    exit 0
fi

{
    echo "===== $(date -Iseconds) START ====="

    if [[ ! -x "$ROOT/auto-certs" ]]; then
        echo "ERROR: binario $ROOT/auto-certs no existe."
        echo "Compilar con: cd $ROOT && go build -o auto-certs ./cmd/monitor"
        exit 1
    fi

    "$ROOT/auto-certs" -config configs/config.yaml
    EXIT=$?

    echo "===== $(date -Iseconds) END (exit=$EXIT) ====="
    exit $EXIT
} >>"$LOG" 2>&1

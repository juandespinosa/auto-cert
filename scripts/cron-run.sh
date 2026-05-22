#!/usr/bin/env bash
# Wrapper para ejecutar auto-certs vía cron en un server on-prem.
#
# Garantías:
#   - CWD correcto (lee configs/, .env y escribe state/ con paths relativos).
#   - Sin runs concurrentes (flock).
#   - umask 077 para que .env y state/ se creen con permisos restrictivos
#     (640/600), no 644/664 default. Defensa en profundidad por si otro user
#     llega a poder entrar al home dir.
#   - Recompila el binario si el fuente cambió desde la última build, así
#     un `git pull` o edit en el repo se refleja en la próxima corrida sin
#     pasos manuales.
#   - Log dateado en state/cron-logs/YYYY-MM-DD.log con timestamps.
#   - Rotación: logs > 90 días se borran al inicio (≈9MB/año al ritmo
#     actual; el corte sigue dando margen para diagnosticar incidentes
#     pasados sin acumular indefinidamente).
#   - Exit code propagado para que cron pueda decidir si manda mail de fallo.

set -uo pipefail
umask 077

# Cron arranca con un PATH mínimo (/usr/bin:/bin). Agregamos las rutas
# típicas donde puede vivir el binario de Go en este server.
export PATH="/snap/bin:/usr/local/go/bin:/usr/bin:/bin:$PATH"

# Project root = directorio padre de donde vive este script. Permite mover el
# proyecto sin tocar crontab.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

LOG_DIR="$ROOT/state/cron-logs"
mkdir -p "$LOG_DIR"
LOG="$LOG_DIR/$(date +%Y-%m-%d).log"

# Rotación: prune > 90 días. Idempotente y barato (find sobre <1000 files).
find "$LOG_DIR" -name "*.log" -type f -mtime +90 -delete 2>/dev/null || true

# Lock: si una corrida anterior está colgada y se viene otra, esta sale.
LOCK="$ROOT/state/cron.lock"
exec 9>"$LOCK"
if ! flock -n 9; then
    echo "$(date -Iseconds) | otro run en progreso; salteando" >> "$LOG"
    exit 0
fi

{
    echo "===== $(date -Iseconds) START ====="

    # Auto-rebuild si el fuente está más nuevo que el binario (o no existe).
    # find -newer devuelve líneas si hay archivos .go más recientes que
    # ./auto-certs; salimos a build cuando hay coincidencias o cuando el
    # binario aún no existe.
    NEEDS_BUILD=0
    if [[ ! -x "$ROOT/auto-certs" ]]; then
        NEEDS_BUILD=1
    elif [[ -n "$(find cmd internal -name '*.go' -newer "$ROOT/auto-certs" -print -quit 2>/dev/null)" ]]; then
        NEEDS_BUILD=1
    elif [[ go.mod -nt "$ROOT/auto-certs" || go.sum -nt "$ROOT/auto-certs" ]]; then
        NEEDS_BUILD=1
    fi
    if [[ "$NEEDS_BUILD" == "1" ]]; then
        echo "[$(date -Iseconds)] fuente cambió; recompilando binario..."
        if ! go build -o "$ROOT/auto-certs" ./cmd/monitor 2>&1; then
            echo "ERROR: go build falló"
            exit 1
        fi
        echo "[$(date -Iseconds)] build OK"
    fi

    "$ROOT/auto-certs" -config configs/config.yaml
    EXIT=$?

    echo "===== $(date -Iseconds) END (exit=$EXIT) ====="
    exit $EXIT
} >>"$LOG" 2>&1

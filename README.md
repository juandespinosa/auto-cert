# auto-certs

Monitor de expiración de **certificados TLS** y **registros de dominio**, con
alertas vía email (SMTP). Corre como CLI local en un server on-prem disparado
por cron.

```
            ┌─────────────────────────────────────────────────────────┐
            │  cron (diario / mensual)  →  scripts/cron-run.sh        │
            └────────────────────────┬────────────────────────────────┘
                                     │ trigger
                                     ▼
┌────────────────────────────────────────────────────────────────────────┐
│  cmd/monitor (binario Go en el server)                                 │
│                                                                        │
│    Discovery     ──▶  RDAP      ──▶  TLS check    ──▶  Alert engine    │
│    (Route53,           (apex,         (handshake        (thresholds,   │
│     Name.com,           eTLD+1,        InsecureSkip      mismatch,     │
│     YAML estático)      cacheable)     para leer         expired)      │
│                                        certs rotos)                    │
│                                                                        │
│         ▼                                                              │
│    Inventory snapshot  ──▶  State filter  ──▶  Notifier (SMTP)         │
│    (JSON + XLSX en           (dedup por          → HTML + plain        │
│     disco, overwrite)         domain×kind×thr)      + adjunto XLSX     │
└────────────────────────────────────────────────────────────────────────┘
```

## ¿Por qué existe?

Administrar dominios entre varios registradores (Route53, Name.com, mi.com.co,
Marcaria, NetworkSolutions, etc.) significa que cualquiera puede vencerse sin
aviso. Las herramientas comerciales son caras o sólo cubren un registrador;
parsear WHOIS es frágil. **auto-certs** descubre dominios automáticamente,
resuelve la fecha de expiración vía **RDAP** (estándar IETF, independiente
del registrar), hace handshake TLS para leer el `NotAfter` del certificado y
manda un correo con resumen accionable + un Excel adjunto con el inventario
completo.

## Features

- **Múltiples discoverers**: Route53 (apex + DNS records), Name.com, listas
  estáticas YAML por registrar (mi.com.co, Marcaria, NetworkSolutions, etc.).
- **RDAP universal** para vencimiento de dominio — funciona con cualquier TLD
  que IANA exponga en `data.iana.org/rdap/dns.json`. Cache local en JSON.
- **Fallback YAML por dominio** para TLDs sin RDAP (.co, .com.co): se
  compara contra el valor del registrar si RDAP funciona y se emite alerta
  de inconsistencia ante divergencias.
- **Lectura de certs incluso vencidos**: TLS handshake con `InsecureSkipVerify`
  para poder alertar sobre certs caducados o rotos.
- **Reporte HTML**: banner color-coded (rojo/ámbar/violeta/verde), TL;DR de
  acción inmediata, tablas detalladas, breakdown por origen, badges por CA
  emisora (Let's Encrypt / Amazon / Sectigo / etc.).
- **Excel adjunto** (.xlsx) con el inventario completo por dominio — fila
  congelada + autofiltro, fechas como tipo nativo. Pensado para que personas
  no técnicas filtren/ordenen sin saber Excel avanzado.
- **Idempotencia**: state JSON local evita reenviar la misma alerta hasta
  que la fecha cambie (renovación detectada automáticamente). El email
  muestra el cuadro completo de alertas vigentes; solo no se envía si
  ninguna alerta es nueva desde el run anterior.
- **Deadman switch** opcional (healthchecks.io / cronitor.io) — si el cron
  deja de correr, el servicio externo alerta.
- **Dry-run**: emite el correo a stdout + HTML a disco sin enviar SMTP.

## Quick start

```bash
# 1. Clonar y configurar
git clone <repo>
cd auto-certs
cp .env.example .env                                              # completar con valores reales
chmod 600 .env
cp configs/static_domains.example.yaml configs/static_domains.yaml   # editar con los dominios reales

# 2. Probar con dry-run (no envía correos, escribe HTML + XLSX a disco)
make dry-run

# 3. Ver el HTML del reporte
xdg-open state/last-email.html
xdg-open state/inventory.xlsx
```

Para enviar SMTP de verdad, en `configs/config.yaml`:
```yaml
notifier:
  backend: smtp
  smtp:
    host: smtp.example.com
    port: 587
    username: ${SMTP_USERNAME}
    password: ${SMTP_PASSWORD}
    from: alerts@example.com
    to: [oncall@example.com]
```

## Setup en el server (cron)

```bash
# 1. Compilar el binario en la raíz del repo
make install     # = go build -o auto-certs ./cmd/monitor + chmod 600 .env

# 2. Probar el wrapper manualmente
./scripts/cron-run.sh   # corre el flujo completo (envía correo si hay alertas)

# 3. Agregar al crontab del user dueño del repo
crontab -e
# Línea recomendada (todos los días a las 9am hora local):
0 9 * * * /ruta/al/repo/scripts/cron-run.sh
```

El wrapper `scripts/cron-run.sh` se encarga de:
- `umask 077` para que los archivos de estado se creen con permisos restrictivos.
- Auto-rebuild si el fuente cambió desde la última corrida (zero-touch updates).
- Lock con `flock` para evitar runs concurrentes.
- Rotación: borra logs > 90 días de `state/cron-logs/`.
- Propaga el exit code para que MAILTO del cron actúe si falla.

**Importante**: name.com requiere whitelist de IP en su panel de cuenta
(`https://www.name.com/account/settings/api`). La IP pública saliente del
server tiene que estar agregada o el discoverer namecom devuelve 403.

## Configuración

Archivo único [configs/config.yaml](configs/config.yaml). Estructura:

```yaml
discovery:
  route53:  { enabled: true,  region: ..., exclude_zones: [...] }
  namecom:  { enabled: true,  username: ${NAMECOM_USERNAME}, ... }
  static:   { enabled: true,  path: configs/static_domains.yaml }
  cloudflare: { enabled: false }   # placeholder, no implementado aún

tls:        { timeout: 10s, workers: 30, default_port: 443 }
thresholds: [30, 15, 7, 3]         # días antes de vencer para disparar alerta

rdap:
  enrich_workers: 5
  mismatch_tolerance_days: 1       # |RDAP − YAML| permitido sin alertar
  cache: { backend: file | none, ttl: 168h, path: state/rdap-cache.json }

notifier: { backend: dryrun | smtp, ... }
state:    { backend: file, path: state/alerts.json }
inventory:{ backend: file, path: state/inventory.json }    # + .xlsx automáticamente
secrets:  { backend: env, dotenv_path: .env }
healthcheck: { url: ..., timeout: 5s }   # opcional deadman
```

Los `${VAR}` se expanden contra el entorno. `.env` los carga al startup vía
[github.com/joho/godotenv](https://github.com/joho/godotenv). Ver [.env.example](.env.example).

## Discoverers

| Discoverer | Trae | Auth |
|---|---|---|
| **route53** | Dominios registrados vía AWS + apex + A/AAAA/CNAME de cada hosted zone pública | IAM (perfil local o env vars) |
| **namecom** | Dominios de la cuenta Name.com + DNS records hospedados ahí | Basic auth (`username:token`) + IP whitelist |
| **static** | Lista manual en YAML (mi.com.co, Marcaria, NetworkSolutions, …) | — |
| ~~cloudflare~~ | Placeholder, no implementado | — |

### Static discoverer

El archivo real está **gitignored** (contiene inventario sensible: nombres de
dominios, registrador, fechas de vencimiento). El template versionado vive en
[configs/static_domains.example.yaml](configs/static_domains.example.yaml).

Estructura:

```yaml
groups:
  - source: mi.com.co
    domains:
      - name: midominio.com
        expiry_fallback: 2027-03-15   # ISO o DD-MM-YYYY
        subdomains: [www, api]        # se expanden como dominios separados
  - source: marcaria.com
    domains:
      - name: otro.com.es
        expiry_fallback: 09-06-2026
```

- El `source:` del grupo se hereda y aparece como filtro en el HTML del
  correo (breakdown por origen, ordenado por total descendente).
- `expiry_fallback` solo se usa si RDAP falla para el dominio. Si RDAP
  funciona, se compara y se emite `AlertDomainMismatch` ante divergencia
  (sujeto a `rdap.mismatch_tolerance_days`).
- `subdomains` es azúcar: cada prefijo se expande a un FQDN aparte, comparte
  `port` y `source` con el apex pero NO el `expiry_fallback` (dominio se
  renueva una sola vez para todo).

## Email report

Cada corrida produce un email HTML (+ plain text fallback) + adjunto `.xlsx`.
Layout HTML en orden de urgencia:

1. **Banner color-coded** según peor estado: rojo (vencidos) → ámbar
   (próximos) → violeta (inconsistencias RDAP/YAML) → verde (todo OK).
2. **TL;DR** con bullets accionables.
3. **Tablas**: Vencidos → Por vencer → Inconsistencias.
4. **Estado general** (al final, contexto): KPIs de inventario (total / sanos
   / con alerta / sin cert) + tabla "Cobertura por origen" ordenada por total.

Subject dinámico:
- `[auto-certs] N vencido(s) + M próximo(s) — acción requerida`
- `[auto-certs] M dominios por vencer en ≤30 días`
- `[auto-certs] Sin alertas — X/Y sanos`

El adjunto XLSX trae una fila por FQDN con todo el contexto (cert, dominio,
alertas), fila 1 congelada + autofiltro para que el destinatario pueda
filtrar/ordenar al abrirlo en Excel/Sheets/Numbers.

## Development

```bash
make build         # go build -o auto-certs ./cmd/monitor
make vet           # go vet ./...
make test          # go test ./...
make run           # corrida completa (envía correo si hay alertas fresh)
make dry-run       # corrida local sin enviar SMTP, escribe HTML + XLSX
make install       # build + chmod 600 .env
make clean         # rm -f auto-certs
```

Tests cubren: motor de alertas, summary + ordenamiento por total, RDAP
enrich (grouping por apex, tolerance, cache), publicsuffix lookups, parser
de DN.

## Estructura

```
auto-certs/
├── cmd/monitor/      # CLI entrypoint
├── internal/
│   ├── alert/        # Engine puro: certs/infos + thresholds → []Alert
│   ├── config/       # YAML config con ${VAR} expansion
│   ├── discovery/    # Route53, Name.com, static YAML, agregador con dedup
│   ├── healthcheck/  # Pinger deadman (healthchecks.io / cronitor.io)
│   ├── inventory/    # Snapshot JSON + XLSX por corrida
│   ├── model/        # Tipos compartidos (Domain, CertInfo, DomainInfo, Alert)
│   ├── notify/       # Render HTML/plain, SMTP con adjuntos MIME, DryRun
│   ├── rdap/         # Bootstrap IANA, lookup por apex, cache JSON local
│   ├── runner/       # Orquesta el pipeline
│   ├── secrets/      # Loader .env via godotenv
│   ├── state/        # Dedup de alertas enviadas (file JSON)
│   └── tlscheck/     # Worker pool, handshake con InsecureSkipVerify
├── configs/
│   ├── config.yaml             # config principal
│   └── static_domains.yaml     # dominios manuales por registrar (gitignored)
├── scripts/cron-run.sh         # wrapper: lock, umask, auto-rebuild, log rotation
├── Makefile
├── .env.example
└── go.mod
```

## Decisiones de diseño

- **RDAP por sobre WHOIS**: estándar IETF (RFC 7480–7484), respuestas JSON
  estructuradas, vive en el registry del TLD (sirve para cualquier dominio).
  WHOIS es texto libre y cada registry lo formatea distinto.
- **RDAP por apex, no por FQDN**: agrupamos dominios por eTLD+1
  (`golang.org/x/net/publicsuffix`). 147 FQDNs típicos colapsan a ~19 lookups
  RDAP. Subdominios heredan los datos del apex.
- **InsecureSkipVerify deliberado** en TLS check: necesitamos leer
  `NotAfter` incluso si el cert está vencido / chain rota / hostname
  mismatch. La validez del cert NO bloquea el monitor — eso ES lo que
  queremos detectar.
- **Estado idempotente, contenido completo**: una vez que se envía una
  alerta para `(domain, kind, threshold, expiresAt)`, no se reenvía hasta
  que la fecha cambie. PERO cada email que sí se manda incluye el cuadro
  COMPLETO de alertas vigentes (no solo las nuevas), para que el
  destinatario nunca tenga que correlacionar correos históricos.
- **Stack mínimo**: stdlib `net/http`, `crypto/tls`, `log/slog`, `encoding/json`.
  Externas: `gopkg.in/yaml.v3`, `aws-sdk-go-v2/route53*` (solo discovery),
  `golang.org/x/net/publicsuffix`, `joho/godotenv`, `xuri/excelize/v2`.
  Sin frameworks.

## Roadmap

Cubierto:
- ✅ Esqueleto + tipos + discovery estático con groups por registrar
- ✅ RDAP + fallback + mismatch detection
- ✅ TLS check con worker pool
- ✅ Alert engine + state JSON + SMTP/dry-run
- ✅ Route53 (apex + DNS records) + Name.com
- ✅ Tests, RDAP cache, healthcheck deadman, issuer en HTML, hardening
- ✅ Inventory XLSX adjunto al correo + email con cuadro completo
- ✅ Hardening on-prem (permisos, auto-rebuild, log rotation, eliminación
   del path Lambda/S3/SES/SSM)

Pendiente / a evaluar:
- Cloudflare discoverer (placeholder no implementado).
- CT logs (crt.sh) discovery — útil para descubrir subdominios con certs
  emitidos no listados en DNS público.
- Diff entre runs ("se agregó X, se quitó Y") usando inventarios históricos.
- Slack webhook como notificador alternativo.

## Licencia

[Definir antes de hacer público.]

## Antes de hacer público el repo

El repo está estructurado para ser commiteable hoy mismo (privado), y
preparado para hacerse público sin perder configuración. Lo que tenés que
hacer EN EL MOMENTO de cambiar la visibilidad:

### 1. Limpiar el historial de `configs/static_domains.yaml`

El archivo ya está gitignored, pero quedó en commits anteriores con los
dominios reales. Antes de cambiar a público, reescribir historial:

```bash
# Backup primero
git clone --mirror . ../auto-certs-backup.git

# Instalar git-filter-repo si no está
pip install --user git-filter-repo

# Borrar el archivo de toda la historia
git filter-repo --invert-paths --path configs/static_domains.yaml --force

# Verificar que ya no aparece
git log --all --full-history -- configs/static_domains.yaml   # debe estar vacío

# Force-push al remote (DESTRUCTIVO — coordinar con cualquiera que tenga clones)
git push origin --force --all
git push origin --force --tags
```

### 2. Verificar otros archivos sensibles

```bash
# Confirmar que .env nunca se commiteó
git log --all --full-history -- .env   # debe estar vacío

# Buscar tokens / claves olvidados en commits viejos
git log -p --all | grep -iE 'AKIA[0-9A-Z]{16}|api[_-]?key|password.*=|token.*='
```

### 3. Rotar cualquier secret que haya estado expuesto

Si encontrás algo en el historial: rotalo en el sistema correspondiente
(AWS IAM, Name.com, SMTP) y agregalo al `git filter-repo` antes de
force-push.

### 4. Agregar licencia + Code of Conduct

Para repo público, GitHub recomienda LICENSE (MIT/Apache 2.0/GPL) y
CODE_OF_CONDUCT.md. Crear ambos antes de cambiar visibilidad.

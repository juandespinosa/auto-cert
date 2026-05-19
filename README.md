# auto-certs

Monitor de expiración de **certificados TLS** y **registros de dominio**, con
alertas vía email (SMTP o AWS SES). Corre como CLI local o como AWS Lambda
disparada por EventBridge cron.

```
            ┌─────────────────────────────────────────────────────────┐
            │  EventBridge (diario / mensual)                         │
            └────────────────────────┬────────────────────────────────┘
                                     │ trigger
                                     ▼
┌────────────────────────────────────────────────────────────────────────┐
│  Lambda (provided.al2023, arm64)  ───  o el CLI cmd/monitor en local   │
│                                                                        │
│    Discovery     ──▶  RDAP      ──▶  TLS check    ──▶  Alert engine    │
│    (Route53,           (apex,         (handshake        (thresholds,   │
│     Name.com,           eTLD+1,        InsecureSkip      mismatch,     │
│     YAML estático)      cacheable)     para leer         expired)      │
│                                        certs rotos)                    │
│                                                                        │
│         ▼                                                              │
│    Inventory snapshot  ──▶  State filter  ──▶  Notifier (SES/SMTP)     │
│    (S3 / file, dated)        (dedup por          → HTML + plain text   │
│                               domain×kind×thr)                         │
└────────────────────────────────────────────────────────────────────────┘
```

## ¿Por qué existe?

Administrar dominios entre varios registradores (Route53, Name.com, mi.com.co,
Marcaria, NetworkSolutions, etc.) significa que cualquiera puede vencerse sin
aviso. Las herramientas comerciales son caras o sólo cubren un registrador;
parsear WHOIS es frágil. **auto-certs** descubre dominios automáticamente,
resuelve la fecha de expiración vía **RDAP** (estándar IETF, independiente
del registrar), hace handshake TLS para leer el `NotAfter` del certificado y
manda un correo con resumen accionable.

## Features

- **Múltiples discoverers**: Route53 (apex + DNS records), Name.com, listas
  estáticas YAML por registrar (mi.com.co, Marcaria, NetworkSolutions, etc.).
- **RDAP universal** para vencimiento de dominio — funciona con cualquier TLD
  que IANA exponga en `data.iana.org/rdap/dns.json`. Cache opcional en S3.
- **Fallback YAML por dominio** para TLDs sin RDAP (.co, .com.co): se
  compara contra el valor del registrar si RDAP funciona y se emite alerta
  de inconsistencia ante divergencias.
- **Lectura de certs incluso vencidos**: TLS handshake con `InsecureSkipVerify`
  para poder alertar sobre certs caducados o rotos.
- **Reporte HTML**: banner color-coded (rojo/ámbar/violeta/verde), TL;DR de
  acción inmediata, tablas detalladas, breakdown por origen, badges por CA
  emisora (Let's Encrypt / Amazon / Sectigo / etc.).
- **Idempotencia**: state JSON (local o S3) evita reenviar la misma alerta
  hasta que la fecha cambie (renovación detectada automáticamente).
- **Deadman switch** opcional (healthchecks.io / cronitor.io) — si la Lambda
  deja de correr, el servicio externo alerta.
- **Dry-run**: emite el correo a stdout + HTML a disco sin enviar SMTP/SES.

## Quick start (local)

```bash
# 1. Clonar y configurar
git clone <repo>
cd auto-certs
cp .env.example .env                                              # completar con valores reales
cp configs/static_domains.example.yaml configs/static_domains.yaml   # editar con los dominios reales

# 2. Probar con dry-run (no envía correos, escribe HTML a disco)
go run ./cmd/monitor -config configs/config.yaml -dry-run

# 3. Ver el HTML del reporte
open state/last-email.html   # macOS
xdg-open state/last-email.html   # Linux
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

## Configuración

Archivo único [configs/config.yaml](configs/config.yaml) (local) y
[configs/config.lambda.yaml](configs/config.lambda.yaml) (bundle con la
Lambda). Estructura:

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
  cache: { backend: file | s3 | none, ttl: 168h, ... }

notifier: { backend: dryrun | smtp | ses, ... }
state:    { backend: file | s3, ... }
inventory:{ backend: file | s3, ... }   # snapshot completo por corrida
secrets:  { backend: env | ssm, ... }
healthcheck: { url: ..., timeout: 5s }  # opcional deadman
```

Los `${VAR}` se expanden contra el entorno. En local los carga `.env`; en
Lambda los carga SSM Parameter Store al startup. Ver `.env.example`.

## Discoverers

| Discoverer | Trae | Auth |
|---|---|---|
| **route53** | Dominios registrados vía AWS + apex + A/AAAA/CNAME de cada hosted zone pública | IAM role |
| **namecom** | Dominios de la cuenta Name.com + DNS records hospedados ahí | Basic auth (`username:token`) |
| **static** | Lista manual en YAML (mi.com.co, Marcaria, NetworkSolutions, …) | — |
| ~~cloudflare~~ | Placeholder, no implementado | — |

### Static discoverer

El archivo real está **gitignored** (contiene inventario sensible: nombres de
dominios, registrador, fechas de vencimiento). El template versionado vive en
[configs/static_domains.example.yaml](configs/static_domains.example.yaml).
Cada deploy mantiene su propia copia en `configs/static_domains.yaml`.

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
  correo (breakdown por origen).
- `expiry_fallback` solo se usa si RDAP falla para el dominio. Si RDAP
  funciona, se compara y se emite `AlertDomainMismatch` ante divergencia
  (sujeto a `rdap.mismatch_tolerance_days`).
- `subdomains` es azúcar: cada prefijo se expande a un FQDN aparte, comparte
  `port` y `source` con el apex pero NO el `expiry_fallback` (dominio se
  renueva una sola vez para todo).

## Email report

Cada corrida produce un email HTML (+ plain text fallback). Layout en orden
de urgencia:

1. **Banner color-coded** según peor estado: rojo (vencidos) → ámbar
   (próximos) → violeta (inconsistencias RDAP/YAML) → verde (todo OK).
2. **TL;DR** con bullets accionables.
3. **Tablas**: Vencidos → Por vencer → Inconsistencias.
4. **Estado general** (al final, contexto): KPIs de inventario (total / sanos
   / con alerta / sin cert) + tabla "Cobertura por origen".

Subject dinámico:
- `[auto-certs] N vencido(s) + M próximo(s) — acción requerida`
- `[auto-certs] M dominios por vencer en ≤30 días`
- `[auto-certs] Sin alertas — X/Y sanos`

## Deploy a AWS Lambda

Guía paso a paso: [infra/README.md](infra/README.md).

TL;DR:
```bash
cd infra/
sam build --template template.yaml
sam deploy --guided
# Después: cargar secrets reales vía `aws ssm put-parameter --type SecureString ...`
```

Costo estimado: **<$0.02/mes** para 30 runs/mes con ~150 dominios.

## Development

```bash
make local-build       # go build ./...
make local-vet         # go vet ./...
make local-test        # go test ./...
make dry-run           # corrida local sin enviar
make sam-validate      # valida el SAM template
make sam-build         # build de la Lambda (cross-compile arm64 + bundle)
make sam-deploy        # build + deploy
```

Tests: ~40 unitarios cubriendo motor de alertas, summary, RDAP enrich
(grouping por apex, tolerance, cache), publicsuffix lookups, parser de DN.

## Estructura

```
auto-certs/
├── cmd/
│   ├── monitor/      # CLI local
│   └── lambda/       # Lambda handler (mismo pipeline, JSON logging)
├── internal/
│   ├── alert/        # Engine puro: certs/infos + thresholds → []Alert
│   ├── config/       # YAML config con ${VAR} expansion
│   ├── discovery/    # Route53, Name.com, static YAML, agregador con dedup
│   ├── healthcheck/  # Pinger deadman (healthchecks.io / cronitor.io)
│   ├── inventory/    # Snapshot completo por corrida (file | s3)
│   ├── model/        # Tipos compartidos (Domain, CertInfo, DomainInfo, Alert)
│   ├── notify/       # Render HTML/plain, SMTP, SES, DryRun
│   ├── rdap/         # Bootstrap IANA, lookup por apex, cache opcional (file | s3)
│   ├── runner/       # Orquesta el pipeline; ambos cmd/ lo importan
│   ├── secrets/      # Loaders: .env local, SSM Parameter Store en Lambda
│   ├── state/        # Dedup de alertas enviadas (file | s3)
│   └── tlscheck/     # Worker pool, handshake con InsecureSkipVerify
├── configs/
│   ├── config.yaml           # local
│   ├── config.lambda.yaml    # bundle Lambda
│   └── static_domains.yaml   # dominios manuales por registrar
├── infra/
│   ├── template.yaml         # SAM template (Lambda + S3 + EventBridge + IAM + SSM)
│   └── README.md             # Guía de despliegue
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
- **Estado idempotente**: una vez que se envía una alerta para
  `(domain, kind, threshold, expiresAt)`, no se reenvía. Si `expiresAt`
  cambia (renovación), las alertas vuelven a poder dispararse
  automáticamente.
- **Stack mínimo**: stdlib `net/http`, `crypto/tls`, `log/slog`, `encoding/json`.
  Externas: `gopkg.in/yaml.v3`, `aws-sdk-go-v2`, `cloudflare/aws-lambda-go`,
  `golang.org/x/net/publicsuffix`, `joho/godotenv`. Sin frameworks.

## Roadmap

Cubierto:
- ✅ Hito 1: esqueleto + tipos
- ✅ Hito 2: discovery estático con groups por registrar
- ✅ Hito 2.5: RDAP + fallback + mismatch detection
- ✅ Hito 3: TLS check con worker pool
- ✅ Hito 4: alert engine + state JSON + SMTP/dry-run
- ✅ Hito 5: Route53 (apex + DNS records)
- ✅ Hito 6: Name.com
- ✅ Hito 7: Lambda-ready (S3 state, SSM secrets, SES, SAM template)
- ✅ Hito 8: Tests, RDAP cache, healthcheck deadman, issuer en HTML, hardening

Pendiente / a evaluar:
- Cloudflare discoverer (placeholder no implementado).
- CT logs (crt.sh) discovery — útil para descubrir subdominios con certs
  emitidos no listados en DNS público.
- Diff entre runs ("se agregó X, se quitó Y") usando inventarios históricos
  en S3.
- CloudWatch custom metrics (sanos%, alertas activas) para dashboards de
  tendencia.
- Slack webhook como notificador alternativo.

## Licencia

[Definir antes de hacer público.]

## Antes de hacer público el repo

El repo está estructurado para ser commiteable hoy mismo (privado), y
preparado para hacerse público sin perder configuración. Lo que tenés que
hacer EN EL MOMENTO de cambiar la visibilidad:

### 1. Limpiar el historial de `configs/static_domains.yaml`

El archivo ya está gitignored a partir de este commit, pero quedó en
commits anteriores con los dominios reales. Antes de cambiar a público,
reescribir historial:

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

# Revisar samconfig.toml si lo tenés (lo crea `sam deploy --guided`).
# Suele contener account-id y bucket names — decidir si es sensible.

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

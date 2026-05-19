package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Discovery   DiscoveryConfig   `yaml:"discovery"`
	TLS         TLSConfig         `yaml:"tls"`
	RDAP        RDAPConfig        `yaml:"rdap"`
	Thresholds  []int             `yaml:"thresholds"`
	Secrets     SecretsConfig     `yaml:"secrets"`
	Notifier    NotifierConfig    `yaml:"notifier"`
	State       StateConfig       `yaml:"state"`
	Inventory   InventoryConfig   `yaml:"inventory"`
	Healthcheck HealthcheckConfig `yaml:"healthcheck"`
}

// HealthcheckConfig — opcional deadman-switch ping al final del run. Pensado
// para healthchecks.io / cronitor.io / endpoint propio. Si no se completa la
// URL, el ping se omite silenciosamente.
type HealthcheckConfig struct {
	URL        string        `yaml:"url"`         // ej. https://hc-ping.com/<uuid>
	FailureURL string        `yaml:"failure_url"` // opcional; si vacío, derivar de URL agregando /fail
	Timeout    time.Duration `yaml:"timeout"`
}

type DiscoveryConfig struct {
	Route53    Route53Config    `yaml:"route53"`
	Cloudflare CloudflareConfig `yaml:"cloudflare"`
	NameCom    NameComConfig    `yaml:"namecom"`
	Static     StaticConfig     `yaml:"static"`
}

type Route53Config struct {
	Enabled      bool     `yaml:"enabled"`
	Region       string   `yaml:"region"`
	Profile      string   `yaml:"profile"`
	ExcludeZones []string `yaml:"exclude_zones"`
}

type CloudflareConfig struct {
	Enabled  bool   `yaml:"enabled"`
	APIToken string `yaml:"api_token"`
}

type NameComConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Token    string `yaml:"token"`
	BaseURL  string `yaml:"base_url"`
}

type StaticConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type TLSConfig struct {
	Timeout     time.Duration `yaml:"timeout"`
	Workers     int           `yaml:"workers"`
	DefaultPort int           `yaml:"default_port"`
}

// RDAPConfig — tuning for the registry-expiration lookup step.
type RDAPConfig struct {
	// EnrichWorkers is the pool size for parallel RDAP lookups (one per
	// registrable domain, not per FQDN). Default 5: conservative to avoid
	// upsetting smaller TLD registries.
	EnrichWorkers int `yaml:"enrich_workers"`
	// MismatchToleranceDays controls when an RDAP vs YAML-fallback date
	// disagreement triggers AlertDomainMismatch. Fires when
	// |rdap - fallback| > tolerance. 0 = strict (1d off reports). 1 silences
	// the common "registrar UI shows local-tz date, registry returns UTC"
	// off-by-one.
	MismatchToleranceDays int `yaml:"mismatch_tolerance_days"`
	// Cache opciones: si backend != "none", las respuestas RDAP exitosas se
	// cachean por TTL y los siguientes runs evitan el round-trip HTTP.
	Cache RDAPCacheConfig `yaml:"cache"`
}

type RDAPCacheConfig struct {
	Backend string         `yaml:"backend"` // "none" (default) | "file" | "s3"
	TTL     time.Duration  `yaml:"ttl"`     // default 168h (7 días)
	Path    string         `yaml:"path"`    // si backend=file
	S3      RDAPCacheS3    `yaml:"s3"`      // si backend=s3
}

type RDAPCacheS3 struct {
	Bucket string `yaml:"bucket"`
	Key    string `yaml:"key"`
	Region string `yaml:"region"`
}

// SecretsConfig — backend "env" reads .env at startup (local). "ssm" reads
// every parameter under SSMPrefix and exports them as env vars (Lambda).
type SecretsConfig struct {
	Backend   string `yaml:"backend"`    // "env" (default) | "ssm"
	DotenvPath string `yaml:"dotenv_path"` // default ".env"
	SSMPrefix string `yaml:"ssm_prefix"` // required if backend=ssm
	SSMRegion string `yaml:"ssm_region"` // optional; defaults to AWS_REGION
}

// NotifierConfig picks how alerts are delivered. "dryrun" prints the email
// to stdout (and writes the HTML to disk) — used for local development.
type NotifierConfig struct {
	Backend string     `yaml:"backend"` // "dryrun" | "smtp" | "ses"
	SMTP    SMTPConfig `yaml:"smtp"`
	SES     SESConfig  `yaml:"ses"`
	DryRun  DryRunConfig `yaml:"dryrun"`
}

type SMTPConfig struct {
	Host     string   `yaml:"host"`
	Port     int      `yaml:"port"`
	Username string   `yaml:"username"`
	Password string   `yaml:"password"`
	From     string   `yaml:"from"`
	To       []string `yaml:"to"`
}

type SESConfig struct {
	From   string   `yaml:"from"`
	To     []string `yaml:"to"`
	Region string   `yaml:"region"` // optional, defaults to AWS_REGION
}

type DryRunConfig struct {
	HTMLPath string `yaml:"html_path"` // writes the HTML body here for inspection
}

// StateConfig — alert-dedup persistence.
//   backend=file: path on local disk.
//   backend=s3:   bucket + key.
type StateConfig struct {
	Backend string `yaml:"backend"` // "file" (default) | "s3"
	Path    string `yaml:"path"`    // when backend=file
	S3      StateS3Config `yaml:"s3"`
}

type StateS3Config struct {
	Bucket string `yaml:"bucket"`
	Key    string `yaml:"key"`
	Region string `yaml:"region"`
}

// InventoryConfig — full snapshot persistence (written every run).
//   backend=file: single path, overwritten.
//   backend=s3:   one dated object per run under key_prefix (history).
type InventoryConfig struct {
	Backend string `yaml:"backend"` // "file" (default) | "s3"
	Path    string `yaml:"path"`    // when backend=file
	S3      InventoryS3Config `yaml:"s3"`
}

type InventoryS3Config struct {
	Bucket    string `yaml:"bucket"`
	KeyPrefix string `yaml:"key_prefix"`
	Region    string `yaml:"region"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	// Expand ${VAR} / $VAR placeholders against the process environment so
	// secrets can come from .env, SSM, or shell exports without changing the
	// YAML shape.
	expanded := os.ExpandEnv(string(data))
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.TLS.DefaultPort == 0 {
		c.TLS.DefaultPort = 443
	}
	if c.TLS.Workers == 0 {
		c.TLS.Workers = 30
	}
	if c.TLS.Timeout == 0 {
		c.TLS.Timeout = 10 * time.Second
	}
	if c.RDAP.EnrichWorkers == 0 {
		c.RDAP.EnrichWorkers = 5
	}
	if c.RDAP.Cache.Backend == "" {
		c.RDAP.Cache.Backend = "none"
	}
	if c.RDAP.Cache.TTL == 0 {
		c.RDAP.Cache.TTL = 7 * 24 * time.Hour
	}
	if len(c.Thresholds) == 0 {
		c.Thresholds = []int{30, 15, 7, 3}
	}
	if c.Secrets.Backend == "" {
		c.Secrets.Backend = "env"
	}
	if c.Secrets.DotenvPath == "" {
		c.Secrets.DotenvPath = ".env"
	}
	if c.Notifier.Backend == "" {
		c.Notifier.Backend = "dryrun"
	}
	if c.Notifier.DryRun.HTMLPath == "" {
		c.Notifier.DryRun.HTMLPath = "state/last-email.html"
	}
	if c.State.Backend == "" {
		c.State.Backend = "file"
	}
	if c.State.Path == "" {
		c.State.Path = "state/alerts.json"
	}
	if c.Inventory.Backend == "" {
		c.Inventory.Backend = "file"
	}
	if c.Inventory.Path == "" {
		c.Inventory.Path = "state/inventory.json"
	}
}

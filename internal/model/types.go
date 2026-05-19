package model

import "time"

// Domain is a target produced by a Discoverer.
type Domain struct {
	Name   string // FQDN, e.g. "example.com"
	Port   int    // TLS port; defaults to TLS.DefaultPort when zero
	Source string // discoverer that produced it: "route53", "cloudflare", "namecom", "static"

	// ExpiryFallback is used only when RDAP fails for this domain. RDAP is
	// always the primary source; the YAML value never wins over a successful
	// RDAP lookup.
	ExpiryFallback *time.Time
}

// CertInfo is the outcome of a TLS handshake against a Domain.
type CertInfo struct {
	Domain    string
	Port      int
	NotAfter  time.Time
	Issuer    string
	Subject   string
	DNSNames  []string
	CheckedAt time.Time
	Err       error
}

// DomainInfo is the registry-level expiration for a domain (via RDAP or fallback).
type DomainInfo struct {
	Domain    string
	ExpiresAt time.Time
	Source    string // "rdap", "fallback"
	CheckedAt time.Time
	Err       error

	// IsApex marks the alert-emitting representative of an apex group:
	// usually the FQDN that equals its eTLD+1 (e.g., "bodytech.com.es"), or
	// the first subdomain when the apex itself isn't in the inventory.
	// Subdomain infos (IsApex=false) carry the same ExpiresAt — useful for
	// the inventory snapshot — but the alert engine skips them so domain
	// alerts fire once per registration, not once per FQDN.
	IsApex bool

	// FallbackExpected is set only when RDAP succeeded AND the YAML
	// expiry_fallback was also present AND the two dates disagree by more
	// than the configured tolerance. Only set on the apex info.
	FallbackExpected time.Time
}

// AlertKind tags what an Alert is about.
type AlertKind string

const (
	AlertCertExpiring   AlertKind = "cert_expiring"
	AlertCertExpired    AlertKind = "cert_expired"
	AlertDomainExpiring AlertKind = "domain_expiring"
	AlertDomainExpired  AlertKind = "domain_expired"
	// AlertDomainMismatch fires when RDAP and the YAML expiry_fallback both
	// report a date but they differ by more than 1 day. Surfaces user-entry
	// typos or registrar/registry sync issues.
	AlertDomainMismatch AlertKind = "domain_mismatch"
)

// Alert is one triggered notification for a single (domain, kind, threshold).
type Alert struct {
	Domain    string
	Source    string // discoverer that found the domain: "static", "route53", "namecom"
	Kind      AlertKind
	ExpiresAt time.Time
	DaysLeft  int
	Threshold int

	// Issuer is the raw cert issuer DN (e.g., "CN=R3,O=Let's Encrypt,C=US").
	// Only set for cert-kind alerts; empty for domain alerts. The render
	// layer extracts a short label for display.
	Issuer string

	// FallbackExpected is set only for AlertDomainMismatch: the date the
	// registrar (YAML fallback) provided, which disagrees with ExpiresAt
	// (RDAP). Zero for all other kinds.
	FallbackExpected time.Time
}

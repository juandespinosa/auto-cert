// Package notify renders the alert summary and delivers it via SMTP or stdout.
// Rendering is split into its own file so the SMTP and dry-run implementations
// share the exact same plain-text body.
package notify

import (
	"fmt"
	"strings"
	"time"

	"auto-certs/internal/model"
)

// Render returns (subject, plain-text body). Layout mirrors the HTML:
// status banner + TL;DR up top, action sections, then inventory context.
// Subject is built dynamically — the urgency level is in the first words so
// the reader sees it without opening the email.
func Render(alerts []model.Alert, summary Summary, now time.Time) (string, string) {
	expired, expiring, mismatch := bucketAndDedup(alerts)
	subject := buildSubject(len(expired), len(expiring), len(mismatch), summary)
	_, _, label := emailStatus(len(expired), len(expiring), len(mismatch))

	var b strings.Builder

	// Banner (plain)
	fmt.Fprintf(&b, "[%s]\n", strings.ToUpper(label))
	fmt.Fprintf(&b, "Reporte auto-certs — %s UTC\n\n", now.UTC().Format("2006-01-02 15:04"))

	// TL;DR
	b.WriteString("== RESUMEN ==\n")
	for _, line := range tldrPlainLines(expired, expiring, mismatch, summary) {
		fmt.Fprintf(&b, "  • %s\n", line)
	}
	b.WriteString("\n")

	// Action sections in urgency order
	if len(expired) > 0 {
		b.WriteString("== VENCIDOS ==\n")
		for _, a := range expired {
			fmt.Fprintf(&b, "  - %s (%s) — venció hace %d días (expiraba %s)\n",
				a.Domain, shortKind(a.Kind), -a.DaysLeft,
				a.ExpiresAt.Format("2006-01-02"))
		}
		b.WriteString("\n")
	}
	if len(expiring) > 0 {
		b.WriteString("== PRÓXIMOS A VENCER ==\n")
		for _, a := range expiring {
			fmt.Fprintf(&b, "  - %s (%s) — %d días restantes (vence %s) [umbral %d]\n",
				a.Domain, shortKind(a.Kind), a.DaysLeft,
				a.ExpiresAt.Format("2006-01-02"), a.Threshold)
		}
		b.WriteString("\n")
	}
	if len(mismatch) > 0 {
		b.WriteString("== INCONSISTENCIAS RDAP vs REGISTRADOR ==\n")
		for _, a := range mismatch {
			fmt.Fprintf(&b, "  - %s — RDAP %s vs YAML %s (diferencia %+d días)\n",
				a.Domain,
				a.ExpiresAt.Format("2006-01-02"),
				a.FallbackExpected.Format("2006-01-02"),
				a.DaysLeft)
		}
		b.WriteString("\n")
	}

	// Inventory context (al final)
	b.WriteString("== ESTADO GENERAL ==\n")
	fmt.Fprintf(&b, "  Total: %d  (apex: %d, subdominios: %d)\n",
		summary.Total, summary.Apex, summary.Subdomain)
	fmt.Fprintf(&b, "  Sanos: %d   Con alerta: %d   Sin cert: %d\n",
		summary.Healthy, summary.Alerted, summary.NoCertData)
	if len(summary.BySource) > 0 {
		b.WriteString("\n  Por origen:\n")
		for _, ss := range summary.BySource {
			src := ss.Source
			if src == "" {
				src = "—"
			}
			fmt.Fprintf(&b, "    %-22s  total=%-3d  apex/sub=%d/%d  sanos=%-3d  alerta=%-3d  sin_cert=%d\n",
				src, ss.Total, ss.Apex, ss.Subdomain, ss.Healthy, ss.Alerted, ss.NoCertData)
		}
	}
	return subject, b.String()
}

// buildSubject picks a one-line subject sized to the most urgent signal.
// The reader sees the urgency before opening the email.
func buildSubject(expired, expiring, mismatch int, summary Summary) string {
	switch {
	case expired > 0:
		if expiring > 0 {
			return fmt.Sprintf("[auto-certs] %d vencido(s) + %d próximo(s) — acción requerida",
				expired, expiring)
		}
		return fmt.Sprintf("[auto-certs] %d vencido(s) — acción requerida", expired)
	case expiring > 0:
		return fmt.Sprintf("[auto-certs] %d dominio(s) por vencer en ≤30 días", expiring)
	case mismatch > 0:
		return fmt.Sprintf("[auto-certs] %d inconsistencia(s) RDAP vs registrador", mismatch)
	default:
		return fmt.Sprintf("[auto-certs] Sin alertas — %d/%d sanos", summary.Healthy, summary.Total)
	}
}

// tldrPlainLines is the plain-text equivalent of tldrLines (no HTML tags).
func tldrPlainLines(expired, expiring, mismatch []model.Alert, summary Summary) []string {
	var lines []string
	if n := len(expired); n > 0 {
		lines = append(lines, fmt.Sprintf("%d vencido(s) — acción inmediata: %s",
			n, firstDomains(expired, 2)))
	}
	if n := len(expiring); n > 0 {
		lines = append(lines, fmt.Sprintf("%d por vencer en ≤30 días: %s",
			n, firstDomains(expiring, 2)))
	}
	if n := len(mismatch); n > 0 {
		lines = append(lines, fmt.Sprintf(
			"%d inconsistencia(s) entre RDAP y registrador (revisar YAML).", n))
	}
	if len(lines) == 0 {
		lines = append(lines, fmt.Sprintf("Sin alertas nuevas. %d/%d dominios sanos.",
			summary.Healthy, summary.Total))
	}
	return lines
}

func shortKind(k model.AlertKind) string {
	switch k {
	case model.AlertCertExpiring, model.AlertCertExpired:
		return "cert"
	case model.AlertDomainExpiring, model.AlertDomainExpired:
		return "domain"
	}
	return string(k)
}

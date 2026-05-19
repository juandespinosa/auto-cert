package notify

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"auto-certs/internal/model"
)

// RenderHTML produces an HTML body suited for email clients (table layout +
// inline styles for Outlook/Gmail compatibility). Layout puts urgency first:
//
//   1. Color-coded status banner (rojo|ámbar|violeta|verde).
//   2. TL;DR bullets — one-glance action items.
//   3. Detail tables in priority order: Vencidos → Por vencer → Inconsistencias.
//   4. Inventory context (4 KPIs + per-source table) at the bottom.
//   5. Footer.
//
// Alerts that share (domain, kind) but differ by threshold collapse to the
// smallest matching threshold.
func RenderHTML(alerts []model.Alert, summary Summary, now time.Time) string {
	expired, expiring, mismatch := bucketAndDedup(alerts)
	level, accent, label := emailStatus(len(expired), len(expiring), len(mismatch))

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1"></head>`)
	b.WriteString(`<body style="margin:0;padding:0;background:#f5f6f8;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#111827;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0"><tr><td align="center" style="padding:32px 12px;">`)
	b.WriteString(`<table role="presentation" width="720" cellspacing="0" cellpadding="0" border="0" style="max-width:720px;width:100%;background:#ffffff;border:1px solid #e5e7eb;border-radius:8px;overflow:hidden;">`)

	// 1. Status banner (color depende de urgencia)
	b.WriteString(statusBanner(accent, label, now))

	// 2. TL;DR — qué acción se requiere
	b.WriteString(tldrSection(expired, expiring, mismatch, summary))

	// 3. Detalle de alertas en orden de urgencia
	if len(expired) > 0 {
		b.WriteString(sectionTable(
			"Vencidos",
			"#dc2626",
			[]string{"Dominio", "Tipo", "Origen", "Emisor", "Días vencido", "Expiraba"},
			renderExpiredRows(expired),
		))
	}
	if len(expiring) > 0 {
		b.WriteString(sectionTable(
			"Por vencer",
			"#d97706",
			[]string{"Dominio", "Tipo", "Origen", "Emisor", "Días restantes", "Vence", "Umbral"},
			renderExpiringRows(expiring),
		))
	}
	if len(mismatch) > 0 {
		b.WriteString(sectionTable(
			"Inconsistencias RDAP vs registrador",
			"#7c3aed",
			[]string{"Dominio", "Origen", "RDAP", "Registrador (YAML)", "Diferencia"},
			renderMismatchRows(mismatch),
		))
	}

	// 4. Contexto general (inventario + por origen) — abajo, como referencia
	b.WriteString(divider())
	b.WriteString(sectionHeader("Estado general", "#0f172a"))
	b.WriteString(inventoryKPIs(summary))
	if len(summary.BySource) > 0 {
		b.WriteString(sourceTable(summary.BySource))
	}

	// 5. Footer
	b.WriteString(`<tr><td style="padding:18px 28px;background:#f9fafb;border-top:1px solid #e5e7eb;">`)
	b.WriteString(`<p style="margin:0;color:#6b7280;font-size:11px;line-height:1.5;">`)
	b.WriteString(`Reporte automático generado por auto-certs. No respondas a este correo.<br>`)
	b.WriteString(`Las alertas se disparan al cruzar los umbrales [30, 15, 7, 3] días y al vencer.`)
	b.WriteString(`</p></td></tr>`)

	b.WriteString(`</table></td></tr></table>`)
	b.WriteString(`</body></html>`)
	_ = level
	return b.String()
}

// emailStatus picks the headline accent based on what's the most urgent
// signal in this run. Order: vencidos > próximos > inconsistencias > ok.
func emailStatus(expired, expiring, mismatch int) (level, accent, label string) {
	switch {
	case expired > 0:
		return "critical", "#dc2626", "Acción requerida"
	case expiring > 0:
		return "warning", "#d97706", "Vencimientos próximos"
	case mismatch > 0:
		return "info", "#7c3aed", "Inconsistencias detectadas"
	default:
		return "ok", "#059669", "Todo en orden"
	}
}

func statusBanner(accent, label string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<tr><td style="background:%s;padding:28px;">`, accent)
	fmt.Fprintf(&b,
		`<div style="color:rgba(255,255,255,0.85);font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:0.1em;">%s</div>`,
		html.EscapeString(label))
	b.WriteString(`<h1 style="margin:8px 0 0;color:#ffffff;font-size:22px;font-weight:600;letter-spacing:-0.01em;">Reporte auto-certs</h1>`)
	fmt.Fprintf(&b,
		`<div style="margin-top:6px;color:rgba(255,255,255,0.8);font-size:13px;">%s UTC</div>`,
		html.EscapeString(now.UTC().Format("Mon 02 Jan 2006, 15:04")))
	b.WriteString(`</td></tr>`)
	return b.String()
}

// tldrSection renders the headline bullets right under the banner so the
// reader gets the action items in one scan.
func tldrSection(expired, expiring, mismatch []model.Alert, summary Summary) string {
	lines := tldrLines(expired, expiring, mismatch, summary)
	var b strings.Builder
	b.WriteString(`<tr><td style="padding:22px 28px 18px;background:#fafbfc;border-bottom:1px solid #e5e7eb;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0">`)
	for _, line := range lines {
		b.WriteString(`<tr><td style="padding:4px 0;">`)
		b.WriteString(`<table role="presentation" cellspacing="0" cellpadding="0" border="0"><tr>`)
		b.WriteString(`<td valign="top" style="padding-right:10px;color:#9ca3af;font-size:14px;line-height:1.5;">•</td>`)
		fmt.Fprintf(&b, `<td style="font-size:14px;line-height:1.5;color:#111827;">%s</td>`, line)
		b.WriteString(`</tr></table></td></tr>`)
	}
	b.WriteString(`</table></td></tr>`)
	return b.String()
}

// tldrLines builds the bullet content. HTML inside is trusted (we control it);
// dynamic strings are escaped at construction.
func tldrLines(expired, expiring, mismatch []model.Alert, summary Summary) []string {
	var lines []string
	if n := len(expired); n > 0 {
		domain := html.EscapeString(firstDomains(expired, 2))
		lines = append(lines, fmt.Sprintf(
			`<strong style="color:#dc2626;">%d vencido(s)</strong> — acción inmediata: %s`,
			n, domain))
	}
	if n := len(expiring); n > 0 {
		domain := html.EscapeString(firstDomains(expiring, 2))
		lines = append(lines, fmt.Sprintf(
			`<strong style="color:#d97706;">%d por vencer</strong> en ≤30 días: %s`,
			n, domain))
	}
	if n := len(mismatch); n > 0 {
		lines = append(lines, fmt.Sprintf(
			`<strong style="color:#7c3aed;">%d inconsistencia(s)</strong> entre RDAP y registrador (revisar YAML).`,
			n))
	}
	if len(lines) == 0 {
		lines = append(lines, fmt.Sprintf(
			`Sin alertas nuevas. <strong style="color:#059669;">%d de %d</strong> dominios sanos.`,
			summary.Healthy, summary.Total))
	}
	return lines
}

// firstDomains formats up to n domain names from alerts, with "y N más" when
// truncated. Used inside TL;DR bullets.
func firstDomains(alerts []model.Alert, n int) string {
	if len(alerts) == 0 {
		return ""
	}
	cap := n
	if len(alerts) < cap {
		cap = len(alerts)
	}
	names := make([]string, 0, cap)
	for i := 0; i < cap; i++ {
		names = append(names, alerts[i].Domain)
	}
	out := strings.Join(names, ", ")
	if extra := len(alerts) - cap; extra > 0 {
		out += fmt.Sprintf(" y %d más", extra)
	}
	return out
}

// divider draws a horizontal rule + extra padding between the action area
// and the context section.
func divider() string {
	return `<tr><td style="padding:28px 28px 0;"><div style="height:1px;background:#e5e7eb;line-height:1px;">&nbsp;</div></td></tr>`
}

func sectionHeader(title, accent string) string {
	return fmt.Sprintf(
		`<tr><td style="padding:20px 28px 8px;">`+
			`<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0"><tr>`+
			`<td style="border-left:3px solid %s;padding-left:10px;">`+
			`<h2 style="margin:0;font-size:13px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.08em;">%s</h2>`+
			`</td></tr></table></td></tr>`,
		accent, html.EscapeString(title))
}

func inventoryKPIs(summary Summary) string {
	var b strings.Builder
	b.WriteString(`<tr><td style="padding:8px 28px 0;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0"><tr>`)
	b.WriteString(kpiCellSub(summary.Total, "Inventario",
		fmt.Sprintf("apex %d · sub %d", summary.Apex, summary.Subdomain), "#0f172a"))
	b.WriteString(kpiCellSub(summary.Healthy, "Sanos", "sin alertas ni errores", "#059669"))
	b.WriteString(kpiCellSub(summary.Alerted, "Con alerta", "≥ 1 alerta activa", "#dc2626"))
	b.WriteString(kpiCellSub(summary.NoCertData, "Sin cert", "TLS check falló", "#6b7280"))
	b.WriteString(`</tr></table></td></tr>`)
	return b.String()
}

// bucketAndDedup splits alerts into expired/expiring/mismatch and collapses
// entries that share (domain, kind) into a single representative (smallest
// threshold for expiring).
func bucketAndDedup(alerts []model.Alert) (expired, expiring, mismatch []model.Alert) {
	type key struct {
		domain string
		kind   model.AlertKind
	}
	byKey := map[key]model.Alert{}
	for _, a := range alerts {
		switch a.Kind {
		case model.AlertCertExpired, model.AlertDomainExpired:
			expired = append(expired, a)
		case model.AlertDomainMismatch:
			mismatch = append(mismatch, a)
		default:
			k := key{a.Domain, a.Kind}
			cur, ok := byKey[k]
			if !ok || a.Threshold < cur.Threshold {
				byKey[k] = a
			}
		}
	}
	for _, a := range byKey {
		expiring = append(expiring, a)
	}
	sort.Slice(expired, func(i, j int) bool {
		if expired[i].DaysLeft != expired[j].DaysLeft {
			return expired[i].DaysLeft < expired[j].DaysLeft
		}
		return expired[i].Domain < expired[j].Domain
	})
	sort.Slice(expiring, func(i, j int) bool {
		if expiring[i].DaysLeft != expiring[j].DaysLeft {
			return expiring[i].DaysLeft < expiring[j].DaysLeft
		}
		return expiring[i].Domain < expiring[j].Domain
	})
	sort.Slice(mismatch, func(i, j int) bool { return mismatch[i].Domain < mismatch[j].Domain })
	return
}

// kpiCellSub is a KPI card with a small subtitle line under the label.
func kpiCellSub(count int, label, subtitle, accent string) string {
	return fmt.Sprintf(
		`<td align="center" style="padding:18px 10px;border-right:1px solid #e5e7eb;width:25%%;">`+
			`<div style="font-size:26px;font-weight:600;color:%s;line-height:1;">%d</div>`+
			`<div style="margin-top:6px;font-size:11px;color:#6b7280;text-transform:uppercase;letter-spacing:0.05em;font-weight:600;">%s</div>`+
			`<div style="margin-top:3px;font-size:10.5px;color:#9ca3af;">%s</div>`+
			`</td>`,
		accent, count, html.EscapeString(label), html.EscapeString(subtitle))
}

// sourceTable renders the per-discoverer breakdown. Apex/Sub merged into a
// single column to keep the table mobile-friendly (6 cols instead of 7).
func sourceTable(stats []SourceStat) string {
	var b strings.Builder
	b.WriteString(`<tr><td style="padding:18px 28px 8px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0"><tr>`)
	b.WriteString(`<td style="border-left:3px solid #6b7280;padding-left:10px;">`)
	b.WriteString(`<h3 style="margin:0;font-size:12px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.06em;">Cobertura por origen</h3>`)
	b.WriteString(`</td></tr></table></td></tr>`)

	b.WriteString(`<tr><td style="padding:6px 28px 8px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="border-collapse:collapse;font-size:13px;">`)
	b.WriteString(`<thead><tr>`)
	for _, c := range []string{"Origen", "Total", "Apex/Sub", "Sanos", "Con alerta", "Sin cert"} {
		fmt.Fprintf(&b,
			`<th align="left" style="padding:8px 10px;border-bottom:1px solid #e5e7eb;font-size:11px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.05em;">%s</th>`,
			html.EscapeString(c))
	}
	b.WriteString(`</tr></thead><tbody>`)

	var tot, apex, sub, healthy, alerted, nocert int
	for i, ss := range stats {
		bg := "#ffffff"
		if i%2 == 1 {
			bg = "#fafbfc"
		}
		fmt.Fprintf(&b, `<tr style="background:%s;">`, bg)
		fmt.Fprintf(&b, `<td style="padding:9px 10px;border-bottom:1px solid #f1f5f9;">%s</td>`, sourceBadge(ss.Source))
		fmt.Fprintf(&b, `<td style="padding:9px 10px;border-bottom:1px solid #f1f5f9;color:#111827;font-weight:600;">%d</td>`, ss.Total)
		fmt.Fprintf(&b, `<td style="padding:9px 10px;border-bottom:1px solid #f1f5f9;color:#6b7280;">%d / %d</td>`, ss.Apex, ss.Subdomain)
		fmt.Fprintf(&b, `<td style="padding:9px 10px;border-bottom:1px solid #f1f5f9;color:#059669;font-weight:600;">%d</td>`, ss.Healthy)
		fmt.Fprintf(&b, `<td style="padding:9px 10px;border-bottom:1px solid #f1f5f9;color:#dc2626;font-weight:600;">%d</td>`, ss.Alerted)
		fmt.Fprintf(&b, `<td style="padding:9px 10px;border-bottom:1px solid #f1f5f9;color:#6b7280;">%d</td>`, ss.NoCertData)
		b.WriteString(`</tr>`)
		tot += ss.Total
		apex += ss.Apex
		sub += ss.Subdomain
		healthy += ss.Healthy
		alerted += ss.Alerted
		nocert += ss.NoCertData
	}
	if len(stats) > 1 {
		b.WriteString(`<tr style="background:#f9fafb;">`)
		b.WriteString(`<td style="padding:10px;border-top:2px solid #e5e7eb;font-size:11px;font-weight:700;color:#111827;text-transform:uppercase;letter-spacing:0.05em;">Total</td>`)
		fmt.Fprintf(&b, `<td style="padding:10px;border-top:2px solid #e5e7eb;color:#111827;font-weight:700;">%d</td>`, tot)
		fmt.Fprintf(&b, `<td style="padding:10px;border-top:2px solid #e5e7eb;color:#111827;font-weight:700;">%d / %d</td>`, apex, sub)
		fmt.Fprintf(&b, `<td style="padding:10px;border-top:2px solid #e5e7eb;color:#059669;font-weight:700;">%d</td>`, healthy)
		fmt.Fprintf(&b, `<td style="padding:10px;border-top:2px solid #e5e7eb;color:#dc2626;font-weight:700;">%d</td>`, alerted)
		fmt.Fprintf(&b, `<td style="padding:10px;border-top:2px solid #e5e7eb;color:#111827;font-weight:700;">%d</td>`, nocert)
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table></td></tr>`)
	return b.String()
}

func sectionTable(title, accent string, columns []string, rowsHTML string) string {
	var b strings.Builder
	b.WriteString(`<tr><td style="padding:24px 28px 8px;">`)
	fmt.Fprintf(&b,
		`<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0"><tr>`+
			`<td style="border-left:3px solid %s;padding-left:10px;">`+
			`<h2 style="margin:0;font-size:15px;font-weight:600;color:#111827;">%s</h2>`+
			`</td></tr></table>`,
		accent, html.EscapeString(title))
	b.WriteString(`</td></tr>`)

	b.WriteString(`<tr><td style="padding:8px 28px 4px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="border-collapse:collapse;font-size:13px;">`)
	b.WriteString(`<thead><tr>`)
	for _, c := range columns {
		fmt.Fprintf(&b,
			`<th align="left" style="padding:8px 10px;border-bottom:1px solid #e5e7eb;font-size:11px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.05em;">%s</th>`,
			html.EscapeString(c))
	}
	b.WriteString(`</tr></thead><tbody>`)
	b.WriteString(rowsHTML)
	b.WriteString(`</tbody></table></td></tr>`)
	return b.String()
}

func renderExpiredRows(alerts []model.Alert) string {
	var b strings.Builder
	for i, a := range alerts {
		bg := "#ffffff"
		if i%2 == 1 {
			bg = "#fafbfc"
		}
		fmt.Fprintf(&b, `<tr style="background:%s;">`, bg)
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#111827;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12.5px;">%s</td>`,
			html.EscapeString(a.Domain))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;">%s</td>`, kindBadge(a.Kind))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;">%s</td>`, sourceBadge(a.Source))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;">%s</td>`, issuerBadge(a.Issuer))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#dc2626;font-weight:600;">%d</td>`, -a.DaysLeft)
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#6b7280;">%s</td>`,
			html.EscapeString(a.ExpiresAt.Format("2006-01-02")))
		b.WriteString(`</tr>`)
	}
	return b.String()
}

func renderMismatchRows(alerts []model.Alert) string {
	var b strings.Builder
	for i, a := range alerts {
		bg := "#ffffff"
		if i%2 == 1 {
			bg = "#fafbfc"
		}
		fmt.Fprintf(&b, `<tr style="background:%s;">`, bg)
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#111827;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12.5px;">%s</td>`,
			html.EscapeString(a.Domain))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;">%s</td>`, sourceBadge(a.Source))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#6b7280;">%s</td>`,
			html.EscapeString(a.ExpiresAt.Format("2006-01-02")))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#6b7280;">%s</td>`,
			html.EscapeString(a.FallbackExpected.Format("2006-01-02")))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#7c3aed;font-weight:600;">%+d d</td>`, a.DaysLeft)
		b.WriteString(`</tr>`)
	}
	return b.String()
}

func renderExpiringRows(alerts []model.Alert) string {
	var b strings.Builder
	for i, a := range alerts {
		bg := "#ffffff"
		if i%2 == 1 {
			bg = "#fafbfc"
		}
		fmt.Fprintf(&b, `<tr style="background:%s;">`, bg)
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#111827;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12.5px;">%s</td>`,
			html.EscapeString(a.Domain))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;">%s</td>`, kindBadge(a.Kind))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;">%s</td>`, sourceBadge(a.Source))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;">%s</td>`, issuerBadge(a.Issuer))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#d97706;font-weight:600;">%d</td>`, a.DaysLeft)
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#6b7280;">%s</td>`,
			html.EscapeString(a.ExpiresAt.Format("2006-01-02")))
		fmt.Fprintf(&b, `<td style="padding:10px;border-bottom:1px solid #f1f5f9;color:#6b7280;">≤ %d días</td>`, a.Threshold)
		b.WriteString(`</tr>`)
	}
	return b.String()
}

// issuerBadge renders a compact label for the cert issuer. Domain alerts have
// no issuer, so the cell shows "—" in muted gray.
func issuerBadge(issuer string) string {
	label := issuerShort(issuer)
	if label == "" {
		return `<span style="color:#9ca3af;">—</span>`
	}
	// Auto-renewing CAs (ACM, Let's Encrypt) get a green tint to signal
	// "should renew on its own". Manual CAs stay neutral.
	var bg, fg string
	switch label {
	case "Amazon", "Let's Encrypt":
		bg, fg = "#ecfdf5", "#065f46"
	case "Sectigo", "DigiCert", "GoDaddy", "Comodo":
		bg, fg = "#fef3c7", "#92400e"
	default:
		bg, fg = "#f1f5f9", "#475569"
	}
	return fmt.Sprintf(
		`<span style="display:inline-block;padding:2px 8px;background:%s;color:%s;border-radius:4px;font-size:11px;font-weight:500;">%s</span>`,
		bg, fg, html.EscapeString(label))
}

// issuerShort extracts a human-friendly CA name from an x509 issuer DN like
// "CN=Sectigo Public Server Authentication CA OV R36,O=Sectigo Limited,C=GB".
// Prefers the Organization (O=) field, falls back to CN. Returns "" if the
// input is empty or unparseable.
func issuerShort(raw string) string {
	if raw == "" {
		return ""
	}
	o := extractRDN(raw, "O=")
	cn := extractRDN(raw, "CN=")
	candidate := o
	if candidate == "" {
		candidate = cn
	}
	if candidate == "" {
		return ""
	}
	// Collapse common verbose names to a recognizable brand.
	switch {
	case strings.HasPrefix(candidate, "Amazon"):
		return "Amazon"
	case strings.HasPrefix(candidate, "Sectigo"):
		return "Sectigo"
	case strings.HasPrefix(candidate, "Let's Encrypt"):
		return "Let's Encrypt"
	case strings.HasPrefix(candidate, "DigiCert"):
		return "DigiCert"
	case strings.HasPrefix(candidate, "GoDaddy"), strings.HasPrefix(candidate, "Starfield"):
		return "GoDaddy"
	case strings.HasPrefix(candidate, "Comodo"):
		return "Comodo"
	}
	return candidate
}

// extractRDN pulls a Relative Distinguished Name (e.g., "O=", "CN=") from a
// comma-separated DN, returning the value with surrounding whitespace removed.
func extractRDN(dn, prefix string) string {
	for _, p := range strings.Split(dn, ",") {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(p, prefix))
		}
	}
	return ""
}

func sourceBadge(source string) string {
	if source == "" {
		source = "—"
	}
	var bg, fg string
	switch source {
	case "route53":
		bg, fg = "#fff7ed", "#9a3412"
	case "namecom":
		bg, fg = "#eff6ff", "#1e3a8a"
	case "static":
		bg, fg = "#ecfdf5", "#065f46"
	case "mi.com.co":
		bg, fg = "#fef3c7", "#92400e"
	case "marcaria.com":
		bg, fg = "#fce7f3", "#9d174d"
	case "networksolutions.com":
		bg, fg = "#e0e7ff", "#3730a3"
	default:
		bg, fg = "#f1f5f9", "#475569"
	}
	return fmt.Sprintf(
		`<span style="display:inline-block;padding:2px 8px;background:%s;color:%s;border-radius:4px;font-size:11px;font-weight:500;letter-spacing:0.02em;">%s</span>`,
		bg, fg, html.EscapeString(source))
}

func kindBadge(k model.AlertKind) string {
	label := shortKind(k)
	var bg, fg string
	switch label {
	case "cert":
		bg, fg = "#dbeafe", "#1e40af"
	case "domain":
		bg, fg = "#ede9fe", "#5b21b6"
	default:
		bg, fg = "#f1f5f9", "#475569"
	}
	return fmt.Sprintf(
		`<span style="display:inline-block;padding:2px 8px;background:%s;color:%s;border-radius:4px;font-size:11px;font-weight:500;text-transform:uppercase;letter-spacing:0.03em;">%s</span>`,
		bg, fg, label)
}

package inventory

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// MarshalXLSX renders the snapshot as a single-sheet .xlsx. Pensado para
// abrirse en Excel / Google Sheets / Numbers sin pasos previos — fechas son
// fechas (no texto), `*_days_left` son números (ordenables), y la primera fila
// queda congelada + con autofiltro para que el destinatario filtre por origen,
// estado o días sin saber Excel avanzado.
func MarshalXLSX(snap Snapshot) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	sheet := "Inventario"
	idx, err := f.NewSheet(sheet)
	if err != nil {
		return nil, fmt.Errorf("xlsx new sheet: %w", err)
	}
	// Reemplazamos la hoja default ("Sheet1") por "Inventario".
	if err := f.DeleteSheet("Sheet1"); err != nil {
		return nil, fmt.Errorf("xlsx delete default sheet: %w", err)
	}
	f.SetActiveSheet(idx)

	headers := []string{
		"name", "port", "source", "expiry_fallback",
		"cert_not_after", "cert_days_left", "cert_issuer", "cert_subject",
		"cert_dns_names", "cert_checked_at", "cert_error",
		"domain_expires_at", "domain_days_left", "domain_source",
		"domain_checked_at", "domain_error",
		"alerts",
	}
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"374151"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
	})
	if err != nil {
		return nil, fmt.Errorf("xlsx header style: %w", err)
	}
	dateStyle, err := f.NewStyle(&excelize.Style{NumFmt: 14}) // m/d/yyyy
	if err != nil {
		return nil, fmt.Errorf("xlsx date style: %w", err)
	}

	for col, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(col+1, 1)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return nil, fmt.Errorf("xlsx header %s: %w", cell, err)
		}
	}
	endCol, _ := excelize.CoordinatesToCellName(len(headers), 1)
	if err := f.SetCellStyle(sheet, "A1", endCol, headerStyle); err != nil {
		return nil, fmt.Errorf("xlsx header style apply: %w", err)
	}

	for i, e := range snap.Entries {
		row := i + 2 // header en fila 1
		writes := []struct {
			col int
			val any
		}{
			{1, e.Name},
			{2, e.Port},
			{3, e.Source},
			{4, dateOrEmpty(e.ExpiryFallback)},
		}
		if e.Cert != nil {
			writes = append(writes,
				struct {
					col int
					val any
				}{5, dateOrEmptyT(e.Cert.NotAfter)},
				struct {
					col int
					val any
				}{6, daysLeftInt(snap.GeneratedAt, e.Cert.NotAfter)},
				struct {
					col int
					val any
				}{7, e.Cert.Issuer},
				struct {
					col int
					val any
				}{8, e.Cert.Subject},
				struct {
					col int
					val any
				}{9, strings.Join(e.Cert.DNSNames, "; ")},
				struct {
					col int
					val any
				}{10, dateOrEmptyT(e.Cert.CheckedAt)},
				struct {
					col int
					val any
				}{11, e.Cert.Error},
			)
		}
		if e.Domain != nil {
			writes = append(writes,
				struct {
					col int
					val any
				}{12, dateOrEmptyT(e.Domain.ExpiresAt)},
				struct {
					col int
					val any
				}{13, daysLeftInt(snap.GeneratedAt, e.Domain.ExpiresAt)},
				struct {
					col int
					val any
				}{14, e.Domain.Source},
				struct {
					col int
					val any
				}{15, dateOrEmptyT(e.Domain.CheckedAt)},
				struct {
					col int
					val any
				}{16, e.Domain.Error},
			)
		}
		writes = append(writes, struct {
			col int
			val any
		}{17, formatAlertsXLSX(e.Alerts)})

		for _, w := range writes {
			if w.val == nil {
				continue
			}
			cell, _ := excelize.CoordinatesToCellName(w.col, row)
			if err := f.SetCellValue(sheet, cell, w.val); err != nil {
				return nil, fmt.Errorf("xlsx cell %s: %w", cell, err)
			}
		}
	}

	// Aplicar formato de fecha a las columnas de fechas (D, E, J, L, O).
	dateCols := []int{4, 5, 10, 12, 15}
	if n := len(snap.Entries); n > 0 {
		for _, c := range dateCols {
			first, _ := excelize.CoordinatesToCellName(c, 2)
			last, _ := excelize.CoordinatesToCellName(c, n+1)
			if err := f.SetCellStyle(sheet, first, last, dateStyle); err != nil {
				return nil, fmt.Errorf("xlsx date style apply: %w", err)
			}
		}
	}

	// Congelar primera fila + autofiltro: el destinatario filtra/ordena al abrir.
	if err := f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	}); err != nil {
		return nil, fmt.Errorf("xlsx freeze: %w", err)
	}
	lastRow := len(snap.Entries) + 1
	if lastRow < 1 {
		lastRow = 1
	}
	endRange, _ := excelize.CoordinatesToCellName(len(headers), lastRow)
	if err := f.AutoFilter(sheet, "A1:"+endRange, []excelize.AutoFilterOptions{}); err != nil {
		return nil, fmt.Errorf("xlsx autofilter: %w", err)
	}

	// Ancho de columnas razonable — un autosize completo es caro en datasets
	// grandes y excelize no lo soporta directamente; estos valores quedaron
	// bien para los nombres típicos de dominio (~25-35 chars).
	widths := map[string]float64{
		"A": 38, "B": 6, "C": 16, "D": 14,
		"E": 14, "F": 8, "G": 22, "H": 32,
		"I": 40, "J": 14, "K": 28,
		"L": 14, "M": 8, "N": 12,
		"O": 14, "P": 28,
		"Q": 50,
	}
	for col, w := range widths {
		if err := f.SetColWidth(sheet, col, col, w); err != nil {
			return nil, fmt.Errorf("xlsx col width %s: %w", col, err)
		}
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("xlsx write: %w", err)
	}
	return buf.Bytes(), nil
}

func dateOrEmpty(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC()
}

func dateOrEmptyT(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func daysLeftInt(now, until time.Time) any {
	if until.IsZero() {
		return nil
	}
	return int(until.Sub(now).Hours() / 24)
}

func formatAlertsXLSX(alerts []AlertEntry) string {
	if len(alerts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(alerts))
	for _, a := range alerts {
		exp := ""
		if !a.ExpiresAt.IsZero() {
			exp = a.ExpiresAt.UTC().Format("2006-01-02")
		}
		parts = append(parts, fmt.Sprintf("%s@%dd(left=%d,exp=%s)",
			a.Kind, a.Threshold, a.DaysLeft, exp))
	}
	return strings.Join(parts, "; ")
}

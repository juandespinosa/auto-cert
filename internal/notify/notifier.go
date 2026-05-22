package notify

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"

	"auto-certs/internal/model"
)

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Attachment es un adjunto opcional del correo (ej. inventario .xlsx). El
// runner lo construye y se lo pasa al notifier (SMTP lo enchufa en
// multipart/mixed; DryRun solo loguea metadata).
type Attachment struct {
	Filename    string
	ContentType string // ej. "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	Data        []byte
}

// Notifier delivers alerts. Implementations must handle the empty-alerts case
// (typically by no-op'ing). Summary carries inventory-wide stats que el header
// del correo renderiza incluso si alerts está vacío. attachments puede ser nil.
type Notifier interface {
	Notify(alerts []model.Alert, summary Summary, attachments []Attachment) error
}

// DryRun writes the rendered email to W instead of sending it. The HTML body
// is written to HTMLPath (if set) for visual inspection; otherwise it's
// dumped to W under a separator. Los adjuntos solo se listan (no se duplican
// a disco — el FileSink ya los escribió).
type DryRun struct {
	W        io.Writer
	HTMLPath string // optional: write the HTML body to this file
}

func (d *DryRun) Notify(alerts []model.Alert, summary Summary, attachments []Attachment) error {
	now := time.Now()
	subject, plainBody := Render(alerts, summary, now)
	htmlBody := RenderHTML(alerts, summary, now)

	if _, err := fmt.Fprintf(d.W, "--- dry-run email (plain) ---\nSubject: %s\n\n%s", subject, plainBody); err != nil {
		return err
	}
	if d.HTMLPath != "" {
		if err := writeFile(d.HTMLPath, []byte(htmlBody)); err != nil {
			return fmt.Errorf("dry-run html write %s: %w", d.HTMLPath, err)
		}
		fmt.Fprintf(d.W, "--- html body written to %s (%d bytes) ---\n", d.HTMLPath, len(htmlBody))
	} else {
		fmt.Fprintf(d.W, "--- dry-run email (html) ---\n%s\n--- end ---\n", htmlBody)
	}
	for _, a := range attachments {
		fmt.Fprintf(d.W, "--- attachment: %s (%s, %d bytes) ---\n", a.Filename, a.ContentType, len(a.Data))
	}
	return nil
}

// SMTP sends a single RFC 822 message to all recipients. Uses PLAIN auth when
// Username is set; otherwise unauthenticated (test/local relay).
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
}

func (s *SMTP) Notify(alerts []model.Alert, summary Summary, attachments []Attachment) error {
	if len(alerts) == 0 {
		return nil
	}
	if s.Host == "" || len(s.To) == 0 || s.From == "" {
		return fmt.Errorf("smtp: missing host/from/to")
	}
	now := time.Now()
	subject, plain := Render(alerts, summary, now)
	htmlBody := RenderHTML(alerts, summary, now)
	msg, err := BuildMIME(s.From, s.To, subject, plain, htmlBody, attachments)
	if err != nil {
		return fmt.Errorf("smtp build message: %w", err)
	}
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	var auth smtp.Auth
	if s.Username != "" {
		auth = smtp.PlainAuth("", s.Username, s.Password, s.Host)
	}
	if err := smtp.SendMail(addr, auth, s.From, s.To, msg); err != nil {
		return fmt.Errorf("smtp send to %s: %w", addr, err)
	}
	return nil
}

// BuildMIME returns a properly formatted RFC 822 message. Without attachments
// uses multipart/alternative (plain + html); with attachments wraps that in a
// multipart/mixed so clients render the body normally Y muestran el adjunto
// abajo.
func BuildMIME(from string, to []string, subject, plain, htmlBody string, attachments []Attachment) ([]byte, error) {
	// 1. Construir el bloque multipart/alternative (text + html).
	var altBuf bytes.Buffer
	altWriter := multipart.NewWriter(&altBuf)

	plainHeader := textproto.MIMEHeader{}
	plainHeader.Set("Content-Type", "text/plain; charset=utf-8")
	plainHeader.Set("Content-Transfer-Encoding", "8bit")
	plainPart, err := altWriter.CreatePart(plainHeader)
	if err != nil {
		return nil, err
	}
	if _, err := plainPart.Write([]byte(strings.ReplaceAll(plain, "\n", "\r\n"))); err != nil {
		return nil, err
	}

	htmlHeader := textproto.MIMEHeader{}
	htmlHeader.Set("Content-Type", "text/html; charset=utf-8")
	htmlHeader.Set("Content-Transfer-Encoding", "8bit")
	htmlPart, err := altWriter.CreatePart(htmlHeader)
	if err != nil {
		return nil, err
	}
	if _, err := htmlPart.Write([]byte(strings.ReplaceAll(htmlBody, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := altWriter.Close(); err != nil {
		return nil, err
	}

	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\n", from)
	fmt.Fprintf(&msg, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	msg.WriteString("MIME-Version: 1.0\r\n")

	// Sin adjuntos: el mensaje ES multipart/alternative directamente.
	if len(attachments) == 0 {
		fmt.Fprintf(&msg, "Content-Type: multipart/alternative; boundary=%q\r\n", altWriter.Boundary())
		msg.WriteString("\r\n")
		msg.Write(altBuf.Bytes())
		return msg.Bytes(), nil
	}

	// Con adjuntos: multipart/mixed envuelve [alternative + adjuntos...].
	var mixedBuf bytes.Buffer
	mixedWriter := multipart.NewWriter(&mixedBuf)

	altHeader := textproto.MIMEHeader{}
	altHeader.Set("Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", altWriter.Boundary()))
	altPart, err := mixedWriter.CreatePart(altHeader)
	if err != nil {
		return nil, err
	}
	if _, err := altPart.Write(altBuf.Bytes()); err != nil {
		return nil, err
	}

	for _, a := range attachments {
		ah := textproto.MIMEHeader{}
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		ah.Set("Content-Type", fmt.Sprintf("%s; name=%q", ct, a.Filename))
		ah.Set("Content-Transfer-Encoding", "base64")
		ah.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", a.Filename))
		part, err := mixedWriter.CreatePart(ah)
		if err != nil {
			return nil, err
		}
		// base64 con line-wrap a 76 chars (RFC 2045).
		encoded := base64.StdEncoding.EncodeToString(a.Data)
		for i := 0; i < len(encoded); i += 76 {
			end := i + 76
			if end > len(encoded) {
				end = len(encoded)
			}
			if _, err := part.Write([]byte(encoded[i:end] + "\r\n")); err != nil {
				return nil, err
			}
		}
	}
	if err := mixedWriter.Close(); err != nil {
		return nil, err
	}

	fmt.Fprintf(&msg, "Content-Type: multipart/mixed; boundary=%q\r\n", mixedWriter.Boundary())
	msg.WriteString("\r\n")
	msg.Write(mixedBuf.Bytes())
	return msg.Bytes(), nil
}

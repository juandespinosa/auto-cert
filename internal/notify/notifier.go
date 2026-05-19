package notify

import (
	"bytes"
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

// Notifier delivers alerts. Implementations must handle the empty-alerts case
// (typically by no-op'ing). Summary carries inventory-wide stats that the
// email header renders even when alerts is empty.
type Notifier interface {
	Notify(alerts []model.Alert, summary Summary) error
}

// DryRun writes the rendered email to W instead of sending it. The HTML body
// is written to HTMLPath (if set) for visual inspection; otherwise it's
// dumped to W under a separator.
type DryRun struct {
	W        io.Writer
	HTMLPath string // optional: write the HTML body to this file
}

func (d *DryRun) Notify(alerts []model.Alert, summary Summary) error {
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

func (s *SMTP) Notify(alerts []model.Alert, summary Summary) error {
	if len(alerts) == 0 {
		return nil
	}
	if s.Host == "" || len(s.To) == 0 || s.From == "" {
		return fmt.Errorf("smtp: missing host/from/to")
	}
	now := time.Now()
	subject, plain := Render(alerts, summary, now)
	htmlBody := RenderHTML(alerts, summary, now)
	msg, err := buildMultipart(s.From, s.To, subject, plain, htmlBody)
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

// buildMultipart returns a properly formatted multipart/alternative RFC 822
// message: clients render the HTML part when available, fall back to text.
func buildMultipart(from string, to []string, subject, plain, htmlBody string) ([]byte, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	plainHeader := textproto.MIMEHeader{}
	plainHeader.Set("Content-Type", "text/plain; charset=utf-8")
	plainHeader.Set("Content-Transfer-Encoding", "8bit")
	plainPart, err := mw.CreatePart(plainHeader)
	if err != nil {
		return nil, err
	}
	if _, err := plainPart.Write([]byte(strings.ReplaceAll(plain, "\n", "\r\n"))); err != nil {
		return nil, err
	}

	htmlHeader := textproto.MIMEHeader{}
	htmlHeader.Set("Content-Type", "text/html; charset=utf-8")
	htmlHeader.Set("Content-Transfer-Encoding", "8bit")
	htmlPart, err := mw.CreatePart(htmlHeader)
	if err != nil {
		return nil, err
	}
	if _, err := htmlPart.Write([]byte(strings.ReplaceAll(htmlBody, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\n", from)
	fmt.Fprintf(&msg, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	msg.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&msg, "Content-Type: multipart/alternative; boundary=%q\r\n", mw.Boundary())
	msg.WriteString("\r\n")
	msg.Write(body.Bytes())
	return msg.Bytes(), nil
}

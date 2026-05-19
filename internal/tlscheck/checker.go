// Package tlscheck reads the leaf certificate of a TLS endpoint by completing
// a handshake. It does NOT validate the cert chain: an already-expired or
// otherwise invalid cert must still be readable so we can alert on it.
package tlscheck

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"auto-certs/internal/model"
)

const (
	defaultTimeout = 10 * time.Second
	defaultWorkers = 30
)

// Checker performs concurrent TLS leaf-cert lookups with a bounded worker pool.
type Checker struct {
	Timeout time.Duration
	Workers int
}

// New returns a Checker. Zero values fall back to defaults (30 workers, 10s).
func New(timeout time.Duration, workers int) *Checker {
	if workers <= 0 {
		workers = defaultWorkers
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Checker{Timeout: timeout, Workers: workers}
}

// Check performs a single handshake and returns the leaf cert metadata.
// On any failure (tcp, handshake, empty chain) Err is populated; the rest of
// the fields except Domain/Port/CheckedAt are zero.
func (c *Checker) Check(ctx context.Context, host string, port int) *model.CertInfo {
	info := &model.CertInfo{
		Domain:    host,
		Port:      port,
		CheckedAt: time.Now().UTC(),
	}
	if host == "" {
		info.Err = errors.New("empty host")
		return info
	}
	if port == 0 {
		port = 443
		info.Port = port
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))

	dialer := &net.Dialer{Timeout: c.Timeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		info.Err = fmt.Errorf("tcp dial %s: %w", addr, err)
		return info
	}
	// rawConn is wrapped below; closing tlsConn closes rawConn too.
	_ = rawConn.SetDeadline(time.Now().Add(c.Timeout))

	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true, // we want to read expired/invalid certs to alert on them
	})
	defer tlsConn.Close()

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		info.Err = fmt.Errorf("tls handshake %s: %w", addr, err)
		return info
	}

	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		info.Err = fmt.Errorf("no peer certificates from %s", addr)
		return info
	}
	leaf := certs[0]
	info.NotAfter = leaf.NotAfter
	info.Issuer = leaf.Issuer.String()
	info.Subject = leaf.Subject.String()
	info.DNSNames = leaf.DNSNames
	return info
}

// CheckAll runs Check across domains with the configured worker pool. A per-
// domain failure stays in CertInfo.Err and does not stop the others. The output
// slice preserves the input order.
func (c *Checker) CheckAll(ctx context.Context, domains []model.Domain) []model.CertInfo {
	out := make([]model.CertInfo, len(domains))
	if len(domains) == 0 {
		return out
	}
	workers := c.Workers
	if len(domains) < workers {
		workers = len(domains)
	}

	type job struct {
		idx int
		d   model.Domain
	}
	jobs := make(chan job)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				info := c.Check(ctx, j.d.Name, j.d.Port)
				out[j.idx] = *info
			}
		}()
	}

	for i, d := range domains {
		select {
		case jobs <- job{idx: i, d: d}:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return out
		}
	}
	close(jobs)
	wg.Wait()
	return out
}

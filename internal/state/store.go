// Package state persists "we already sent this alert" facts so repeated runs
// don't spam. Backend: FileStore (JSON en disco). Antes era pluggable con S3
// pero ese path se removió cuando el proyecto se quedó on-prem.
package state

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"auto-certs/internal/model"
)

// Record is a single (domain, kind, threshold) entry remembered after an
// alert was sent. ExpiresAt is captured so renewals re-arm the alert.
type Record struct {
	Domain    string          `json:"domain"`
	Kind      model.AlertKind `json:"kind"`
	Threshold int             `json:"threshold"`
	ExpiresAt time.Time       `json:"expires_at"`
	SentAt    time.Time       `json:"sent_at"`
}

// Store is the alert-dedup interface. Hoy hay un único backend (FileStore);
// la interfaz queda para no acoplar runner/build con un tipo concreto.
type Store interface {
	Filter(alerts []model.Alert) ([]model.Alert, error)
	Save(sent []model.Alert) error
}

func key(domain string, kind model.AlertKind, threshold int) string {
	return fmt.Sprintf("%s|%s|%d", domain, kind, threshold)
}

// filterAgainst returns alerts that are NOT already in existing (matched by
// key AND ExpiresAt).
func filterAgainst(existing map[string]Record, alerts []model.Alert) []model.Alert {
	out := alerts[:0:0]
	for _, a := range alerts {
		r, ok := existing[key(a.Domain, a.Kind, a.Threshold)]
		if ok && r.ExpiresAt.Equal(a.ExpiresAt) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// upsert merges sent alerts into existing and returns a stable-sorted slice
// ready for JSON marshalling.
func upsert(existing map[string]Record, sent []model.Alert) []Record {
	now := time.Now().UTC()
	for _, a := range sent {
		existing[key(a.Domain, a.Kind, a.Threshold)] = Record{
			Domain:    a.Domain,
			Kind:      a.Kind,
			Threshold: a.Threshold,
			ExpiresAt: a.ExpiresAt,
			SentAt:    now,
		}
	}
	recs := make([]Record, 0, len(existing))
	for _, r := range existing {
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Domain != recs[j].Domain {
			return recs[i].Domain < recs[j].Domain
		}
		if recs[i].Kind != recs[j].Kind {
			return recs[i].Kind < recs[j].Kind
		}
		return recs[i].Threshold < recs[j].Threshold
	})
	return recs
}

func decode(data []byte) (map[string]Record, error) {
	if len(data) == 0 {
		return map[string]Record{}, nil
	}
	var recs []Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("state parse: %w", err)
	}
	m := make(map[string]Record, len(recs))
	for _, r := range recs {
		m[key(r.Domain, r.Kind, r.Threshold)] = r
	}
	return m, nil
}

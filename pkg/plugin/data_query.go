package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// DataQueryCapability requires a fresh host authorization handoff for EVERY
// operation of the plugin. Old hosts refuse this capability at install.
const DataQueryCapability = "data_query_v1"
const DataPermitConfigKey = "_cerberus_data_permit_v1"

// DataQuery contains selectors and parameters, never SQL or an endpoint.
type DataQuery struct {
	Target     string          `json:"target"`
	Projection string          `json:"projection"`
	Version    string          `json:"version"`
	Parameters json.RawMessage `json:"parameters"`
}

// DataPermit travels only through a one-shot process's private Init channel.
// It is not a bearer credential or an argument accepted from callers.
type DataPermit struct {
	Digest    string    `json:"digest"`
	ExpiresAt time.Time `json:"expires_at"`
	MaxRows   int       `json:"max_rows"`
	MaxBytes  int       `json:"max_bytes"`
}

func DataQueryDigest(q DataQuery) string {
	// Canonicalize object ordering without losing integer precision.
	var value any
	dec := json.NewDecoder(bytes.NewReader(q.Parameters))
	dec.UseNumber()
	if dec.Decode(&value) != nil {
		return ""
	}
	q.Parameters, _ = json.Marshal(value)
	b, _ := json.Marshal(q)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (p DataPermit) Validate(q DataQuery, now time.Time) error {
	if p.Digest == "" || p.Digest != DataQueryDigest(q) || !now.Before(p.ExpiresAt) || p.ExpiresAt.Sub(now) > 2*time.Second || p.MaxRows < 1 || p.MaxRows > 100 || p.MaxBytes < 1 || p.MaxBytes > 1<<20 {
		return errors.New("data query requires a current host-bound permit")
	}
	return nil
}

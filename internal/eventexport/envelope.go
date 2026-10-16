package eventexport

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Envelope is the event-stream envelope v1.1 (libs harness/core
// adapters/runtimeevents), reduced to the fields Cerberus fills. Its JSON is
// what runtimeevents.Event decodes; a later change replaces this type with
// that one when the eventstage chain is wired in.
type Envelope struct {
	SchemaVersion string          `json:"schema_version"`
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	Time          time.Time       `json:"time"`
	Sequence      uint64          `json:"sequence"`
	Generation    uint64          `json:"generation,omitempty"`
	Subject       string          `json:"subject,omitempty"`
	Source        Source          `json:"source"`
	Corr          *Corr           `json:"corr,omitempty"`
	Actor         *Actor          `json:"actor,omitempty"`
	Class         *Class          `json:"class,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// Source names where the envelope came from.
type Source struct {
	Channel string `json:"channel"`
}

// Corr groups the envelopes of one operation or approval episode.
type Corr struct {
	CorrelationID string `json:"correlation_id,omitempty"`
}

// Actor is who caused the event. Verified is token only for a verified
// bearer; everything else Cerberus records is claimed.
type Actor struct {
	URN      string `json:"urn,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Verified string `json:"verified,omitempty"`
}

// Class is the classification block. Visibility is a constant per kind family
// until the owner rules D9.
type Class struct {
	Category      string   `json:"category,omitempty"`
	Severity      string   `json:"severity,omitempty"`
	Visibility    string   `json:"visibility,omitempty"`
	PayloadSchema string   `json:"payload_schema,omitempty"`
	Labels        []string `json:"labels,omitempty"`
}

// Source channels. Each has its own outbox, sequence and generation.
const (
	ChannelAudit      = "audit"
	ChannelScheduling = "scheduling"
)

// Visibility constants, pending D9.
const (
	VisibilityOperator    = "operator"
	VisibilityEnvironment = "environment"
)

// newEventID is evt_ plus 32 hex digits, the id scheme of the contract (D6).
func newEventID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("eventexport: no randomness: %v", err))
	}
	return "evt_" + hex.EncodeToString(b[:])
}

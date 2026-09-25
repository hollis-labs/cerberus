// Package audit is Cerberus's append-only record of what it was asked to do
// and what happened (docs/plans/live-systems-security-target.md, section 9).
//
// Every operation writes two records: an intent before anything runs and an
// outcome after, correlated by operation_id. Records are hash-chained across
// monthly files, and a file is only ever appended to. A record is a DTO: it
// names things — the connector, the target, the credentials resolved — and
// never carries a value a caller supplied, beyond the fields that identify
// the target. Arguments are recorded as a keyed digest.
package audit

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Record kinds.
const (
	// KindChainStart is the first record ever written to an audit directory.
	KindChainStart = "chain_start"
	// KindFileStart opens each new monthly file. It carries the previous
	// file's name and its last hash, so the chain is checkable across files.
	KindFileStart = "file_start"
	// KindChainBreak records that the previous tail could not be read — a
	// torn write from a crash — so the chain resumes from the last complete
	// record rather than being silently repaired.
	KindChainBreak = "chain_break"
	// KindIntent is written before an operation runs.
	KindIntent = "intent"
	// KindOutcome is written after it, on every exit.
	KindOutcome = "outcome"
)

// SchemaVersion is the record format version.
const SchemaVersion = 1

// PostureSecure is the posture recorded on every record; the only one until
// P2 adds permissive.
const PostureSecure = "secure"

// Principal is who asked, as the serving process knows it. The surface is
// self-reported (cerbapi.CallerSurface) and proves nothing: an in-process
// CLI call is a local principal, never "the human".
type Principal struct {
	Surface      string `json:"surface"`
	SelfReported bool   `json:"self_reported"`
}

// Target is what an operation touched: the contract's target kind, and the
// values of the input fields its descriptor names (a droplet id, a resource
// id). Those identify the target; no other argument value is recorded.
type Target struct {
	Kind   string            `json:"kind,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

// Record is one audit record.
type Record struct {
	Version     int       `json:"v"`
	Seq         uint64    `json:"seq"`
	ID          string    `json:"id"`
	Time        time.Time `json:"time"`
	Kind        string    `json:"kind"`
	OperationID string    `json:"operation_id,omitempty"`

	Principal Principal `json:"principal"`
	Connector string    `json:"connector,omitempty"`
	Operation string    `json:"operation,omitempty"`
	Effect    string    `json:"effect,omitempty"`
	Target    Target    `json:"target,omitzero"`
	// ArgsDigest is an HMAC-SHA256 of the canonical JSON of the arguments,
	// under a per-install key, so equal arguments are recognizable and a
	// short secret in them cannot be brute-forced from the log.
	ArgsDigest string `json:"args_digest,omitempty"`
	// CredentialNames are the secrets the operation's connector resolves, as
	// "<connector>/<name>". Names, never values.
	CredentialNames []string `json:"credential_names,omitempty"`
	// For a plugin operation: fingerprints of the plugin's connector config
	// (~/.cerberus/connector-config.yaml as loaded) and of its entrypoint,
	// so a record shows which config and which binary ran.
	PluginConfigSHA256     string `json:"plugin_config_sha256,omitempty"`
	PluginEntrypointSHA256 string `json:"plugin_entrypoint_sha256,omitempty"`

	Acknowledged bool `json:"acknowledged"`
	DryRun       bool `json:"dry_run"`

	// Decision is set on the outcome: allowed, or refused by a gate.
	Decision string `json:"decision,omitempty"`
	// OutcomeCode is ok, or the error code the caller received.
	OutcomeCode string `json:"outcome_code,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	Posture     string `json:"posture"`

	// Note explains a chain_start, file_start or chain_break record.
	Note     string `json:"note,omitempty"`
	PrevFile string `json:"prev_file,omitempty"`

	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
}

// Decisions.
const (
	DecisionAllowed = "allowed"
	DecisionRefused = "refused"
)

// OutcomeOK is the outcome code of an operation that succeeded.
const OutcomeOK = "ok"

// NewID returns a random record or operation id.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("audit: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

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

	// The approval broker's records, one per transition (P3-1). Each names
	// the approval, and a consumed one links to the intent of the operation
	// it let through by operation_id.
	KindApprovalRequested = "approval_requested"
	KindApprovalDecided   = "approval_decided"
	KindApprovalExpired   = "approval_expired"
	KindApprovalConsumed  = "approval_consumed"
	KindApprovalRevoked   = "approval_revoked"
)

// SchemaVersion is the record format version.
const SchemaVersion = 1

// PostureSecure is the default posture, recorded when nothing says
// otherwise. Records made through the admin lane carry the posture their
// operation was evaluated under (policy.PostureFor), secure or permissive.
const PostureSecure = "secure"

// Principal is who asked, as the serving process knows it
// (cerbapi.Principal). It is a label for default policy and proves nothing
// about a human: an agent with a shell can run the CLI as the user. Only
// the uid can be established, and UIDVerified says when it was — the
// socket's peer credentials, or the CLI's own process. Kind, Via and Client
// are the caller's claim when SelfReported is set.
type Principal struct {
	// Kind is human, agent or automation.
	Kind string `json:"kind,omitempty"`
	// Surface is the transport the request entered through (in_process,
	// socket, web, monitor); Via is who is on the other end of it (cli,
	// mcp_stdio, mcp_http, web, monitor, pipeline).
	Surface      string `json:"surface"`
	Via          string `json:"via,omitempty"`
	UID          *int   `json:"uid,omitempty"`
	UIDVerified  bool   `json:"uid_verified,omitempty"`
	Client       string `json:"client,omitempty"`
	Session      string `json:"session,omitempty"`
	OnBehalfOf   string `json:"on_behalf_of,omitempty"`
	SelfReported bool   `json:"self_reported"`
}

// PrincipalAutomation is the kind of a principal that is Cerberus itself.
const PrincipalAutomation = "automation"

// Target is what an operation touched: the contract's target kind, and the
// values of the input fields its descriptor names (a droplet id, a resource
// id). Those identify the target; no other argument value is recorded.
type Target struct {
	Kind   string            `json:"kind,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`

	// The target's policy labels (target.Target): inherited from the
	// registered resource it was resolved through, or unknown. Admin is who
	// administers the part this operation touches. Adhoc marks a target
	// named by connection settings rather than a registered resource.
	Resource string   `json:"resource,omitempty"`
	Env      string   `json:"env,omitempty"`
	Owner    string   `json:"owner,omitempty"`
	Admin    string   `json:"admin,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Adhoc    bool     `json:"adhoc,omitempty"`
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

	// Reason is why automation acted: what the monitor saw.
	Reason string `json:"reason,omitempty"`

	// PluginTelemetry is what a plugin reported for this operation, bounded
	// and redacted by the host. It enriches the host's outcome record; a
	// plugin cannot write a record of its own.
	PluginTelemetry *PluginTelemetry `json:"plugin_telemetry,omitempty"`

	// Preview is plugin_claimed on a dry run a plugin served from its own
	// preview: the plugin's claim, which Cerberus cannot verify (Decision 7).
	Preview string `json:"preview,omitempty"`

	// Policy is what the policy decision point said about the operation. In
	// P2 it is recorded and enforces nothing (shadow mode): would_block says
	// what enforcement would do.
	Policy *PolicyDecision `json:"policy,omitempty"`

	// ApprovalID and PlanHash are the approval an operation ran under and
	// the plan it was bound to (I6): a verifier joins the request, the
	// decision, the consume and this outcome on them.
	ApprovalID string `json:"approval_id,omitempty"`
	PlanHash   string `json:"plan_hash,omitempty"`

	// Approval is the approval a broker record is about.
	Approval *ApprovalRef `json:"approval,omitempty"`

	// PluginReview is what an install review showed and what was accepted.
	PluginReview *PluginReview `json:"plugin_review,omitempty"`

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

// PluginTelemetry is the bounded, redacted record of what a plugin reported
// during one operation: the events it returned with its result, and the
// stderr lines it wrote while the call ran.
type PluginTelemetry struct {
	Events []PluginEvent `json:"events,omitempty"`
	Stderr []string      `json:"stderr,omitempty"`
	// SharedStderr is set when another call to the same plugin ran at the
	// same time, so a stderr line may belong to either.
	SharedStderr bool `json:"shared_stderr,omitempty"`
	// Truncated is set when the plugin reported more than the bounds keep.
	Truncated bool `json:"truncated,omitempty"`
}

// PluginEvent is one event a plugin reported.
type PluginEvent struct {
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message,omitempty"`
	Target  string `json:"target,omitempty"`
}

// PreviewPluginClaimed marks a dry run served by a plugin's own preview.
const PreviewPluginClaimed = "plugin_claimed"

// PluginReview is the record of a plugin install review: the digest of the
// summary the operator was shown, so the record names exactly what was
// accepted, the bundle it covers, its gaps, and for a re-review the changes
// against the review accepted before.
type PluginReview struct {
	Kind          string   `json:"kind"`
	SummarySHA256 string   `json:"summary_sha256"`
	BundleDigest  string   `json:"bundle_digest"`
	Source        string   `json:"source,omitempty"`
	Gaps          []string `json:"gaps,omitempty"`
	Changes       []string `json:"changes,omitempty"`
	// Unattended marks a review accepted without the typed confirmation:
	// `install --yes` under the permissive posture (section 13).
	Unattended bool `json:"unattended,omitempty"`
}

// PolicyDecision is a policy result as recorded: the decision, every rule
// that matched, whether enforcement would stop the operation, and which
// policy snapshot decided (its hash, "baseline", or "mismatch").
type PolicyDecision struct {
	Decision     string        `json:"decision"`
	MatchedRules []MatchedRule `json:"matched_rules"`
	WouldBlock   bool          `json:"would_block"`
	Snapshot     string        `json:"snapshot"`
	Shadow       bool          `json:"shadow"`
}

// MatchedRule is one rule behind a decision.
type MatchedRule struct {
	Rule     string `json:"rule"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// ApprovalRef is an approval as a broker record names it.
type ApprovalRef struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Channel   string    `json:"channel,omitempty"`
	Scope     string    `json:"scope,omitempty"`
	Rule      string    `json:"rule,omitempty"`
	PlanHash  string    `json:"plan_hash,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	// DecidedBy and KeyFingerprint are a decision's: who, and with which
	// enrolled key when it was out of band.
	DecidedBy      *Principal `json:"decided_by,omitempty"`
	KeyFingerprint string     `json:"key_fingerprint,omitempty"`
}

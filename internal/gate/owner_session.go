package gate

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
)

// Out-of-band verification by the owner's console session (decision 0.1).
//
// The old build verified an out-of-band decision with a passkey: the
// decision carried a WebAuthn assertion over the approval, the broker's
// presence verifier (approval.PresenceVerifier, backed by the passkey
// service) checked it when the decision was made, and checked it again when
// the approval was used, because the store's file can be edited in between.
//
// Passkeys are dropped. In their place an out-of-band decision is verified
// when the decider is the owner signed in to the console: the HTTP layer,
// which authenticated the request, tells the gate so with an OwnerSession
// (ConsolePerson set from api.IsConsolePerson, and the session's id). Only
// then does DecideAs put the owner-session proof on the decision, in the
// field the passkey's key fingerprint used (approval.Decision.KeyFingerprint,
// so the approval event file format is unchanged): the fixed marker
// "owner-session:" and a digest of the session id, never the id itself.
// OwnerSessionVerifier accepts exactly those proofs, on a human's approve
// made over the web console.
//
// Consume re-runs the same verifier over the decision as recorded, so no
// fresh proof is asked for when the approval is used: the approval is
// consumable because its recorded decision carries the owner-session
// verification. The decision's Assertion carries a seal: the time it was
// made and an HMAC over the approval id, the action's digests, the verdict,
// the marker and that time, under the audit digest key (@archie's ruling (b)).
// A process that can write the approvals store but cannot read the key cannot
// forge one. The owner's own account can read the key, so a same-user process
// still could: that residual risk is recorded in docs/conversion-notes.md.

// OwnerSession is what the HTTP layer knows about the person deciding: that
// it authenticated them as the owner signed in to the console, and that
// session's id. The zero value is "not the owner's console session".
type OwnerSession struct {
	// ConsolePerson is api.IsConsolePerson for the request's principal.
	ConsolePerson bool
	// SessionID is the console session's id. It is digested before it is
	// recorded.
	SessionID string
}

// ownerSessionPrefix is the marker an owner-session proof starts with.
const ownerSessionPrefix = "owner-session:"

// ownerSessionDigestLen is the hex length of the session digest a proof
// carries.
const ownerSessionDigestLen = 16

// proof is the owner-session proof for this decider, or "" for anyone the
// HTTP layer did not establish as the owner's console session.
func (o OwnerSession) proof() string {
	if !o.ConsolePerson || strings.TrimSpace(o.SessionID) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(o.SessionID))
	return ownerSessionPrefix + hex.EncodeToString(sum[:])[:ownerSessionDigestLen]
}

// validOwnerSessionProof reports a proof DecideAs could have made: the
// marker and a session digest, nothing else.
func validOwnerSessionProof(p string) bool {
	digest, ok := strings.CutPrefix(p, ownerSessionPrefix)
	if !ok || len(digest) != ownerSessionDigestLen {
		return false
	}
	for _, r := range digest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Keyer is the keyed digest the owner-session seal is made with: the audit chain's digest key
// (audit.Digester, audit/.digest_key in the data dir, 0600).
type Keyer interface {
	Digest(v any) string
}

// sealSkew is how far the time inside a seal may be from the decision's recorded time: the
// store stamps the decision just after the seal is made.
const sealSkew = 2 * time.Minute

// seal is an owner-session decision's Assertion: when it was made, and an HMAC over the whole
// decision under the audit digest key (@archie's ruling (b) on decision 0.1). A process that
// cannot read the key cannot make one; a process that can read it, the owner's own account,
// still can. That residual risk is recorded in docs/conversion-notes.md.
type seal struct {
	V   int    `json:"v"`
	At  int64  `json:"t"`
	MAC string `json:"mac"`
}

// sealInput is what the HMAC covers: the approval, the action it binds to, the verdict, the
// owner-session marker and the time.
type sealInput struct {
	ApprovalID string `json:"approval_id"`
	ArgsDigest string `json:"args_digest"`
	PlanHash   string `json:"plan_hash"`
	Approve    bool   `json:"approve"`
	Marker     string `json:"marker"`
	At         int64  `json:"at"`
}

// OwnerSessionVerifier is the broker's approval.PresenceVerifier: an
// out-of-band decision verifies when it is a human's approve, made over the
// web console, carrying the owner-session proof and a seal that verifies
// under Key. Anything else — a key or token caller, an agent, a self-reported
// web claim, an old passkey decision, a forged or unsealed decision, or no key
// at all — is approval.ErrNoPresence.
type OwnerSessionVerifier struct {
	Key Keyer
}

// Seal is the Assertion DecideAs puts on an owner-session approve of a, made at.
func (v OwnerSessionVerifier) Seal(a approval.Approval, marker string, at time.Time) []byte {
	if v.Key == nil || marker == "" {
		return nil
	}
	raw, _ := json.Marshal(seal{V: 1, At: at.UTC().UnixNano(), MAC: v.mac(a, true, marker, at.UTC().UnixNano())})
	return raw
}

func (v OwnerSessionVerifier) mac(a approval.Approval, approve bool, marker string, at int64) string {
	return v.Key.Digest(sealInput{ApprovalID: a.ID, ArgsDigest: a.ArgsDigest, PlanHash: a.PlanHash, Approve: approve, Marker: marker, At: at})
}

// Verify implements approval.PresenceVerifier.
func (v OwnerSessionVerifier) Verify(a approval.Approval, d approval.Decision) error {
	if !d.Approve || d.By.Kind != string(PrincipalHuman) || d.By.Via != ViaWeb || !validOwnerSessionProof(d.KeyFingerprint) {
		return approval.ErrNoPresence
	}
	return v.checkSeal(a, d)
}

// checkSeal verifies d's seal: present, made with the key, over this approval and verdict,
// and made when the decision was recorded (when it has been).
func (v OwnerSessionVerifier) checkSeal(a approval.Approval, d approval.Decision) error {
	if v.Key == nil {
		return approval.ErrNoPresence
	}
	var sl seal
	if err := json.Unmarshal(d.Assertion, &sl); err != nil || sl.V != 1 || sl.MAC == "" {
		return approval.ErrNoPresence
	}
	if !hmac.Equal([]byte(sl.MAC), []byte(v.mac(a, d.Approve, d.KeyFingerprint, sl.At))) {
		return approval.ErrNoPresence
	}
	if !d.At.IsZero() {
		if skew := d.At.Sub(time.Unix(0, sl.At)); skew > sealSkew || skew < -sealSkew {
			return approval.ErrNoPresence
		}
	}
	return nil
}

// ApprovalsReport is what VerifyApprovals found among the out-of-band approves in a store.
type ApprovalsReport struct {
	// Verified are owner-session approves whose seal verifies.
	Verified []string
	// Legacy are approves made before the cutover (a passkey or no owner-session proof):
	// accepted as the old build's record, listed, never consumable here.
	Legacy []string
	// Failed are owner-session approves whose seal is missing or does not verify: forged or
	// edited. Any entry fails verification.
	Failed []string
	// Problems are what folding the store found wrong (its own hash chain).
	Problems []string
}

// Err is the report's verdict: an error when any owner-session approve failed its seal or the
// store's chain has problems.
func (r ApprovalsReport) Err() error {
	if len(r.Failed) == 0 && len(r.Problems) == 0 {
		return nil
	}
	return fmt.Errorf("approvals do not verify: %d owner-session decision(s) fail their seal (%s); %d store problem(s)",
		len(r.Failed), strings.Join(r.Failed, ", "), len(r.Problems))
}

// VerifyApprovals checks every out-of-band approve in the approvals store at dir against key:
// what `cerberus audit verify` and the export-legacy check run.
func VerifyApprovals(dir string, key Keyer) (ApprovalsReport, error) {
	store, err := approval.Open(dir)
	if err != nil {
		return ApprovalsReport{}, err
	}
	r := ApprovalsReport{Problems: append([]string(nil), store.Problems...)}
	v := OwnerSessionVerifier{Key: key}
	for _, a := range store.List() {
		d := a.Decision
		if d == nil || !d.Approve || a.Channel != approval.ChannelOutOfBand {
			continue
		}
		switch {
		case !strings.HasPrefix(d.KeyFingerprint, ownerSessionPrefix):
			r.Legacy = append(r.Legacy, a.ID)
		case v.checkSeal(a, *d) != nil:
			r.Failed = append(r.Failed, a.ID)
		default:
			r.Verified = append(r.Verified, a.ID)
		}
	}
	return r, nil
}

package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

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
// verification. Unlike a passkey signature, the marker is not
// cryptographic: a process that can write the approvals store can write
// one. That is the trade decision 0.1 makes; the store's own hash chain
// (approval.Follow) is what notices such an edit.

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

// OwnerSessionVerifier is the broker's approval.PresenceVerifier: an
// out-of-band decision verifies when it is a human's approve, made over the
// web console, carrying the owner-session proof. Anything else — a key or
// token caller, an agent, a self-reported web claim, an old passkey
// decision — is approval.ErrNoPresence.
type OwnerSessionVerifier struct{}

// Verify implements approval.PresenceVerifier.
func (OwnerSessionVerifier) Verify(_ approval.Approval, d approval.Decision) error {
	if !d.Approve || d.By.Kind != string(PrincipalHuman) || d.By.Via != ViaWeb || !validOwnerSessionProof(d.KeyFingerprint) {
		return approval.ErrNoPresence
	}
	return nil
}

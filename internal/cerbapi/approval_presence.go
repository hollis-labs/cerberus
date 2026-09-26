package cerbapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// The process's passkey service (P3-4b): the daemon installs one beside its
// broker, and it is the broker's PresenceVerifier.
var presencePoint atomic.Pointer[presence.Service]

// SetPresence installs the passkey service and makes it the broker's
// presence verifier.
func SetPresence(p *presence.Service) {
	presencePoint.Store(p)
	if b := ProcessBroker(); b != nil && p != nil {
		b.SetPresenceVerifier(p)
	}
}

// ProcessPresence is the installed passkey service, or nil.
func ProcessPresence() *presence.Service { return presencePoint.Load() }

// ApprovalChallengeArgs starts the passkey ceremony for approving: the
// console names its own origin, which must be a running console's.
type ApprovalChallengeArgs struct {
	Origin string `json:"origin"`
}

// WebAuthnOptions is a ceremony's WebAuthn options, as base64url of their
// JSON. They are public by construction — a challenge, the relying party,
// and credential ids, which an authenticator hands to any page that asks —
// and they are carried opaque because the response redactor walks JSON by
// key: WebAuthn's own "allowCredentials" and "excludeCredentials" match its
// CREDENTIAL marker, and came back as "[REDACTED]", which no browser can
// use. The string still passes the redactor's text rules; only the key walk
// is kept out of a protocol message it cannot read.
type WebAuthnOptions string

func webAuthnOptions(raw json.RawMessage) WebAuthnOptions {
	if len(raw) == 0 {
		return ""
	}
	return WebAuthnOptions(base64.RawURLEncoding.EncodeToString(raw))
}

// JSON is the options as the browser takes them.
func (o WebAuthnOptions) JSON() (json.RawMessage, error) {
	return base64.RawURLEncoding.DecodeString(string(o))
}

// PasskeyCeremony is a ceremony's id and the options the browser needs.
type PasskeyCeremony struct {
	Ceremony string          `json:"ceremony"`
	Options  WebAuthnOptions `json:"options"`
}

// EnrollCeremony is an enrollment's id, the options for the new passkey,
// and, when keys are already enrolled, the assertion one of them must give.
type EnrollCeremony struct {
	Ceremony  string          `json:"ceremony"`
	Creation  WebAuthnOptions `json:"creation"`
	Authorize WebAuthnOptions `json:"authorize,omitempty"`
}

// EnrollBeginArgs starts an enrollment with the token its link carried, at
// the console's origin. EnrollFinishArgs, RemoveBeginArgs and
// RemoveFinishArgs are the other halves of enrollment and removal.
type EnrollBeginArgs struct {
	Token  string `json:"token"`
	Origin string `json:"origin"`
	Label  string `json:"label,omitempty"`
}

type EnrollFinishArgs struct {
	Ceremony    string          `json:"ceremony"`
	Attestation json.RawMessage `json:"attestation"`
	Authorize   json.RawMessage `json:"authorize,omitempty"`
}

type RemoveBeginArgs struct {
	Fingerprint string `json:"fingerprint"`
	Origin      string `json:"origin"`
}

type RemoveFinishArgs struct {
	Ceremony  string          `json:"ceremony"`
	Assertion json.RawMessage `json:"assertion"`
}

// EnrollAllowArgs is `cerberus approvals enroll`: the digest of the
// one-time token its console link carries. The token stays with the
// terminal that made it; the daemon never hands one out.
type EnrollAllowArgs struct {
	Digest string `json:"digest"`
}

// sealAssertion turns the console's passkey answer into what the decision
// keeps, and names the key. Without a passkey service there is nothing to
// seal, and the store refuses an out-of-band approve as it always has.
func sealAssertion(d *approval.Decision, id string, raw json.RawMessage) error {
	p := ProcessPresence()
	if p == nil || len(raw) == 0 {
		return nil
	}
	sealed, fp, err := p.Seal(id, raw)
	if err != nil {
		return err
	}
	d.Assertion, d.KeyFingerprint = sealed, fp
	return nil
}

var errNoPresenceService = redact.Guidance("this daemon has no passkey service, so out-of-band approval is not available; see its log for why")

// presenceErrorStatus maps a passkey refusal to a status.
func presenceErrorStatus(err error) (int, string) {
	var cooldown presence.CooldownError
	switch {
	case errors.As(err, &cooldown):
		return http.StatusLocked, err.Error()
	case errors.Is(err, presence.ErrNotSetUp), errors.Is(err, errNoPresenceService):
		return http.StatusServiceUnavailable, err.Error()
	case errors.Is(err, presence.ErrCeremony), errors.Is(err, presence.ErrEnrollToken):
		return http.StatusConflict, err.Error()
	case errors.Is(err, presence.ErrUnknownKey), errors.Is(err, presence.ErrAssertion), errors.Is(err, presence.ErrOrigin),
		errors.Is(err, presence.ErrCloned), errors.Is(err, presence.ErrNeedEnrolled):
		return http.StatusForbidden, err.Error()
	}
	return http.StatusBadRequest, redact.Text(err.Error())
}

// handleApprovalKeys is /approvals/keys: GET the registry's state, POST
// enroll-allow (from `cerberus approvals enroll` on a terminal), and the
// enrollment and removal ceremonies, which the console drives.
func (s *SocketServer) handleApprovalKeys(w http.ResponseWriter, r *http.Request, rest string) {
	p := ProcessPresence()
	if p == nil {
		status, msg := presenceErrorStatus(errNoPresenceService)
		writeJSONError(w, status, msg)
		return
	}
	if rest == "" {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, p.Status())
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var (
		out any
		err error
	)
	by := principalFor(r.Context(), auditSpec{})
	switch rest {
	case "enroll-allow":
		var args EnrollAllowArgs
		if err = decodeJSONBody(r, &args); err == nil {
			err = p.AllowEnrollment(args.Digest)
			out = map[string]bool{"allowed": err == nil}
		}
	case "register/begin":
		var args EnrollBeginArgs
		if err = decodeJSONBody(r, &args); err == nil {
			var begin presence.EnrollBegin
			begin, err = p.BeginEnroll(args.Token, args.Origin, args.Label)
			out = EnrollCeremony{Ceremony: begin.Ceremony, Creation: webAuthnOptions(begin.Creation), Authorize: webAuthnOptions(begin.Authorize)}
		}
	case "register/finish":
		var args EnrollFinishArgs
		if err = decodeJSONBody(r, &args); err == nil {
			out, err = p.FinishEnroll(by, args.Ceremony, args.Attestation, args.Authorize)
		}
	case "remove/begin":
		var args RemoveBeginArgs
		if err = decodeJSONBody(r, &args); err == nil {
			var (
				c    PasskeyCeremony
				opts json.RawMessage
			)
			c.Ceremony, opts, err = p.BeginRemove(args.Fingerprint, args.Origin)
			c.Options = webAuthnOptions(opts)
			out = c
		}
	case "remove/finish":
		var args RemoveFinishArgs
		if err = decodeJSONBody(r, &args); err == nil {
			err = p.FinishRemove(by, args.Ceremony, args.Assertion)
			out = map[string]bool{"removed": err == nil}
		}
	default:
		writeJSONError(w, http.StatusNotFound, "unknown passkey action "+rest)
		return
	}
	if err != nil {
		status, msg := presenceErrorStatus(err)
		writeJSONError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// approvalChallenge starts the passkey ceremony for approving a pending
// out-of-band approval.
func approvalChallenge(broker *Broker, id string, args ApprovalChallengeArgs) (PasskeyCeremony, error) {
	p := ProcessPresence()
	if p == nil {
		return PasskeyCeremony{}, errNoPresenceService
	}
	a, ok := broker.Get(id)
	if !ok {
		return PasskeyCeremony{}, approval.ErrNotFound
	}
	if a.Status != approval.Pending {
		return PasskeyCeremony{}, approval.ErrNotPending
	}
	if a.Channel != approval.ChannelOutOfBand {
		return PasskeyCeremony{}, redact.Guidance("approval %s is confirmed by typing its target, not with a passkey", id)
	}
	cer, opts, err := p.BeginApproval(a, strings.TrimRight(args.Origin, "/"))
	return PasskeyCeremony{Ceremony: cer, Options: webAuthnOptions(opts)}, err
}

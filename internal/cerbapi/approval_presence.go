package cerbapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hollis-labs/cerberus/internal/audit"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/userpresence"
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

// passkeyAdminRefusal is the daemon's own check on who may change the
// passkeys out-of-band approvals are verified against (B1). The CLI's
// terminal check is not enough on its own: the socket routes were open to
// any caller, so an agent could allow an enrollment, register a key it
// made itself (the first is trusted on first use) and from then on approve
// its own requests. Allowing an enrollment is a person at the CLI on an
// interactive terminal — the human CLI claim, which the CLI makes only with
// stdin and stdout both terminals. The ceremonies that follow run on the
// console, for a person there or at the CLI. An agent, automation or an
// unknown caller is refused; so is any MCP surface and any verified token
// caller. A key after the first also needs an enrolled key's assertion,
// which presence checks.
func passkeyAdminRefusal(ctx context.Context, action string) error {
	p, _ := PrincipalFrom(ctx)
	allowed := p.Kind == PrincipalHuman && !strings.HasPrefix(p.Via, "mcp") && !p.Verified()
	if action == "enroll-allow" {
		allowed = allowed && p.Via == ViaCLI
	} else {
		allowed = allowed && (p.Via == ViaCLI || p.Via == ViaWeb)
	}
	if allowed && action == "enroll-allow" {
		return enrollPresenceRefusal(ctx)
	}
	if allowed {
		return nil
	}
	return externalConnectorError(ExternalConnectorOperationArgs{Connector: "approvals", Operation: "keys_" + strings.ReplaceAll(action, "/", "_")}, ExternalConnectorApprovalRequired,
		redact.Guidance("passkeys are enrolled and removed by a person: run `cerberus approvals enroll` in an interactive terminal, then finish on the console. This caller is %s over %s, so nothing was changed", p.Kind, p.Via))
}

// userPresencePoint is the check that a person is at the machine (B1-b),
// which allowing a passkey enrollment needs on top of a person's claim.
var userPresencePoint atomic.Pointer[userPresenceHolder]

type userPresenceHolder struct {
	v    userpresence.Verifier
	sink audit.Sink
	gate *promptGate
}

// SetUserPresence installs the check; nil removes it, and then enrollment
// is refused: it never falls back to the claim.
func SetUserPresence(v userpresence.Verifier) { SetUserPresenceWith(v, nil) }

// SetUserPresenceWith installs the check with the audit sink each prompt
// it raises is recorded in. The daemon's; nil records nothing.
func SetUserPresenceWith(v userpresence.Verifier, sink audit.Sink) {
	if v == nil {
		userPresencePoint.Store(nil)
		return
	}
	userPresencePoint.Store(&userPresenceHolder{v: v, sink: sink, gate: &promptGate{}})
}

// PromptCooldown is how long enrollment is refused, without asking, after
// the person at the Mac refused or canceled a prompt.
var PromptCooldown = 5 * time.Minute

// promptGate keeps a caller from wearing the person down with prompts: one
// on screen at a time, and none for PromptCooldown after one was refused,
// so a loop of enroll-allow calls cannot raise sheets until one is tapped.
type promptGate struct {
	mu        sync.Mutex
	busy      bool
	coolUntil time.Time
	now       func() time.Time
}

func (g *promptGate) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// acquire takes the one prompt slot, or says why not.
func (g *promptGate) acquire() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	switch {
	case g.busy:
		return "a prompt is already on this Mac's screen; answer it, or wait for it to time out"
	case now.Before(g.coolUntil):
		return fmt.Sprintf("the last prompt was refused or canceled, so none is raised until %s (%s from now)", g.coolUntil.Local().Format("15:04:05"), g.coolUntil.Sub(now).Round(time.Second))
	}
	g.busy = true
	return ""
}

// release frees the slot; refused starts the cool-down.
func (g *promptGate) release(refused bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy = false
	if refused {
		g.coolUntil = g.clock().Add(PromptCooldown)
	}
}

// enrollPresenceRefusal asks the person at the machine to allow a passkey
// enrollment (B1-b). The claim that let the call this far is
// self-reported: a process running as the operator can make it. The check
// is one the daemon raises itself, which such a process cannot answer
// without replacing the daemon or its helper. Each prompt raised is
// recorded; a prompt is not raised while one is on screen, or for a while
// after one was refused.
func enrollPresenceRefusal(ctx context.Context) error {
	args := ExternalConnectorOperationArgs{Connector: "approvals", Operation: "keys_enroll-allow"}
	h := userPresencePoint.Load()
	if h == nil {
		return externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.Guidance("this daemon cannot ask the person at this Mac to allow an enrollment, so none is allowed; %s", userpresence.Recovery))
	}
	if why := h.gate.acquire(); why != "" {
		return externalConnectorError(args, ExternalConnectorApprovalRequired, redact.Guidance("%s; nothing was allowed", why))
	}
	const reason = "allow a new passkey for Cerberus approvals"
	rec := audit.Record{Kind: audit.KindIntent, OperationID: audit.NewID(), Principal: principalFor(ctx, auditSpec{}), Connector: "approvals", Operation: "presence_prompt",
		Effect: string(contract.EffectAdmin), Target: audit.Target{Kind: "presence.prompt"}, Note: "asked the person at this Mac to " + reason, Posture: audit.PostureSecure}
	if h.sink != nil {
		if _, err := h.sink.Write(rec); err != nil {
			h.gate.release(false)
			return externalConnectorError(args, ExternalConnectorAuditUnavailable,
				redact.Guidance("the prompt could not be recorded, so none was raised and nothing was allowed: %v", err))
		}
	}
	err := h.v.Verify(ctx, reason)
	refused := errors.Is(err, userpresence.ErrRefused)
	h.gate.release(refused)
	if h.sink != nil {
		rec.Kind, rec.Decision, rec.OutcomeCode = audit.KindOutcome, audit.DecisionAllowed, audit.OutcomeOK
		if err != nil {
			rec.Decision, rec.OutcomeCode = audit.DecisionRefused, string(ExternalConnectorApprovalRequired)
			rec.Note = "the person at this Mac did not allow it: " + err.Error()
		}
		_, _ = h.sink.Write(rec)
	}
	if err != nil {
		next := ""
		if refused {
			next = fmt.Sprintf("; no prompt is raised for %s", PromptCooldown)
		}
		return externalConnectorError(args, ExternalConnectorApprovalRequired,
			redact.Guidance("an enrollment needs the person at this Mac to allow it, and %v; nothing was allowed%s", err, next))
	}
	return nil
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
	if refusal := passkeyAdminRefusal(r.Context(), rest); refusal != nil {
		writeServiceError(w, http.StatusForbidden, refusal)
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

// Package presence is the out-of-band approval proof (P3-4, the P3 cut's
// D1): WebAuthn user verification — Touch ID or a security key — on the
// console, verified here, in the daemon, against the passkeys enrolled for
// this Cerberus.
//
// It is the approval broker's PresenceVerifier. An out-of-band approval
// counts only with an assertion signed by an enrolled credential over a
// challenge bound to that approval: its id, plan, arguments, operation and
// requester, plus a single-use nonce. The broker calls Verify when the
// decision is made and again when the approval is consumed, because the
// store it lives in is a file the operator's uid can edit in between. The
// assertion is kept with the decision, so the second check needs nothing
// from memory.
//
// The registry of enrolled keys is a same-uid file too. Its hash is written
// to the audit log whenever Cerberus changes it. A registry that no longer
// matches the last recorded hash was changed some other way, and starts a
// 24-hour cool-down (D7) in which no out-of-band approval verifies, with a
// notification and an alert in status and the console. This is detection,
// not prevention: preventing it needs a second uid or hardware-bound
// storage (CERB-GAP-879).
package presence

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// RPID is the WebAuthn relying party: the console is served as localhost,
// because a passkey cannot be used on an IP address.
const RPID = "localhost"

// Cooldown is how long out-of-band approvals are refused after the key
// registry changed outside Cerberus (D7).
const Cooldown = 24 * time.Hour

const (
	registryName   = "keys.json"
	countersName   = "key-counters.json"
	ceremonyTTL    = 5 * time.Minute
	enrollTokenTTL = 10 * time.Minute
)

// Registry states.
const (
	StateNotSetUp = "not_set_up"
	StateOK       = "ok"
	StateCooldown = "cooldown"
)

// Refusals, as Guidance.
var (
	ErrNotSetUp     = redact.Guidance("out-of-band approval is not set up: no passkey is enrolled; run `cerberus approvals enroll` in a terminal")
	ErrUnknownKey   = redact.Guidance("the assertion is not from a passkey enrolled for this Cerberus")
	ErrAssertion    = redact.Guidance("the passkey assertion does not verify for this approval")
	ErrCeremony     = redact.Guidance("that passkey request is unknown, used or expired; start the approval again on the console")
	ErrOrigin       = redact.Guidance("the passkey was used from a page that is not a running Cerberus console")
	ErrCloned       = redact.Guidance("the passkey's signature counter went backwards, which is how a cloned authenticator shows; the approval is refused")
	ErrEnrollToken  = redact.Guidance("that enrollment link is unknown, used or expired; run `cerberus approvals enroll` again")
	ErrNeedEnrolled = redact.Guidance("adding or removing a passkey needs an assertion from a passkey already enrolled")
)

// CooldownError is a refusal during the cool-down.
type CooldownError struct{ Until time.Time }

func (e CooldownError) Error() string {
	return redact.Guidance("the passkey registry changed outside `cerberus approvals enroll`, so out-of-band approvals are refused until %s; check `cerberus approvals keys`, and if you did not make the change, remove what you do not recognize",
		e.Until.UTC().Format(time.RFC3339)).Error()
}

// Key is one enrolled passkey.
type Key struct {
	Fingerprint string              `json:"fingerprint"`
	Label       string              `json:"label,omitempty"`
	EnrolledAt  time.Time           `json:"enrolled_at"`
	Credential  webauthn.Credential `json:"credential"`
}

type registryFile struct {
	Version int    `json:"version"`
	UserID  []byte `json:"user_id"`
	Keys    []Key  `json:"keys"`
}

// Status is the registry as status and the console show it.
type Status struct {
	State          string    `json:"state"`
	Keys           []KeyInfo `json:"keys"`
	CooldownUntil  time.Time `json:"cooldown_until,omitzero"`
	LastEnrolledAt time.Time `json:"last_enrolled_at,omitzero"`
	RegistryHash   string    `json:"registry_hash"`
}

// KeyInfo is a key without its public key.
type KeyInfo struct {
	Fingerprint string    `json:"fingerprint"`
	Label       string    `json:"label,omitempty"`
	EnrolledAt  time.Time `json:"enrolled_at"`
}

type ceremony struct {
	kind       string // approve, enroll, remove
	approvalID string
	nonce      []byte
	origin     string
	expires    time.Time
	session    *webauthn.SessionData // enroll: the registration session
	fp         string                // remove: the key to remove
	label      string
}

// Service is the presence verifier and the enrollment ceremonies.
type Service struct {
	mu            sync.Mutex
	dir           string
	sink          audit.Sink
	now           func() time.Time
	notify        func(title, message string)
	origins       func() []string
	expected      string
	cooldownUntil time.Time
	lastEnrolled  time.Time
	ceremonies    map[string]*ceremony
	tokens        map[string]time.Time
}

// Options configure a Service.
type Options struct {
	// Records are the audit log's records, read at start: the last
	// enrollment_changed record says what the registry should be.
	Records []audit.Record
	// Origins are the console origins a passkey may be used from: the
	// running consoles. Called at every ceremony.
	Origins func() []string
	// Notify raises a notification the operator will see.
	Notify func(title, message string)
	Now    func() time.Time
}

// New is the service for the registry in dir.
func New(dir string, sink audit.Sink, o Options) *Service {
	s := &Service{dir: dir, sink: sink, now: o.Now, notify: o.Notify, origins: o.Origins,
		ceremonies: map[string]*ceremony{}, tokens: map[string]time.Time{}}
	if s.now == nil {
		s.now = time.Now
	}
	if s.notify == nil {
		s.notify = func(string, string) {}
	}
	if s.origins == nil {
		s.origins = func() []string { return nil }
	}
	s.expected = hashOf(nil)
	for _, rec := range o.Records {
		if rec.Kind != audit.KindEnrollmentChanged {
			continue
		}
		f := rec.Target.Fields
		switch f["change"] {
		case "unaudited":
			s.cooldownUntil, _ = time.Parse(time.RFC3339, f["cooldown_until"])
		default:
			s.expected, s.cooldownUntil = f["registry_hash"], time.Time{}
			if f["change"] == "enrolled" {
				s.lastEnrolled = rec.Time
			}
		}
	}
	return s
}

func hashOf(data []byte) string {
	if data == nil {
		return "none"
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *Service) readRegistry() (registryFile, []byte, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, registryName)) //nolint:gosec // the operator's own approvals directory
	if errors.Is(err, os.ErrNotExist) {
		return registryFile{Version: 1}, nil, nil
	}
	if err != nil {
		return registryFile{}, nil, err
	}
	var r registryFile
	if err := json.Unmarshal(data, &r); err != nil {
		return registryFile{}, data, fmt.Errorf("the passkey registry %s does not parse: %w", filepath.Join(s.dir, registryName), err)
	}
	return r, data, nil
}

// check compares the registry with the last hash Cerberus recorded, and
// starts or ends the cool-down. Called with s.mu held.
func (s *Service) check(by audit.Principal) (registryFile, string, error) {
	reg, data, err := s.readRegistry()
	found := hashOf(data)
	now := s.now()
	switch {
	case found == s.expected:
		s.cooldownUntil = time.Time{}
	case !s.cooldownUntil.IsZero() && now.Before(s.cooldownUntil):
		return reg, StateCooldown, CooldownError{Until: s.cooldownUntil}
	case !s.cooldownUntil.IsZero():
		// The cool-down ran its course: the registry as it now is becomes
		// the recorded one, and that is recorded.
		s.record(by, "accepted_after_cooldown", "", found, map[string]string{"recorded": s.expected})
		s.expected, s.cooldownUntil = found, time.Time{}
	default:
		s.cooldownUntil = now.Add(Cooldown)
		s.record(by, "unaudited", "", found, map[string]string{"recorded": s.expected, "cooldown_until": s.cooldownUntil.UTC().Format(time.RFC3339)})
		s.notify("Cerberus: passkey registry changed", fmt.Sprintf("The passkeys for out-of-band approval changed outside `cerberus approvals enroll`. Out-of-band approvals are refused until %s.", s.cooldownUntil.Local().Format(time.RFC1123)))
		return reg, StateCooldown, CooldownError{Until: s.cooldownUntil}
	}
	if err != nil {
		return reg, StateCooldown, err
	}
	if len(reg.Keys) == 0 {
		return reg, StateNotSetUp, nil
	}
	return reg, StateOK, nil
}

// Status is the registry's state.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg, state, _ := s.check(audit.Principal{Kind: audit.PrincipalAutomation, Via: "approvals"})
	_, data, _ := s.readRegistry()
	out := Status{State: state, Keys: []KeyInfo{}, CooldownUntil: s.cooldownUntil, LastEnrolledAt: s.lastEnrolled, RegistryHash: hashOf(data)}
	for _, k := range reg.Keys {
		out.Keys = append(out.Keys, KeyInfo{Fingerprint: k.Fingerprint, Label: k.Label, EnrolledAt: k.EnrolledAt})
	}
	return out
}

func (s *Service) record(by audit.Principal, change, fp, hash string, extra map[string]string) {
	fields := map[string]string{"change": change, "registry_hash": hash}
	if fp != "" {
		fields["key"] = fp
	}
	for k, v := range extra {
		fields[k] = v
	}
	rec := audit.Record{Kind: audit.KindEnrollmentChanged, OperationID: audit.NewID(), Principal: by, Connector: "approvals", Operation: "keys",
		Effect: "admin", Target: audit.Target{Kind: "approvals.keys", Fields: fields}, Posture: audit.PostureSecure}
	if s.sink != nil {
		_, _ = s.sink.Write(rec)
	}
}

// user is the one WebAuthn user: the operator of this Cerberus.
type user struct {
	id   []byte
	keys []Key
}

func (u user) WebAuthnID() []byte          { return u.id }
func (u user) WebAuthnName() string        { return "operator" }
func (u user) WebAuthnDisplayName() string { return "Cerberus operator" }
func (u user) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.keys))
	for _, k := range u.keys {
		out = append(out, k.Credential)
	}
	return out
}

func relyingParty(origin string) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{RPID: RPID, RPDisplayName: "Cerberus", RPOrigins: []string{origin}})
}

// allowedOrigin reports whether origin is a running console's: an
// http://localhost:<port> origin that one of the consoles names as its own.
func (s *Service) allowedOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Hostname() != RPID || u.Path != "" {
		return false
	}
	for _, o := range s.origins() {
		if strings.EqualFold(strings.TrimRight(o, "/"), origin) {
			return true
		}
	}
	return false
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("presence: no randomness: " + err.Error())
	}
	return b
}

func newID() string { return base64.RawURLEncoding.EncodeToString(randomBytes(16)) }

func challenge(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(p))
		h.Write(p)
	}
	return h.Sum(nil)
}

// approvalChallenge binds an assertion to what is approved: editing any of
// these in the store makes the stored assertion stop verifying.
func approvalChallenge(a approval.Approval, nonce []byte) []byte {
	return challenge([]byte("cerberus approval v1"), []byte(a.ID), []byte(a.PlanHash), []byte(a.ArgsDigest),
		[]byte(a.Connector), []byte(a.Operation), []byte(a.Principal.Via), []byte(a.Principal.Session), nonce)
}

func descriptors(keys []Key) []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.Credential.Descriptor())
	}
	return out
}

// sessionFor is the WebAuthn login session a challenge verifies against.
func sessionFor(reg registryFile, ch []byte, origin string) webauthn.SessionData {
	ids := make([][]byte, 0, len(reg.Keys))
	for _, k := range reg.Keys {
		ids = append(ids, k.Credential.ID)
	}
	return webauthn.SessionData{Challenge: base64.RawURLEncoding.EncodeToString(ch), RelyingPartyID: RPID, Origin: origin, UserID: reg.UserID,
		AllowedCredentialIDs: ids, UserVerification: protocol.VerificationRequired}
}

// beginLogin is the assertion options for challenge ch at origin.
func beginLogin(reg registryFile, ch []byte, origin string) (json.RawMessage, error) {
	rp, err := relyingParty(origin)
	if err != nil {
		return nil, err
	}
	opts, _, err := rp.BeginLogin(user{id: reg.UserID, keys: reg.Keys}, webauthn.WithChallenge(ch), webauthn.WithLoginOrigin(origin),
		webauthn.WithUserVerification(protocol.VerificationRequired), webauthn.WithAllowedCredentials(descriptors(reg.Keys)))
	if err != nil {
		return nil, err
	}
	return json.Marshal(opts)
}

// verifyAssertion checks raw, a browser's PublicKeyCredential JSON, against
// challenge ch from origin, and returns the key that signed it.
func verifyAssertion(reg registryFile, raw []byte, ch []byte, origin string) (Key, *webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		return Key{}, nil, ErrAssertion
	}
	var key Key
	found := false
	for _, k := range reg.Keys {
		if bytes.Equal(k.Credential.ID, parsed.RawID) {
			key, found = k, true
		}
	}
	if !found {
		return Key{}, nil, ErrUnknownKey
	}
	rp, err := relyingParty(origin)
	if err != nil {
		return Key{}, nil, ErrOrigin
	}
	cred, err := rp.ValidateLogin(user{id: reg.UserID, keys: reg.Keys}, sessionFor(reg, ch, origin), parsed)
	if err != nil {
		return Key{}, nil, ErrAssertion
	}
	return key, cred, nil
}

// sealed is what a decision keeps as its assertion: enough to verify it
// again with nothing from memory.
type sealed struct {
	V          int             `json:"v"`
	Ceremony   string          `json:"ceremony"`
	Origin     string          `json:"origin"`
	Nonce      []byte          `json:"nonce"`
	Credential json.RawMessage `json:"credential"`
}

// submitted is what the console sends back from the browser.
type submitted struct {
	Ceremony   string          `json:"ceremony"`
	Credential json.RawMessage `json:"credential"`
}

// BeginApproval starts the passkey ceremony for approving a at origin.
func (s *Service) BeginApproval(a approval.Approval, origin string) (string, json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg, state, err := s.check(audit.Principal{Kind: audit.PrincipalAutomation, Via: "approvals"})
	if err != nil {
		return "", nil, err
	}
	if state == StateNotSetUp {
		return "", nil, ErrNotSetUp
	}
	if !s.allowedOrigin(origin) {
		return "", nil, ErrOrigin
	}
	nonce := randomBytes(32)
	opts, err := beginLogin(reg, approvalChallenge(a, nonce), origin)
	if err != nil {
		return "", nil, err
	}
	id := newID()
	s.prune()
	s.ceremonies[id] = &ceremony{kind: "approve", approvalID: a.ID, nonce: nonce, origin: origin, expires: s.now().Add(ceremonyTTL)}
	return id, opts, nil
}

// Seal turns what the console sent into the assertion a decision keeps, and
// names the key. It does not spend the ceremony; Verify does.
func (s *Service) Seal(approvalID string, raw json.RawMessage) (json.RawMessage, string, error) {
	var in submitted
	if err := json.Unmarshal(raw, &in); err != nil || in.Ceremony == "" || len(in.Credential) == 0 {
		return nil, "", ErrAssertion
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ceremonies[in.Ceremony]
	if !ok || c.kind != "approve" || c.approvalID != approvalID || s.now().After(c.expires) {
		return nil, "", ErrCeremony
	}
	reg, _, _ := s.readRegistry()
	fp := ""
	if parsed, err := protocol.ParseCredentialRequestResponseBytes(in.Credential); err == nil {
		for _, k := range reg.Keys {
			if bytes.Equal(k.Credential.ID, parsed.RawID) {
				fp = k.Fingerprint
			}
		}
	}
	out, err := json.Marshal(sealed{V: 1, Ceremony: in.Ceremony, Origin: c.origin, Nonce: c.nonce, Credential: in.Credential})
	return out, fp, err
}

var _ approval.PresenceVerifier = (*Service)(nil)

// Verify implements approval.PresenceVerifier. While the approval is
// pending, this is the decision: the ceremony it answers must be live and
// is spent, the page must be a running console, and the signature counter
// must move forward. At consume the same assertion is checked again against
// the approval as it now reads, and against the keys as they now are.
func (s *Service) Verify(a approval.Approval, d approval.Decision) error {
	var env sealed
	if len(d.Assertion) == 0 || json.Unmarshal(d.Assertion, &env) != nil || env.V != 1 {
		return approval.ErrNoPresence
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reg, state, err := s.check(audit.Principal{Kind: audit.PrincipalAutomation, Via: "approvals"})
	if err != nil {
		return err
	}
	if state == StateNotSetUp {
		return ErrNotSetUp
	}
	deciding := a.Status == approval.Pending
	if deciding {
		c, ok := s.ceremonies[env.Ceremony]
		if !ok || c.kind != "approve" || c.approvalID != a.ID || !bytes.Equal(c.nonce, env.Nonce) || s.now().After(c.expires) {
			return ErrCeremony
		}
		delete(s.ceremonies, env.Ceremony)
		if !s.allowedOrigin(env.Origin) {
			return ErrOrigin
		}
	}
	// Deciding, the key carries its last seen counter, so a counter that
	// goes backwards shows; at consume the same assertion is checked again
	// and its counter is the one already seen.
	check := reg
	if deciding {
		check = s.withCounters(reg)
	}
	key, cred, err := verifyAssertion(check, env.Credential, approvalChallenge(a, env.Nonce), env.Origin)
	if err != nil {
		return err
	}
	if deciding {
		if cred.Authenticator.CloneWarning {
			return ErrCloned
		}
		s.saveCounter(key.Credential.ID, cred.Authenticator.SignCount)
	}
	return nil
}

// enrolledAssertion is whether raw is an enrolled key's assertion of ch, as
// an approval's is at decide: its counter must move forward, and is kept.
func (s *Service) enrolledAssertion(reg registryFile, raw json.RawMessage, ch []byte, origin string) bool {
	key, cred, err := verifyAssertion(s.withCounters(reg), raw, ch, origin)
	if err != nil || cred.Authenticator.CloneWarning {
		return false
	}
	s.saveCounter(key.Credential.ID, cred.Authenticator.SignCount)
	return true
}

// Counters live beside the registry, not in it: they change on every
// approval, and the registry's hash must change only when a key does.
func (s *Service) saveCounter(id []byte, count uint32) {
	counters := map[string]uint32{}
	if data, err := os.ReadFile(filepath.Join(s.dir, countersName)); err == nil { //nolint:gosec // the operator's own approvals directory
		_ = json.Unmarshal(data, &counters)
	}
	counters[base64.RawURLEncoding.EncodeToString(id)] = count
	if data, err := json.Marshal(counters); err == nil {
		_ = writeAtomic(filepath.Join(s.dir, countersName), data)
	}
}

func (s *Service) counters() map[string]uint32 {
	counters := map[string]uint32{}
	if data, err := os.ReadFile(filepath.Join(s.dir, countersName)); err == nil { //nolint:gosec // the operator's own approvals directory
		_ = json.Unmarshal(data, &counters)
	}
	return counters
}

// withCounters is reg with each key's last seen signature counter, so a
// counter that goes backwards shows.
func (s *Service) withCounters(reg registryFile) registryFile {
	counters := s.counters()
	out := reg
	out.Keys = append([]Key(nil), reg.Keys...)
	for i, k := range out.Keys {
		if c, ok := counters[base64.RawURLEncoding.EncodeToString(k.Credential.ID)]; ok {
			out.Keys[i].Credential.Authenticator.SignCount = c
		}
	}
	return out
}

func (s *Service) prune() {
	now := s.now()
	for id, c := range s.ceremonies {
		if now.After(c.expires) {
			delete(s.ceremonies, id)
		}
	}
	for t, exp := range s.tokens {
		if now.After(exp) {
			delete(s.tokens, t)
		}
	}
}

// NewEnrollToken is a one-time token for an enrollment link, and the
// digest the service is given for it. `cerberus approvals enroll` makes one
// on a terminal and hands the daemon only the digest, so the token itself
// is never in a response.
func NewEnrollToken() (token, digest string) {
	token = newID()
	return token, EnrollTokenDigest(token)
}

// EnrollTokenDigest is what the service keeps for an enrollment token.
func EnrollTokenDigest(token string) string {
	sum := sha256.Sum256([]byte("cerberus enroll token v1\x00" + token))
	return hex.EncodeToString(sum[:])
}

// AllowEnrollment accepts the enrollment token whose digest this is, for
// ten minutes and one use.
func (s *Service) AllowEnrollment(digest string) error {
	if raw, err := hex.DecodeString(digest); err != nil || len(raw) != sha256.Size {
		return ErrEnrollToken
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	s.tokens[digest] = s.now().Add(enrollTokenTTL)
	return nil
}

// EnrollBegin is the options for a new passkey, and, when keys are already
// enrolled, the assertion an enrolled key must give first.
type EnrollBegin struct {
	Ceremony  string          `json:"ceremony"`
	Creation  json.RawMessage `json:"creation"`
	Authorize json.RawMessage `json:"authorize,omitempty"`
}

// BeginEnroll spends an enrollment token and starts the ceremony for a new
// passkey at origin.
func (s *Service) BeginEnroll(token, origin, label string) (EnrollBegin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	digest := EnrollTokenDigest(token)
	exp, ok := s.tokens[digest]
	if !ok || s.now().After(exp) {
		return EnrollBegin{}, ErrEnrollToken
	}
	delete(s.tokens, digest)
	reg, _, err := s.check(audit.Principal{Kind: audit.PrincipalAutomation, Via: "approvals"})
	if err != nil {
		return EnrollBegin{}, err
	}
	if !s.allowedOrigin(origin) {
		return EnrollBegin{}, ErrOrigin
	}
	if len(reg.UserID) == 0 {
		reg.UserID = randomBytes(32)
	}
	rp, err := relyingParty(origin)
	if err != nil {
		return EnrollBegin{}, err
	}
	creation, session, err := rp.BeginRegistration(user{id: reg.UserID, keys: reg.Keys},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired, ResidentKey: protocol.ResidentKeyRequirementPreferred}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation), webauthn.WithExclusions(descriptors(reg.Keys)), webauthn.WithRegistrationOrigin(origin))
	if err != nil {
		return EnrollBegin{}, err
	}
	session.UserID = reg.UserID
	id := newID()
	nonce := randomBytes(32)
	out := EnrollBegin{Ceremony: id}
	if out.Creation, err = json.Marshal(creation); err != nil {
		return EnrollBegin{}, err
	}
	if len(reg.Keys) > 0 {
		if out.Authorize, err = beginLogin(reg, challenge([]byte("cerberus enroll v1"), []byte(id), nonce), origin); err != nil {
			return EnrollBegin{}, err
		}
	}
	s.ceremonies[id] = &ceremony{kind: "enroll", nonce: nonce, origin: origin, expires: s.now().Add(ceremonyTTL), session: session, label: label}
	return out, nil
}

// FinishEnroll adds the new passkey. With keys already enrolled, the
// assertion from one of them is checked first.
func (s *Service) FinishEnroll(by audit.Principal, ceremonyID string, attestation, authorize json.RawMessage) (KeyInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ceremonies[ceremonyID]
	if !ok || c.kind != "enroll" || s.now().After(c.expires) {
		return KeyInfo{}, ErrCeremony
	}
	delete(s.ceremonies, ceremonyID)
	reg, _, err := s.check(by)
	if err != nil {
		return KeyInfo{}, err
	}
	if len(reg.Keys) > 0 {
		if len(authorize) == 0 {
			return KeyInfo{}, ErrNeedEnrolled
		}
		if !s.enrolledAssertion(reg, authorize, challenge([]byte("cerberus enroll v1"), []byte(ceremonyID), c.nonce), c.origin) {
			return KeyInfo{}, ErrNeedEnrolled
		}
	}
	if len(reg.UserID) == 0 {
		reg.UserID = c.session.UserID
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(attestation)
	if err != nil {
		return KeyInfo{}, ErrAssertion
	}
	rp, err := relyingParty(c.origin)
	if err != nil {
		return KeyInfo{}, ErrOrigin
	}
	cred, err := rp.CreateCredential(user{id: reg.UserID, keys: reg.Keys}, *c.session, parsed)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("%w: %s", ErrAssertion, redact.Text(err.Error()))
	}
	if !cred.Flags.UserVerified {
		return KeyInfo{}, redact.Guidance("the passkey did not verify you (no user verification); use Touch ID, a PIN or a security key that asks for one")
	}
	sum := sha256.Sum256(cred.PublicKey)
	key := Key{Fingerprint: hex.EncodeToString(sum[:8]), Label: c.label, EnrolledAt: s.now().UTC(), Credential: *cred}
	reg.Keys = append(reg.Keys, key)
	hash, err := s.writeRegistry(reg)
	if err != nil {
		return KeyInfo{}, err
	}
	s.expected, s.lastEnrolled = hash, s.now().UTC()
	s.record(by, "enrolled", key.Fingerprint, hash, map[string]string{"keys": fmt.Sprint(len(reg.Keys))})
	s.notify("Cerberus: passkey enrolled", fmt.Sprintf("A passkey (%s) was enrolled for out-of-band approval; %d enrolled. If this was not you, run `cerberus approvals keys`.", key.Fingerprint, len(reg.Keys)))
	return KeyInfo{Fingerprint: key.Fingerprint, Label: key.Label, EnrolledAt: key.EnrolledAt}, nil
}

// BeginRemove starts removing the key fp, which an enrolled key must
// authorize.
func (s *Service) BeginRemove(fp, origin string) (string, json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg, _, err := s.check(audit.Principal{Kind: audit.PrincipalAutomation, Via: "approvals"})
	if err != nil {
		return "", nil, err
	}
	if !hasKey(reg, fp) {
		return "", nil, redact.Guidance("no enrolled passkey %s; `cerberus approvals keys` lists them", fp)
	}
	if !s.allowedOrigin(origin) {
		return "", nil, ErrOrigin
	}
	id := newID()
	nonce := randomBytes(32)
	opts, err := beginLogin(reg, challenge([]byte("cerberus remove v1"), []byte(id), []byte(fp), nonce), origin)
	if err != nil {
		return "", nil, err
	}
	s.ceremonies[id] = &ceremony{kind: "remove", nonce: nonce, origin: origin, expires: s.now().Add(ceremonyTTL), fp: fp}
	return id, opts, nil
}

// FinishRemove removes the key once an enrolled key has signed for it.
func (s *Service) FinishRemove(by audit.Principal, ceremonyID string, assertion json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ceremonies[ceremonyID]
	if !ok || c.kind != "remove" || s.now().After(c.expires) {
		return ErrCeremony
	}
	delete(s.ceremonies, ceremonyID)
	reg, _, err := s.check(by)
	if err != nil {
		return err
	}
	if !s.enrolledAssertion(reg, assertion, challenge([]byte("cerberus remove v1"), []byte(ceremonyID), []byte(c.fp), c.nonce), c.origin) {
		return ErrNeedEnrolled
	}
	kept := reg.Keys[:0]
	for _, k := range reg.Keys {
		if k.Fingerprint != c.fp {
			kept = append(kept, k)
		}
	}
	reg.Keys = kept
	hash, err := s.writeRegistry(reg)
	if err != nil {
		return err
	}
	s.expected = hash
	s.record(by, "removed", c.fp, hash, map[string]string{"keys": fmt.Sprint(len(reg.Keys))})
	s.notify("Cerberus: passkey removed", fmt.Sprintf("Passkey %s was removed; %d enrolled.", c.fp, len(reg.Keys)))
	return nil
}

func hasKey(reg registryFile, fp string) bool {
	for _, k := range reg.Keys {
		if k.Fingerprint == fp {
			return true
		}
	}
	return false
}

func (s *Service) writeRegistry(reg registryFile) (string, error) {
	reg.Version = 1
	sort.SliceStable(reg.Keys, func(i, j int) bool { return reg.Keys[i].EnrolledAt.Before(reg.Keys[j].EnrolledAt) })
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Join(s.dir, registryName), data); err != nil {
		return "", err
	}
	return hashOf(data), nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Summary is the registry's state as one line, for `cerberus status` and the
// console header, and whether it is an alert. A key enrolled in the last 24h
// stays an alert that long, so an enrollment the operator did not make is
// seen (the TOFU window, CERB-GAP-879).
func (st Status) Summary(now time.Time) (string, bool) {
	switch {
	case st.State == StateCooldown:
		return fmt.Sprintf("COOL-DOWN: the passkey registry changed outside `cerberus approvals enroll`; out-of-band approvals are refused until %s", st.CooldownUntil.Local().Format(time.RFC3339)), true
	case len(st.Keys) == 0:
		return "out-of-band approval not set up: run `cerberus approvals enroll`", true
	case !st.LastEnrolledAt.IsZero() && now.Sub(st.LastEnrolledAt) < Cooldown:
		return fmt.Sprintf("key enrolled %s, %s (not you? see `cerberus approvals keys`)", st.LastEnrolledAt.Local().Format(time.RFC3339), keysCount(len(st.Keys))), true
	}
	return fmt.Sprintf("%s enrolled for out-of-band approval", keysCount(len(st.Keys))), false
}

func keysCount(n int) string {
	if n == 1 {
		return "1 key"
	}
	return fmt.Sprintf("%d keys", n)
}

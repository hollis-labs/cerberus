package dataaccess

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/pkg/plugin"
	auth "github.com/hollis-labs/libs/security/auth"
)

const Audience = "cerberus-data"
const Scope = "cerberus-data.read"

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)
var ErrDenied = errors.New("data access refused: current session identity, projection grant and policy are required")
var ErrUnavailable = errors.New("data access unavailable: authorization or audit is not current")

type Grant struct {
	ID         string    `json:"id"`
	Issuer     string    `json:"issuer"`
	Subject    string    `json:"subject"`
	Session    string    `json:"session"`
	TokenID    string    `json:"token_id"`
	Parent     string    `json:"parent"`
	Connector  string    `json:"connector"`
	Target     string    `json:"target"`
	Projection string    `json:"projection"`
	Version    string    `json:"version"`
	ExpiresAt  time.Time `json:"expires_at"`
	MaxRows    int       `json:"max_rows"`
	MaxBytes   int       `json:"max_bytes"`
}
type Request struct {
	GrantID string `json:"grant_id"`
	plugin.DataQuery
}
type Result struct {
	Columns []string          `json:"columns"`
	Rows    []json.RawMessage `json:"rows"`
}
type Event struct {
	Kind      string
	ID        string
	Principal auth.Principal
	Issuer    string
	Grant     Grant
	Query     plugin.DataQuery
	Rows      int
	Bytes     int
	Duration  time.Duration
	Outcome   string
}
type Service struct {
	Issuer      string
	Verifier    auth.Verifier
	Revocations *Revocations
	Grants      func() ([]Grant, error)
	// Authorize must check the host's current policy, both before execution and
	// before delivery. A data grant never widens a policy denial.
	Authorize func(context.Context, auth.Principal, Grant) error
	Execute   func(context.Context, auth.Principal, Grant, plugin.DataQuery) (Result, error)
	Audit     func(Event) error
	Now       func() time.Time
	mu        sync.Mutex
	active    map[string]int
	total     int
}

func ReadGrants(path string) ([]Grant, error) {
	f, err := os.Open(path) //nolint:gosec // operator-configured grant file, bounded and checked below
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Size() > 128<<10 {
		return nil, ErrUnavailable
	}
	var grants []Grant
	d := json.NewDecoder(io.LimitReader(f, 128<<10))
	d.DisallowUnknownFields()
	if d.Decode(&grants) != nil || d.Decode(new(any)) != io.EOF {
		return nil, ErrUnavailable
	}
	seen := map[string]bool{}
	for _, g := range grants {
		if !identifier.MatchString(g.ID) || seen[g.ID] || g.Issuer == "" || g.Subject == "" || g.Session == "" || g.TokenID == "" || g.Parent == "" || !identifier.MatchString(g.Connector) || !identifier.MatchString(g.Target) || !identifier.MatchString(g.Projection) || !identifier.MatchString(g.Version) || g.ExpiresAt.IsZero() || g.MaxRows < 1 || g.MaxRows > 100 || g.MaxBytes < 1 || g.MaxBytes > 1<<20 {
			return nil, ErrUnavailable
		}
		seen[g.ID] = true
	}
	return grants, nil
}

func (s *Service) Query(ctx context.Context, raw string, r Request) (result Result, resultErr error) {
	var audited bool
	var principal auth.Principal
	defer func() {
		if resultErr == nil || audited || s.Audit == nil {
			return
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			resultErr = ErrUnavailable
			return
		}
		// Rejected untrusted selectors never become audit target/name fields.
		q := plugin.DataQuery{Parameters: json.RawMessage(`{}`)}
		if len(r.Parameters) <= 64<<10 {
			q.Parameters = r.Parameters
		}
		if s.Audit(Event{Kind: "outcome", ID: hex.EncodeToString(id[:]), Principal: principal, Issuer: s.Issuer, Query: q, Outcome: "refused"}) != nil {
			resultErr = ErrUnavailable
		}
	}()
	if s.Verifier == nil || s.Revocations == nil || s.Grants == nil || s.Authorize == nil || s.Execute == nil || s.Audit == nil || s.Issuer == "" {
		return Result{}, ErrUnavailable
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	if len(raw) > 4096 || len(r.Parameters) > 64<<10 || !identifier.MatchString(r.GrantID) || !identifier.MatchString(r.Target) || !identifier.MatchString(r.Projection) || !identifier.MatchString(r.Version) {
		return Result{}, ErrDenied
	}
	p, err := s.Verifier.Verify(ctx, raw)
	if err != nil {
		return Result{}, ErrDenied
	}
	principal = p
	// Shared JWT validation supplies signature/issuer/audience verification;
	// this adapter additionally forbids leeway at the data boundary and binds
	// the entire five-minute session token (including its issuer and parent).
	parts := bytes.Split([]byte(raw), []byte("."))
	if len(parts) != 3 {
		return Result{}, ErrDenied
	}
	payload, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
	if err != nil {
		return Result{}, ErrDenied
	}
	var claims struct {
		Issued    int64  `json:"iat"`
		NotBefore int64  `json:"nbf"`
		Expires   int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Issuer != s.Issuer || claims.Issued <= 0 || claims.Expires <= claims.Issued || claims.Expires-claims.Issued > 300 || now().Before(time.Unix(claims.Issued, 0)) || now().Before(time.Unix(claims.NotBefore, 0)) || !now().Before(p.ExpiresAt) {
		return Result{}, ErrDenied
	}
	exactScope := false
	for _, v := range p.Scopes {
		if v == Scope {
			exactScope = true
		}
	}
	if !p.Verified || p.Kind != auth.KindAgent || p.Audience != Audience || !exactScope || p.ID == "" || p.SessionID == "" || p.TokenID == "" || p.Parent == "" {
		return Result{}, ErrDenied
	}
	grants, err := s.Grants()
	if err != nil {
		return Result{}, ErrUnavailable
	}
	var g Grant
	found := false
	for _, v := range grants {
		if v.ID == r.GrantID {
			g = v
			found = true
			break
		}
	}
	if !found || g.Issuer != s.Issuer || g.Subject != p.ID || g.Session != p.SessionID || g.TokenID != p.TokenID || g.Parent != p.Parent || g.Target != r.Target || g.Projection != r.Projection || g.Version != r.Version || g.MaxRows < 1 || g.MaxRows > 100 || g.MaxBytes < 1 || g.MaxBytes > 1<<20 {
		return Result{}, ErrDenied
	}
	check := func() error {
		if !now().Before(g.ExpiresAt) || !now().Before(p.ExpiresAt) {
			return ErrDenied
		}
		if s.Revocations.Check(p) != nil {
			return ErrUnavailable
		}
		current, e := s.Grants()
		if e != nil {
			return ErrUnavailable
		}
		same := false
		for _, v := range current {
			if v == g {
				same = true
				break
			}
		}
		if !same {
			return ErrDenied
		}
		if s.Authorize(ctx, p, g) != nil {
			return ErrDenied
		}
		return nil
	}
	if err = check(); err != nil {
		return Result{}, err
	}
	key := s.Issuer + "\x00" + p.SessionID
	s.mu.Lock()
	if s.active == nil {
		s.active = map[string]int{}
	}
	if s.active[key] >= 2 || s.total >= 8 {
		s.mu.Unlock()
		return Result{}, ErrUnavailable
	}
	s.active[key]++
	s.total++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active[key]--
		s.total--
		if s.active[key] == 0 {
			delete(s.active, key)
		}
		s.mu.Unlock()
	}()
	start := now()
	deadline := start.Add(2 * time.Second)
	for _, v := range []time.Time{g.ExpiresAt, p.ExpiresAt} {
		if v.Before(deadline) {
			deadline = v
		}
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	event := Event{Kind: "intent", Principal: p, Issuer: s.Issuer, Grant: g, Query: r.DataQuery}
	// One identifier is stable across intent/outcome, with no token material.
	event.ID = plugin.DataQueryDigest(plugin.DataQuery{Target: g.ID, Projection: p.TokenID, Version: start.Format(time.RFC3339Nano), Parameters: json.RawMessage(`{}`)})
	if s.Audit(event) != nil {
		return Result{}, ErrUnavailable
	}
	audited = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-callCtx.Done():
				return
			case <-t.C:
				if check() != nil {
					cancel()
					return
				}
			}
		}
	}()
	proof := Proof{Connector: g.Connector, Query: r.DataQuery, Permit: plugin.DataPermit{Digest: plugin.DataQueryDigest(r.DataQuery), ExpiresAt: deadline, MaxRows: g.MaxRows, MaxBytes: g.MaxBytes}}
	out, runErr := s.Execute(WithProof(callCtx, proof), p, g, r.DataQuery)
	// Check cancellation BEFORE stopping the monitor. Output stays buffered.
	if callCtx.Err() != nil {
		runErr = ErrDenied
	}
	if e := check(); e != nil {
		runErr = e
	}
	cancel()
	<-done
	b, encodeErr := json.Marshal(out)
	if encodeErr != nil || len(b) > g.MaxBytes || len(out.Rows) > g.MaxRows {
		runErr = ErrDenied
	}
	event.Kind = "outcome"
	event.Duration = now().Sub(start)
	event.Outcome = "refused"
	if runErr == nil {
		event.Outcome = "ok"
		event.Rows = len(out.Rows)
		event.Bytes = len(b)
	}
	if s.Audit(event) != nil {
		return Result{}, ErrUnavailable
	}
	if runErr != nil {
		return Result{}, ErrDenied
	}
	// Audit persistence may take time: never release data on authority that
	// expired or was revoked while completion was being written.
	if ctx.Err() != nil || check() != nil {
		return Result{}, ErrDenied
	}
	return out, nil
}

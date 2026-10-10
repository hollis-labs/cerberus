package cerbapi

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/oauth"
	"github.com/hollis-labs/cerberus/internal/scheduling"
)

// bearerProof exists only in an authenticated request context. Exported
// Principal labels cannot create it. The credential lives for that request
// only, under its redaction scope, and is never serialized or audited.
type bearerProof struct {
	auth     *Auth
	raw      string
	identity oauth.Identity
}
type bearerProofKey struct{}

// ScheduleGrant is provisioned by a trusted host, never by schedule JSON.
// A grant binds one verified issuer/subject to one app. AdminView grants only
// the separate view operation; it never permits cross-app mutations.
type ScheduleGrant struct {
	Issuer    string
	Subject   string
	TokenID   string
	App       string
	MaxJobs   int
	AdminView bool
}

// ScheduleGrants copies every host snapshot. Replace increments an epoch so
// requests waiting on policy or storage cannot use an obsolete grant.
type ScheduleGrants struct {
	mu     sync.RWMutex
	epoch  uint64
	grants []ScheduleGrant
}

func NewScheduleGrants(grants []ScheduleGrant) (*ScheduleGrants, error) {
	s := &ScheduleGrants{}
	if err := s.Replace(grants); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *ScheduleGrants) Replace(grants []ScheduleGrant) error {
	copyGrants := append([]ScheduleGrant(nil), grants...)
	seen := map[[2]string]bool{}
	for _, g := range copyGrants {
		if g.Issuer == "" || g.Subject == "" || !scheduling.ValidName(g.App) || g.MaxJobs < 1 || g.MaxJobs > scheduling.MaxAppJobs {
			return errors.New("schedule grants require verified issuer/subject, valid app and quota 1..128")
		}
		key := [2]string{g.Issuer, g.Subject}
		if seen[key] {
			return errors.New("schedule grant issuer/subject must be unique")
		}
		seen[key] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants = copyGrants
	s.epoch++
	return nil
}
func (s *ScheduleGrants) resolve(id oauth.Identity) (ScheduleGrant, uint64, error) {
	if s == nil {
		return ScheduleGrant{}, 0, scheduling.Refusal("unavailable", "this host has no trusted scheduler app grants")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.grants {
		if g.Issuer == id.Issuer && g.Subject == id.Subject && (g.TokenID == "" || g.TokenID == id.TokenID) {
			return g, s.epoch, nil
		}
	}
	return ScheduleGrant{}, 0, scheduling.Refusal("forbidden", "verified identity has no scheduler app grant")
}
func checkBearerProof(ctx context.Context, proof *bearerProof) error {
	if proof == nil || proof.auth == nil || ProcessAuth() != proof.auth || proof.auth.Verifier == nil {
		return scheduling.Refusal("forbidden", "scheduler app access requires current authenticated bearer proof")
	}
	if proof.identity.Expiry.IsZero() || !time.Now().Before(proof.identity.Expiry) {
		return scheduling.Refusal("forbidden", "scheduler caller proof expired")
	}
	id, err := proof.auth.Verifier.Verify(ctx, proof.raw)
	if err != nil || !reflect.DeepEqual(id, proof.identity) || ProcessAuth() != proof.auth || !time.Now().Before(proof.identity.Expiry) {
		return scheduling.Refusal("forbidden", "scheduler caller proof is no longer current")
	}
	return nil
}
func bindScheduleCall(ctx context.Context, r scheduling.Call, grants *ScheduleGrants) (context.Context, scheduling.Call, error) {
	proof, _ := ctx.Value(bearerProofKey{}).(*bearerProof)
	if err := checkBearerProof(ctx, proof); err != nil {
		return ctx, r, err
	}
	p := Principal{}
	p.Kind, p.Via, p.SelfReported = PrincipalAgent, ViaMCPHTTP, false
	p.Subject, p.Issuer, p.AuthMethod, p.TokenID = proof.identity.Subject, proof.identity.Issuer, AuthOAuth, proof.identity.TokenID
	p.Scopes = append([]string(nil), proof.identity.Scopes...)
	p.Client = clip(proof.identity.Client)
	ctx = WithPrincipal(ctx, p)
	g, epoch, err := grants.resolve(proof.identity)
	if err != nil {
		return ctx, r, err
	}
	if r.Operation == "admin_view" {
		if !g.AdminView {
			return ctx, r, scheduling.Refusal("forbidden", "admin view requires a separate host grant")
		}
	} else {
		if r.OwnerApp == "" {
			r.OwnerApp = g.App
		}
		if r.OwnerApp != g.App || r.Job != nil && r.Job.OwnerApp != g.App {
			return ctx, r, scheduling.Refusal("forbidden", "job namespace is outside the caller app grant")
		}
		for _, entry := range r.Registration {
			if entry.Job.OwnerApp != g.App {
				return ctx, r, scheduling.Refusal("forbidden", "registration namespace is outside the caller app grant")
			}
		}
	}
	check := func(checkCtx context.Context) error {
		if e := checkBearerProof(checkCtx, proof); e != nil {
			return e
		}
		current, currentEpoch, e := grants.resolve(proof.identity)
		if e != nil {
			return e
		}
		if currentEpoch != epoch || current != g {
			return scheduling.Refusal("forbidden", "scheduler app grant changed during request")
		}
		return nil
	}
	return scheduling.WithAccessCheck(ctx, check, g.MaxJobs), r, nil
}

type namespaceService struct {
	service scheduling.Service
	grants  *ScheduleGrants
}

func (s *namespaceService) Schedule(ctx context.Context, r scheduling.Call) (scheduling.Result, error) {
	ctx, r, err := bindScheduleCall(ctx, r, s.grants)
	if err != nil {
		return scheduling.Result{}, err
	}
	return s.service.Schedule(ctx, r)
}

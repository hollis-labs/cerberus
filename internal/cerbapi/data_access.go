package cerbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/dataaccess"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/plugin"
	auth "github.com/hollis-labs/libs/security/auth"
)

func dataPrincipal(p auth.Principal, issuer string) Principal {
	return Principal{Kind: PrincipalAgent, Via: ViaMCPHTTP, UID: -1, Client: "cerberus-data", Session: p.SessionID, OnBehalfOf: p.Parent, Subject: p.ID, Issuer: issuer, AuthMethod: AuthOAuth, TokenID: p.TokenID, Scopes: p.Scopes}
}
func dataArgs(connector string, q plugin.DataQuery) ExternalConnectorOperationArgs {
	var params map[string]any
	dec := json.NewDecoder(bytes.NewReader(q.Parameters))
	dec.UseNumber()
	_ = dec.Decode(&params)
	return ExternalConnectorOperationArgs{Connector: connector, Operation: "query", Config: map[string]any{"target": q.Target, "projection": q.Projection, "version": q.Version, "parameters": params}}
}

// BindDataService keeps execution inside the canonical connector gate. The
// extra audit pair is fail-closed even on completion, before any rows escape.
func (s *ExternalConnectorService) BindDataService(d *dataaccess.Service) {
	d.Authorize = func(ctx context.Context, p auth.Principal, g dataaccess.Grant) error {
		ctx = WithPrincipal(ctx, dataPrincipal(p, g.Issuer))
		args := dataArgs(g.Connector, plugin.DataQuery{Target: g.Target, Projection: g.Projection, Version: g.Version, Parameters: json.RawMessage(`{}`)})
		spec := s.auditSpecIn(ctx, args)
		if !spec.known || spec.op.Effect != contract.EffectReadSensitive || s.managedPlugins == nil || !s.managedPlugins.dataQueryPlugin(g.Connector) {
			return dataaccess.ErrDenied
		}
		_, target := auditTarget(spec)
		if brakeRefusal(ctx, spec, target, false) != nil {
			return dataaccess.ErrDenied
		}
		if PolicyDecisionPoint().Authorize(policyRequest(ctx, spec, target)).Decision != policy.Allow {
			return dataaccess.ErrDenied
		}
		return nil
	}
	d.Execute = func(ctx context.Context, p auth.Principal, g dataaccess.Grant, q plugin.DataQuery) (dataaccess.Result, error) {
		result, err := s.Execute(WithPrincipal(ctx, dataPrincipal(p, g.Issuer)), dataArgs(g.Connector, q))
		if err != nil {
			return dataaccess.Result{}, err
		}
		b, err := json.Marshal(result.Data)
		if err != nil {
			return dataaccess.Result{}, dataaccess.ErrUnavailable
		}
		var out dataaccess.Result
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if dec.Decode(&out) != nil {
			return out, dataaccess.ErrUnavailable
		}
		return out, nil
	}
	d.Audit = func(e dataaccess.Event) error {
		ctx := WithPrincipal(context.Background(), dataPrincipal(e.Principal, e.Issuer))
		spec := s.auditSpec(dataArgs(e.Grant.Connector, e.Query))
		target, resolved := auditTarget(spec)
		decision, posture := shadowDecision(ctx, spec, resolved)
		detail, _ := json.Marshal(struct {
			Grant      string `json:"grant"`
			Projection string `json:"projection"`
			Version    string `json:"version"`
			Parent     string `json:"parent"`
			Rows       int    `json:"rows"`
			Bytes      int    `json:"bytes"`
		}{e.Grant.ID, e.Grant.Projection, e.Grant.Version, e.Principal.Parent, e.Rows, e.Bytes})
		_, err := s.audit.Write(audit.Record{Kind: e.Kind, OperationID: e.ID, Principal: principalFor(ctx, spec), Connector: e.Grant.Connector, Operation: "data_query", Effect: string(contract.EffectReadSensitive), Target: target, ArgsDigest: s.audit.Digest(e.Query.Parameters), Note: string(detail), OutcomeCode: e.Outcome, DurationMS: e.Duration.Milliseconds(), Policy: decision, Posture: posture})
		return err
	}
}

func (s *ManagedPluginConnectorService) dataQueryPlugin(id string) bool {
	for _, p := range s.manager.InstalledPlugins() {
		if p.ID == id {
			for _, c := range p.Spec.Capabilities {
				if c.Name == plugin.DataQueryCapability {
					return true
				}
			}
		}
	}
	return false
}

// DataHTTP is deliberately a new versioned route: an older daemon answers404.
func DataHTTP(d *dataaccess.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Cerberus-Data-Version", "1")
		if r.URL.Path != "/data/v1/query" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if d == nil {
			http.Error(w, "data access is not configured", http.StatusServiceUnavailable)
			return
		}
		token, state := auth.ParseBearer(r.Header.Values("Authorization"))
		if state != "" {
			http.Error(w, "data session token required", http.StatusUnauthorized)
			return
		}
		ctx, scope := redact.EnsureScope(r.Context())
		scope.Add("data session", token)
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		var req dataaccess.Request
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid data request", http.StatusBadRequest)
			return
		}
		result, err := d.Query(ctx, token, req)
		if err != nil {
			code := http.StatusForbidden
			if errors.Is(err, dataaccess.ErrUnavailable) {
				code = http.StatusServiceUnavailable
			}
			http.Error(w, err.Error(), code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(result)
		_, _ = w.Write([]byte(scope.ReplaceValues(string(b))))
	})
}

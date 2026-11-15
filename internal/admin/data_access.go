package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/dataaccess"
	"github.com/hollis-labs/cerberus/internal/gate"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/plugin"
)

// The data lane (docs/data-access.md; slice 7d): the old cerbapi BindDataService, over this
// admin lane and its gate. A session-bound data query is admitted only for a read_sensitive
// operation of an installed plugin granted data_query_v1, that the brakes do not refuse and
// policy explicitly allows; it runs as the connector's "query" operation through Execute, the one
// path that gates, refuses and records; and its own fail-closed audit pair (operation
// data_query) is written on the gate's sink, before any rows are delivered.

// dataPrincipal is a verified data session as the gate labels it: an agent over MCP HTTP, with
// the session's subject, issuer, token and parent (old's dataPrincipal).
func dataPrincipal(p libauth.Principal, issuer string) gate.Principal {
	return gate.Principal{Kind: gate.PrincipalAgent, Via: gate.ViaMCPHTTP, UID: -1, Client: "cerberus-data", Session: p.SessionID,
		OnBehalfOf: p.Parent, Subject: p.ID, Issuer: issuer, AuthMethod: gate.AuthOAuth, TokenID: p.TokenID, Scopes: p.Scopes}
}

// dataArgs is the query as the connector's "query" operation.
func dataArgs(connector string, q plugin.DataQuery) OperationArgs {
	var params map[string]any
	dec := json.NewDecoder(bytes.NewReader(q.Parameters))
	dec.UseNumber()
	_ = dec.Decode(&params)
	return OperationArgs{Connector: connector, Operation: "query", Config: map[string]any{"target": q.Target, "projection": q.Projection, "version": q.Version, "parameters": params}}
}

// dataQueryPlugin reports an installed plugin granted the data query capability.
func (s *Service) dataQueryPlugin(id string) bool {
	ip, ok := s.plugins.manager.Installed(id)
	return ok && slices.Contains(ip.Granted, plugin.DataQueryCapability)
}

// BindDataService binds d's authorization, execution and audit to this lane and its gate.
func (s *Service) BindDataService(d *dataaccess.Service) {
	d.Authorize = func(ctx context.Context, p libauth.Principal, g dataaccess.Grant) error {
		ctx = gate.WithPrincipal(gate.BeginRequest(ctx, gate.SurfaceSocket), dataPrincipal(p, g.Issuer))
		args := dataArgs(g.Connector, plugin.DataQuery{Target: g.Target, Projection: g.Projection, Version: g.Version, Parameters: json.RawMessage(`{}`)})
		spec := s.specIn(ctx, args, nil)
		if !spec.Known || spec.Op.Effect != contract.EffectReadSensitive || !s.dataQueryPlugin(g.Connector) {
			return dataaccess.ErrDenied
		}
		if s.gate.DataAuthorize(ctx, spec) != nil {
			return dataaccess.ErrDenied
		}
		return nil
	}
	d.Execute = func(ctx context.Context, p libauth.Principal, g dataaccess.Grant, q plugin.DataQuery) (dataaccess.Result, error) {
		ctx = gate.WithPrincipal(gate.BeginRequest(ctx, gate.SurfaceSocket), dataPrincipal(p, g.Issuer))
		result, err := s.Execute(ctx, dataArgs(g.Connector, q))
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
		ctx := gate.WithPrincipal(gate.BeginRequest(context.Background(), gate.SurfaceSocket), dataPrincipal(e.Principal, e.Issuer))
		spec := s.specIn(ctx, dataArgs(e.Grant.Connector, e.Query), nil)
		detail, _ := json.Marshal(struct {
			Grant      string `json:"grant"`
			Projection string `json:"projection"`
			Version    string `json:"version"`
			Parent     string `json:"parent"`
			Rows       int    `json:"rows"`
			Bytes      int    `json:"bytes"`
		}{e.Grant.ID, e.Grant.Projection, e.Grant.Version, e.Principal.Parent, e.Rows, e.Bytes})
		return s.gate.DataRecord(ctx, spec, audit.Record{Kind: e.Kind, OperationID: e.ID, Connector: e.Grant.Connector, Operation: "data_query",
			Effect: string(contract.EffectReadSensitive), ArgsDigest: s.gate.Sink().Digest(e.Query.Parameters), Note: string(detail),
			OutcomeCode: e.Outcome, DurationMS: e.Duration.Milliseconds()})
	}
}

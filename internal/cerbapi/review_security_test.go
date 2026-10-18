package cerbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
	"github.com/hollis-labs/cerberus/pkg/outpost"
	"gopkg.in/yaml.v3"
)

type reviewHostHTTPClient struct {
	HostClient
	svc *HostService
}

func (c reviewHostHTTPClient) PlanHostAction(ctx context.Context, name string, args HostActionArgs) (*ConnectorPlan, error) {
	return c.svc.PlanAction(ctx, name, args)
}
func (c reviewHostHTTPClient) ExecuteHostAction(ctx context.Context, name string, args HostActionArgs) (outpost.Result, error) {
	return c.svc.ExecuteAction(ctx, name, args)
}

func TestReviewHTTPPlanAndGates(t *testing.T) {
	s, calls := outpostFixture(t)
	withPDP(t, constantPDP{decision: policy.Allow})
	withEnforcement(t, nil)
	handler := HostHTTP(reviewHostHTTPClient{svc: s}, "/hosts")
	request := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		r = r.WithContext(BeginRequest(r.Context(), SurfaceSocket))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("/hosts/box/actions/restart/plan", `{"request":{"unit":"one.service"}}`)
	if w.Code != 200 || calls.Load() != 0 {
		t.Fatalf("plan executed: %d %s", w.Code, w.Body.String())
	}
	w = request("/hosts/box/actions/restart", `{"request":{"unit":"one.service"}}`)
	if w.Code == 200 || calls.Load() != 0 {
		t.Fatalf("missing acknowledgment executed: %d", w.Code)
	}
	w = request("/hosts/box/actions/restart", `{"request":{"unit":"one.service"},"dry_run":true,"acknowledged":true}`)
	if w.Code != 400 || calls.Load() != 0 {
		t.Fatalf("unknown restrictive field executed: %d", w.Code)
	}
	withPDP(t, constantPDP{decision: policy.Deny})
	w = request("/hosts/box/actions/restart", `{"request":{"unit":"one.service"},"acknowledged":true}`)
	if w.Code == 200 || calls.Load() != 0 {
		t.Fatalf("denied HTTP request executed: %d", w.Code)
	}
}

func TestReviewUnitCannotBorrowLocalPolicyLabels(t *testing.T) {
	withPDP(t, policy.NewEvaluator(policy.File{Version: policy.FileVersion, PostureRules: []policy.PostureRule{{Match: policy.TargetMatch{Env: "dev", Owner: "self", Admin: "self"}, Posture: policy.PosturePermissive}}}, "test"))
	withEnforcement(t, nil)
	s, calls := outpostFixture(t)
	s.resources = ConfigResourceLookup(&config.ConfigV2{Resources: []config.ResourceDef{
		{ID: "innocent", Type: "process", Connector: "local", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}},
		{ID: "recovery", Type: "server", Connector: "ssh", Env: target.EnvProd, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}},
	}})
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	for _, unit := range []string{"other", "innocent"} {
		args := HostActionArgs{Request: outpost.Request{Verb: "restart", Unit: unit}, Acknowledged: true}
		shown, err := s.PlanAction(ctx, "box", args)
		if err != nil {
			t.Errorf("plan for remote %s: %v", unit, err)
			continue
		}
		got := shown.Plan.Target
		if got.Fields["name"] != "box" || got.Fields["unit"] != unit || got.Resource != "recovery" || got.Env != "prod" || got.Owner != "self" || got.Admin != "self" {
			t.Errorf("wrong remote identity: %+v", got)
		}
		_, err = s.ExecuteAction(ctx, "box", args)
		if connectorErrorCode(err) != ExternalConnectorApprovalPending || calls.Load() != 0 {
			t.Errorf("remote %s must require approval without reaching Outpost: %v, calls %d", unit, err, calls.Load())
		}
	}

}

func TestReviewHostApprovalBindsRequestAndDeclaration(t *testing.T) {
	for _, change := range []string{"unit", "host", "none"} {
		t.Run(change, func(t *testing.T) {
			withPDP(t, constantPDP{decision: policy.Approve})
			s, calls := outpostFixture(t)
			broker, err := NewBroker(s.audit, filepath.Join(t.TempDir(), "approvals"))
			if err != nil {
				t.Fatal(err)
			}
			broker.SetPresenceVerifier(acceptPresence{})
			withEnforcement(t, broker)
			ctx := BeginRequest(context.Background(), SurfaceSocket)
			args := HostActionArgs{Request: outpost.Request{Verb: "restart", Unit: "one.service"}, Acknowledged: true}
			_, err = s.ExecuteAction(ctx, "box", args)
			var coded *ExternalConnectorError
			if !errors.As(err, &coded) || coded.Approval == nil {
				t.Fatalf("approval not requested: %v", err)
			}
			args.ApprovalID = coded.Approval.ID
			if _, err = broker.Decide(ctx, args.ApprovalID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: "cli"}}); err != nil {
				t.Fatal(err)
			}
			if change == "unit" {
				args.Request.Unit = "two.service"
			}
			if change == "host" {
				var b []byte
				b, err = os.ReadFile(s.hostsPath)
				if err != nil {
					t.Fatal(err)
				}
				var f hosts.File
				if err = yaml.Unmarshal(b, &f); err != nil {
					t.Fatal(err)
				}
				f.Hosts[0].Reach.SSH = "different-recovery"
				b, err = yaml.Marshal(f)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(s.hostsPath, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.ExecuteAction(ctx, "box", args)
			if change == "none" {
				if err != nil || calls.Load() != 1 {
					t.Fatalf("approved call: %v", err)
				}
			} else if connectorErrorCode(err) != ExternalConnectorPlanStale || calls.Load() != 0 {
				t.Fatalf("stale plan reached endpoint: %v", err)
			}
		})
	}
}

func TestReviewActionRedaction(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Allow})
	withEnforcement(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(outpost.Result{Output: "fixture-token password=very-secret-value"})
	}))
	defer server.Close()
	s, _ := outpostFixture(t)
	b, err := os.ReadFile(s.hostsPath)
	if err != nil {
		t.Fatal(err)
	}
	var f hosts.File
	if err = yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	f.Hosts[0].Reach.Outpost.URL = server.URL
	b, err = yaml.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.hostsPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := s.ExecuteAction(BeginRequest(context.Background(), SurfaceSocket), "box", HostActionArgs{Request: outpost.Request{Verb: "logs", Unit: "example.service"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, "fixture-token") || strings.Contains(result.Output, "very-secret-value") {
		t.Fatalf("secret leaked: %q", result.Output)
	}
}

func TestHostPolicyLabelsRequireExplicitServerBinding(t *testing.T) {
	for _, binding := range []string{"absent", "wrong-type", "server"} {
		t.Run(binding, func(t *testing.T) {
			s, _ := outpostFixture(t)
			defs := []config.ResourceDef{
				{ID: "box", Type: "process", Connector: "local", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}},
				{ID: "innocent", Type: "process", Connector: "local", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}},
			}
			if binding != "absent" {
				bound := config.ResourceDef{ID: "recovery", Type: "server", Connector: "ssh", Env: target.EnvStaging, Owner: "operators", Admin: target.Admin{Default: target.AdminOwner, ByKind: map[string]string{"host.unit": target.AdminShared}}}
				if binding == "wrong-type" {
					bound.Type = "process"
					bound.Connector = "local"
				}
				defs = append(defs, bound)
			}
			s.resources = ConfigResourceLookup(&config.ConfigV2{Resources: defs})
			spec, _, err := s.actionSpec("box", HostActionArgs{Request: outpost.Request{Verb: "restart", Unit: "innocent"}}, true)
			if err != nil {
				t.Fatal(err)
			}
			recorded, resolved := auditTarget(spec)
			if resolved.ID != "box" || recorded.Fields["name"] != "box" || recorded.Fields["unit"] != "innocent" {
				t.Fatalf("lost remote target: %+v %+v", recorded, resolved)
			}
			if binding == "server" {
				if resolved.Resource != "recovery" || resolved.Env != target.EnvStaging || resolved.Owner != "operators" || resolved.AdminFor != target.AdminShared {
					t.Fatalf("lost declared labels: %+v", resolved)
				}
			} else if resolved.Resource != "" || resolved.Env != target.EnvUnknown || resolved.Owner != target.OwnerUnknown || resolved.AdminFor != target.AdminUnknown {
				t.Fatalf("borrowed labels: %+v", resolved)
			}
		})
	}
}

func TestHostAuditAndEgressUseDeclaredBinding(t *testing.T) {
	s, calls := outpostFixture(t)
	sink := audit.NewMemory()
	s.audit = sink
	s.resources = ConfigResourceLookup(&config.ConfigV2{Resources: []config.ResourceDef{
		{ID: "innocent", Type: "process", Connector: "local", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}},
		{ID: "recovery", Type: "server", Connector: "ssh", Env: target.EnvProd, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}},
	}})
	store := policy.Store{Dir: t.TempDir()}
	_, err := store.Apply(policy.File{Version: policy.FileVersion, Posture: policy.PosturePermissive, Egress: []policy.EgressRule{
		{ID: "production-output", Match: policy.TargetMatch{Env: "prod"}, Label: "untrusted", Action: policy.EgressMask, Mode: policy.EgressEnforce},
	}})
	if err != nil {
		t.Fatal(err)
	}
	withPDP(t, policy.NewReloading(store, nil))
	withEnforcement(t, nil)
	result, err := s.ExecuteAction(BeginRequest(context.Background(), SurfaceSocket), "box", HostActionArgs{Request: outpost.Request{Verb: "logs", Unit: "innocent"}})
	if err != nil || calls.Load() != 1 || !strings.Contains(result.Output, "masked by egress rule production-output") {
		t.Fatalf("wrong egress target: %+v %v", result, err)
	}
	for _, record := range sink.Records() {
		if record.Kind != audit.KindIntent && record.Kind != audit.KindOutcome {
			continue
		}
		if record.Target.Resource != "recovery" || record.Target.Env != "prod" || record.Target.Fields["name"] != "box" || record.Target.Fields["unit"] != "innocent" {
			t.Fatalf("wrong audit target: %+v", record.Target)
		}
	}
}

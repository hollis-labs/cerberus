package cerbapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/pkg/outpost"
	"gopkg.in/yaml.v3"
)

func outpostFixture(t *testing.T) (*HostService, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("credential not sent to executor")
		}
		_ = json.NewEncoder(w).Encode(outpost.Result{Output: goodProbeOutput})
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	path := filepath.Join(dir, "hosts.yaml")
	if err := os.WriteFile(token, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	f := hosts.File{Version: 1, Hosts: []hosts.Host{{Name: "box", Reach: hosts.Reach{Transport: "outpost", SSH: "recovery", Outpost: &hosts.OutpostReach{URL: server.URL, TokenFile: token}}}}}
	b, err := yaml.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return NewHostService(audit.NewMemory(), HostServiceOptions{HostsPath: path, StatePath: "-"}), &calls
}

func TestHostOutpostProbeUsesSharedHostState(t *testing.T) {
	s, calls := outpostFixture(t)
	v, err := s.Probe(context.Background(), "box")
	if err != nil {
		t.Fatal(err)
	}
	if v.State != hosts.StateOnline || v.Probed == nil || v.Probed.CPU != 4 || calls.Load() != 1 {
		t.Fatalf("probe: %+v", v)
	}
	if v.Host.Reach.SSH != "recovery" {
		t.Fatal("recovery transport lost")
	}
}

func TestHostOutpostPlanDoesNotExecuteAndBindsUnit(t *testing.T) {
	s, calls := outpostFixture(t)
	a := HostActionArgs{Request: outpost.Request{Verb: "restart", Unit: "one.service"}}
	p1, err := s.PlanAction(context.Background(), "box", a)
	if err != nil {
		t.Fatal(err)
	}
	a.Request.Unit = "two.service"
	p2, err := s.PlanAction(context.Background(), "box", a)
	if err != nil {
		t.Fatal(err)
	}
	if p1.PlanHash == p2.PlanHash || calls.Load() != 0 {
		t.Fatal("plan did not bind unit or executed remotely")
	}
}

func TestHostOutpostPolicyDenialPrecedesRemoteCall(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Deny})
	withEnforcement(t, nil)
	s, calls := outpostFixture(t)
	_, err := s.ExecuteAction(BeginRequest(context.Background(), SurfaceInProcess), "box", HostActionArgs{Request: outpost.Request{Verb: "restart", Unit: "one.service"}, Acknowledged: true})
	if connectorErrorCode(err) != ExternalConnectorPolicyDenied || calls.Load() != 0 {
		t.Fatalf("gate bypass: %v, calls %d", err, calls.Load())
	}
}

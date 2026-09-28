package cerbapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
)

// profileDaemon is a daemon whose saved state holds one profile.
func profileDaemon(t *testing.T, sink audit.Sink, profile infra.DeploymentProfile) *SocketClient {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := infra.SaveState(cfgPath, &infra.State{Version: 1, Profiles: []infra.DeploymentProfile{profile}}); err != nil {
		t.Fatal(err)
	}
	client := NewInProcessClient(WithConfigPath(cfgPath), WithDeploySecrets(noSecrets{}),
		WithResourceRuntimeService(NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(&config.ConfigV2{}))))
	return startConnectorSocket(t, client)
}

func devProfile(t *testing.T) infra.DeploymentProfile {
	return infra.DeploymentProfile{ID: "site", Provider: "vercel", RepoPath: linkedVercelRepo(t), DeployCommand: "true", VercelScope: "team",
		Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}}
}

// A deploy-profile run is asked for, confirmed and run in the daemon, where
// the broker holds its approval (CERB-GAP-886), and a profile labeled
// dev/self can be confirmed on the call.
func TestDeployProfileRunsInTheDaemon(t *testing.T) {
	sink := audit.NewMemory()
	broker := enforcedBroker(t, sink)
	client := profileDaemon(t, sink, devProfile(t))
	// The console's claim: a signed-in session.
	client.claim = func(context.Context) Principal { return WebSessionPrincipal("sess-1") }
	ctx := context.Background()
	_, err := client.RunDeploymentProfile(ctx, "site", WithAcknowledged(true))
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil || coded.Approval.Channel != approval.ChannelTTYConfirm || coded.Approval.ID == "" {
		t.Fatalf("a labeled dev/self profile should ask for a confirmation the daemon holds: %v", err)
	}
	shown, err := client.PlanDeploymentProfile(ctx, "site", WithAcknowledged(true))
	if err != nil || !strings.HasPrefix(shown.PlanHash, "sha256:") || shown.Plan.Target.Env != "dev" || shown.ComputedBy != SurfaceSocket {
		t.Fatalf("plan: %+v %v", shown, err)
	}
	result, err := client.RunDeploymentProfile(ctx, "site", WithAcknowledged(true), WithApprovalID(coded.Approval.ID), WithConfirmedPlanHash(shown.PlanHash))
	if err != nil || result == nil || !result.Success {
		t.Fatalf("confirmed run: %+v %v", result, err)
	}
	if a, _ := broker.Get(coded.Approval.ID); a.Status != approval.Consumed || a.Decision.By.Session != "sess-1" {
		t.Fatalf("approval %+v", a)
	}
}

// An unlabeled profile reads as unknown and needs out of band, as before.
func TestUnlabeledProfileNeedsOutOfBand(t *testing.T) {
	sink := audit.NewMemory()
	enforcedBroker(t, sink)
	profile := devProfile(t)
	profile.Env, profile.Owner, profile.Admin = "", "", target.Admin{}
	client := profileDaemon(t, sink, profile)
	_, err := client.RunDeploymentProfile(context.Background(), "site", WithAcknowledged(true))
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil || coded.Approval.Channel != approval.ChannelOutOfBand {
		t.Fatalf("err = %v", err)
	}
}

// An unknown profile is a recorded, refused attempt, answered 404; a
// confirm without a hash is refused before anything runs.
func TestDeployProfileRouteRefusals(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Allow})
	sink := audit.NewMemory()
	client := profileDaemon(t, sink, devProfile(t))
	if _, err := client.RunDeploymentProfile(context.Background(), "ghost", WithAcknowledged(true)); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown profile: %v", err)
	}
	if o := outcome(sink.Records()); o.Decision != audit.DecisionRefused || o.Operation != infra.OpRunProfile {
		t.Fatalf("the unknown profile's attempt was not recorded: %+v", o)
	}
	err := client.doJSON(context.Background(), http.MethodPost, "/deployments/site/run/confirm", map[string]any{"acknowledged": true}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs confirmed_plan_hash") {
		t.Fatalf("confirm without a hash: %v", err)
	}
}

// A daemon that predates running deploy profiles has no route for them:
// the console's request is refused, not run somewhere else.
func TestDeployProfileOnAnOlderDaemon(t *testing.T) {
	sock := filepath.Join(tempSocketDir(t), "old.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NewServeMux()} //nolint:gosec // a test server on a temp unix socket
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	_, err = NewSocketClient(sock).RunDeploymentProfile(context.Background(), "site", WithAcknowledged(true))
	if err == nil || !strings.Contains(err.Error(), "predates running deploy profiles") {
		t.Fatalf("err = %v", err)
	}
}

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	libauth "github.com/hollis-labs/libs/security/auth"
	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// The callers the brake server is called as, as the auth guard would put them on a request.
var (
	cliKey   = libauth.Principal{Verified: true, ID: "key:cli", Kind: libauth.KindService, Scopes: []string{api.ScopeRead, api.ScopeOperate, api.ScopeAdmin}}
	theOwner = libauth.Principal{Verified: true, ID: "user:u1", Kind: "user", SessionID: "s1", Scopes: []string{"*"}}
)

// brakeServer is the API over a real gate (gatetest), served to the CLI's client as *as, without
// the guard: these tests are about the CLI, the routes' scopes have their own. A nil world is
// observe mode.
type brakeServer struct {
	w  *gatetest.World
	as *libauth.Principal
	c  *apiClient
}

func newBrakeServer(t *testing.T, gated bool) *brakeServer {
	t.Helper()
	s := &brakeServer{as: &libauth.Principal{}}
	*s.as = cliKey
	o := api.Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir()}
	if gated {
		s.w = gatetest.New(t, gatetest.Options{})
		o.Gate = s.w.Gate
	}
	a, err := api.New(o)
	if err != nil {
		t.Fatal(err)
	}
	mux := a.Mux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(libauth.WithPrincipal(r.Context(), *s.as)))
	}))
	t.Cleanup(srv.Close)
	s.c = &apiClient{base: srv.URL + "/api", key: "test", keyFile: "/keys/cli.key", http: srv.Client()}
	return s
}

// run runs the brake verbs with args and stdin, on a terminal or not.
func (s *brakeServer) run(t *testing.T, terminal bool, stdin string, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "cerberus", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(brakeCommands(func() (*apiClient, error) { return s.c, nil }, func() bool { return terminal })...)
	var out bytes.Buffer
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func (s *brakeServer) state(t *testing.T) brake.State {
	t.Helper()
	st, _ := s.w.Brakes.Load()
	return st
}

// Engaging asks for nothing: no terminal, no phrase; it prints the old LOCKDOWN line, and the
// server records it as the key's brake_changed.
func TestLockdownEngagesWithoutConfirmation(t *testing.T) {
	s := newBrakeServer(t, true)
	out, err := s.run(t, false, "", "lockdown", "--reason", "agent loop")
	if err != nil {
		t.Fatal(err)
	}
	st := s.state(t)
	if st.Lockdown == nil || st.Lockdown.Reason != "agent loop" {
		t.Fatalf("the store: %+v", st)
	}
	if !strings.HasPrefix(out, "LOCKDOWN "+st.Lockdown.ID+" since ") || !strings.Contains(out, "by agent over api: agent loop\n  lift: cerberus lockdown --off\n") {
		t.Fatalf("lockdown output:\n%s", out)
	}
	var recorded bool
	for _, r := range s.w.Records(audit.KindBrakeChanged) {
		recorded = recorded || (r.Operation == "lockdown" && r.Note == "LOCKDOWN engaged: agent loop" && r.Principal.Client == "key:cli")
	}
	if !recorded {
		t.Fatalf("no brake_changed for the lockdown: %+v", s.w.Sink.Records())
	}
}

func TestFreezeNeedsAScope(t *testing.T) {
	s := newBrakeServer(t, true)
	if _, err := s.run(t, false, "", "freeze"); err == nil || !strings.Contains(err.Error(), "--scope") {
		t.Fatalf("a freeze without a scope: %v", err)
	}
	if _, err := s.run(t, false, "", "freeze", "--scope", "nope=x"); err == nil || !strings.Contains(err.Error(), "is not id, connector") {
		t.Fatalf("a bad scope: %v", err)
	}
	out, err := s.run(t, false, "", "freeze", "--scope", "env=prod", "--scope", "connector=docker", "--reason", "release")
	if err != nil {
		t.Fatal(err)
	}
	st := s.state(t)
	if len(st.Freezes) != 1 || st.Freezes[0].Match.String() != (policy.TargetMatch{Env: "prod", Connector: "docker"}).String() {
		t.Fatalf("freezes: %+v", st.Freezes)
	}
	f := st.Freezes[0]
	if !strings.Contains(out, "FREEZE "+f.ID+" on "+f.Match.String()+" since ") || !strings.Contains(out, ": release\n  lift: cerberus freeze --off "+f.ID+"\n") {
		t.Fatalf("freeze output:\n%s", out)
	}
	if out, err = s.run(t, false, "", "freeze", "list"); err != nil || !strings.Contains(out, "FREEZE "+f.ID) {
		t.Fatalf("freeze list: %v\n%s", err, out)
	}
	if _, err = s.run(t, true, "", "freeze", "--off"); err == nil || !strings.Contains(err.Error(), "name the freeze to lift") {
		t.Fatalf("--off without an id: %v", err)
	}
}

// Lifting is a person's act on a terminal, with the typed phrase. This CLI calls with a key,
// which the server refuses a lift (428 approval_required); the CLI says where it is done.
func TestLiftingNeedsATerminalAndThePhrase(t *testing.T) {
	s := newBrakeServer(t, true)
	if _, err := s.run(t, false, "", "lockdown"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(t, false, "lift lockdown\n", "lockdown", "--off"); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("lift off a terminal: %v", err)
	}
	if _, err := s.run(t, true, "yes\n", "lockdown", "--off"); err == nil || !strings.Contains(err.Error(), "nothing was lifted") {
		t.Fatalf("a wrong phrase: %v", err)
	}
	_, err := s.run(t, true, "lift lockdown\n", "lockdown", "--off")
	if err == nil || !strings.Contains(err.Error(), "428 approval_required") || !strings.Contains(err.Error(), "owner signed in to the console") || !strings.Contains(err.Error(), "/keys/cli.key") {
		t.Fatalf("a key lifting: %v", err)
	}
	if s.state(t).Lockdown == nil {
		t.Fatal("a key lifted the lockdown")
	}
}

// Where the server knows the caller as the owner's console session, the first --off asks for the
// approval and the CLI names it; once the owner approves, --off --approval <id> lifts. The CLI
// never reaches this with a key; it is the old flow, kept for the server's answer.
func TestLiftWithTheOwnersApproval(t *testing.T) {
	s := newBrakeServer(t, true)
	if _, err := s.run(t, false, "", "freeze", "--scope", "id=web"); err != nil {
		t.Fatal(err)
	}
	fid := s.state(t).Freezes[0].ID
	*s.as = theOwner
	out, err := s.run(t, true, "lift "+fid+"\n", "freeze", "--off", fid)
	if err != nil {
		t.Fatal(err)
	}
	approvals := s.w.Broker.List()
	if len(approvals) != 1 {
		t.Fatalf("approvals: %+v", approvals)
	}
	id := approvals[0].ID
	if !strings.Contains(out, "Approve "+id+" on the console") || !strings.Contains(out, "--approval "+id) {
		t.Fatalf("pending lift output:\n%s", out)
	}
	var decided any
	if err = s.c.post(context.Background(), "/approvals/"+id+"/decide", gate.ApprovalDecisionArgs{Approve: true}, &decided); err != nil {
		t.Fatal(err)
	}
	out, err = s.run(t, true, "lift "+fid+"\n", "freeze", "--off", fid, "--approval", id)
	if err != nil || !strings.Contains(out, "Lifted.\nNo brake is engaged.\n") {
		t.Fatalf("lift with the approval: %v\n%s", err, out)
	}
	if len(s.state(t).Freezes) != 0 {
		t.Fatal("the freeze was not lifted")
	}
}

// A reset is a person's act on a terminal, typing the phrase, which also travels to the server
// to check. A key is refused it (428); the owner's session resets.
func TestBreakerListAndReset(t *testing.T) {
	s := newBrakeServer(t, true)
	key := "agent|api|session:key:other"
	if _, _, err := s.w.Brakes.Suspend(key, audit.Principal{Kind: "agent", Via: "api", Client: "key:other", Session: "key:other"}, 3, "10m0s"); err != nil {
		t.Fatal(err)
	}
	sid := s.state(t).Suspensions[0].ID
	out, err := s.run(t, false, "", "breaker", "list")
	if err != nil || !strings.Contains(out, "SUSPENDED "+sid+": agent over api ("+key+") since ") || !strings.Contains(out, "after 3 policy denials in 10m0s\n  reset: cerberus breaker reset "+sid+"\n") {
		t.Fatalf("breaker list: %v\n%s", err, out)
	}
	if banners := statusBanners(context.Background(), s.c); len(banners) != 1 || !strings.HasPrefix(banners[0], "!!! BREAKER: 1 agent session(s) suspended") {
		t.Fatalf("status banners: %q", banners)
	}
	if _, err = s.run(t, false, "reset "+sid+"\n", "breaker", "reset", sid); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("reset off a terminal: %v", err)
	}
	if _, err = s.run(t, true, "yes\n", "breaker", "reset", sid); err == nil || !strings.Contains(err.Error(), "nothing was reset") {
		t.Fatalf("a wrong phrase: %v", err)
	}
	if _, err = s.run(t, true, "reset "+sid+"\n", "breaker", "reset", sid); err == nil || !strings.Contains(err.Error(), "428 approval_required") || !strings.Contains(err.Error(), "owner signed in to the console") {
		t.Fatalf("a key resetting: %v", err)
	}
	*s.as = theOwner
	if _, err = s.run(t, true, "reset sus_nope\n", "breaker", "reset", "sus_nope"); err == nil || !strings.Contains(err.Error(), "no suspension sus_nope") {
		t.Fatalf("an unknown suspension: %v", err)
	}
	out, err = s.run(t, true, "reset "+sid+"\n", "breaker", "reset", sid)
	if err != nil || !strings.Contains(out, "Reset.\nNo session is suspended.\n") {
		t.Fatalf("the owner's reset: %v\n%s", err, out)
	}
}

// status leads with the brakes, in the old banner lines; in observe mode there are none.
func TestStatusLeadsWithTheBrakes(t *testing.T) {
	st := brake.State{Lockdown: &brake.Lockdown{ID: "ldn_1", EngagedAt: time.Now(), By: audit.Principal{Kind: "agent", Via: "api"}, Reason: "loop"},
		Freezes: []brake.Freeze{{ID: "frz_2", Match: policy.TargetMatch{Env: "prod"}, EngagedAt: time.Now()}}}
	var out bytes.Buffer
	writeStatus(&out, brakeBanners(st), []statusSection{{"daemon", "running"}})
	lines := strings.Split(out.String(), "\n")
	if !strings.HasPrefix(lines[0], "!!! LOCKDOWN since ") || !strings.Contains(lines[0], "by agent over api: loop") ||
		!strings.HasPrefix(lines[1], "!!! FREEZE frz_2 on env=prod") || lines[2] != "Cerberus status" {
		t.Fatalf("status:\n%s", out.String())
	}
	if b := statusBanners(context.Background(), newBrakeServer(t, false).c); b != nil {
		t.Fatalf("observe mode banners: %q", b)
	}
}

// Observe mode holds no brakes: the verbs show the server's 503.
func TestBrakeVerbsInObserveMode(t *testing.T) {
	s := newBrakeServer(t, false)
	for _, args := range [][]string{{"lockdown"}, {"freeze", "list"}, {"breaker", "list"}} {
		if _, err := s.run(t, false, "", args...); err == nil || !strings.Contains(err.Error(), "503 brakes_unavailable") {
			t.Errorf("%s: %v", strings.Join(args, " "), err)
		}
	}
}

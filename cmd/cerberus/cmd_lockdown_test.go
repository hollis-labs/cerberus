package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
)

type fakeBrakes struct {
	engageErr error
	liftErr   error
	engaged   []cerbapi.BrakeEngageArgs
	lifted    []string
	reset     []string
	resetErr  error
	state     brake.State
}

func (f *fakeBrakes) Brakes(context.Context) (cerbapi.BrakesView, error) {
	return cerbapi.BrakesView{State: f.state}, nil
}

func (f *fakeBrakes) EngageBrake(_ context.Context, args cerbapi.BrakeEngageArgs) (cerbapi.BrakesView, error) {
	f.engaged = append(f.engaged, args)
	if f.engageErr != nil {
		return cerbapi.BrakesView{}, f.engageErr
	}
	return cerbapi.BrakesView{State: f.state}, nil
}

func (f *fakeBrakes) LiftBrake(_ context.Context, id string, args cerbapi.BrakeLiftArgs) (cerbapi.BrakesView, error) {
	f.lifted = append(f.lifted, id+"|"+args.ApprovalID)
	if f.liftErr != nil {
		return cerbapi.BrakesView{}, f.liftErr
	}
	return cerbapi.BrakesView{}, nil
}

func (f *fakeBrakes) ResetSuspension(_ context.Context, id string, args cerbapi.BrakeResetArgs) (cerbapi.BrakesView, error) {
	f.reset = append(f.reset, id+"|"+args.Typed)
	return cerbapi.BrakesView{}, f.resetErr
}

func brakesFixture(t *testing.T, terminal bool) (*fakeBrakes, brake.Store) {
	t.Helper()
	f := &fakeBrakes{state: brake.State{Lockdown: &brake.Lockdown{ID: "ldn_1", EngagedAt: time.Now(), By: audit.Principal{Kind: "human", Via: "cli"}}}}
	store := brake.Store{Dir: t.TempDir()}
	oldTerm, oldClient, oldStore, oldURL := policyIsTerminal, newBrakesClient, brakesStore, consolePageURL
	policyIsTerminal = func() bool { return terminal }
	newBrakesClient = func() (brakesClient, error) { return f, nil }
	brakesStore = func() (brake.Store, error) { return store, nil }
	consolePageURL = func(next string) (string, error) { return "http://localhost:4783/login?next=" + next, nil }
	t.Cleanup(func() {
		policyIsTerminal, newBrakesClient, brakesStore, consolePageURL = oldTerm, oldClient, oldStore, oldURL
		brakeFlags.reason, brakeFlags.off, brakeFlags.approval, brakeFlags.scope = "", false, "", nil
	})
	return f, store
}

func runBrakeCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&out)
	rootCmd.SetIn(strings.NewReader(stdin))
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetIn(nil) })
	err := rootCmd.Execute()
	return out.String(), err
}

// Engaging asks for nothing: no terminal, no phrase.
func TestLockdownEngagesWithoutConfirmation(t *testing.T) {
	f, _ := brakesFixture(t, false)
	out, err := runBrakeCmd(t, "", "lockdown", "--reason", "agent loop")
	if err != nil || len(f.engaged) != 1 || f.engaged[0].Reason != "agent loop" || f.engaged[0].Match != nil {
		t.Fatalf("lockdown: %v, %+v", err, f.engaged)
	}
	if !strings.Contains(out, "LOCKDOWN ldn_1") || !strings.Contains(out, "cerberus lockdown --off") {
		t.Fatalf("lockdown output:\n%s", out)
	}
}

// With the daemon down, engaging writes the brake store itself.
func TestLockdownEngagesWithTheDaemonDown(t *testing.T) {
	f, store := brakesFixture(t, false)
	f.engageErr = &cerbapi.DaemonUnreachableError{Path: "/nowhere", Err: errors.New("dial: no such file")}
	out, err := runBrakeCmd(t, "", "lockdown")
	if err != nil {
		t.Fatalf("lockdown with the daemon down: %v", err)
	}
	if st, _ := store.Load(); st.Lockdown == nil {
		t.Fatalf("the store holds no lockdown:\n%s", out)
	}
	if !strings.Contains(out, "the daemon is not running") {
		t.Fatalf("the fallback is not said:\n%s", out)
	}
}

func TestFreezeNeedsAScope(t *testing.T) {
	f, _ := brakesFixture(t, false)
	if _, err := runBrakeCmd(t, "", "freeze"); err == nil || !strings.Contains(err.Error(), "--scope") {
		t.Fatalf("a freeze without a scope: %v", err)
	}
	if _, err := runBrakeCmd(t, "", "freeze", "--scope", "env=prod", "--scope", "connector=docker"); err != nil || len(f.engaged) != 1 {
		t.Fatalf("freeze: %v", err)
	}
	if m := f.engaged[0].Match; m == nil || m.String() != (policy.TargetMatch{Env: "prod", Connector: "docker"}).String() {
		t.Fatalf("freeze match: %+v", f.engaged[0].Match)
	}
}

// Lifting is a person's act on a terminal, with the typed phrase.
func TestLiftingNeedsATerminalAndThePhrase(t *testing.T) {
	f, _ := brakesFixture(t, false)
	if _, err := runBrakeCmd(t, "lift lockdown\n", "lockdown", "--off"); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("lift off a terminal: %v", err)
	}
	policyIsTerminal = func() bool { return true }
	if _, err := runBrakeCmd(t, "yes\n", "lockdown", "--off"); err == nil || len(f.lifted) != 0 {
		t.Fatalf("a wrong phrase lifted: %v", err)
	}
	if out, err := runBrakeCmd(t, "lift lockdown\n", "lockdown", "--off"); err != nil || len(f.lifted) != 1 || !strings.Contains(out, "Lifted.") {
		t.Fatalf("lift: %v, %v\n%s", err, f.lifted, out)
	}
	if _, err := runBrakeCmd(t, "lift frz_1\n", "freeze", "--off", "frz_1"); err != nil || f.lifted[1] != "frz_1|" {
		t.Fatalf("freeze lift: %v, %v", err, f.lifted)
	}
}

// With a passkey enrolled the daemon answers approval_pending, and the
// command sends the person to the console with the id to lift with.
func TestLiftWithAPasskeyGoesToTheConsole(t *testing.T) {
	f, _ := brakesFixture(t, true)
	pending := &cerbapi.ExternalConnectorError{Code: cerbapi.ExternalConnectorApprovalPending, Connector: "brake", Operation: "lift_lockdown",
		Err: errors.New("approve it with your passkey"), Approval: &cerbapi.ApprovalRef{ID: "apr_9"}}
	f.liftErr = pending
	out, err := runBrakeCmd(t, "lift lockdown\n", "lockdown", "--off")
	if err != nil || !strings.Contains(out, "/approvals?id=apr_9") || !strings.Contains(out, "--approval apr_9") {
		t.Fatalf("passkey lift: %v\n%s", err, out)
	}
	f.liftErr = nil
	if _, err = runBrakeCmd(t, "lift lockdown\n", "lockdown", "--off", "--approval", "apr_9"); err != nil || f.lifted[1] != "|apr_9" {
		t.Fatalf("lift with the approval: %v, %v", err, f.lifted)
	}
}

// A lift never falls back in-process: the passkey check is the daemon's.
func TestLiftNeedsTheDaemon(t *testing.T) {
	f, store := brakesFixture(t, true)
	if _, _, err := store.EngageLockdown(audit.Principal{Kind: "human", Via: "cli"}, ""); err != nil {
		t.Fatal(err)
	}
	f.liftErr = &cerbapi.DaemonUnreachableError{Path: "/nowhere", Err: errors.New("dial")}
	if _, err := runBrakeCmd(t, "lift lockdown\n", "lockdown", "--off"); err == nil || !strings.Contains(err.Error(), "needs the daemon") {
		t.Fatalf("lift with the daemon down: %v", err)
	}
	if st, _ := store.Load(); st.Lockdown == nil {
		t.Fatal("a lift with the daemon down lifted the store")
	}
}

// status leads with the brakes.
func TestStatusLeadsWithTheBrakes(t *testing.T) {
	var out bytes.Buffer
	st := brake.State{Lockdown: &brake.Lockdown{ID: "ldn_1", EngagedAt: time.Now(), By: audit.Principal{Kind: "agent", Via: "mcp_stdio"}, Reason: "loop"},
		Freezes: []brake.Freeze{{ID: "frz_2", Match: policy.TargetMatch{Env: "prod"}, EngagedAt: time.Now()}}}
	if err := writeStatus(&out, statusReport{Web: []statusWebApp{}, Brakes: st}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if !strings.HasPrefix(lines[0], "!!! LOCKDOWN") || !strings.Contains(lines[0], "loop") || !strings.Contains(out.String(), "!!! FREEZE frz_2 on env=prod") {
		t.Fatalf("status:\n%s", out.String())
	}
}

// A reset is a person's act on a terminal, typing the phrase, which also
// travels to the daemon to check.
func TestBreakerReset(t *testing.T) {
	f, _ := brakesFixture(t, false)
	if _, err := runBrakeCmd(t, "reset sus_1\n", "breaker", "reset", "sus_1"); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("reset off a terminal: %v", err)
	}
	policyIsTerminal = func() bool { return true }
	if _, err := runBrakeCmd(t, "yes\n", "breaker", "reset", "sus_1"); err == nil || len(f.reset) != 0 {
		t.Fatalf("a wrong phrase: %v", err)
	}
	if out, err := runBrakeCmd(t, "reset sus_1\n", "breaker", "reset", "sus_1"); err != nil || f.reset[0] != "sus_1|reset sus_1" || !strings.Contains(out, "Reset.") {
		t.Fatalf("reset: %v %v\n%s", err, f.reset, out)
	}
}

// With the daemon down, the store is reset in-process, the phrase still
// checked.
func TestBreakerResetWithTheDaemonDown(t *testing.T) {
	f, store := brakesFixture(t, true)
	_, x, err := store.Suspend("agent|mcp_stdio|session:s1", audit.Principal{Kind: "agent", Via: "mcp_stdio", Session: "s1"}, 3, "10m")
	if err != nil {
		t.Fatal(err)
	}
	f.resetErr = &cerbapi.DaemonUnreachableError{Path: "/nowhere", Err: errors.New("dial")}
	// A person at a terminal, as DetectCLI classifies one.
	oldDetect := detectCLI
	detectCLI = func() cerbapi.CLIClassification { return cerbapi.ClassifyCLI(true, true, "") }
	t.Cleanup(func() { detectCLI = oldDetect })
	if _, err = runBrakeCmd(t, "reset "+x.ID+"\n", "breaker", "reset", x.ID); err != nil {
		t.Fatalf("reset with the daemon down: %v", err)
	}
	if st, _ := store.Load(); len(st.Suspensions) != 0 {
		t.Fatal("the store still holds the suspension")
	}
}

func TestBreakerListAndStatus(t *testing.T) {
	f, _ := brakesFixture(t, false)
	f.state = brake.State{Suspensions: []brake.Suspension{{ID: "sus_1", Key: "agent|mcp_stdio|session:s1", Principal: audit.Principal{Kind: "agent", Via: "mcp_stdio"}, Denials: 3, Window: "10m0s", TrippedAt: time.Now()}}}
	out, err := runBrakeCmd(t, "", "breaker", "list")
	if err != nil || !strings.Contains(out, "SUSPENDED sus_1: agent over mcp_stdio") || !strings.Contains(out, "cerberus breaker reset sus_1") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	var b bytes.Buffer
	if err = writeStatus(&b, statusReport{Web: []statusWebApp{}, Brakes: f.state}); err != nil || !strings.HasPrefix(b.String(), "!!! BREAKER: 1 agent session(s) suspended") {
		t.Fatalf("status: %v\n%s", err, b.String())
	}
}

// status leads with approvals an upgrade expired, so they are asked for
// again rather than silently gone (H4).
func TestStatusNamesApprovalsToApproveAgain(t *testing.T) {
	var out bytes.Buffer
	if err := writeStatus(&out, statusReport{Web: []statusWebApp{}, Grants: statusGrants{Active: []statusGrant{}, Reapprove: 2}}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "!!! 2 approval(s) or grant(s) were expired by the upgrade") {
		t.Fatalf("status:\n%s", out.String())
	}
}

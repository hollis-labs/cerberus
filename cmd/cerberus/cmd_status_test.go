package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/presence"
)

type statusFakeDaemon struct {
	principal cerbapi.Principal
	plugins   []cerbapi.ManagedPluginConnectorState
	passkeys  presence.Status
	err       error
}

func (f statusFakeDaemon) PasskeyStatus(context.Context) (presence.Status, error) {
	return f.passkeys, f.err
}

func (f statusFakeDaemon) WhoAmI(context.Context) (cerbapi.Principal, error) {
	return f.principal, f.err
}
func (f statusFakeDaemon) ListManagedPlugins(context.Context) ([]cerbapi.ManagedPluginConnectorState, error) {
	return f.plugins, f.err
}

// You and plugins are the daemon's view: who it sees and which plugins are
// still waiting for review.
func TestStatusReportsTheDaemonsView(t *testing.T) {
	you, plugins, _ := statusFromDaemon(context.Background(), statusFakeDaemon{
		principal: cerbapi.Principal{Kind: cerbapi.PrincipalHuman, Via: cerbapi.ViaCLI, UID: 501, UIDVerified: true},
		plugins:   []cerbapi.ManagedPluginConnectorState{{ID: "azure"}, {ID: "forge", ReviewPending: true}, {ID: "legacy", ReviewPending: true}},
	})
	if you.Principal == nil || you.Principal.Kind != cerbapi.PrincipalHuman || !you.Principal.UIDVerified {
		t.Fatalf("you = %+v", you)
	}
	if plugins.Installed != 3 || strings.Join(plugins.ReviewPending, ",") != "forge,legacy" {
		t.Fatalf("plugins = %+v", plugins)
	}

	you, plugins, passkeys := statusFromDaemon(context.Background(), statusFakeDaemon{err: &cerbapi.DaemonUnreachableError{Err: errors.New("dial")}})
	if you.Note != "the daemon is not running" || plugins.Note != "the daemon is not running" || passkeys.Note != "the daemon is not running" || you.Principal != nil {
		t.Fatalf("unreachable: you = %+v, plugins = %+v", you, plugins)
	}
}

// Audit is the chain's verify result and the last record's time; a broken
// chain says so and points at the command that explains it.
func TestStatusReportsTheAuditChain(t *testing.T) {
	dir := auditFixture(t, false)
	got := statusOfAudit()
	if !got.Intact || got.Records == 0 || got.LastRecord == nil || time.Since(*got.LastRecord) > time.Minute {
		t.Fatalf("audit = %+v", got)
	}

	current := filepath.Join(dir, time.Now().UTC().Format("2006-01")+".jsonl")
	data, err := os.ReadFile(current) //nolint:gosec // the test's own fixture
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(current, bytes.Replace(data, []byte(`"deploy"`), []byte(`"remove"`), 1), 0o600); err != nil { //nolint:gosec // the test's own fixture
		t.Fatal(err)
	}
	got = statusOfAudit()
	if got.Intact || !strings.Contains(got.Note, "cerberus audit verify") {
		t.Fatalf("tampered audit = %+v", got)
	}
}

// Web lists the consoles whose key files exist and whether each answers. It
// reads the address and never shows the key.
func TestStatusReportsWebConsolesWithoutTheirKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	live := httptest.NewServer(http.NotFoundHandler())
	defer live.Close()
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	goneURL := "http://" + gone.Addr().String()
	_ = gone.Close()

	dir := filepath.Join(home, ".cerberus", "web")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, u := range map[string]string{"login-live.key": live.URL, "login-gone.key": goneURL} {
		data, _ := json.Marshal(map[string]string{"url": u, "key": "SECRET-KEY-MATERIAL", "ttl": "2m0s"})
		if err = os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	apps := statusOfWebConsoles()
	running := map[string]bool{}
	for _, app := range apps {
		running[app.URL] = app.Running
	}
	if len(apps) != 2 || !running[live.URL] || running[goneURL] {
		t.Fatalf("web = %+v", apps)
	}
	var text bytes.Buffer
	report := statusReport{Posture: policy.PostureSummary{Global: policy.PostureSecure}, Web: apps}
	_ = writeStatus(&text, report)
	encoded, _ := json.Marshal(report)
	if strings.Contains(text.String()+string(encoded), "SECRET-KEY-MATERIAL") {
		t.Fatal("status showed a console's login key")
	}
}

func TestStatusTextIsCompact(t *testing.T) {
	last := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	err := writeStatus(&out, statusReport{
		Daemon:   statusDaemon{Running: true, PID: 42},
		Posture:  policy.PostureSummary{Global: policy.PostureSecure},
		You:      statusYou{Principal: &cerbapi.Principal{Kind: cerbapi.PrincipalAgent, Via: cerbapi.ViaCLI, UID: 501, UIDVerified: true}},
		Plugins:  statusPlugins{Installed: 7, ReviewPending: []string{"forge"}},
		Audit:    statusAudit{Intact: true, Records: 12, LastRecord: &last},
		Web:      []statusWebApp{{URL: "http://127.0.0.1:4783", Running: true}},
		Passkeys: statusPasskeys{Summary: "out-of-band approval not set up: run `cerberus approvals enroll`", Alert: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"daemon   running (pid 42)",
		"posture  secure",
		"you      agent via cli (uid 501, verified",
		"plugins  7 installed, 1 review pending: forge",
		"audit    chain intact, 12 records, last ",
		"web      running at http://127.0.0.1:4783",
		"passkeys ! out-of-band approval not set up: run `cerberus approvals enroll`",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
	if lines := strings.Count(out.String(), "\n"); lines != 8 {
		t.Errorf("status is %d lines, want 8:\n%s", lines, out.String())
	}
}

// The passkeys line is loud while nothing is enrolled, for a day after an
// enrollment (so one the operator did not make is seen), and during a
// cool-down; quiet otherwise.
func TestStatusPasskeysLine(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	keys := []presence.KeyInfo{{Fingerprint: "aa"}, {Fingerprint: "bb"}}
	for _, tc := range []struct {
		name  string
		st    presence.Status
		want  string
		alert bool
	}{
		{"none", presence.Status{State: presence.StateNotSetUp}, "not set up: run `cerberus approvals enroll`", true},
		{"recent", presence.Status{State: presence.StateOK, Keys: keys, LastEnrolledAt: now.Add(-time.Hour)}, "key enrolled ", true},
		{"settled", presence.Status{State: presence.StateOK, Keys: keys, LastEnrolledAt: now.Add(-48 * time.Hour)}, "2 keys enrolled", false},
		{"cooldown", presence.Status{State: presence.StateCooldown, Keys: keys, CooldownUntil: now.Add(time.Hour)}, "COOL-DOWN", true},
	} {
		got, alert := tc.st.Summary(now)
		if !strings.Contains(got, tc.want) || alert != tc.alert {
			t.Errorf("%s: %q alert=%v", tc.name, got, alert)
		}
	}
}

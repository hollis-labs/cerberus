package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
)

func bareToolName(name string) (string, error) { return name, nil }

// fakeSystemd is a user manager for tests: it knows a unit once daemon-reload
// has seen its file, and restart makes it active/running under a new PID —
// or, with crashLoop, leaves it restarting after a non-zero exit.
type fakeSystemd struct {
	unitDir   string
	calls     []string
	units     map[string]*fakeUnit
	crashLoop bool
	nextPID   int
	journal   string
	linger    string
}

type fakeUnit struct {
	loaded, enabled   bool
	active, sub       string
	pid, status, runs int
}

func newFakeSystemd(home string) *fakeSystemd {
	return &fakeSystemd{unitDir: SystemdUserUnitDir(home), units: map[string]*fakeUnit{}, nextPID: 4000}
}

func (f *fakeSystemd) unit(name string) *fakeUnit {
	u, ok := f.units[name]
	if !ok {
		u = &fakeUnit{active: "inactive", sub: "dead"}
		f.units[name] = u
	}
	return u
}

func (f *fakeSystemd) CombinedOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	switch name {
	case "journalctl":
		return []byte(f.journal), nil
	case "loginctl":
		if f.linger == "" {
			return []byte("Failed to get user: User not logged in"), errors.New("exit status 1")
		}
		return []byte("Linger=" + f.linger + "\n"), nil
	case "systemctl":
	default:
		return nil, fmt.Errorf("unexpected tool %s", name)
	}
	if len(args) < 2 || args[0] != "--user" {
		return nil, fmt.Errorf("systemctl call without --user: %v", args)
	}
	verb, rest := args[1], args[2:]
	notLoaded := func(unit string) ([]byte, error) {
		return []byte("Failed to " + verb + " " + unit + ": Unit " + unit + " not loaded."), errors.New("exit status 5")
	}
	switch verb {
	case "daemon-reload":
		for unitName, u := range f.units {
			_, err := os.Stat(filepath.Join(f.unitDir, unitName))
			u.loaded = err == nil
		}
		entries, _ := os.ReadDir(f.unitDir)
		for _, e := range entries {
			f.unit(e.Name()).loaded = true
		}
		return nil, nil
	case "show":
		u := f.unit(rest[0])
		load := "not-found"
		if u.loaded {
			load = "loaded"
		}
		fileState := "disabled"
		if u.enabled {
			fileState = "enabled"
		}
		code := "0"
		if u.runs > 0 {
			code = "1"
		}
		return []byte(fmt.Sprintf("Id=%s\nLoadState=%s\nActiveState=%s\nSubState=%s\nUnitFileState=%s\nResult=%s\nNeedDaemonReload=no\nMainPID=%d\nExecMainStatus=%d\nExecMainCode=%s\nNRestarts=%d\nExecStart={ path=/bin/app ; argv[]=/bin/app --token sentinel-arg ; ignore_errors=no }\n",
			rest[0], load, u.active, u.sub, fileState, map[bool]string{true: "exit-code", false: "success"}[u.status != 0], u.pid, u.status, code, u.runs)), nil
	case "enable":
		u := f.unit(rest[0])
		if !u.loaded {
			return notLoaded(rest[0])
		}
		u.enabled = true
		return nil, nil
	case "restart":
		u := f.unit(rest[0])
		if !u.loaded {
			return notLoaded(rest[0])
		}
		u.runs++
		if f.crashLoop {
			u.active, u.sub, u.pid, u.status = "activating", "auto-restart", 0, 1
			return nil, nil
		}
		f.nextPID++
		u.active, u.sub, u.pid, u.status = "active", "running", f.nextPID, 0
		return nil, nil
	case "stop", "disable":
		unitName := rest[len(rest)-1]
		u := f.unit(unitName)
		if !u.loaded {
			return notLoaded(unitName)
		}
		if verb == "disable" {
			u.enabled = false
		}
		u.active, u.sub, u.pid = "inactive", "dead", 0
		return nil, nil
	case "reset-failed":
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected systemctl verb %s", verb)
}

func (f *fakeSystemd) verbs() []string {
	out := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		fields := strings.Fields(call)
		if fields[0] == "systemctl" && fields[2] != "show" {
			out = append(out, fields[2])
		}
	}
	return out
}

type systemdFixture struct {
	home, workspace string
	fake            *fakeSystemd
	backend         systemdBackend
	res             *domain.Resource
	spec            ProcessSpec
}

func newSystemdFixture(t *testing.T) *systemdFixture {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil { //nolint:gosec // test workspace
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "app"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil { //nolint:gosec // test binary
		t.Fatal(err)
	}
	fake := newFakeSystemd(home)
	return &systemdFixture{
		home:      home,
		workspace: workspace,
		fake:      fake,
		backend: systemdBackend{
			runner:       fake,
			homeDir:      func() (string, error) { return home, nil },
			install:      artifactInstaller{homeDir: func() (string, error) { return home, nil }, now: time.Now},
			lookPath:     bareToolName,
			envPath:      func() string { return "/home/op/.local/go/bin:relative:/usr/bin" },
			selfPath:     func() (string, error) { return "/opt/cerberus/bin/cerberus", nil },
			startTimeout: 2 * time.Second,
		},
		res: &domain.Resource{ID: "app", ProjectID: "demo"},
		spec: ProcessSpec{
			Mode:       ProcessModeOSService,
			Supervisor: ProcessSupervisorSystemdUser,
			RunFrom:    ProcessRunFromArtifact,
			Dir:        workspace,
			Command:    []string{"./app", "serve", "--name", "100% $HOME \"quoted\""},
		},
	}
}

const fixtureUnit = "com.hollis-labs.cerberus.demo.app.service"

func (f *systemdFixture) unitText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(SystemdUserUnitDir(f.home), fixtureUnit)) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	return string(data)
}

func TestSystemdApplyInstallsEnablesAndStartsTheUnit(t *testing.T) {
	f := newSystemdFixture(t)
	result, err := f.backend.Apply(context.Background(), f.res, f.spec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Action != ApplyActionStarted {
		t.Fatalf("action = %q, want started", result.Action)
	}
	if got, want := strings.Join(f.fake.verbs(), ","), "daemon-reload,enable,restart"; got != want {
		t.Fatalf("systemctl verbs = %s, want %s", got, want)
	}
	unit := f.unitText(t)
	root := filepath.Join(f.home, ".cerberus", "apps", "demo", "app")
	for _, needle := range []string{
		"[Service]\n",
		"WorkingDirectory=" + f.workspace + "\n",
		`ExecStart="` + filepath.Join(root, "bin", "app") + `" "serve" "--name" "100%% $$HOME \"quoted\""` + "\n",
		"Restart=always\n",
		"StartLimitIntervalSec=0\n",
		"StandardOutput=append:" + filepath.Join(root, "logs", "stdout.log") + "\n",
		"StandardError=append:" + filepath.Join(root, "logs", "stderr.log") + "\n",
		// The daemon's PATH, absolute entries only, then systemd's own.
		`Environment="PATH=/home/op/.local/go/bin:/usr/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/sbin:/bin"` + "\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(unit, needle) {
			t.Fatalf("unit missing %q:\n%s", needle, unit)
		}
	}
	if strings.Contains(unit, "DBUS_SESSION_BUS_ADDRESS") || strings.Contains(unit, "XDG_RUNTIME_DIR") {
		t.Fatalf("unit must not override the manager's bus environment:\n%s", unit)
	}
	info, err := os.Stat(filepath.Join(SystemdUserUnitDir(f.home), fixtureUnit))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unit mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(filepath.Join(root, "logs")); err != nil {
		t.Fatalf("log dir: %v", err)
	}
}

func TestSystemdApplyNoopsWhenRunningAndCurrent(t *testing.T) {
	f := newSystemdFixture(t)
	if _, err := f.backend.Apply(context.Background(), f.res, f.spec); err != nil {
		t.Fatal(err)
	}
	f.fake.calls = nil
	result, err := f.backend.Apply(context.Background(), f.res, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ApplyActionNoop {
		t.Fatalf("action = %q, want noop", result.Action)
	}
	if verbs := f.fake.verbs(); len(verbs) != 0 {
		t.Fatalf("a current, running unit ran %v", verbs)
	}
}

func TestSystemdApplyReloadsWhenTheUnitOrArtifactChanges(t *testing.T) {
	f := newSystemdFixture(t)
	if _, err := f.backend.Apply(context.Background(), f.res, f.spec); err != nil {
		t.Fatal(err)
	}

	f.fake.calls = nil
	f.spec.Env = map[string]string{"REGION": "us-east"}
	result, err := f.backend.Apply(context.Background(), f.res, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ApplyActionReloaded || !result.PlistChanged {
		t.Fatalf("result = %+v, want reloaded with the unit changed", result)
	}
	if got, want := strings.Join(f.fake.verbs(), ","), "daemon-reload,restart"; got != want {
		t.Fatalf("systemctl verbs = %s, want %s", got, want)
	}

	f.fake.calls = nil
	if writeErr := os.WriteFile(filepath.Join(f.workspace, "app"), []byte("#!/bin/sh\necho changed\n"), 0o755); writeErr != nil { //nolint:gosec // test binary
		t.Fatal(writeErr)
	}
	result, err = f.backend.Apply(context.Background(), f.res, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ApplyActionReloaded || !result.ArtifactChanged || result.PlistChanged {
		t.Fatalf("result = %+v, want reloaded with only the artifact changed", result)
	}
	if got, want := strings.Join(f.fake.verbs(), ","), "restart"; got != want {
		t.Fatalf("systemctl verbs = %s, want %s (no daemon-reload for an unchanged unit)", got, want)
	}
}

func TestSystemdApplyStartsAStoppedUnit(t *testing.T) {
	f := newSystemdFixture(t)
	ctx := context.Background()
	if _, err := f.backend.Apply(ctx, f.res, f.spec); err != nil {
		t.Fatal(err)
	}
	if err := f.backend.Stop(ctx, f.res, f.spec); err != nil {
		t.Fatal(err)
	}
	if state, err := f.backend.Status(ctx, f.res, f.spec); err != nil || state != domain.StateStopped {
		t.Fatalf("after Stop: %q, %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(SystemdUserUnitDir(f.home), fixtureUnit)); err != nil {
		t.Fatalf("Stop must leave the unit installed: %v", err)
	}
	f.fake.calls = nil
	result, err := f.backend.Apply(ctx, f.res, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ApplyActionStarted {
		t.Fatalf("action = %q, want started", result.Action)
	}
	if state, err := f.backend.Status(ctx, f.res, f.spec); err != nil || state != domain.StateRunning {
		t.Fatalf("after Apply: %q, %v", state, err)
	}
}

func TestSystemdEnvFileIsReferencedAndItsChangeRestarts(t *testing.T) {
	f := newSystemdFixture(t)
	envFile := filepath.Join(f.workspace, ".env")
	if err := os.WriteFile(envFile, []byte("REGION=one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.spec.EnvFile = ".env"
	if _, err := f.backend.Apply(context.Background(), f.res, f.spec); err != nil {
		t.Fatal(err)
	}
	unit := f.unitText(t)
	if !strings.Contains(unit, "EnvironmentFile=-"+envFile+"\n") {
		t.Fatalf("unit does not reference the env file:\n%s", unit)
	}
	if strings.Contains(unit, "REGION=one") {
		t.Fatalf("env file values must stay in the env file:\n%s", unit)
	}

	if err := os.WriteFile(envFile, []byte("REGION=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := f.backend.Apply(context.Background(), f.res, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ApplyActionReloaded || !result.PlistChanged {
		t.Fatalf("a changed env file must restart the service: %+v", result)
	}
}

func TestSystemdFrontsSecretRefsWithRunSecrets(t *testing.T) {
	f := newSystemdFixture(t)
	f.spec.Env = map[string]string{"API_TOKEN": "keychain://acme/api-token", "REGION": "us-east"} //nolint:gosec // a reference, not a credential
	if _, err := f.backend.Apply(context.Background(), f.res, f.spec); err != nil {
		t.Fatal(err)
	}
	unit := f.unitText(t)
	if !strings.Contains(unit, `ExecStart="/opt/cerberus/bin/cerberus" "run-secrets" "--" "`) {
		t.Fatalf("ExecStart is not fronted by run-secrets:\n%s", unit)
	}
	if !strings.Contains(unit, `Environment="API_TOKEN=keychain://acme/api-token"`) {
		t.Fatalf("the unit must keep the reference, not a credential:\n%s", unit)
	}
	unitPath := filepath.Join(SystemdUserUnitDir(f.home), fixtureUnit)
	if err := CheckSecretReferenceUnit(unitPath, f.spec); err != nil {
		t.Fatalf("CheckSecretReferenceUnit: %v", err)
	}

	stale := strings.Replace(unit, `"/opt/cerberus/bin/cerberus" "run-secrets" "--" `, "", 1)
	if err := validateSecretReferenceUnit([]byte(stale), f.spec); err == nil || !strings.Contains(err.Error(), "run-secrets") {
		t.Fatalf("an unfronted unit must fail the check, got %v", err)
	}
}

func TestSystemdLeavesLiteralEnvUnfronted(t *testing.T) {
	f := newSystemdFixture(t)
	f.spec.Env = map[string]string{"REGION": "us-east"}
	if _, err := f.backend.Apply(context.Background(), f.res, f.spec); err != nil {
		t.Fatal(err)
	}
	if unit := f.unitText(t); strings.Contains(unit, "run-secrets") {
		t.Fatalf("literal env must not be fronted:\n%s", unit)
	}
}

func TestSystemdApplyKeepsAnExplicitPath(t *testing.T) {
	f := newSystemdFixture(t)
	f.spec.Env = map[string]string{"PATH": "/custom/bin"}
	if _, err := f.backend.Apply(context.Background(), f.res, f.spec); err != nil {
		t.Fatal(err)
	}
	if unit := f.unitText(t); !strings.Contains(unit, `Environment="PATH=/custom/bin"`+"\n") {
		t.Fatalf("a resource's own PATH must win:\n%s", unit)
	}
}

func TestSystemdApplyFailsWhenTheServiceCrashLoops(t *testing.T) {
	f := newSystemdFixture(t)
	f.fake.crashLoop = true
	f.fake.journal = "systemd[1]: app.service: Main process exited, code=exited, status=1/FAILURE"
	f.backend.startTimeout = 300 * time.Millisecond
	_, err := f.backend.Apply(context.Background(), f.res, f.spec)
	if err == nil {
		t.Fatal("Apply confirmed a service that never ran")
	}
	for _, needle := range []string{
		`systemd service "` + fixtureUnit + `" did not reach running`,
		"startup was not confirmed and systemd may still retry",
		"check `cerberus resource status app` before retrying the operation",
		"status=1/FAILURE",
		"unit: " + filepath.Join(SystemdUserUnitDir(f.home), fixtureUnit),
	} {
		if !strings.Contains(err.Error(), needle) {
			t.Fatalf("error missing %q: %v", needle, err)
		}
	}
	state, statusErr := f.backend.Status(context.Background(), f.res, f.spec)
	if statusErr != nil || state != domain.StateFailed {
		t.Fatalf("a crash-looping unit is %q (%v), want failed", state, statusErr)
	}
}

func TestSystemdStopIgnoresAMissingUnit(t *testing.T) {
	f := newSystemdFixture(t)
	if err := f.backend.Stop(context.Background(), f.res, f.spec); err != nil {
		t.Fatalf("Stop of an uninstalled unit: %v", err)
	}
}

func TestSystemdReloadRequiresAnInstalledUnit(t *testing.T) {
	f := newSystemdFixture(t)
	err := f.backend.Reload(context.Background(), f.res, f.spec)
	if err == nil || !strings.Contains(err.Error(), "use resource apply") {
		t.Fatalf("Reload of an uninstalled unit: %v", err)
	}
	if _, err := f.backend.Apply(context.Background(), f.res, f.spec); err != nil {
		t.Fatal(err)
	}
	f.fake.calls = nil
	if err := f.backend.Reload(context.Background(), f.res, f.spec); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := strings.Join(f.fake.verbs(), ","); got != "restart" {
		t.Fatalf("Reload ran %s, want restart", got)
	}
}

func TestSystemdRemoveDisablesAndDeletesEverything(t *testing.T) {
	f := newSystemdFixture(t)
	if _, err := f.backend.Apply(context.Background(), f.res, f.spec); err != nil {
		t.Fatal(err)
	}
	f.fake.calls = nil
	if err := f.backend.Remove(context.Background(), f.res, f.spec); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got, want := strings.Join(f.fake.verbs(), ","), "disable,daemon-reload,reset-failed"; got != want {
		t.Fatalf("systemctl verbs = %s, want %s", got, want)
	}
	if !strings.Contains(f.fake.calls[0], "disable --now "+fixtureUnit) {
		t.Fatalf("first call = %q, want disable --now", f.fake.calls[0])
	}
	if _, err := os.Stat(filepath.Join(SystemdUserUnitDir(f.home), fixtureUnit)); !os.IsNotExist(err) {
		t.Fatalf("unit still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".cerberus", "apps", "demo", "app")); !os.IsNotExist(err) {
		t.Fatalf("install root still present: %v", err)
	}
	// Removing again is not an error.
	if err := f.backend.Remove(context.Background(), f.res, f.spec); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

func TestSystemdInspectRedactsAndTailsTheJournal(t *testing.T) {
	f := newSystemdFixture(t)
	f.fake.journal = "app.service: Started with --password sentinel-journal"
	if _, err := f.backend.Apply(context.Background(), f.res, f.spec); err != nil {
		t.Fatal(err)
	}
	rec, err := f.backend.Inspect(context.Background(), f.res, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Loaded || rec.ActiveState != "active" || rec.SubState != "running" || rec.PID == 0 {
		t.Fatalf("record = %+v", rec)
	}
	if rec.Diagnosis != "service is loaded and running" {
		t.Fatalf("diagnosis = %q", rec.Diagnosis)
	}
	if strings.Contains(rec.Raw, "sentinel-arg") || strings.Contains(rec.Journal, "sentinel-journal") {
		t.Fatalf("credential survived inspect: raw=%q journal=%q", rec.Raw, rec.Journal)
	}
	if !strings.Contains(rec.Journal, "Started with") {
		t.Fatalf("journal tail missing: %q", rec.Journal)
	}
}

func TestSystemdStateMapping(t *testing.T) {
	one := 1
	cases := []struct {
		name string
		rec  systemdRecord
		want domain.State
	}{
		{"not installed", systemdRecord{LoadState: "not-found", ActiveState: "inactive"}, domain.StateStopped},
		{"running", systemdRecord{LoadState: "loaded", ActiveState: "active", SubState: "running"}, domain.StateRunning},
		{"starting", systemdRecord{LoadState: "loaded", ActiveState: "activating", SubState: "start"}, domain.StateStarting},
		{"crash loop", systemdRecord{LoadState: "loaded", ActiveState: "activating", SubState: "auto-restart", ExecMainStatus: &one, Result: "exit-code"}, domain.StateFailed},
		{"stopped cleanly", systemdRecord{LoadState: "loaded", ActiveState: "inactive", SubState: "dead", Result: "success"}, domain.StateStopped},
		{"exited non-zero", systemdRecord{LoadState: "loaded", ActiveState: "inactive", SubState: "dead", ExecMainStatus: &one}, domain.StateFailed},
		{"failed", systemdRecord{LoadState: "loaded", ActiveState: "failed", SubState: "failed"}, domain.StateFailed},
		{"bad unit", systemdRecord{LoadState: "bad-setting", ActiveState: "inactive"}, domain.StateFailed},
		{"masked", systemdRecord{LoadState: "masked", ActiveState: "inactive"}, domain.StateStopped},
		{"unrecognized", systemdRecord{LoadState: "loaded", ActiveState: "novel"}, domain.StateUnknown},
	}
	for _, tc := range cases {
		if got := systemdState(tc.rec); got != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSystemdStatusNeverReturnsUnknownWithoutAnError(t *testing.T) {
	f := newSystemdFixture(t)
	f.backend.runner = staticRunner("LoadState=loaded\nActiveState=novel\nSubState=x\n")
	state, err := f.backend.Status(context.Background(), f.res, f.spec)
	if state != domain.StateUnknown || err == nil || !strings.Contains(err.Error(), "ActiveState=novel") {
		t.Fatalf("Status = %q, %v", state, err)
	}
}

func TestParseSystemdShowIgnoresTheStatusOfAUnitThatNeverRan(t *testing.T) {
	rec := parseSystemdShow("LoadState=loaded\nActiveState=inactive\nSubState=dead\nExecMainStatus=0\nExecMainCode=0\nResult=success\n")
	if rec.ExecMainStatus != nil {
		t.Fatalf("ExecMainStatus = %d, want unset", *rec.ExecMainStatus)
	}
	rec = parseSystemdShow("LoadState=loaded\nActiveState=inactive\nExecMainStatus=3\nExecMainCode=1\nNRestarts=4\nMainPID=0\n")
	if rec.ExecMainStatus == nil || *rec.ExecMainStatus != 3 || rec.Restarts != 4 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestSystemdExecutableResolution(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test binary
		t.Fatal(err)
	}
	cases := map[string]string{
		"/abs/app":  "/abs/app",
		"./app":     "/work/app",
		"bin/app":   "/work/bin/app",
		"tool":      tool,
		"not-found": "not-found",
	}
	for program, want := range cases {
		if got := systemdExecutable(program, "/work", "/nowhere:"+dir); got != want {
			t.Errorf("systemdExecutable(%q) = %q, want %q", program, got, want)
		}
	}
}

func TestSystemdExecLineRoundTrips(t *testing.T) {
	args := []string{"/bin/app", "plain", "with space", `back\slash`, `"quoted"`, "100%", "$HOME", ""}
	if got := splitSystemdWords(systemdExecLine(args)); strings.Join(got, "|") != strings.Join(args, "|") {
		t.Fatalf("round trip = %q, want %q", got, args)
	}
}

func TestSystemdRenderRefusesNewlines(t *testing.T) {
	f := newSystemdFixture(t)
	f.spec.Env = map[string]string{"BAD": "line one\nExecStartPre=/bin/evil"}
	if _, err := f.backend.Apply(context.Background(), f.res, f.spec); err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("Apply with a newline in env: %v", err)
	}
}

func TestSystemdLinger(t *testing.T) {
	f := newSystemdFixture(t)
	f.fake.linger = "yes"
	if on, err := f.backend.linger(context.Background()); err != nil || !on {
		t.Fatalf("linger = %v, %v; want true", on, err)
	}
	f.fake.linger = "no"
	if on, err := f.backend.linger(context.Background()); err != nil || on {
		t.Fatalf("linger = %v, %v; want false", on, err)
	}
}

func TestResolveSystemToolFallsBackBeyondPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	got, err := resolveSystemTool("sh")
	if err != nil || !filepath.IsAbs(got) {
		t.Fatalf("resolveSystemTool(sh) = %q, %v", got, err)
	}
	if _, err := resolveSystemTool("cerberus-no-such-tool"); err == nil {
		t.Fatal("a missing tool must be an error")
	}
}

type staticRunner string

func (s staticRunner) CombinedOutput(context.Context, string, ...string) ([]byte, error) {
	return []byte(s), nil
}

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

type fakeCommandRunner struct {
	calls     []fakeCall
	out       map[string][]byte
	err       map[string]error
	keepState bool
}

type sequencedCommandError struct {
	values []error
	idx    int
}

type fakeCall struct {
	name string
	args []string
}

func setLaunchdPrintNotFound(runner *fakeCommandRunner, label string) {
	key := "launchctl print gui/501/" + label
	runner.out[key] = []byte("Could not find service")
	runner.err[key] = errors.New("exit status 113")
}

func (f *fakeCommandRunner) CombinedOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{name: name, args: append([]string(nil), args...)})
	key := name + " " + strings.Join(args, " ")
	err := f.err[key]
	if name == "launchctl" && len(args) == 3 && args[0] == "kickstart" && err == nil && !f.keepState {
		printKey := "launchctl print " + args[2]
		f.out[printKey] = []byte("state = running\npid = 1234")
		delete(f.err, printKey)
	}
	if seq, ok := err.(*sequencedCommandError); ok {
		return f.out[key], seq.next()
	}
	return f.out[key], err
}

func (s *sequencedCommandError) next() error {
	if len(s.values) == 0 {
		return nil
	}
	if s.idx >= len(s.values) {
		return s.values[len(s.values)-1]
	}
	out := s.values[s.idx]
	s.idx++
	return out
}

func (s *sequencedCommandError) Error() string {
	if len(s.values) == 0 || s.values[0] == nil {
		return ""
	}
	return s.values[0].Error()
}

func TestLaunchdProgramArgumentsArtifact(t *testing.T) {
	args, err := launchdProgramArguments(InstallLayout{ArtifactPath: "/tmp/app/bin/api"}, ProcessSpec{
		RunFrom: ProcessRunFromArtifact,
		Command: []string{"./api", "serve", "--port", "8080"},
	})
	if err != nil {
		t.Fatalf("launchdProgramArguments failed: %v", err)
	}
	got := strings.Join(args, " ")
	want := "/tmp/app/bin/api serve --port 8080"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestRenderLaunchdPlist(t *testing.T) {
	data := plistTemplateData{
		Label:             "com.example.app",
		ProgramArguments:  []string{"/tmp/app/bin/app", "serve"},
		WorkingDirectory:  "/tmp/app/current",
		StandardOutPath:   "/tmp/app/logs/stdout.log",
		StandardErrorPath: "/tmp/app/logs/stderr.log",
		Environment:       map[string]string{"FOO": "bar"},
		EnvironmentEntries: []plistEnvEntry{
			{Key: "FOO", Value: "bar"},
		},
	}
	out, err := renderLaunchdPlist(data)
	if err != nil {
		t.Fatalf("renderLaunchdPlist failed: %v", err)
	}
	text := string(out)
	for _, needle := range []string{
		"<string>com.example.app</string>",
		"<string>/tmp/app/bin/app</string>",
		"<string>serve</string>",
		"<key>EnvironmentVariables</key>",
		"<key>FOO</key>",
		"<string>bar</string>",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("plist missing %q", needle)
		}
	}
}

func TestLaunchdBackendStartWritesPlistAndRunsLaunchctl(t *testing.T) {
	tmp := t.TempDir()
	runner := &fakeCommandRunner{
		out: map[string][]byte{},
		err: map[string]error{},
	}
	setLaunchdPrintNotFound(runner, "com.fragments-engine.cerberus.volon.volon-api")
	backend := launchdBackend{
		runner: runner,
		homeDir: func() (string, error) {
			return tmp, nil
		},
		uid: func() int { return 501 },
	}
	res := &domain.Resource{ID: "volon-api", ProjectID: "volon"}
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
		RunFrom:    ProcessRunFromArtifact,
		Dir:        filepath.Join(tmp, "workspace"),
		Command:    []string{"./volon-api", "serve"},
	}

	if err := os.MkdirAll(spec.Dir, 0755); err != nil { //nolint:gosec
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(spec.Dir, "volon-api"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatalf("write source binary: %v", err)
	}

	if err := backend.Start(context.Background(), res, spec); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	plistPath := filepath.Join(tmp, "Library", "LaunchAgents", "com.fragments-engine.cerberus.volon.volon-api.plist")
	data, err := os.ReadFile(plistPath) //nolint:gosec // test path is constructed in temp dir
	if err != nil {
		t.Fatalf("expected plist at %s: %v", plistPath, err)
	}
	if !strings.Contains(string(data), "<string>"+filepath.Join(tmp, ".cerberus", "apps", "volon", "volon-api", "bin", "volon-api")+"</string>") {
		t.Fatalf("plist did not point at installed artifact: %s", string(data))
	}

	if len(runner.calls) != 5 {
		t.Fatalf("launchctl calls = %d, want 5", len(runner.calls))
	}
}

func TestLaunchdBackendStartIncludesEnvFileInPlist(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "contextd"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".env"), []byte("OPENAI_API_KEY=from-dotenv\nCONTEXTD_ROOT=from-dotenv\n"), 0600); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	runner := &fakeCommandRunner{out: map[string][]byte{}, err: map[string]error{}}
	setLaunchdPrintNotFound(runner, "com.fragments-engine.cerberus.vanta-conduit.conduit-api-service")
	backend := launchdBackend{
		runner:  runner,
		homeDir: func() (string, error) { return tmp, nil },
		uid:     func() int { return 501 },
	}
	res := &domain.Resource{ID: "conduit-api-service", ProjectID: "vanta-conduit"}
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
		RunFrom:    ProcessRunFromArtifact,
		Dir:        workspace,
		Command:    []string{"./contextd", "serve", "--addr", ":8089"},
		EnvFile:    ".env",
		Env: map[string]string{
			"CONTEXTD_ROOT": "/Users/chrispian/.conduit",
		},
	}

	if err := backend.Start(context.Background(), res, spec); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	plistPath := filepath.Join(tmp, "Library", "LaunchAgents", "com.fragments-engine.cerberus.vanta-conduit.conduit-api-service.plist")
	data, err := os.ReadFile(plistPath) //nolint:gosec // test path is constructed in temp dir
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	text := string(data)
	for _, needle := range []string{
		"<key>OPENAI_API_KEY</key>",
		"<string>from-dotenv</string>",
		"<key>CONTEXTD_ROOT</key>",
		"<string>/Users/chrispian/.conduit</string>",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("plist missing %q:\n%s", needle, text)
		}
	}
	if strings.Contains(text, "<key>CONTEXTD_ROOT</key>\n        <string>from-dotenv</string>") {
		t.Fatalf("expected explicit env to override env_file value:\n%s", text)
	}
}

func TestLaunchdBackendStartNoopsWhenLoadedAndCurrent(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "app")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	runner := &fakeCommandRunner{out: map[string][]byte{}, err: map[string]error{}}
	setLaunchdPrintNotFound(runner, "com.fragments-engine.cerberus.demo.app")
	backend := launchdBackend{
		runner:  runner,
		homeDir: func() (string, error) { return tmp, nil },
		uid:     func() int { return 501 },
		install: artifactInstaller{homeDir: func() (string, error) { return tmp, nil }, now: time.Now},
	}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
		RunFrom:    ProcessRunFromArtifact,
		Dir:        workspace,
		Command:    []string{"./app", "serve"},
	}

	if err := backend.Start(context.Background(), res, spec); err != nil {
		t.Fatalf("initial Start failed: %v", err)
	}

	runner.calls = nil
	runner.out["launchctl print gui/501/com.fragments-engine.cerberus.demo.app"] = []byte("state = running")
	delete(runner.err, "launchctl print gui/501/com.fragments-engine.cerberus.demo.app")
	applyRes, err := backend.Apply(context.Background(), res, spec)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("launchctl calls = %d, want 1", len(runner.calls))
	}
	if got := runner.calls[0]; got.name != "launchctl" || strings.Join(got.args, " ") != "print gui/501/com.fragments-engine.cerberus.demo.app" {
		t.Fatalf("unexpected call: %#v", got)
	}
	if applyRes.Action != ApplyActionNoop {
		t.Fatalf("action = %q, want %q", applyRes.Action, ApplyActionNoop)
	}
}

func TestLaunchdBackendStartReloadsWhenArtifactChanges(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "app")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	runner := &fakeCommandRunner{out: map[string][]byte{}, err: map[string]error{}}
	setLaunchdPrintNotFound(runner, "com.fragments-engine.cerberus.demo.app")
	backend := launchdBackend{
		runner:  runner,
		homeDir: func() (string, error) { return tmp, nil },
		uid:     func() int { return 501 },
		install: artifactInstaller{homeDir: func() (string, error) { return tmp, nil }, now: time.Now},
	}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
		RunFrom:    ProcessRunFromArtifact,
		Dir:        workspace,
		Command:    []string{"./app", "serve"},
	}

	if err := backend.Start(context.Background(), res, spec); err != nil {
		t.Fatalf("initial Start failed: %v", err)
	}

	if err := os.WriteFile(source, []byte("#!/bin/sh\necho changed\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	runner.calls = nil
	runner.out["launchctl print gui/501/com.fragments-engine.cerberus.demo.app"] = []byte("state = running")
	delete(runner.err, "launchctl print gui/501/com.fragments-engine.cerberus.demo.app")
	applyRes, err := backend.Apply(context.Background(), res, spec)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if len(runner.calls) != 6 {
		t.Fatalf("launchctl calls = %d, want 6", len(runner.calls))
	}
	got := []string{
		runner.calls[0].name + " " + strings.Join(runner.calls[0].args, " "),
		runner.calls[1].name + " " + strings.Join(runner.calls[1].args, " "),
		runner.calls[2].name + " " + strings.Join(runner.calls[2].args, " "),
		runner.calls[3].name + " " + strings.Join(runner.calls[3].args, " "),
	}
	want := []string{
		"launchctl print gui/501/com.fragments-engine.cerberus.demo.app",
		"launchctl bootout gui/501/com.fragments-engine.cerberus.demo.app",
		"launchctl bootstrap gui/501 " + filepath.Join(tmp, "Library", "LaunchAgents", "com.fragments-engine.cerberus.demo.app.plist"),
		"launchctl kickstart -k gui/501/com.fragments-engine.cerberus.demo.app",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if applyRes.Action != ApplyActionReloaded {
		t.Fatalf("action = %q, want %q", applyRes.Action, ApplyActionReloaded)
	}
	if !applyRes.ArtifactChanged {
		t.Fatalf("expected artifactChanged=true")
	}
}

func TestLaunchdBackendApplyIncludesDiagnosticsOnBootstrapFailure(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "app")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	label := "com.fragments-engine.cerberus.demo.app"
	plistPath := filepath.Join(tmp, "Library", "LaunchAgents", label+".plist")
	runner := &fakeCommandRunner{
		out: map[string][]byte{
			"launchctl print gui/501/" + label:         []byte("Could not find service"),
			"launchctl bootstrap gui/501 " + plistPath: []byte("bootstrap failed"),
		},
		err: map[string]error{
			"launchctl print gui/501/" + label:         errors.New("exit status 113"),
			"launchctl bootstrap gui/501 " + plistPath: errors.New("exit status 5"),
		},
	}
	backend := launchdBackend{
		runner:  runner,
		homeDir: func() (string, error) { return tmp, nil },
		uid:     func() int { return 501 },
		install: artifactInstaller{homeDir: func() (string, error) { return tmp, nil }, now: time.Now},
	}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
		RunFrom:    ProcessRunFromArtifact,
		Dir:        workspace,
		Command:    []string{"./app", "serve"},
	}

	_, err := backend.Apply(context.Background(), res, spec)
	if err == nil {
		t.Fatal("expected bootstrap failure")
	}
	text := err.Error()
	for _, needle := range []string{
		"launchd output: bootstrap failed",
		"stderr log:",
		"stdout log:",
		"plist:",
		"install:",
		"artifact:",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("error missing %q: %s", needle, text)
		}
	}
}

func TestLaunchdBackendApplyRetriesBootstrapAfterConflict(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "app")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	label := "com.fragments-engine.cerberus.demo.app"
	plistPath := filepath.Join(tmp, "Library", "LaunchAgents", label+".plist")
	runner := &fakeCommandRunner{
		out: map[string][]byte{
			"launchctl print gui/501/" + label:         []byte("Could not find service"),
			"launchctl bootstrap gui/501 " + plistPath: []byte("Bootstrap failed: 5: Input/output error"),
			"launchctl bootout gui/501/" + label:       []byte("Could not find service"),
			"launchctl bootout gui/501 " + plistPath:   []byte("Could not find service"),
		},
		err: map[string]error{
			"launchctl print gui/501/" + label:         errors.New("exit status 113"),
			"launchctl bootstrap gui/501 " + plistPath: &sequencedCommandError{values: []error{errors.New("exit status 5"), nil}},
			"launchctl bootout gui/501/" + label:       errors.New("exit status 113"),
			"launchctl bootout gui/501 " + plistPath:   errors.New("exit status 113"),
		},
	}
	backend := launchdBackend{
		runner:  runner,
		homeDir: func() (string, error) { return tmp, nil },
		uid:     func() int { return 501 },
		install: artifactInstaller{homeDir: func() (string, error) { return tmp, nil }, now: time.Now},
	}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
		RunFrom:    ProcessRunFromArtifact,
		Dir:        workspace,
		Command:    []string{"./app", "serve"},
	}

	if _, err := backend.Apply(context.Background(), res, spec); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	bootstraps := 0
	for _, call := range runner.calls {
		if call.args[0] == "bootout" {
			t.Fatal("attempted recovery bootout although the job was absent")
		}
		if call.args[0] == "bootstrap" {
			bootstraps++
		}
	}
	if bootstraps != 2 {
		t.Fatalf("expected one bounded retry, got %d bootstraps", bootstraps)
	}
}

func TestLaunchdBackendStatusMapsNotFoundToStopped(t *testing.T) {
	runner := &fakeCommandRunner{
		out: map[string][]byte{
			"launchctl print gui/501/com.example.app": []byte("Could not find service"),
		},
		err: map[string]error{
			"launchctl print gui/501/com.example.app": errors.New("exit status 113"),
		},
	}
	backend := launchdBackend{
		runner: runner,
		homeDir: func() (string, error) {
			return "/tmp", nil
		},
		uid: func() int { return 501 },
	}
	state, err := backend.Status(context.Background(), &domain.Resource{ID: "app"}, ProcessSpec{
		ServiceName: "com.example.app",
	})
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if state != domain.StateStopped {
		t.Fatalf("state = %q, want %q", state, domain.StateStopped)
	}
}

func TestLaunchdBackendStopIgnoresMissingService(t *testing.T) {
	label := "com.example.app"
	target := fmt.Sprintf("gui/%d/%s", 501, label)
	runner := &fakeCommandRunner{
		out: map[string][]byte{
			"launchctl bootout " + target: []byte("Could not find service"),
		},
		err: map[string]error{
			"launchctl bootout " + target: errors.New("exit status 113"),
		},
	}
	backend := launchdBackend{
		runner: runner,
		homeDir: func() (string, error) {
			return "/tmp", nil
		},
		uid: func() int { return 501 },
	}
	if err := backend.Stop(context.Background(), &domain.Resource{ID: "app"}, ProcessSpec{
		ServiceName: label,
	}); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestLaunchdBackendStopPreservesInstallRootAndPlist(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "app"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{out: map[string][]byte{}, err: map[string]error{}}
	setLaunchdPrintNotFound(runner, "com.fragments-engine.cerberus.demo.app")
	backend := launchdBackend{
		runner:  runner,
		homeDir: func() (string, error) { return tmp, nil },
		uid:     func() int { return 501 },
		install: artifactInstaller{
			homeDir: func() (string, error) { return tmp, nil },
			now:     time.Now,
		},
	}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
		RunFrom:    ProcessRunFromArtifact,
		Dir:        workspace,
		Command:    []string{"./app", "serve"},
	}
	if err := backend.Start(context.Background(), res, spec); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	layout, err := defaultInstallLayoutFromBackend(backend, res, spec)
	if err != nil {
		t.Fatalf("defaultInstallLayoutFromBackend failed: %v", err)
	}
	if err := backend.Stop(context.Background(), res, spec); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if _, err := os.Stat(layout.PlistPath); err != nil {
		t.Fatalf("expected plist preserved, got err=%v", err)
	}
	if _, err := os.Stat(layout.RootDir); err != nil {
		t.Fatalf("expected install root preserved, got err=%v", err)
	}
}

func TestParseLaunchdState(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want domain.State
	}{
		{name: "running", in: "state = running", want: domain.StateRunning},
		{name: "spawn scheduled", in: "state = spawn scheduled", want: domain.StateStarting},
		{name: "spawn scheduled clean exit", in: "state = spawn scheduled\nlast exit code = 0", want: domain.StateStarting},
		{name: "spawn scheduled crash loop", in: "state = spawn scheduled\nlast exit code = 1", want: domain.StateFailed},
		{name: "spawning crash loop", in: "state = spawning\nlast exit code = 1", want: domain.StateFailed},
		{name: "throttled", in: "state = throttled", want: domain.StateFailed},
		{name: "waiting clean", in: "state = waiting\nlast exit code = 0", want: domain.StateStopped},
		{name: "waiting failed", in: "state = waiting\nlast exit code = 78", want: domain.StateFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLaunchdState(tc.in); got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiagnoseLaunchdRecordCrashLoop(t *testing.T) {
	text := "state = spawn scheduled\npid = 0\nlast exit code = 1\nruns = 496\n"
	diagnosis, highlights := diagnoseLaunchdRecord(text)
	if !strings.Contains(diagnosis, "crash-looping") {
		t.Fatalf("diagnosis = %q, want crash-loop message", diagnosis)
	}
	var sawRuns bool
	for _, h := range highlights {
		if strings.HasPrefix(h, "runs =") {
			sawRuns = true
		}
	}
	if !sawRuns {
		t.Fatalf("highlights = %v, want a runs entry", highlights)
	}
}

func TestLaunchdBackendInspectParsesLiveRecord(t *testing.T) {
	runner := &fakeCommandRunner{
		out: map[string][]byte{
			"launchctl print gui/501/com.example.app": []byte("state = throttled\npid = 123\nlast exit code = 78\nreason = crashed\nenvironment = {\n\tOPENAI_API_KEY => sk-test\n\tPATH => /usr/bin\n}\n"),
		},
		err: map[string]error{},
	}
	backend := launchdBackend{
		runner:  runner,
		homeDir: func() (string, error) { return "/tmp", nil },
		uid:     func() int { return 501 },
	}
	rec, err := backend.Inspect(context.Background(), &domain.Resource{ID: "app"}, ProcessSpec{
		ServiceName: "com.example.app",
	})
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if !rec.Loaded || rec.State != "throttled" || rec.PID != 123 || rec.LastExitCode == nil || *rec.LastExitCode != 78 || !rec.Throttled || rec.Reason != "crashed" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if rec.Diagnosis == "" || len(rec.Highlights) == 0 {
		t.Fatalf("expected diagnosis/highlights, got %+v", rec)
	}
	if strings.Contains(rec.Raw, "sk-test") {
		t.Fatalf("raw launchd record leaked secret: %s", rec.Raw)
	}
	if !strings.Contains(rec.Raw, "OPENAI_API_KEY => [REDACTED]") {
		t.Fatalf("raw launchd record did not redact secret: %s", rec.Raw)
	}
	if !strings.Contains(rec.Raw, "PATH => /usr/bin") {
		t.Fatalf("raw launchd record should preserve non-sensitive values: %s", rec.Raw)
	}
}

func TestLaunchdBackendReloadKickstartsLoadedService(t *testing.T) {
	runner := &fakeCommandRunner{
		out: map[string][]byte{
			"launchctl print gui/501/com.example.app":        []byte("state = running"),
			"launchctl kickstart -k gui/501/com.example.app": []byte(""),
		},
		err: map[string]error{},
	}
	backend := launchdBackend{
		runner:  runner,
		homeDir: func() (string, error) { return "/tmp", nil },
		uid:     func() int { return 501 },
	}
	if err := backend.Reload(context.Background(), &domain.Resource{ID: "app"}, ProcessSpec{
		ServiceName: "com.example.app",
	}); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("launchctl calls = %d, want 4", len(runner.calls))
	}
}

func TestLaunchdBackendRemoveRemovesInstallRootAndPlist(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "app"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{out: map[string][]byte{}, err: map[string]error{}}
	setLaunchdPrintNotFound(runner, "com.fragments-engine.cerberus.demo.app")
	backend := launchdBackend{
		runner:  runner,
		homeDir: func() (string, error) { return tmp, nil },
		uid:     func() int { return 501 },
		install: artifactInstaller{
			homeDir: func() (string, error) { return tmp, nil },
			now:     time.Now,
		},
	}
	res := &domain.Resource{ID: "app", ProjectID: "demo"}
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
		RunFrom:    ProcessRunFromArtifact,
		Dir:        workspace,
		Command:    []string{"./app", "serve"},
	}
	if err := backend.Start(context.Background(), res, spec); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	layout, err := defaultInstallLayoutFromBackend(backend, res, spec)
	if err != nil {
		t.Fatalf("defaultInstallLayoutFromBackend failed: %v", err)
	}
	if err := backend.Remove(context.Background(), res, spec); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if _, err := os.Stat(layout.PlistPath); !os.IsNotExist(err) {
		t.Fatalf("expected plist removed, got err=%v", err)
	}
	if _, err := os.Stat(layout.RootDir); !os.IsNotExist(err) {
		t.Fatalf("expected install root removed, got err=%v", err)
	}
}

// TestLaunchdBackendFrontsSecretRefsWithRunSecrets is the regression guard for
// CW-20260518-0087: a service whose environment names a secret reference must
// produce a plist that carries the reference and routes through the
// run-secrets shim, never one that carries the credential itself.
func TestLaunchdBackendFrontsSecretRefsWithRunSecrets(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "tesseract"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	runner := &fakeCommandRunner{out: map[string][]byte{}, err: map[string]error{}}
	setLaunchdPrintNotFound(runner, "com.fragments-engine.cerberus.tesseract.tesseract-api-service")
	backend := launchdBackend{
		runner:   runner,
		homeDir:  func() (string, error) { return tmp, nil },
		uid:      func() int { return 501 },
		selfPath: func() (string, error) { return "/usr/local/bin/cerberus", nil },
	}
	res := &domain.Resource{ID: "tesseract-api-service", ProjectID: "tesseract"}
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
		RunFrom:    ProcessRunFromArtifact,
		Dir:        workspace,
		Command:    []string{"./tesseract", "serve"},
		Env:        map[string]string{"OPENAI_API_KEY": "keychain://openai/work"},
	}

	if err := backend.Start(context.Background(), res, spec); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	plistPath := filepath.Join(tmp, "Library", "LaunchAgents", "com.fragments-engine.cerberus.tesseract.tesseract-api-service.plist")
	raw, err := os.ReadFile(plistPath) //nolint:gosec // test path is constructed in temp dir
	if err != nil {
		t.Fatalf("expected plist at %s: %v", plistPath, err)
	}
	plist := string(raw)

	if !strings.Contains(plist, "<string>keychain://openai/work</string>") {
		t.Errorf("plist does not carry the unresolved reference:\n%s", plist)
	}
	for _, want := range []string{
		"<string>/usr/local/bin/cerberus</string>",
		"<string>run-secrets</string>",
		"<string>--</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist is not fronted by the run-secrets shim (missing %s):\n%s", want, plist)
		}
	}
	// The shim must precede the real program, or launchd starts the service
	// without ever resolving anything.
	shimIdx := strings.Index(plist, "run-secrets")
	binIdx := strings.Index(plist, filepath.Join(tmp, ".cerberus", "apps", "tesseract"))
	if shimIdx < 0 || binIdx < 0 || shimIdx > binIdx {
		t.Errorf("run-secrets does not precede the service binary:\n%s", plist)
	}
}

// TestLaunchdBackendLeavesLiteralEnvUnfronted keeps the shim off the path of
// every service that has no secret references, so this change is inert for
// existing resources.
func TestLaunchdBackendLeavesLiteralEnvUnfronted(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "plain"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	runner := &fakeCommandRunner{out: map[string][]byte{}, err: map[string]error{}}
	setLaunchdPrintNotFound(runner, "com.fragments-engine.cerberus.plain.plain-api")
	backend := launchdBackend{
		runner:  runner,
		homeDir: func() (string, error) { return tmp, nil },
		uid:     func() int { return 501 },
		selfPath: func() (string, error) {
			t.Error("cerberusPath consulted for a literal-only environment")
			return "", nil
		},
	}
	res := &domain.Resource{ID: "plain-api", ProjectID: "plain"}
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
		RunFrom:    ProcessRunFromArtifact,
		Dir:        workspace,
		Command:    []string{"./plain", "serve"},
		Env:        map[string]string{"LOG_LEVEL": "debug"},
	}

	if err := backend.Start(context.Background(), res, spec); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	plistPath := filepath.Join(tmp, "Library", "LaunchAgents", "com.fragments-engine.cerberus.plain.plain-api.plist")
	raw, err := os.ReadFile(plistPath) //nolint:gosec // test path is constructed in temp dir
	if err != nil {
		t.Fatalf("expected plist: %v", err)
	}
	if strings.Contains(string(raw), "run-secrets") {
		t.Errorf("literal-only service was fronted by the shim:\n%s", string(raw))
	}
}

func TestApplyFailsWhenLaunchdAcceptsButCannotExecute(t *testing.T) {
	dir := t.TempDir()
	label := "com.example.failed"
	runner := &fakeCommandRunner{
		out: map[string][]byte{"launchctl print gui/501/" + label: []byte("state = spawn scheduled\nlast exit reason = OS_REASON_CODESIGNING")},
		err: map[string]error{}, keepState: true,
	}
	backend := launchdBackend{runner: runner, homeDir: func() (string, error) { return dir, nil }, uid: func() int { return 501 }, startTimeout: 20 * time.Millisecond}
	_, err := backend.Apply(context.Background(), &domain.Resource{ID: "app", ProjectID: "test"}, ProcessSpec{RunFrom: ProcessRunFromWorkspace, Command: []string{"/bin/false"}, ServiceName: label})
	if err == nil || !strings.Contains(err.Error(), "did not reach running") || !strings.Contains(err.Error(), "OS_REASON_CODESIGNING") {
		t.Fatalf("false deployment success or missing diagnosis: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "launchd may still retry") || !strings.Contains(err.Error(), "cerberus resource status app") {
		t.Fatalf("startup timeout lost its cause or recovery guidance: %v", err)
	}
}

func TestWaitRunningHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backend := launchdBackend{}
	err := backend.waitRunning(ctx, &domain.Resource{}, ProcessSpec{}, InstallLayout{ServiceName: "test"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
}

type interruptedInspectionRunner struct {
	observed bool
	cancel   context.CancelFunc
}

func (r *interruptedInspectionRunner) CombinedOutput(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	if !r.observed {
		r.observed = true
		return []byte("state = spawn scheduled\nlast exit reason = OS_REASON_CODESIGNING"), nil
	}
	r.cancel()
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestWaitRunningPreservesLastObservationWhenInspectionIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	backend := launchdBackend{
		runner:  &interruptedInspectionRunner{cancel: cancel},
		homeDir: func() (string, error) { return dir, nil },
		uid:     func() int { return 501 },
	}
	err := backend.waitRunning(ctx, &domain.Resource{ID: "test-app"}, ProcessSpec{ServiceName: "com.example.test"}, InstallLayout{ServiceName: "com.example.test"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation cause: %v", err)
	}
	for _, want := range []string{"spawn scheduled", "OS_REASON_CODESIGNING", "launchd may still retry", "cerberus resource status test-app"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("interrupted startup verification omitted %q: %v", want, err)
		}
	}
}

func TestBootstrapRecoveryHonorsCancellationAndBootoutFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "bootout-fails"} {
		t.Run(mode, func(t *testing.T) {
			runner := &fakeCommandRunner{out: map[string][]byte{"launchctl bootstrap gui/501 app.plist": []byte("Bootstrap failed: 5: Input/output error")}, err: map[string]error{"launchctl bootstrap gui/501 app.plist": errors.New("exit status 5")}}
			ctx := context.Background()
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			} else {
				runner.err["launchctl bootout gui/501/app"] = errors.New("permission denied")
			}
			backend := launchdBackend{runner: runner}
			_, err := backend.bootstrapService(ctx, "gui/501", "gui/501/app", "app.plist")
			if mode == "cancel" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if mode == "bootout-fails" && (err == nil || !strings.Contains(err.Error(), "recovery bootout failed")) {
				t.Fatalf("lost failed step: %v", err)
			}
			for _, call := range runner.calls[1:] {
				if call.args[0] == "bootstrap" {
					t.Fatal("retried bootstrap after failed/canceled recovery")
				}
			}
		})
	}
}

func TestApplyActivatesArtifactThatWasPreviouslySynced(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	if err := os.WriteFile(src, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{out: map[string][]byte{}, err: map[string]error{}}
	backend := launchdBackend{runner: runner, homeDir: func() (string, error) { return dir, nil }, uid: func() int { return 501 }}
	res := &domain.Resource{ID: "app", ProjectID: "test"}
	spec := ProcessSpec{RunFrom: ProcessRunFromArtifact, Command: []string{src}}
	if _, err := backend.Apply(context.Background(), res, spec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	installer := backend.artifactInstaller()
	if _, _, err := installer.Sync(res, spec); err != nil {
		t.Fatal(err)
	}
	_, status, err := installer.Status(res, spec)
	if err != nil || !status.ActivationPending {
		t.Fatalf("sync claimed runtime current: %+v %v", status, err)
	}
	result, err := backend.Apply(context.Background(), res, spec)
	if err != nil || result.Action != ApplyActionReloaded {
		t.Fatalf("pre-synced binary not activated: %+v %v", result, err)
	}
	_, status, err = installer.Status(res, spec)
	if err != nil || status.ActivationPending {
		t.Fatalf("activation not recorded: %+v %v", status, err)
	}
}

type settlingLaunchdRunner struct {
	base       *fakeCommandRunner
	bootoutAt  time.Time
	bootstraps int
}

func (r *settlingLaunchdRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "bootout" {
		r.bootoutAt = time.Now()
	}
	if len(args) > 0 && args[0] == "bootstrap" {
		r.bootstraps++
		if !r.bootoutAt.IsZero() && time.Since(r.bootoutAt) < 200*time.Millisecond {
			return []byte("Bootstrap failed: 5: Input/output error"), errors.New("slot still draining")
		}
	}
	return r.base.CombinedOutput(ctx, name, args...)
}
func TestApplyWaitsForRemovedLaunchdSlotBeforeBootstrap(t *testing.T) {
	dir := t.TempDir()
	label := "com.test.slot"
	runner := &settlingLaunchdRunner{base: &fakeCommandRunner{out: map[string][]byte{"launchctl print gui/501/" + label: []byte("state = running\npid = 123")}, err: map[string]error{}}}
	backend := launchdBackend{runner: runner, homeDir: func() (string, error) { return dir, nil }, uid: func() int { return 501 }}
	_, err := backend.Apply(context.Background(), &domain.Resource{ID: "slot", ProjectID: "test"}, ProcessSpec{Mode: ProcessModeOSService, Supervisor: ProcessSupervisorLaunchd, RunFrom: ProcessRunFromWorkspace, ServiceName: label, Dir: dir, Command: []string{"/bin/sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	if runner.bootstraps != 1 {
		t.Fatalf("bootstrap raced the draining slot: %d attempts", runner.bootstraps)
	}
}

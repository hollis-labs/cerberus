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

	"github.com/chrispian/cerberus/internal/domain"
)

type fakeCommandRunner struct {
	calls []fakeCall
	out   map[string][]byte
	err   map[string]error
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
	return f.out[key], f.err[key]
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

	if len(runner.calls) != 3 {
		t.Fatalf("launchctl calls = %d, want 3", len(runner.calls))
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

	if len(runner.calls) != 4 {
		t.Fatalf("launchctl calls = %d, want 4", len(runner.calls))
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

func TestParseLaunchdState(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want domain.State
	}{
		{name: "running", in: "state = running", want: domain.StateRunning},
		{name: "spawn scheduled", in: "state = spawn scheduled", want: domain.StateStarting},
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

func TestLaunchdBackendInspectParsesLiveRecord(t *testing.T) {
	runner := &fakeCommandRunner{
		out: map[string][]byte{
			"launchctl print gui/501/com.example.app": []byte("state = throttled\npid = 123\nlast exit code = 78\nreason = crashed\n"),
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
	if len(runner.calls) != 2 {
		t.Fatalf("launchctl calls = %d, want 2", len(runner.calls))
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

package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

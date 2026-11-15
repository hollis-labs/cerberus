package supervise

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/service"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// fakeManager answers `systemctl --user show` from a table of units, and records every
// command, so a test can prove only show ran.
type fakeManager struct {
	mu    sync.Mutex
	units map[string]string // unit -> ActiveState/SubState as "active/running"
	calls []string
}

func (f *fakeManager) CombinedOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	if len(args) < 3 || args[0] != "--user" || args[1] != "show" {
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	state, ok := f.units[args[2]]
	if !ok {
		return []byte("Id=" + args[2] + "\nLoadState=not-found\nActiveState=inactive\nSubState=dead\n"), nil
	}
	active, sub, _ := strings.Cut(state, "/")
	return []byte(fmt.Sprintf("Id=%s\nLoadState=loaded\nActiveState=%s\nSubState=%s\nMainPID=4242\n", args[2], active, sub)), nil
}

type world struct {
	home, state string
	resources   []config.ResourceDef
	manager     *fakeManager
	svc         *Service
}

func process(id, project string, cfg map[string]any) config.ResourceDef {
	return config.ResourceDef{ID: id, Type: "process", Project: project, Connector: "local", Config: cfg}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	base := t.TempDir()
	w := &world{home: filepath.Join(base, "home"), manager: &fakeManager{units: map[string]string{}}}
	w.state = filepath.Join(w.home, ".cerberus")
	t.Setenv("HOME", w.home)
	for _, d := range []string{filepath.Join(w.state, "pids"), filepath.Join(w.home, ".config", "systemd", "user")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	w.resources = []config.ResourceDef{
		process("api", "demo", map[string]any{"mode": "os_service", "supervisor": "systemd_user", "command": []any{"/bin/true"}}),
		process("notes", "notes", map[string]any{"mode": "os_service", "supervisor": "systemd_user", "service_name": "notes", "command": []any{"/bin/true"}}),
		process("web-dev", "web", map[string]any{"mode": "dev_session", "command": []any{"/bin/sleep", "60"}}),
		process("stale-dev", "web", map[string]any{"command": []any{"/bin/sleep", "60"}}),
		{ID: "box-ssh", Type: "server", Connector: "ssh"},
		process("cerberus-substrate", "cerberus", map[string]any{"mode": "os_service", "supervisor": "systemd_user", "command": []any{"/u/op/.local/bin/cerberus-substrate", "serve"}}),
	}
	w.manager.units["com.hollis-labs.cerberus.demo.api.service"] = "active/running"
	w.manager.units["notes.service"] = "inactive/dead"
	w.manager.units["com.hollis-labs.cerberus.cerberus.cerberus-substrate.service"] = "active/running"
	svc, err := New(Options{Mode: settings.ModeObserve, Resources: func() ([]config.ResourceDef, error) { return w.resources, nil },
		Home: w.home, StateDir: w.state, Runner: w.manager, SelfID: "cerberus-substrate"})
	if err != nil {
		t.Fatal(err)
	}
	w.svc = svc
	return w
}

// startSession runs a real process and records it as the old build does: a pid file and
// launch metadata with the process's start identity.
func (w *world) startSession(t *testing.T, id string) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := cmd.Process.Pid
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output() //nolint:gosec // numeric pid
	if err != nil {
		t.Fatal(err)
	}
	meta := service.PIDMeta{PID: pid, ProcessStart: strings.Join(strings.Fields(string(out)), " "), StartedAt: time.Now()}
	raw, _ := json.Marshal(meta)
	w.write(t, filepath.Join("pids", id+".pid"), strconv.Itoa(pid))
	w.write(t, filepath.Join("pids", id+".meta"), string(raw))
	return pid
}

func (w *world) write(t *testing.T, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(w.state, rel), []byte(content), 0o644); err != nil { //nolint:gosec // the test's own tree
		t.Fatal(err)
	}
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func linuxSystemd(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("systemd_user supervision is Linux only")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("systemctl is not installed")
	}
}

// Observe mode refuses every mutation, acknowledged or not, naming observe mode and the old CLI.
func TestObserveRefusesEveryMutation(t *testing.T) {
	w := newWorld(t)
	for _, verb := range Verbs {
		t.Run(verb, func(t *testing.T) {
			out, err := w.svc.Mutate(context.Background(), Request{Verb: verb, ID: "api", Acknowledged: true})
			if out != nil || !IsObserveRefusal(err) {
				t.Fatalf("out = %+v, err = %v, want the observe refusal", out, err)
			}
			var r *Refusal
			if !errors.As(err, &r) || r.Status != 409 {
				t.Fatalf("refusal %+v, want 409 observe_mode", err)
			}
			for _, want := range []string{"observe mode", verb + ` of "api"`, "old CLI"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q lacks %q", err, want)
				}
			}
			if _, err = w.svc.Mutate(context.Background(), Request{Verb: verb, ID: "api"}); !IsObserveRefusal(err) {
				t.Fatalf("without --ack: %v, want the observe refusal first", err)
			}
		})
	}
	_, err := w.svc.Mutate(context.Background(), Request{Verb: "explode", ID: "api", Acknowledged: true})
	if err == nil || IsObserveRefusal(err) {
		t.Fatalf("an unknown verb: %v", err)
	}
	if strings.Join(Verbs, ",") != "apply,deploy,stop,reload,sync,remove" {
		t.Fatalf("Verbs = %v", Verbs)
	}
	if len(w.manager.calls) != 0 {
		t.Fatalf("a refusal ran %v", w.manager.calls)
	}
}

// Supervise mode needs its config, store, digest key and the operation gate; an unknown mode is
// refused. Without the gate nothing is built: no wiring can run a mutation ungated.
func TestSuperviseModeNeedsItsDependencies(t *testing.T) {
	_, err := New(Options{Mode: settings.ModeSupervise, Resources: func() ([]config.ResourceDef, error) { return nil, nil }})
	if err == nil || !strings.Contains(err.Error(), "config source") {
		t.Fatalf("err = %v", err)
	}
	_, err = New(Options{Mode: settings.ModeSupervise, Config: func() (*config.ConfigV2, error) { return &config.ConfigV2{}, nil },
		Store: &store.Store{}, Digester: audit.Digester{}})
	if err == nil || !strings.Contains(err.Error(), "operation gate") {
		t.Fatalf("supervise mode without a gate: %v", err)
	}
	if _, err := New(Options{Mode: "chaos", Resources: func() ([]config.ResourceDef, error) { return nil, nil }}); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}

// A dev session's state comes from the old build's pid and launch files: running when the
// recorded process lives with the recorded identity, stopped with no file or a dead pid,
// unknown when the metadata does not match. Nothing is adopted or tidied.
func TestObserveReadsDevSessions(t *testing.T) {
	w := newWorld(t)
	w.startSession(t, "web-dev")
	w.write(t, filepath.Join("pids", "stale-dev.pid"), strconv.Itoa(deadPID(t)))

	st, err := w.svc.Status(context.Background(), "web-dev")
	if err != nil || st.State != domain.StateRunning || st.Mode != "dev_session" || !st.Observed {
		t.Fatalf("live session: %+v %v", st, err)
	}
	st, _ = w.svc.Status(context.Background(), "stale-dev")
	if st.State != domain.StateStopped || !strings.Contains(st.Detail, "left in place") {
		t.Fatalf("stale session: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(w.state, "pids", "stale-dev.pid")); err != nil {
		t.Fatalf("observe removed the stale pid file: %v", err)
	}
	// Metadata for another process: not this session's.
	pid := w.startSession(t, "web-dev")
	w.write(t, filepath.Join("pids", "web-dev.meta"), fmt.Sprintf(`{"pid": %d, "process_start": "Mon Jan  1 00:00:00 2001"}`, pid))
	if st, _ := w.svc.Status(context.Background(), "web-dev"); st.State != domain.StateUnknown {
		t.Fatalf("mismatched identity: %+v", st)
	}
	_ = os.Remove(filepath.Join(w.state, "pids", "web-dev.pid"))
	if st, _ := w.svc.Status(context.Background(), "web-dev"); st.State != domain.StateStopped || st.Detail != "no pid file" {
		t.Fatalf("no pid file: %+v", st)
	}
}

// A systemd-supervised resource's state comes from the user manager, by its unit name
// (prefixed, or its service_name), through show only.
func TestObserveReadsSystemdUnits(t *testing.T) {
	linuxSystemd(t)
	w := newWorld(t)
	list, err := w.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Status{}
	for _, s := range list {
		got[s.ID] = s
	}
	if s := got["api"]; s.State != domain.StateRunning || s.Unit != "com.hollis-labs.cerberus.demo.api.service" {
		t.Fatalf("api: %+v", s)
	}
	if s := got["notes"]; s.State != domain.StateStopped || s.Unit != "notes.service" {
		t.Fatalf("notes: %+v", s)
	}
	if s := got["box-ssh"]; s.Observed || s.State != domain.StateUnknown {
		t.Fatalf("an ssh handle: %+v", s)
	}
	if s := got["cerberus-substrate"]; !s.Self {
		t.Fatalf("self not marked: %+v", s)
	}
	for _, c := range w.manager.calls {
		if !strings.HasPrefix(c, "systemctl --user show ") {
			t.Fatalf("observe ran %q", c)
		}
	}
	if _, err := w.svc.Status(context.Background(), "nope"); err == nil {
		t.Fatal("an unknown id was found")
	}
}

// fingerprint is every file and directory under root, with content hash, mode and mtime.
func fingerprint(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		sum := ""
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p) //nolint:gosec // the test's own tree
			if err != nil {
				return err
			}
			h := sha256.Sum256(b)
			sum = hex.EncodeToString(h[:8])
		}
		out[rel] = fmt.Sprintf("%v %d %s %d", info.Mode(), info.Size(), sum, info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Observing every resource writes nothing: the home (the old state tree, the unit
// directory) is identical afterwards, a stale pid file included.
func TestObserveWritesNothing(t *testing.T) {
	w := newWorld(t)
	w.startSession(t, "web-dev")
	w.write(t, filepath.Join("pids", "stale-dev.pid"), strconv.Itoa(deadPID(t)))
	if err := os.WriteFile(filepath.Join(w.home, ".config", "systemd", "user", "notes.service"), []byte("[Unit]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := fingerprint(t, w.home)
	for range 3 {
		if _, err := w.svc.List(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, verb := range Verbs {
		_, _ = w.svc.Mutate(context.Background(), Request{Verb: verb, ID: "web-dev", Acknowledged: true})
	}
	after := fingerprint(t, w.home)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("changed: %s", k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("added: %s", k)
		}
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

const cliHostsYAML = `version: 1
hosts:
  - {name: box-a, role: worker, provider: homelab, labels: {agents: "true"}, declared: {os: linux, cpu: 8}}
  - {name: box-b, role: worker, labels: {agents: "false"}}
  - {name: box-c, role: service}
`

// hostHome is a HOME with no daemon, a hosts.yaml and recorded facts: box-a
// measured a minute ago, box-b never, box-c two hours ago.
func hostHome(t *testing.T) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "cerbhost-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".cerberus", "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".cerberus", "hosts.yaml"), []byte(cliHostsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath, _ := hosts.DefaultStatePath()
	store := hosts.NewStore(statePath)
	const gib = int64(1 << 30)
	put := func(name string, age time.Duration, p *hosts.Probed) {
		at := time.Now().UTC().Add(-age)
		if err := store.Put(name, hosts.State{State: hosts.StateOnline, CheckedAt: &at, ProbedAt: &at, Probed: p}); err != nil {
			t.Fatal(err)
		}
	}
	put("box-a", time.Minute, &hosts.Probed{Source: "posix", CPU: 8, MemoryAvailable: 8 * gib, MemoryTotal: 16 * gib, DiskTotal: 200 * gib, DiskFree: 100 * gib, Tools: map[string]bool{"docker": true}})
	put("box-c", 2*time.Hour, &hosts.Probed{Source: "posix", CPU: 4, MemoryAvailable: 4 * gib, DiskTotal: 100 * gib, DiskFree: 50 * gib})
}

func resetHostFlags() {
	hostJSONFlag, hostOutput = false, outputFormatText
	hostFilterOpts.role, hostFilterOpts.provider, hostFilterOpts.state, hostFilterOpts.labels = "", "", "", nil
	hostCapacityOpts = capacityOpts{}
}

func runHost(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	resetHostFlags()
	rootCmd.SetArgs(append([]string{"host"}, args...))
	rootCmd.SetOut(&out)
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); resetHostFlags() })
	err := rootCmd.Execute()
	return out.String(), err
}

func TestHostListInProcessWithFilters(t *testing.T) {
	hostHome(t)
	out, err := runHost(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "box-a", "box-b", "box-c", "never", "(stale)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output lacks %q:\n%s", want, out)
		}
	}
	out, err = runHost(t, "list", "--role", "worker", "--label", "agents=true", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var views []cerbapi.HostView
	if err := json.Unmarshal([]byte(out), &views); err != nil || len(views) != 1 || views[0].Host.Name != "box-a" {
		t.Fatalf("filtered json = %s (%v)", out, err)
	}
	// -o json is the repo's convention and means the same.
	out, _ = runHost(t, "list", "-o", "json", "--provider", "homelab")
	if err := json.Unmarshal([]byte(out), &views); err != nil || len(views) != 1 {
		t.Fatalf("-o json = %s (%v)", out, err)
	}
	if _, err := runHost(t, "list", "--label", "nokey"); err == nil || !strings.Contains(err.Error(), "key=value") {
		t.Fatalf("bad label = %v", err)
	}
}

func TestHostGetSaysWhenFactsAreMissingOrStale(t *testing.T) {
	hostHome(t)
	out, err := runHost(t, "get", "box-b")
	if err != nil || !strings.Contains(out, "No probe data: the host's facts are unknown. Run `cerberus host probe box-b`.") {
		t.Fatalf("get box-b = %q, %v", out, err)
	}
	out, _ = runHost(t, "get", "box-c")
	if !strings.Contains(out, "These facts are stale.") {
		t.Fatalf("get box-c = %q", out)
	}
	out, _ = runHost(t, "get", "box-a")
	if strings.Contains(out, "stale") || !strings.Contains(out, "docker=true") {
		t.Fatalf("get box-a = %q", out)
	}
	if _, err := runHost(t, "get", "nope"); err == nil || !strings.Contains(err.Error(), "is not declared") {
		t.Fatalf("get nope = %v", err)
	}
}

func TestHostCapacityCommand(t *testing.T) {
	hostHome(t)
	out, err := runHost(t, "capacity", "--min-memory", "4GiB", "--min-cpu", "2", "--explain")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 matched, 2 unknown, of 3 declared hosts", "box-a", "unknown  box-b: the host has no probe data",
		"unknown  box-c: the probe data is", "cerberus host probe box-b", "not ranked, nothing reserved"} {
		if !strings.Contains(out, want) {
			t.Errorf("capacity output lacks %q:\n%s", want, out)
		}
	}
	out, err = runHost(t, "capacity", "--role", "service", "--explain", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res cerbapi.CapacityResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Matched) != 1 || res.Matched[0].Host.Name != "box-c" || len(res.Excluded) != 2 {
		t.Fatalf("json = %s (%v)", out, err)
	}
	for _, flag := range [][]string{{"--min-memory", "lots"}, {"--min-disk", "-3"}, {"--tool", "kubectl"}} {
		if _, err := runHost(t, append([]string{"capacity"}, flag...)...); err == nil {
			t.Errorf("capacity %v accepted", flag)
		}
	}
}

// A daemon that answers is used, and an error it returns after the request
// was delivered is the command's error: nothing runs a second time in
// process, which for a probe would be a second ssh session.
type probingDaemon struct {
	cerbapi.Client
	probes int
}

func (p *probingDaemon) ListHosts(context.Context, cerbapi.HostFilter) ([]cerbapi.HostView, error) {
	return nil, nil
}
func (p *probingDaemon) GetHost(context.Context, string) (cerbapi.HostView, error) {
	return cerbapi.HostView{}, nil
}
func (p *probingDaemon) ProbeHost(context.Context, string) (cerbapi.HostView, error) {
	p.probes++
	return cerbapi.HostView{}, errors.New("daemon deliberately refused the probe")
}
func (p *probingDaemon) HostCapacity(context.Context, cerbapi.CapacityQuery) (cerbapi.CapacityResult, error) {
	return cerbapi.CapacityResult{}, nil
}

func TestHostProbeUsesTheDaemonAndNeverRetriesInProcess(t *testing.T) {
	daemon := &probingDaemon{}
	startCommandSocket(t, daemon)
	// A hosts.yaml an in-process retry would find and fail on differently.
	hostsPath, _ := hosts.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(hostsPath), 0o700); err != nil { //nolint:gosec // a path under the test's own temp HOME
		t.Fatal(err)
	}
	if err := os.WriteFile(hostsPath, []byte(cliHostsYAML), 0o600); err != nil { //nolint:gosec // a path under the test's own temp HOME
		t.Fatal(err)
	}
	_, err := runHost(t, "probe", "box-a")
	if err == nil || !strings.Contains(err.Error(), "daemon deliberately refused the probe") {
		t.Fatalf("probe error = %v", err)
	}
	if daemon.probes != 1 {
		t.Fatalf("the daemon saw %d probes", daemon.probes)
	}
	// With no daemon the same command runs in process and reports its own refusal.
	hostHome(t)
	if _, err := runHost(t, "probe", "box-a"); err == nil || !strings.Contains(err.Error(), "no usable reach transport") {
		t.Fatalf("in-process probe = %v", err)
	}
}

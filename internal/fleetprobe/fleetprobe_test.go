package fleetprobe

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/gitenv"
)

func TestFromFileReadsGoBuildInfo(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	id := newIdentifier("")
	a := id.fromFile(context.Background(), self)
	if a.Kind != ArtifactGo || a.Go == nil {
		t.Fatalf("test binary not identified as Go: %+v", a)
	}
	if a.Go.GoVersion == "" || a.Go.Path == "" {
		t.Fatalf("build info incomplete: %+v", a.Go)
	}
	if len(a.SHA256) != 64 || a.Size == 0 {
		t.Fatalf("digest/size missing: %+v", a)
	}
}

func TestFromProcessFlagsReplacedExecutable(t *testing.T) {
	if _, err := os.Stat("/proc/self/exe"); err != nil {
		t.Skip("needs /proc")
	}
	dir := t.TempDir()
	src, _ := os.Executable()
	exe := filepath.Join(dir, "sleeper")
	copyFile(t, src, exe)
	cmd := exec.Command(exe, "-test.run=TestHelperSleep")
	cmd.Env = append(os.Environ(), "FLEETPROBE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	id := newIdentifier("")
	waitFor(t, func() bool { _, err := os.Readlink(procExe(cmd.Process.Pid)); return err == nil })

	a := id.fromProcess(context.Background(), cmd.Process.Pid, []string{exe})
	if a.ReplacedOnDisk || a.Kind != ArtifactGo {
		t.Fatalf("fresh process misreported: %+v", a)
	}
	// Replace the file in place (new inode at the same path), as an install does.
	copyFile(t, src, exe+".new")
	if err := os.Rename(exe+".new", exe); err != nil {
		t.Fatal(err)
	}
	a = id.fromProcess(context.Background(), cmd.Process.Pid, []string{exe})
	if !a.ReplacedOnDisk {
		t.Fatalf("replaced executable not flagged: %+v", a)
	}
	if a.Path != exe {
		t.Fatalf("path = %q, want %q", a.Path, exe)
	}
}

func TestHelperSleep(t *testing.T) {
	if os.Getenv("FLEETPROBE_HELPER") != "1" {
		t.Skip("helper process")
	}
	time.Sleep(30 * time.Second)
}

func TestScriptServiceUsesScriptAndGit(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		c := gitenv.Command(context.Background(), dir, args...)
		c.Env = append(c.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	git("init", "-q")
	script := filepath.Join(dir, "relay.py")
	if err := os.WriteFile(script, []byte("print(1)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "init")
	git("remote", "add", "origin", "https://user:secret@example.invalid/o/r.git")

	id := newIdentifier("")
	s := id.script(context.Background(), "python3", script)
	if s.Git == nil || len(s.Git.Revision) != 40 || s.Git.Dirty {
		t.Fatalf("git info wrong: %+v", s.Git)
	}
	if strings.Contains(s.Git.Remote, "secret") || strings.Contains(s.Git.Remote, "user") {
		t.Fatalf("remote leaks credentials: %q", s.Git.Remote)
	}
	if err := os.WriteFile(script, []byte("print(2)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if g := readGit(context.Background(), dir); g == nil || !g.Dirty {
		t.Fatalf("dirty tree not detected: %+v", g)
	}
}

func TestScriptArg(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "app.py")
	_ = os.WriteFile(f, []byte(""), 0o644)
	cases := []struct {
		cmd  []string
		want string
	}{
		{[]string{"/usr/bin/python3", "-I", f}, f},
		{[]string{"python3", "app.py", "--port", "1"}, f},
		{[]string{"python3", "-m", "http.server"}, ""},
		{[]string{"bash", "-c", "echo hi"}, ""},
		{[]string{"node"}, ""},
	}
	for _, c := range cases {
		if got := scriptArg(c.cmd, dir); got != c.want {
			t.Errorf("scriptArg(%v) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestIsInterpreter(t *testing.T) {
	for _, n := range []string{"python3", "python3.14", "node", "bash"} {
		if !isInterpreter(n) {
			t.Errorf("%s should be an interpreter", n)
		}
	}
	for _, n := range []string{"tether", "pythonista", "nodemon", "bwrap"} {
		if isInterpreter(n) {
			t.Errorf("%s should not be an interpreter", n)
		}
	}
}

func TestReleaseOf(t *testing.T) {
	r := releaseOf("/Users/me/.local/share/fleet/releases/caster/v0.0.0-327-g437deea/caster")
	if r == nil || r.App != "caster" || r.Version != "v0.0.0-327-g437deea" || !strings.HasSuffix(r.Dir, "/releases/caster/v0.0.0-327-g437deea") {
		t.Fatalf("release = %+v", r)
	}
	if releaseOf("/Users/me/.local/bin/caster") != nil {
		t.Fatal("non-release path matched")
	}
}

func TestSkipBinaryName(t *testing.T) {
	for _, n := range []string{"cerberus.prev-20261030-2536a02", "basalisk.pre-fleet-install", "cerberus.before-shared-outpost", "agy.bak-20261024-1.4.0", "x.bak", "x.old", ".hidden", "tool.prev20261001"} {
		if !skipBinaryName(n) {
			t.Errorf("%s should be skipped", n)
		}
	}
	for _, n := range []string{"cerberus", "golangci-lint-v2.11.4", "tether-remote", "cerberus.main-5167ea1", "python3.14", "previewer", "pre-commit"} {
		if skipBinaryName(n) {
			t.Errorf("%s should be kept", n)
		}
	}
}

func TestParsePPIDWithSpacesInComm(t *testing.T) {
	if got := parsePPID([]byte("123 (my (odd) proc) S 42 123 123 0 -1")); got != 42 {
		t.Fatalf("ppid = %d", got)
	}
}

func TestParseShow(t *testing.T) {
	out := "Id=warden.service\nMainPID=10\nExecMainStartTimestamp=Thu 2026-11-19 21:55:43 UTC\n" +
		"ExecStart={ path=/Users/me/.local/bin/warden ; argv[]=/Users/me/.local/bin/warden serve ; ignore_errors=no }\n\n" +
		"Id=other.service\nMainPID=0\nExecMainStartTimestamp=n/a\nExecStart=\n"
	us := parseShow(out)
	if len(us) != 2 {
		t.Fatalf("units = %+v", us)
	}
	if us[0].ID != "warden.service" || us[0].MainPID != 10 || us[0].ExecPath != "/Users/me/.local/bin/warden" {
		t.Fatalf("unit 0 = %+v", us[0])
	}
	if want := time.Date(2026, 11, 19, 21, 55, 43, 0, time.UTC); !us[0].StartedAt.Equal(want) {
		t.Fatalf("started = %v", us[0].StartedAt)
	}
	if !us[1].StartedAt.IsZero() {
		t.Fatalf("n/a timestamp parsed: %v", us[1].StartedAt)
	}
}

func TestAttribute(t *testing.T) {
	svcs := []Service{
		{Kind: KindSystemdUser, Name: "com.hollis-labs.cerberus.caster.caster-uat"},
		{Kind: KindSystemdUser, Name: "com.hollis-labs.cerberus.hub-tether"},
		{Kind: KindSystemdUser, Name: "warden"},
		{Kind: KindSystemdUser, Name: "loom-daemon"},
		{Kind: KindContainer, Name: "grafana"},
		{Kind: KindContainer, Name: "station-gateway-ledger"},
		{Kind: KindSystemdUser, Name: "unknown"},
	}
	refs := []ResourceRef{
		{ID: "caster-uat", Project: "caster"},
		{ID: "hub-tether", Project: "hub"},
		{ID: "warden", Project: "warden"},
		{ID: "loom", Project: "loom", Units: []string{"loom-daemon.service"}},
		{ID: "grafana", Project: "otel"},
		{ID: "postgres-shared", Project: "data", Containers: []string{"station-gateway-ledger"}},
	}
	attribute(svcs, refs, DefaultUnitPrefix)
	want := []string{"caster-uat", "hub-tether", "warden", "loom", "grafana", "postgres-shared", ""}
	for i, s := range svcs {
		if s.ResourceID != want[i] {
			t.Errorf("%s -> %q, want %q", s.Name, s.ResourceID, want[i])
		}
	}
}

func TestContainerServicesNeverCarryEnv(t *testing.T) {
	raw := `[{"Name":"/grafana","Image":"sha256:abc","State":{"Status":"running","StartedAt":"2026-11-20T05:00:00.123Z","Pid":7,"Health":{"Status":"healthy"}},
	  "Config":{"Image":"grafana/grafana:13.2.6","Env":["GF_SECURITY_ADMIN_PASSWORD=hunter2"],
	  "Labels":{"com.docker.compose.project":"otel","com.docker.compose.service":"grafana"}}}]`
	var cs []dockerContainer
	if err := json.Unmarshal([]byte(raw), &cs); err != nil {
		t.Fatal(err)
	}
	imgs := map[string]dockerImage{"sha256:abc": {ID: "sha256:abc", RepoDigests: []string{"grafana/grafana@sha256:def"}, Created: "2026-10-01T00:00:00Z"}}
	svcs := containerServices(cs, imgs)
	b, _ := json.Marshal(svcs)
	if strings.Contains(string(b), "hunter2") {
		t.Fatal("container env leaked into the report")
	}
	s := svcs[0]
	if s.Name != "grafana" || s.Image.Ref != "grafana/grafana:13.2.6" || s.Image.Health != "healthy" || s.Compose.Project != "otel" || len(s.Image.Digests) != 1 {
		t.Fatalf("service = %+v image=%+v", s, s.Image)
	}
}

func TestStripCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"https://x-access-token:abc@github.com/o/r.git": "https://github.com/o/r.git",
		"https://github.com/o/r.git":                    "https://github.com/o/r.git",
		"git@github.com:o/r.git":                        "git@github.com:o/r.git",
	} {
		if got := stripCredentials(in); got != want {
			t.Errorf("stripCredentials(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHashCacheReuse(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "bin")
	_ = os.WriteFile(f, []byte("#!/bin/sh\necho hi\n"), 0o755)
	cachePath := filepath.Join(dir, "cache.json")
	c := loadHashCache(cachePath)
	fi, _ := os.Stat(f)
	d1, err := c.digest(f, fi)
	if err != nil || len(d1) != 64 {
		t.Fatalf("digest: %v %q", err, d1)
	}
	c.save()
	c2 := loadHashCache(cachePath)
	if len(c2.entries) != 1 {
		t.Fatalf("cache not persisted: %+v", c2.entries)
	}
	if info, _ := os.Stat(cachePath); info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode = %v", info.Mode().Perm())
	}
}

func TestProbeLocalSmoke(t *testing.T) {
	dir := t.TempDir()
	self, _ := os.Executable()
	copyFile(t, self, filepath.Join(dir, "tool"))
	r, err := Probe(context.Background(), Options{BinDirs: []string{dir, filepath.Join(dir, "missing")}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Schema != Schema || r.Host == "" || len(r.Binaries) != 1 || r.Binaries[0].Kind != ArtifactGo {
		t.Fatalf("report = %+v", r)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

func procExe(pid int) string { return "/proc/" + strconv.Itoa(pid) + "/exe" }

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

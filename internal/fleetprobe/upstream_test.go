package fleetprobe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/gitenv"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := gitenv.Command(context.Background(), dir, args...)
	c.Env = append(c.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Skipf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestRepoIndexBehindAndMergedPRs builds a "remote" with three commits after the
// deployed one and checks the count and the PR numbers read from subjects.
func TestRepoIndexBehindAndMergedPRs(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "go.mod"), []byte("module example.invalid/svc\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "add", ".")
	gitIn(t, origin, "commit", "-qm", "init")
	deployed := gitIn(t, origin, "rev-parse", "HEAD")
	for _, subj := range []string{"feat: one (#11)", "chore: no pr", "fix: two (#12)"} {
		gitIn(t, origin, "commit", "-q", "--allow-empty", "-m", subj)
	}
	clone := filepath.Join(root, "work", "svc")
	gitIn(t, root, "clone", "-q", origin, clone)

	ix := NewRepoIndex([]string{filepath.Join(root, "work")})
	if ix.Len() != 1 {
		t.Fatalf("indexed %d modules", ix.Len())
	}
	u := ix.Lookup(context.Background(), "example.invalid/svc", deployed)
	if u == nil || u.Behind == nil || *u.Behind != 3 {
		t.Fatalf("upstream = %+v", u)
	}
	if len(u.Merged) != 2 || u.Merged[0].Number != 12 || u.Merged[1].Number != 11 || u.Merged[0].Title != "fix: two" {
		t.Fatalf("merged = %+v", u.Merged)
	}
	if u2 := ix.Lookup(context.Background(), "example.invalid/svc", strings.Repeat("a", 40)); u2 == nil || u2.Behind != nil || u2.Note == "" {
		t.Fatalf("unknown revision = %+v", u2)
	}
	if ix.Lookup(context.Background(), "example.invalid/other", deployed) != nil {
		t.Fatal("unindexed module answered")
	}
}

func TestFindDrift(t *testing.T) {
	art := func(path, mod, rev string, dirty bool) Artifact {
		return Artifact{Path: path, Kind: ArtifactGo, Go: &GoBuild{Module: mod, Revision: rev, Modified: dirty}}
	}
	reports := map[string]*Report{
		"a": {Binaries: []Artifact{art("/x/torque", "m/torque", "1111111111111111", false), art("/x/tether", "m/tether", "", false)}},
		"b": {Binaries: []Artifact{art("/y/torque", "m/torque", "2222222222222222", false)}},
		"c": {Binaries: []Artifact{art("/z/torque", "m/torque", "1111111111111111", true)},
			Services: []Service{{Artifact: ptr(art("/z/hub-torque", "m/torque", "3333333333333333", false))}}},
		// Two builds on one host only (an old copy) is not drift.
		"d": {Binaries: []Artifact{art("/x/solo", "m/solo", "aaaaaaaaaaaa", false), art("/y/solo", "m/solo", "bbbbbbbbbbbb", false)}},
	}
	d := FindDrift(reports)
	if len(d) != 1 || d[0].Name != "torque" {
		t.Fatalf("drift = %+v", d)
	}
	if got := d[0].Builds; len(got) != 3 || len(got["111111111111"]) != 1 || len(got["111111111111+dirty"]) != 1 || len(got["222222222222"]) != 1 {
		t.Fatalf("builds = %+v", got)
	}
}

func ptr[T any](v T) *T { return &v }

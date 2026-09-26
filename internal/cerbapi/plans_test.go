package cerbapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/plan"
)

func testGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Plan Test", "-c", "user.email=plan@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...) //nolint:gosec // a test's own git, on its temp dir
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A deploy profile's plan binds its steps, its definition and the checkout
// it deploys: an edited profile, a new commit or uncommitted changes are a
// different plan (CERB-GAP-853).
func TestDeployProfilePlanBindsProfileAndCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".vercel"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".vercel", "project.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "init", "-q")
	testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-q", "-m", "one")
	sink := audit.NewMemory()
	def := infra.Definition()
	op, known := def.Operation(infra.OpRunProfile)
	spec := auditSpec{connector: def.ID, operation: infra.OpRunProfile, op: op, known: known, config: map[string]any{"id": "site"}}
	profile := infra.DeploymentProfile{ID: "site", Provider: "vercel", RepoPath: repo, DeployCommand: "true"}
	hash := func(p infra.DeploymentProfile) (plan.Plan, string) {
		t.Helper()
		pl, err := planDeploymentProfile(context.Background(), spec, sink, nil, p)
		if err != nil {
			t.Fatal(err)
		}
		h, err := pl.Hash()
		if err != nil {
			t.Fatal(err)
		}
		return pl, h
	}
	base, first := hash(profile)
	if base.Source == nil || len(base.Source.HEAD) != 40 || base.Source.Dirty || len(base.Steps) == 0 || base.Digests["profile"] == "" {
		t.Fatalf("plan %+v", base)
	}
	if _, again := hash(profile); again != first {
		t.Fatal("the same profile and checkout planned differently")
	}
	edited := profile
	edited.BuildCommand = "make"
	if _, h := hash(edited); h == first {
		t.Fatal("an edited profile is the same plan")
	}
	if err := os.WriteFile(filepath.Join(repo, "index.html"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, h := hash(profile)
	if !dirty.Source.Dirty || h == first {
		t.Fatalf("a dirty tree is the same plan: %+v", dirty.Source)
	}
	testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-q", "-m", "two")
	moved, h2 := hash(profile)
	if moved.Source.Dirty || moved.Source.HEAD == base.Source.HEAD || h2 == first || h2 == h {
		t.Fatalf("a new commit is the same plan: %+v", moved.Source)
	}
}

// gitSource reads the directory it is given even when a git hook has set
// GIT_DIR for the process.
func TestGitSourceIgnoresInheritedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo, other := t.TempDir(), t.TempDir()
	testGit(t, repo, "init", "-q")
	testGit(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	if src := gitSource(context.Background(), repo); len(src.HEAD) != 40 {
		t.Fatalf("source %+v", src)
	}
}

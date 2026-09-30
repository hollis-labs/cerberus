package cerbapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/gitenv"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
)

func testGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitenv.Command(context.Background(), dir, append([]string{"-c", "user.name=Plan Test", "-c", "user.email=plan@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
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
		pl, _, err := planDeploymentProfile(context.Background(), spec, sink, nil, p)
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

// A dirty tree's plan binds what the changes are, not only that there are
// some (M10): editing an already-modified file, or an untracked one, is a
// different source, and a deploy checked on the tree refuses to build it
// once it changed.
func TestADirtySourceBindsItsContent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	testGit(t, repo, "init", "-q")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil { //nolint:gosec // the test's own temp repo
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n")
	testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-q", "-m", "one")
	ctx := context.Background()
	if clean := gitSource(ctx, repo); clean.Dirty || clean.Content != "" {
		t.Fatalf("clean: %+v", clean)
	}
	write("main.go", "package main // edit one\n")
	first := gitSource(ctx, repo)
	write("main.go", "package main // edit two\n")
	second := gitSource(ctx, repo)
	if !first.Dirty || !second.Dirty || first.Content == second.Content || first.Content == "" {
		t.Fatalf("an edit to a modified file is the same source: %+v %+v", first, second)
	}
	write("extra.go", "package main\n")
	third := gitSource(ctx, repo)
	write("extra.go", "package main // changed\n")
	if fourth := gitSource(ctx, repo); third.Content == second.Content || fourth.Content == third.Content {
		t.Fatal("an untracked file is not in the source")
	}

	atGate := gitSource(ctx, repo)
	checked := withCheckedSource(ctx, repo)
	if changed := sourceChanged(checked, repo); changed != "" {
		t.Fatalf("an unchanged tree: %s", changed)
	}
	write("extra.go", "package main // after the gate\n")
	changed := sourceChanged(checked, repo)
	if !strings.Contains(changed, "changed after this deploy was checked") || !strings.Contains(changed, "nothing was built") {
		t.Fatalf("a tree changed after the gate: %q", changed)
	}
	if got := redact.Text(changed); got != changed {
		t.Fatalf("redaction rewrote the refusal: %q", got)
	}
	if p := checkedSource(checked, repo); p.Content != atGate.Content {
		t.Fatal("the plan read the tree again instead of the checked one")
	}
}

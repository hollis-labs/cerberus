package local

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// gitOutput reads the directory it is given even when a git hook has set
// GIT_DIR for the process.
func TestGitOutputIgnoresInheritedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo, other := t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Artifact Test", "-c", "user.email=artifact@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-q", "--allow-empty", "-m", "one"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...) //nolint:gosec // a test's own git, on its temp dir
		cmd.Env = gitEnvWithoutRepo()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	head, err := gitOutput(repo, "rev-parse", "HEAD")
	if err != nil || len(head) != 40 {
		t.Fatalf("gitOutput read another repository: %q %v", head, err)
	}
}

package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
)

// BuildLogPath returns the build-log path for a resource:
// ~/.cerberus/apps/<project>/<resource>/logs/build.log. It sits alongside the
// runtime stdout/stderr logs so build output is findable in one place.
func BuildLogPath(res *domain.Resource, spec ProcessSpec) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	layout, err := DefaultInstallLayout(home, res, spec)
	if err != nil {
		return "", err
	}
	return filepath.Join(layout.RootDir, "logs", "build.log"), nil
}

// WriteBuildLog persists a build's command, working dir, captured output, and
// outcome to the resource's build.log (always-on capture — success or
// failure) and returns the path. The latest build replaces the previous one.
// It is best-effort: callers continue the deploy even if the write fails.
func WriteBuildLog(res *domain.Resource, spec ProcessSpec, result *BuildResult, buildErr error) (string, error) {
	path, err := BuildLogPath(res, spec)
	if err != nil {
		return "", err
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil { //nolint:gosec // logs dir under Cerberus-managed install root
		return path, mkErr
	}

	var b strings.Builder
	fmt.Fprintf(&b, "=== build %s ===\n", time.Now().UTC().Format(time.RFC3339))
	if result != nil {
		if len(result.Command) > 0 {
			fmt.Fprintf(&b, "command: %s\n", strings.Join(OutputRedactor(spec).Args(result.Command), " "))
		}
		if result.Dir != "" {
			fmt.Fprintf(&b, "dir: %s\n", result.Dir)
		}
	}
	if buildErr != nil {
		fmt.Fprintf(&b, "result: FAILED — %s\n", buildErr.Error())
	} else {
		b.WriteString("result: ok\n")
	}
	b.WriteString("---\n")
	if result != nil && result.Output != "" {
		b.WriteString(result.Output)
		if !strings.HasSuffix(result.Output, "\n") {
			b.WriteByte('\n')
		}
	}

	if writeErr := os.WriteFile(path, []byte(OutputRedactor(spec).Text(b.String())), 0o600); writeErr != nil {
		return path, writeErr
	}
	if chmodErr := os.Chmod(path, 0600); chmodErr != nil {
		return path, chmodErr
	}
	return path, nil
}

// BuildCommandSummary renders a build's command + dir for a one-line error
// message, e.g. `make build` in /path. Falls back to the strategy kind when
// the command is unknown.
func BuildCommandSummary(spec ProcessSpec, result *BuildResult) string {
	if result != nil && len(result.Command) > 0 {
		dir := result.Dir
		if dir == "" {
			dir = spec.Dir
		}
		return fmt.Sprintf("`%s` in %s", strings.Join(OutputRedactor(spec).Args(result.Command), " "), dir)
	}
	if spec.BuildStrategy != nil {
		return fmt.Sprintf("build_strategy %q in %s", spec.BuildStrategy.Kind, spec.Dir)
	}
	return spec.Dir
}

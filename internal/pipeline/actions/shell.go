package actions

import (
	"context"
	"fmt"
	"github.com/hollis-labs/cerberus/internal/redact"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
)

// Shell runs an arbitrary shell command.
type Shell struct {
	name    string
	command string
	argv    []string
	dir     string
}

// NewShell creates a shell action. The command is run via "sh -c".
func NewShell(name, command, dir string) *Shell {
	return &Shell{name: name, command: command, dir: dir}
}

func NewShellArgv(name string, argv []string, dir string) *Shell {
	return &Shell{name: name, argv: append([]string(nil), argv...), dir: dir}
}

func (a *Shell) Name() string { return a.name }

func (a *Shell) Execute(ctx context.Context, env *domain.PipelineEnv) error {
	var cmd *exec.Cmd
	if len(a.argv) > 0 {
		cmd = exec.CommandContext(ctx, a.argv[0], a.argv[1:]...) // #nosec G204 -- Frozen operator-authored pipeline config, never job/request argv; existing RunPipeline policy/locks and effect admission govern execution.
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", a.command) //nolint:gosec // existing operator-authored shell transport
	} // operator-authored frozen pipeline
	cmd.WaitDelay = 2 * time.Second
	if a.dir != "" {
		cmd.Dir = a.dir
	}
	if env != nil && env.RunIO != nil {
		childEnv, envErr := env.RunIO.Environ(os.Environ())
		if envErr != nil {
			return redact.Guidance("pipeline environment delivery is unavailable")
		}
		cmd.Env = childEnv
		cmd.Stdout = env.RunIO.Stdout()
		cmd.Stderr = env.RunIO.Stderr()
		if cmd.Stdout == nil || cmd.Stderr == nil {
			return redact.Guidance("pipeline stream capture is unavailable")
		}
		if err := cmd.Run(); err != nil {
			return redact.GuidanceWrap(err, "pipeline action %s failed", a.name)
		}
		return nil
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", a.command, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *Shell) Rollback(_ context.Context, _ *domain.PipelineEnv) error {
	return nil // shell actions have no automatic rollback
}

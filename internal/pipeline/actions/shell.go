package actions

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/hollis-labs/cerberus/internal/domain"
)

// Shell runs an arbitrary shell command.
type Shell struct {
	name    string
	command string
	dir     string
}

// NewShell creates a shell action. The command is run via "sh -c".
func NewShell(name, command, dir string) *Shell {
	return &Shell{name: name, command: command, dir: dir}
}

func (a *Shell) Name() string { return a.name }

func (a *Shell) Execute(ctx context.Context, _ *domain.PipelineEnv) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", a.command) //nolint:gosec
	if a.dir != "" {
		cmd.Dir = a.dir
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

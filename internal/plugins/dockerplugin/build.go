package dockerplugin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func WritePrototypeWithBinary(ctx context.Context, dir, sourceDir string) error {
	if err := WritePrototype(dir); err != nil {
		return err
	}
	moduleRoot, err := resolveModuleRoot(sourceDir)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, "bin", BinaryName)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", target, "./cmd/cerberus-docker-plugin")
	cmd.Dir = moduleRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build %s: %s: %w", BinaryName, strings.TrimSpace(string(out)), err)
	}
	return nil
}

func resolveModuleRoot(start string) (string, error) {
	if start == "" {
		return "", fmt.Errorf("source directory is required")
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve source directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod above %s", start)
		}
		dir = parent
	}
}

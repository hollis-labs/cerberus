package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateConfigLocation rejects the retired central project-config directory.
// indexPath identifies the active Cerberus state directory, including --config
// overrides. The default ~/.cerberus/projects path is always retired as well.
// Both the registered path and its symlink target are checked: a pointer in
// Cerberus state is not repo-owned merely because its content lives elsewhere.
func ValidateConfigLocation(path, indexPath string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	roots := []string{filepath.Join(home, ".cerberus", "projects")}
	if indexPath != "" {
		stateDir, err := filepath.Abs(filepath.Dir(indexPath))
		if err != nil {
			return err
		}
		roots = append(roots, filepath.Join(stateDir, "projects"))
	}
	candidates := []string{absolute}
	if canonical, err := filepath.EvalSymlinks(absolute); err == nil {
		candidates = append(candidates, canonical)
	}
	for _, root := range roots {
		resolvedRoot := root
		if canonical, err := filepath.EvalSymlinks(root); err == nil {
			resolvedRoot = canonical
		} else if parent, err := filepath.EvalSymlinks(filepath.Dir(root)); err == nil {
			resolvedRoot = filepath.Join(parent, filepath.Base(root))
		}
		for _, candidate := range candidates {
			if insideDirectory(root, candidate) || insideDirectory(resolvedRoot, candidate) {
				return fmt.Errorf("project config %s is under the retired centralized directory %s; move the config into its owning repo, commit it on main, then run `cerberus register <repo>/<project>.cerberus.yaml`", path, root)
			}
		}
	}
	return nil
}

func insideDirectory(dir, path string) bool {
	relative, err := filepath.Rel(dir, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

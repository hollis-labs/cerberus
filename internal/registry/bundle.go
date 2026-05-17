package registry

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	// BundleKind is the kind contract for a bundle manifest: a flat list
	// of paths to project configs, for a repo that ships several apps.
	BundleKind = "cerberus-bundle/v1"

	// DefaultBundleFilename is the conventional manifest name. The
	// register command looks for it when handed a directory.
	DefaultBundleFilename = "bundle.cerberus.yaml"
)

// Bundle is a manifest pointing at several project config files. It
// carries no resource definitions itself — it is discovery sugar so a
// multi-app repo can register everything in one command. Each entry
// becomes its own per-owner registry row; the manifest is not itself
// registered.
type Bundle struct {
	// Kind must equal BundleKind.
	Kind string `yaml:"kind"`

	// Projects lists paths to project config files. Relative paths are
	// resolved against the manifest's own directory at load time.
	Projects []string `yaml:"projects"`
}

// LoadBundle reads a bundle manifest. Relative project paths are
// resolved against the manifest's own directory and returned absolute,
// so callers can load each referenced project config directly. Unknown
// top-level fields are rejected, consistent with LoadProjectConfig.
//
// LoadBundle does not check that the referenced files exist or parse;
// register / resolve do that when they load each project config.
func LoadBundle(path string) (*Bundle, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("read bundle %s: %w", path, err)
	}

	var bundle Bundle
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&bundle); err != nil {
		return nil, fmt.Errorf("parse bundle %s: %w", path, err)
	}

	base := filepath.Dir(path)
	for i, p := range bundle.Projects {
		if p != "" && !filepath.IsAbs(p) {
			bundle.Projects[i] = filepath.Join(base, p)
		}
	}
	return &bundle, nil
}

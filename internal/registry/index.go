package registry

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// IndexVersion is the schema version of the registry index file. Bumping
// it signals an on-disk format change requiring a migration.
const IndexVersion = 1

// DefaultIndexFilename is the registry index basename under ~/.cerberus.
const DefaultIndexFilename = "registry.yaml"

// IndexEntry is one registered project config — a pointer, never the
// config body. The body is always re-read fresh from Path, keeping the
// index a local-first resolver handle (Tether D2).
type IndexEntry struct {
	// Owner is the registry key, copied from the project config.
	Owner string `yaml:"owner" json:"owner"`
	// Namespace is copied from the project config (reserved field).
	Namespace string `yaml:"namespace" json:"namespace"`
	// Path is the absolute path to the owning app's project config.
	Path string `yaml:"path" json:"path"`
	// Kind records the config kind at registration time.
	Kind string `yaml:"kind" json:"kind"`
	// RegisteredAt is an RFC3339 UTC timestamp.
	RegisteredAt string `yaml:"registered_at" json:"registered_at"`
	// Via is the manifest path when this entry was registered through a
	// bundle manifest rather than directly. Empty for direct registers.
	Via string `yaml:"via,omitempty" json:"via,omitempty"`
}

// Index is the on-disk registry: ~/.cerberus/registry.yaml. It is a flat
// list of pointer entries — deliberately not a database, since it holds
// only a handful of rows and health is computed live.
type Index struct {
	Version int          `yaml:"version"`
	Entries []IndexEntry `yaml:"entries"`
}

// DefaultIndexPath returns ~/.cerberus/registry.yaml.
func DefaultIndexPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".cerberus", DefaultIndexFilename), nil
}

// LoadIndex reads the registry index. A missing file is not an error —
// it yields an empty index, which is the correct first-run state.
func LoadIndex(path string) (*Index, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-controlled state path
	if err != nil {
		if os.IsNotExist(err) {
			return &Index{Version: IndexVersion}, nil
		}
		return nil, fmt.Errorf("read registry index %s: %w", path, err)
	}
	var idx Index
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse registry index %s: %w", path, err)
	}
	if idx.Version == 0 {
		idx.Version = IndexVersion
	}
	return &idx, nil
}

// Save writes the index atomically: it renders to a temp file in the
// same directory, then renames over the target so a crash mid-write
// cannot leave a truncated index.
func (idx *Index) Save(path string) error {
	idx.Version = IndexVersion
	data, err := yaml.Marshal(idx)
	if err != nil {
		return fmt.Errorf("marshal registry index: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create registry dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, DefaultIndexFilename+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp index: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp index: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp index: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("commit registry index: %w", err)
	}
	return nil
}

// find returns the position of owner in Entries, or -1.
func (idx *Index) find(owner string) int {
	for i, e := range idx.Entries {
		if e.Owner == owner {
			return i
		}
	}
	return -1
}

// upsert inserts entry, or replaces an existing entry with the same
// owner. Re-registering an owner is therefore idempotent.
func (idx *Index) upsert(entry IndexEntry) {
	if i := idx.find(entry.Owner); i >= 0 {
		idx.Entries[i] = entry
		return
	}
	idx.Entries = append(idx.Entries, entry)
}

// remove deletes the entry for owner. It reports whether an entry was
// found and removed.
func (idx *Index) remove(owner string) bool {
	i := idx.find(owner)
	if i < 0 {
		return false
	}
	idx.Entries = append(idx.Entries[:i], idx.Entries[i+1:]...)
	return true
}

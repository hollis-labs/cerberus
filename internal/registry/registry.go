package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Health status values for a registered entry.
const (
	HealthOK      = "ok"      // file present, parses, validates
	HealthMissing = "missing" // pointed-to file no longer exists
	HealthInvalid = "invalid" // file exists but fails parse or validation
)

// Registry is the register/deregister/query/health service over the
// on-disk index. It owns no in-memory state beyond the index path: every
// operation reads the index fresh, consistent with Cerberus's
// no-stale-config discipline.
type Registry struct {
	indexPath string
}

// New constructs a Registry backed by the given index file path.
func New(indexPath string) *Registry {
	return &Registry{indexPath: indexPath}
}

// Default constructs a Registry backed by ~/.cerberus/registry.yaml.
func Default() (*Registry, error) {
	path, err := DefaultIndexPath()
	if err != nil {
		return nil, err
	}
	return New(path), nil
}

// IndexPathFor returns the registry index path paired with a global
// config path: its sibling registry.yaml. An empty globalPath yields
// the default ~/.cerberus/registry.yaml. This is the single source of
// truth for index location — ResolveConfig and ForConfig both use it,
// so a `--config` override or a test temp dir relocates the registry
// consistently with the config it pairs with.
func IndexPathFor(globalPath string) (string, error) {
	if globalPath == "" {
		return DefaultIndexPath()
	}
	return filepath.Join(filepath.Dir(globalPath), DefaultIndexFilename), nil
}

// ForConfig returns a Registry whose index is the sibling of globalPath,
// matching how ResolveConfig locates the index.
func ForConfig(globalPath string) (*Registry, error) {
	path, err := IndexPathFor(globalPath)
	if err != nil {
		return nil, err
	}
	return New(path), nil
}

// IndexPath returns the index file this registry reads and writes.
func (r *Registry) IndexPath() string { return r.indexPath }

// Register adds (or updates) registry entries for the config at path.
//
// path may be a project config, a bundle manifest, or a directory
// containing a bundle manifest. A project config yields one entry; a
// manifest yields one per referenced project config. Registration is
// atomic and all-or-nothing: every config is loaded and validated
// before the index is touched, so a single invalid config aborts the
// whole operation without partial writes.
//
// Re-registering an owner replaces its entry (idempotent).
func (r *Registry) Register(path string) ([]IndexEntry, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path %s: %w", path, err)
	}

	entries, err := r.collect(abs, "", map[string]bool{})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("register %s: nothing to register", path)
	}

	idx, err := LoadIndex(r.indexPath)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		idx.upsert(entry)
	}
	if err := idx.Save(r.indexPath); err != nil {
		return nil, err
	}
	return entries, nil
}

// collect walks a path into validated index entries without writing
// anything. via carries the manifest path when recursing through a
// bundle. seenManifest guards against manifest reference cycles.
func (r *Registry) collect(path, via string, seenManifest map[string]bool) ([]IndexEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		manifest := filepath.Join(path, DefaultBundleFilename)
		if _, statErr := os.Stat(manifest); statErr != nil {
			return nil, fmt.Errorf("directory %s has no %s; pass an explicit file path", path, DefaultBundleFilename)
		}
		path = manifest
	}

	kind, err := PeekKind(path)
	if err != nil {
		return nil, err
	}

	switch kind {
	case ProjectConfigKind:
		pc, err := LoadProjectConfig(path)
		if err != nil {
			return nil, err
		}
		if result := ValidateProjectConfig(pc); result.HasErrors() {
			return nil, fmt.Errorf("invalid project config %s: %s", path, result.Errors()[0])
		}
		return []IndexEntry{{
			Owner:        pc.Owner,
			Namespace:    pc.Namespace,
			Path:         path,
			Kind:         kind,
			RegisteredAt: time.Now().UTC().Format(time.RFC3339),
			Via:          via,
		}}, nil

	case BundleKind:
		if seenManifest[path] {
			return nil, fmt.Errorf("bundle manifest cycle through %s", path)
		}
		seenManifest[path] = true

		bundle, err := LoadBundle(path)
		if err != nil {
			return nil, err
		}
		if result := ValidateBundle(bundle); result.HasErrors() {
			return nil, fmt.Errorf("invalid bundle manifest %s: %s", path, result.Errors()[0])
		}
		var all []IndexEntry
		owners := map[string]bool{}
		for _, ref := range bundle.Projects {
			sub, err := r.collect(ref, path, seenManifest)
			if err != nil {
				return nil, fmt.Errorf("bundle %s: %w", path, err)
			}
			for _, entry := range sub {
				if owners[entry.Owner] {
					return nil, fmt.Errorf("bundle %s registers owner %q more than once", path, entry.Owner)
				}
				owners[entry.Owner] = true
				all = append(all, entry)
			}
		}
		return all, nil

	default:
		return nil, fmt.Errorf("%s declares unknown kind %q; expected %q or %q",
			path, kind, ProjectConfigKind, BundleKind)
	}
}

// Deregister removes the entry for owner. It returns an error if owner
// is not registered.
func (r *Registry) Deregister(owner string) error {
	idx, err := LoadIndex(r.indexPath)
	if err != nil {
		return err
	}
	if !idx.remove(owner) {
		return fmt.Errorf("owner %q is not registered", owner)
	}
	return idx.Save(r.indexPath)
}

// List returns the registered entries, sorted by owner for stable
// output.
func (r *Registry) List() ([]IndexEntry, error) {
	idx, err := LoadIndex(r.indexPath)
	if err != nil {
		return nil, err
	}
	entries := append([]IndexEntry(nil), idx.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Owner < entries[j].Owner })
	return entries, nil
}

// HealthReport is the live health of one registered entry. Health is
// never stored — it is recomputed from the pointed-to file on demand.
type HealthReport struct {
	Owner  string
	Path   string
	Status string // HealthOK | HealthMissing | HealthInvalid
	Detail string
}

// Healthy reports whether the entry resolves cleanly.
func (h HealthReport) Healthy() bool { return h.Status == HealthOK }

// Health checks every registered entry: the pointed-to file must exist,
// parse, and pass schema validation. Results are sorted by owner.
func (r *Registry) Health() ([]HealthReport, error) {
	entries, err := r.List()
	if err != nil {
		return nil, err
	}
	reports := make([]HealthReport, 0, len(entries))
	for _, entry := range entries {
		reports = append(reports, checkEntry(entry))
	}
	return reports, nil
}

func checkEntry(entry IndexEntry) HealthReport {
	report := HealthReport{Owner: entry.Owner, Path: entry.Path}
	if _, err := os.Stat(entry.Path); err != nil {
		report.Status = HealthMissing
		report.Detail = "config file no longer exists at registered path"
		return report
	}
	pc, err := LoadProjectConfig(entry.Path)
	if err != nil {
		report.Status = HealthInvalid
		report.Detail = err.Error()
		return report
	}
	if result := ValidateProjectConfig(pc); result.HasErrors() {
		report.Status = HealthInvalid
		report.Detail = result.Errors()[0].String()
		return report
	}
	report.Status = HealthOK
	return report
}

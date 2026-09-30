package cerbapi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
)

// appliedDefinitions records, per resource, a keyed digest of the definition
// a gated verb last started it with (M10). The monitor restarts a workload
// only while its definition is still that one: a definition edited since
// then waits for a person to apply it, rather than running unapproved the
// next time the workload goes down.
//
// It holds digests, never definitions, since a definition's environment can
// carry values. With a path it survives a restart; without one it lives in
// the process.
type appliedDefinitions struct {
	path string

	mu      sync.Mutex
	loaded  bool
	digests map[string]string
}

// WithResourceRuntimeAppliedPath persists the applied-definition digests at
// path (~/.cerberus/runtime/applied.json).
func WithResourceRuntimeAppliedPath(path string) ResourceRuntimeOption {
	return func(s *ResourceRuntimeService) { s.applied = &appliedDefinitions{path: path} }
}

func (a *appliedDefinitions) load() {
	if a.loaded {
		return
	}
	a.loaded, a.digests = true, map[string]string{}
	if a.path == "" {
		return
	}
	data, err := os.ReadFile(a.path) //nolint:gosec // the operator's own state file
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &a.digests)
	if a.digests == nil {
		a.digests = map[string]string{}
	}
}

// get is the digest of the definition id was last applied with.
func (a *appliedDefinitions) get(id string) (string, bool) {
	if a == nil {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load()
	d, ok := a.digests[id]
	return d, ok
}

// set records the digest id was just applied with.
func (a *appliedDefinitions) set(id, digest string) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load()
	if a.digests[id] == digest {
		return nil
	}
	a.digests[id] = digest
	if a.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(a.digests, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	return nil
}

// definitionDigest is the keyed digest of a resource's definition, as the
// plan's spec digest is.
func (s *ResourceRuntimeService) definitionDigest(res *config.ResourceDef) string {
	return s.audit.Digest(res)
}

// appliedVerbs are the verbs that start a workload with its definition.
var appliedVerbs = map[string]bool{localconn.OpApply: true, localconn.OpDeploy: true, localconn.OpReload: true, localconn.OpSync: true}

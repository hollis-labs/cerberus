package policy

import (
	"os"
	"path/filepath"
	"reflect"
)

// EnforcementFileName is the working file `cerberus policy enforce` owns:
// the enforcement section and nothing else, like posture.yaml for the
// posture, so a later `policy apply` keeps it and no other working file is
// rewritten by a command.
const EnforcementFileName = "enforcement.yaml"

// EnforcementFile is the enforcement working file's contents, or an empty
// file when there is none.
func (s Store) EnforcementFile() (File, error) {
	f, err := readFile(filepath.Join(s.Dir, EnforcementFileName))
	if os.IsNotExist(err) {
		return File{Version: FileVersion}, nil
	}
	return f, err
}

// EnforcementDeclaredElsewhere names the working files other than
// enforcement.yaml that declare an enforcement section.
func (s Store) EnforcementDeclaredElsewhere() ([]string, error) {
	paths, err := s.WorkingFiles()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, path := range paths {
		if filepath.Base(path) == EnforcementFileName && filepath.Dir(path) == s.Dir {
			continue
		}
		if f, err := readFile(path); err == nil && f.Enforcement != nil {
			out = append(out, relName(s.Dir, path))
		}
	}
	return out, nil
}

// LoadWorkingWithEnforcement is LoadWorking with enforcement.yaml replaced
// by p: the policy a `policy enforce` would apply, before anything is
// written.
func (s Store) LoadWorkingWithEnforcement(p File) (File, []string, error) {
	return s.loadWorkingReplacing(EnforcementFileName, p)
}

// WriteEnforcementFile writes p's enforcement section as enforcement.yaml,
// atomically, and returns a func that puts the previous contents back.
func (s Store) WriteEnforcementFile(p File) (restore func(), err error) {
	path := filepath.Join(s.Dir, EnforcementFileName)
	previous, readErr := os.ReadFile(path) //nolint:gosec // the operator's own policy directory
	existed := readErr == nil
	data, err := Encode(File{Version: FileVersion, Enforcement: p.Enforcement})
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	if err = writeAtomic(path, data); err != nil {
		return nil, err
	}
	return func() {
		if existed {
			_ = writeAtomic(path, previous)
		} else {
			_ = os.Remove(path)
		}
	}, nil
}

// SetEnforceEntry adds an enforced scope, replacing one with the same scope.
func (e *Enforcement) SetEnforceEntry(entry EnforceEntry) {
	for i, cur := range e.Enforce {
		if sameScope(cur, entry) {
			e.Enforce[i] = entry
			return
		}
	}
	e.Enforce = append(e.Enforce, entry)
}

// RemoveEnforceEntry removes the enforced scope entry names, reporting
// whether there was one.
func (e *Enforcement) RemoveEnforceEntry(entry EnforceEntry) bool {
	for i, cur := range e.Enforce {
		if sameScope(cur, entry) {
			e.Enforce = append(e.Enforce[:i], e.Enforce[i+1:]...)
			return true
		}
	}
	return false
}

func sameScope(a, b EnforceEntry) bool {
	return reflect.DeepEqual(a.Match, b.Match) && a.Principal == b.Principal && reflect.DeepEqual(a.Effect, b.Effect)
}

package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// PostureFileName is the working file `cerberus posture set` owns: the
// global posture and the posture rules, and nothing else. Keeping them in a
// file of their own means a later `cerberus policy apply` applies them
// again rather than reverting them, and the operator's other working files
// are never rewritten by a command.
const PostureFileName = "posture.yaml"

// PostureFile is the posture working file's contents, or an empty file when
// there is none.
func (s Store) PostureFile() (File, error) {
	f, err := readFile(filepath.Join(s.Dir, PostureFileName))
	if os.IsNotExist(err) {
		return File{Version: FileVersion}, nil
	}
	return f, err
}

// PostureDeclaredElsewhere names the working files other than posture.yaml
// that declare a posture or posture rules. `posture set` refuses while any
// do: two files saying different things about the posture is how an
// operator ends up with a posture nobody chose.
func (s Store) PostureDeclaredElsewhere() ([]string, error) {
	paths, err := s.WorkingFiles()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, path := range paths {
		if filepath.Base(path) == PostureFileName && filepath.Dir(path) == s.Dir {
			continue
		}
		f, err := readFile(path)
		if err != nil {
			continue // LoadWorking reports it
		}
		if f.Posture != "" || len(f.PostureRules) > 0 {
			out = append(out, relName(s.Dir, path))
		}
	}
	return out, nil
}

// LoadWorkingWithPosture is LoadWorking with posture.yaml replaced by p: the
// working policy a `posture set` would apply, computed before anything is
// written.
func (s Store) LoadWorkingWithPosture(p File) (File, []string, error) {
	paths, err := s.WorkingFiles()
	if err != nil {
		return File{}, nil, err
	}
	var files []File
	var problems []string
	for _, path := range paths {
		if filepath.Base(path) == PostureFileName && filepath.Dir(path) == s.Dir {
			continue
		}
		f, err := readFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(path), err))
			continue
		}
		for _, problem := range f.Validate() {
			problems = append(problems, fmt.Sprintf("%s: %s", relName(s.Dir, path), problem))
		}
		files = append(files, f)
	}
	p.Version = FileVersion
	for _, problem := range p.Validate() {
		problems = append(problems, PostureFileName+": "+problem)
	}
	return Merge(append(files, p)...), problems, nil
}

// WritePostureFile writes p as posture.yaml, whole and atomically, and
// returns a func that puts the previous contents back, for an apply that
// fails after the write.
func (s Store) WritePostureFile(p File) (restore func(), err error) {
	path := filepath.Join(s.Dir, PostureFileName)
	previous, readErr := os.ReadFile(path) //nolint:gosec // the operator's own policy directory
	existed := readErr == nil
	data, err := Encode(File{Version: FileVersion, Posture: p.Posture, PostureRules: p.PostureRules})
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

// SetPostureRule adds a posture rule for match, replacing one with the same
// match.
func (f *File) SetPostureRule(match TargetMatch, posture string) {
	for i, r := range f.PostureRules {
		if reflect.DeepEqual(r.Match, match) {
			f.PostureRules[i].Posture = posture
			return
		}
	}
	f.PostureRules = append(f.PostureRules, PostureRule{Match: match, Posture: posture})
}

// RemovePostureRule removes the rule for match, reporting whether there was
// one.
func (f *File) RemovePostureRule(match TargetMatch) bool {
	for i, r := range f.PostureRules {
		if reflect.DeepEqual(r.Match, match) {
			f.PostureRules = append(f.PostureRules[:i], f.PostureRules[i+1:]...)
			return true
		}
	}
	return false
}

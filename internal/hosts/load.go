package hosts

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultPath is the operator's hosts file, ~/.cerberus/hosts.yaml. Like the
// rest of the legacy ~/.cerberus tree it is outside every repo.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cerberus", "hosts.yaml"), nil
}

// DefaultStatePath is where the daemon keeps derived host state.
func DefaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cerberus", "state", "hosts.json"), nil
}

// Parse reads and validates a hosts file. Unknown fields are an error: a
// misspelled key silently ignored would read as a host that is not what the
// operator declared.
func Parse(data []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &ValidationError{Problems: []Problem{{Field: "file", Message: "empty; expected version: 1 and a hosts list"}}}
		}
		return nil, &ValidationError{Problems: []Problem{{Field: "file", Message: err.Error()}}}
	}
	if err := Validate(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Load reads the hosts file at path. A missing file is an empty registry, not
// an error: most installs declare no hosts.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator config path
	if errors.Is(err, os.ErrNotExist) {
		return &File{Version: FileVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hosts config: %w", err)
	}
	return Parse(data)
}

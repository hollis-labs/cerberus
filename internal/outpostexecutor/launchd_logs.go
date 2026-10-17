package outpostexecutor

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cerberus/pkg/outpost"
)

// Log files come from the operator's launchd definition, never request paths.
func launchdLogs(r outpost.Request) (outpost.Result, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return outpost.Result{}, err
	}
	f, err := os.Open(filepath.Join(home, "Library", "LaunchAgents", r.Unit+".plist")) // #nosec G304 -- unit identifiers are validated before this call.
	if err != nil {
		return outpost.Result{}, err
	}
	defer func() { _ = f.Close() }()
	d := xml.NewDecoder(io.LimitReader(f, 1<<20))
	paths := []string{}
	key := ""
	for {
		token, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return outpost.Result{}, err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			if err := d.DecodeElement(&key, &start); err != nil {
				return outpost.Result{}, err
			}
		case "string":
			var value string
			if err := d.DecodeElement(&value, &start); err != nil {
				return outpost.Result{}, err
			}
			if (key == "StandardOutPath" || key == "StandardErrorPath") && filepath.IsAbs(value) {
				if len(paths) == 0 || paths[0] != value {
					paths = append(paths, value)
				}
			}
			key = ""
		}
	}
	if len(paths) == 0 {
		return outpost.Result{}, fmt.Errorf("unit declares no stdout/stderr log paths")
	}
	lines := r.Lines
	if lines == 0 {
		lines = 100
	}
	out := &boundedOutput{}
	for _, path := range paths {
		b, truncated, err := tail(path, lines)
		if err != nil {
			return outpost.Result{}, err
		}
		out.truncated = out.truncated || truncated
		_, _ = out.Write(b)
		_, _ = out.Write([]byte("\n"))
	}
	return outpost.Result{Output: out.String(), Truncated: out.truncated}, nil
}

func tail(path string, lines int) ([]byte, bool, error) {
	f, err := os.Open(path) // #nosec G304 -- paths come from the operator-owned launchd definition.
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("unit log is not a regular file")
	}
	start := info.Size() - (256 << 10)
	if start < 0 {
		start = 0
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return nil, false, err
	}
	b, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		return nil, false, err
	}
	rows := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	truncated := start > 0 || len(rows) > lines
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	return []byte(strings.Join(rows, "\n")), truncated, nil
}

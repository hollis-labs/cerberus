package outpostexecutor

import (
	"bytes"
	"context"
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
func launchdLogs(ctx context.Context, r outpost.Request) (outpost.Result, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return outpost.Result{}, err
	}
	b, _, err := readRegular(ctx, filepath.Join(home, "Library", "LaunchAgents", r.Unit+".plist"), 1<<20, false)
	if err != nil {
		return outpost.Result{}, err
	}
	d := xml.NewDecoder(contextReader{ctx, bytes.NewReader(b)})
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
		b, truncated, err := tail(ctx, path, lines)
		if err != nil {
			return outpost.Result{}, err
		}
		out.truncated = out.truncated || truncated
		_, _ = out.Write(b)
		_, _ = out.Write([]byte("\n"))
	}
	return outpost.Result{Output: out.String(), Truncated: out.truncated}, nil
}

func tail(ctx context.Context, path string, lines int) ([]byte, bool, error) {
	b, truncated, err := readRegular(ctx, path, 256<<10, true)
	if err != nil {
		return nil, false, err
	}
	rows := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	truncated = truncated || len(rows) > lines
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	return []byte(strings.Join(rows, "\n")), truncated, nil
}

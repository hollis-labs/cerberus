package hosts

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// History defaults: one file up to 1 MiB, then rotated, keeping this many
// older files.
const (
	DefaultHistoryMax  = 1 << 20
	DefaultHistoryKeep = 3
	HistoryFileName    = "hosts.history.jsonl"
)

// Change is one entry of the append-only change history: who changed what,
// and only the fields that changed, redacted.
type Change struct {
	Time   time.Time `json:"time"`
	Caller string    `json:"caller"`
	Op     string    `json:"op"`
	// Kind is host or component; Name its name or id.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Hosts lists every host the change touches, so a host's history also
	// shows parts moved on or off it.
	Hosts          []string       `json:"hosts,omitempty"`
	Before         map[string]any `json:"before,omitempty"`
	After          map[string]any `json:"after,omitempty"`
	RevisionBefore string         `json:"revision_before"`
	RevisionAfter  string         `json:"revision_after"`
	OperationID    string         `json:"operation_id,omitempty"`
}

// History is the change log kept beside hosts.yaml.
type History struct {
	Path string
	Max  int64
	Keep int
}

// HistoryFor is the history beside a hosts file.
func HistoryFor(hostsPath string) History {
	return History{Path: filepath.Join(filepath.Dir(hostsPath), HistoryFileName)}
}

// Append adds an entry, rotating first when the file would pass its cap. The
// file is 0600.
func (h History) Append(c Change) error {
	max, keep := h.Max, h.Keep
	if max <= 0 {
		max = DefaultHistoryMax
	}
	if keep <= 0 {
		keep = DefaultHistoryKeep
	}
	line, err := json.Marshal(c)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if err := os.MkdirAll(filepath.Dir(h.Path), 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(h.Path); err == nil && st.Size()+int64(len(line)) > max {
		_ = os.Remove(fmt.Sprintf("%s.%d", h.Path, keep))
		for i := keep - 1; i >= 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", h.Path, i), fmt.Sprintf("%s.%d", h.Path, i+1))
		}
		if err := os.Rename(h.Path, h.Path+".1"); err != nil {
			return fmt.Errorf("rotate history: %w", err)
		}
	}
	f, err := os.OpenFile(h.Path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // beside the operator's config
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	serr := f.Sync()
	return errors.Join(werr, serr, f.Close())
}

// Read returns entries newest first for which keep is true, at most limit
// (0 means 100), reading the current file and then the rotated ones.
func (h History) Read(keep func(Change) bool, limit int) ([]Change, error) {
	if limit <= 0 {
		limit = 100
	}
	files := []string{h.Path}
	rotated, _ := filepath.Glob(h.Path + ".*")
	sort.Strings(rotated)
	files = append(files, rotated...)
	var out []Change
	for _, path := range files {
		data, err := os.ReadFile(path) //nolint:gosec // beside the operator's config
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var batch []Change
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			var c Change
			if json.Unmarshal(sc.Bytes(), &c) == nil && keep(c) {
				batch = append(batch, c)
			}
		}
		for i := len(batch) - 1; i >= 0 && len(out) < limit; i-- {
			out = append(out, batch[i])
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Diff returns only the leaf fields whose value differs between before and
// after (dotted paths; a list is one leaf), with strings redacted. A nil
// before or after (a record created or removed) lists every field of the
// other side.
func Diff(before, after any) (b, a map[string]any, err error) {
	flat := func(v any) (map[string]any, error) {
		if v == nil || (reflect.ValueOf(v).Kind() == reflect.Pointer && reflect.ValueOf(v).IsNil()) {
			return map[string]any{}, nil
		}
		m, err := toMap(v)
		if err != nil {
			return nil, err
		}
		out := map[string]any{}
		flatten("", m, out)
		return out, nil
	}
	fb, err := flat(before)
	if err != nil {
		return nil, nil, err
	}
	fa, err := flat(after)
	if err != nil {
		return nil, nil, err
	}
	b, a = map[string]any{}, map[string]any{}
	for k, v := range fb {
		if !reflect.DeepEqual(v, fa[k]) {
			b[k] = scrub(v)
		}
	}
	for k, v := range fa {
		if !reflect.DeepEqual(v, fb[k]) {
			a[k] = scrub(v)
		}
	}
	return b, a, nil
}

func flatten(prefix string, m map[string]any, out map[string]any) {
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			flatten(path, sub, out)
			continue
		}
		out[path] = v
	}
}

func scrub(v any) any {
	switch t := v.(type) {
	case string:
		return redact.Text(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = scrub(x)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = scrub(x)
		}
		return out
	}
	return v
}

// Touches reports whether c is part of the history of the named record.
func (c Change) Touches(kind, name string) bool {
	if c.Kind == kind && c.Name == name {
		return true
	}
	if kind == "host" {
		for _, h := range c.Hosts {
			if h == name {
				return true
			}
		}
	}
	return false
}

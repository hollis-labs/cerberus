package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Check takes the tree as it is now and compares: every file's
// sha256 (a file that appeared where the manifest recorded none is a mismatch, as is a new
// paused.<id> or receipt), the reservation directories, the anchor's head, and main.db
// logically. It returns the mismatches, first the files in path order; none means the tree
// matches. The database is opened read-only (o.Live: through its WAL, never checkpointing).
func Check(ctx context.Context, m *Manifest, o Options) ([]string, error) {
	if o.AnchorAccount == "" && m.Anchor.Account != "" {
		o.AnchorAccount = m.Anchor.Account
	}
	now, err := Take(ctx, o)
	if err != nil {
		return nil, err
	}
	return Compare(m, now), nil
}

// Compare lists how now differs from m.
func Compare(m, now *Manifest) []string {
	var out []string
	paths := map[string]bool{}
	for p := range m.Files {
		paths[p] = true
	}
	for p := range now.Files {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	for _, p := range sorted {
		was, inM := m.Files[p]
		is, inNow := now.Files[p]
		switch {
		case !inM && is.Absent:
		case !inM:
			out = append(out, fmt.Sprintf("%s: present (%s), the manifest has no such file", p, is.SHA256))
		case !inNow && was.Absent:
		case !inNow:
			out = append(out, fmt.Sprintf("%s: gone, the manifest has %s", p, was.SHA256))
		case was.Absent && !is.Absent:
			out = append(out, fmt.Sprintf("%s: present (%s), the manifest recorded it absent", p, is.SHA256))
		case !was.Absent && is.Absent:
			out = append(out, fmt.Sprintf("%s: gone, the manifest has %s", p, was.SHA256))
		case was.SHA256 != is.SHA256 || was.Size != is.Size:
			out = append(out, fmt.Sprintf("%s: %s (%d bytes), the manifest has %s (%d bytes)", p, is.SHA256, is.Size, was.SHA256, was.Size))
		}
	}
	if fmt.Sprint(m.Dirs) != fmt.Sprint(now.Dirs) {
		out = append(out, fmt.Sprintf("credential-issuance reservations: %v, the manifest has %v", now.Dirs, m.Dirs))
	}
	if !sameAnchor(m.Anchor, now.Anchor) {
		out = append(out, fmt.Sprintf("the audit anchor for %s: %s, the manifest has %s", now.Anchor.Account, anchorString(now.Anchor), anchorString(m.Anchor)))
	}
	if d := m.DB.Diff(now.DB); d != "" {
		out = append(out, "main.db: "+d)
	}
	return out
}

func sameAnchor(a, b Anchor) bool {
	if a.Absent || b.Absent {
		return a.Absent == b.Absent
	}
	var x, y any
	if json.Unmarshal(a.Head, &x) != nil || json.Unmarshal(b.Head, &y) != nil {
		return bytes.Equal(a.Head, b.Head)
	}
	return fmt.Sprint(x) == fmt.Sprint(y)
}

func anchorString(a Anchor) string {
	if a.Absent {
		return "absent"
	}
	var buf bytes.Buffer
	if json.Compact(&buf, a.Head) != nil {
		return string(a.Head)
	}
	return buf.String()
}

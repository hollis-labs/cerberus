package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hollis-labs/cerberus/internal/legacyimport/hostdump"
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
	diffs := Diff(m, now)
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.Detail)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// The keys of the mismatches that are not a file.
const (
	KeyDirs   = "<credential-issuance reservations>"
	KeyAnchor = "<audit anchor>"
	KeyDB     = "<main.db>"
)

// Mismatch is one way now differs from a manifest: Key is the file's path relative to the old
// home, or KeyDirs, KeyAnchor or KeyDB; Detail says how.
type Mismatch struct {
	Key    string
	Detail string
}

// Diff is Compare keyed: the files in path order, then the reservations, the anchor and main.db.
func Diff(m, now *Manifest) []Mismatch {
	var out []Mismatch
	add := func(key, format string, args ...any) {
		out = append(out, Mismatch{Key: key, Detail: fmt.Sprintf(format, args...)})
	}
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
			add(p, "%s: present (%s), the manifest has no such file", p, is.SHA256)
		case !inNow && was.Absent:
		case !inNow:
			add(p, "%s: gone, the manifest has %s", p, was.SHA256)
		case was.Absent && !is.Absent:
			add(p, "%s: present (%s), the manifest recorded it absent", p, is.SHA256)
		case !was.Absent && is.Absent:
			add(p, "%s: gone, the manifest has %s", p, was.SHA256)
		case was.SHA256 != is.SHA256 || was.Size != is.Size:
			add(p, "%s: %s (%d bytes), the manifest has %s (%d bytes)", p, is.SHA256, is.Size, was.SHA256, was.Size)
		}
	}
	if fmt.Sprint(m.Dirs) != fmt.Sprint(now.Dirs) {
		add(KeyDirs, "credential-issuance reservations: %v, the manifest has %v", now.Dirs, m.Dirs)
	}
	if !sameAnchor(m.Anchor, now.Anchor) {
		add(KeyAnchor, "the audit anchor for %s: %s, the manifest has %s", now.Anchor.Account, anchorString(now.Anchor), anchorString(m.Anchor))
	}
	if d := m.DB.Diff(now.DB); d != "" {
		add(KeyDB, "main.db: %s", d)
	}
	return out
}

// Overrides are the post-write state export-legacy recorded for the steps it has done: each
// replaces the manifest's value for what it names. A nil field keeps the manifest's.
type Overrides struct {
	// Files are files by path relative to the old home (Absent for one a step removed).
	Files map[string]File
	// Dirs, when non-nil, are the reservation directories.
	Dirs []string
	// Anchor, when set, is the anchor's head.
	Anchor *Anchor
	// DB, when set, is main.db's logical state.
	DB *hostdump.State
}

// Expect is the manifest the old tree should match once the overridden steps are done: a clone of
// m with Files, Dirs, Anchor and DB replaced where o says. m is not changed. Compare (or Diff) it
// against Take of the tree as it is.
func Expect(m *Manifest, o Overrides) *Manifest {
	x := *m
	x.Files = make(map[string]File, len(m.Files)+len(o.Files))
	for p, f := range m.Files {
		x.Files[p] = f
	}
	for p, f := range o.Files {
		x.Files[p] = f
	}
	x.Dirs = append([]string{}, m.Dirs...)
	if o.Dirs != nil {
		x.Dirs = append([]string{}, o.Dirs...)
		sort.Strings(x.Dirs)
	}
	x.Anchor = m.Anchor
	x.Anchor.Head = append(json.RawMessage(nil), m.Anchor.Head...)
	if o.Anchor != nil {
		x.Anchor = *o.Anchor
		x.Anchor.Head = append(json.RawMessage(nil), o.Anchor.Head...)
	}
	x.DB = cloneState(m.DB)
	if o.DB != nil {
		x.DB = cloneState(*o.DB)
	}
	x.Excluded = append([]string(nil), m.Excluded...)
	return &x
}

func cloneState(s hostdump.State) hostdump.State {
	out := s
	out.Tables = make(map[string]hostdump.TableState, len(s.Tables))
	for k, v := range s.Tables {
		if v.MaxTime != nil {
			t := *v.MaxTime
			v.MaxTime = &t
		}
		out.Tables[k] = v
	}
	if s.HostEventsMaxID != nil {
		id := *s.HostEventsMaxID
		out.HostEventsMaxID = &id
	}
	out.SQLiteSequence = make(map[string]int64, len(s.SQLiteSequence))
	for k, v := range s.SQLiteSequence {
		out.SQLiteSequence[k] = v
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

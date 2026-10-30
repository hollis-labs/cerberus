package hosts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const editBase = partsBase + `      storage_slots:
        - {id: s0, kind: sata_25, occupant_component: d1}
        - {id: s1, kind: sata_25}
  - name: box-b
    role: worker
components:
  - {id: d1, kind: disk, size: 1TB, type: hdd, location: {host: box-a, slot: s0}}
  - {id: r1, kind: ram, size: 8GiB, type: DDR4, location: {host: box-a, slot: dimm1}}
`

func newEditor(t *testing.T) (*Editor, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts.yaml")
	if err := os.WriteFile(path, []byte(editBase), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Editor{Path: path}, path
}

func TestApplyWritesValidatesAndBacksUp(t *testing.T) {
	e, path := newEditor(t)
	rev, _ := e.Revision()
	res, err := e.Apply(rev, func(f *File) error {
		h, err := PatchHost(f.Hosts[0], map[string]any{"labels": map[string]any{"zone": "a"}, "hardware": map[string]any{"memory": map[string]any{"total": "16GiB"}}})
		f.Hosts[0] = h
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Backup == "" || res.RevisionAfter == rev {
		t.Fatalf("result: %+v", res)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	f, err := Load(path)
	if err != nil || f.Hosts[0].Labels["zone"] != "a" || f.Hosts[0].Hardware.Memory.Total != 16<<30 {
		t.Fatalf("reloaded: %+v %v", f.Hosts[0], err)
	}
	if want, _ := e.Revision(); want != res.RevisionAfter {
		t.Fatalf("revision after %s, file %s", res.RevisionAfter, want)
	}
	// The sizes stay sizes, not floats.
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "e+") {
		t.Fatalf("float in output:\n%s", data)
	}
}

func TestInvalidResultLeavesFileByteIdentical(t *testing.T) {
	e, path := newEditor(t)
	before, _ := os.ReadFile(path)
	rev, _ := e.Revision()
	for name, mutate := range map[string]func(*File) error{
		"bad role":          func(f *File) error { f.Hosts[0].Role = "wizard"; return nil },
		"dangling host":     func(f *File) error { return f.RemoveHost("box-a") },
		"mutate error":      func(f *File) error { return errors.New("nope") },
		"component missing": func(f *File) error { f.Components[0].Location.Host = "ghost"; return nil },
	} {
		if _, err := e.Apply(rev, mutate); err == nil {
			t.Fatalf("%s: accepted", name)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(before) {
			t.Fatalf("%s: file changed", name)
		}
	}
	matches, _ := filepath.Glob(path + ".bak-*")
	tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".hosts.yaml.tmp-*"))
	if len(matches) != 0 || len(tmps) != 0 {
		t.Fatalf("a refused write left files: %v %v", matches, tmps)
	}
}

func TestRevisionIsRequiredAndChecked(t *testing.T) {
	e, _ := newEditor(t)
	if _, err := e.Apply("", func(*File) error { return nil }); !errors.As(err, new(RevisionRequiredError)) {
		t.Fatalf("no revision: %v", err)
	}
	rev, _ := e.Revision()
	touch := func(f *File) error { f.Hosts[1].Owner = "ops"; return nil }
	if _, err := e.Apply(rev, touch); err != nil {
		t.Fatal(err)
	}
	var c *RevisionConflictError
	if _, err := e.Apply(rev, touch); !errors.As(err, &c) {
		t.Fatalf("stale revision: %v", err)
	}
}

func TestConcurrentWritersNeverClobber(t *testing.T) {
	e, path := newEditor(t)
	const n = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, conflict := 0, 0
	rev, _ := e.Revision()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := e.Apply(rev, func(f *File) error { f.Hosts[1].Labels = map[string]string{"writer": fmt.Sprint(i)}; return nil })
			mu.Lock()
			defer mu.Unlock()
			var c *RevisionConflictError
			switch {
			case err == nil:
				ok++
			case errors.As(err, &c):
				conflict++
			default:
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 1 || conflict != n-1 {
		t.Fatalf("with one shared revision exactly one writer wins: ok=%d conflict=%d", ok, conflict)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("file left invalid: %v", err)
	}
	// Writers that each re-read get serialized, and none is lost.
	var wg2 sync.WaitGroup
	for i := 0; i < n; i++ {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			for {
				r, _ := e.Revision()
				_, err := e.Apply(r, func(f *File) error {
					f.Components = append(f.Components, Component{ID: fmt.Sprintf("spare-%02d", i), Kind: "other", Location: ComponentLocation{Spare: true}})
					return nil
				})
				var c *RevisionConflictError
				if !errors.As(err, &c) {
					if err != nil {
						t.Errorf("writer %d: %v", i, err)
					}
					return
				}
			}
		}(i)
	}
	wg2.Wait()
	f, _ := Load(path)
	if len(f.Components) != 2+n {
		t.Fatalf("components %d, want %d (a write was lost)", len(f.Components), 2+n)
	}
}

func TestBackupsArePrunedToTwenty(t *testing.T) {
	e, path := newEditor(t)
	tick := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { tick = tick.Add(time.Second); return tick }
	for i := 0; i < MaxBackups+5; i++ {
		rev, _ := e.Revision()
		if _, err := e.Apply(rev, func(f *File) error { f.Hosts[1].Owner = fmt.Sprint("o", i); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	matches, _ := filepath.Glob(path + ".bak-*")
	if len(matches) != MaxBackups {
		t.Fatalf("%d backups, want %d", len(matches), MaxBackups)
	}
}

func TestMoveComponentKeepsSlotsInStep(t *testing.T) {
	f := parseOK(t, editBase)
	if err := f.MoveComponent("d1", ComponentLocation{Spare: true}); err != nil {
		t.Fatal(err)
	}
	if f.Hosts[0].Hardware.StorageSlots[0].OccupantComponent != "" {
		t.Fatal("old slot not vacated")
	}
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
	if err := f.MoveComponent("d1", ComponentLocation{Host: "box-a", Slot: "s1"}); err != nil {
		t.Fatal(err)
	}
	if f.Hosts[0].Hardware.StorageSlots[1].OccupantComponent != "d1" {
		t.Fatal("new slot not filled")
	}
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
	// Taken slot refused.
	f.Components = append(f.Components, Component{ID: "d2", Kind: "disk", Location: ComponentLocation{Spare: true}})
	if err := f.MoveComponent("d2", ComponentLocation{Host: "box-a", Slot: "s1"}); err == nil || !strings.Contains(err.Error(), `already holds component "d1"`) {
		t.Fatalf("taken slot: %v", err)
	}
	if err := f.RemoveComponent("d1"); err != nil || f.Hosts[0].Hardware.StorageSlots[1].OccupantComponent != "" {
		t.Fatalf("remove: %v", err)
	}
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveHostRefusedWhileComponentsRemain(t *testing.T) {
	f := parseOK(t, editBase)
	if err := f.RemoveHost("box-a"); err == nil || !strings.Contains(err.Error(), "d1, r1") {
		t.Fatalf("remove: %v", err)
	}
	if err := f.RemoveHost("box-b"); err != nil {
		t.Fatal(err)
	}
}

func TestPatchRejectsRenameUnknownKeysAndSecrets(t *testing.T) {
	f := parseOK(t, editBase)
	if _, err := PatchHost(f.Hosts[0], map[string]any{"name": "other"}); err == nil {
		t.Fatal("rename accepted")
	}
	if _, err := PatchHost(f.Hosts[0], map[string]any{"colour": "red"}); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, err := PatchComponent(f.Components[0], map[string]any{"location": map[string]any{"spare": true}}); err == nil {
		t.Fatal("location patched")
	}
	h, err := PatchHost(f.Hosts[0], map[string]any{"labels": map[string]any{"a": "1"}, "owner": nil})
	if err != nil || h.Labels["a"] != "1" {
		t.Fatalf("%+v %v", h, err)
	}
	if p := FindSecret("", map[string]any{"notes": "token=ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD"}); p != "notes" {
		t.Fatalf("secret not found: %q", p)
	}
	if p := FindSecret("", map[string]any{"notes": "a 1 TB spare HDD bay"}); p != "" {
		t.Fatalf("false positive: %q", p)
	}
}

func TestHistoryDiffAppendRotateRead(t *testing.T) {
	before := Component{ID: "r1", Kind: "ram", Size: 8 << 30, Location: ComponentLocation{Host: "box-a", Slot: "dimm1"}}
	after := before
	after.Size = 16 << 30
	b, a, err := Diff(before, after)
	if err != nil || len(b) != 1 || b["size"] != 8<<30 || a["size"] != 16<<30 {
		t.Fatalf("diff: %v %v %v", b, a, err)
	}
	h := History{Path: filepath.Join(t.TempDir(), HistoryFileName), Max: 600, Keep: 2}
	for i := 0; i < 12; i++ {
		c := Change{Time: time.Unix(int64(1000+i), 0).UTC(), Caller: "t", Op: "component_update", Kind: "component", Name: "r1", Hosts: []string{"box-a"}, Before: b, After: a}
		if i%2 == 1 {
			c.Name = "other"
		}
		if err := h.Append(c); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{h.Path, h.Path + ".1", h.Path + ".2"} {
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o600 || st.Size() > 700 {
			t.Fatalf("%s: %v %v", p, st, err)
		}
	}
	if _, err := os.Stat(h.Path + ".3"); err == nil {
		t.Fatal("kept more rotated files than Keep")
	}
	got, err := h.Read(func(c Change) bool { return c.Touches("component", "r1") }, 100)
	if err != nil || len(got) == 0 {
		t.Fatalf("read: %v %v", got, err)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Time.After(got[i-1].Time) {
			t.Fatal("not newest first")
		}
	}
	if hostSide, _ := h.Read(func(c Change) bool { return c.Touches("host", "box-a") }, 3); len(hostSide) != 3 {
		t.Fatalf("host history must include component changes on the host: %d", len(hostSide))
	}
}

func TestAFileKeepsTheFormatItWasFoundIn(t *testing.T) {
	// The fleet inventory script reads and writes hosts.yaml as JSON.
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.yaml")
	js := `{"version": 1, "hosts": [{"name": "box-a", "role": "worker", "created_at": "2026-10-11T00:00:00Z", "hardware": {"memory": {"total": 8589934592}}}]}`
	if err := os.WriteFile(path, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &Editor{Path: path}
	rev, _ := e.Revision()
	if _, err := e.Apply(rev, func(f *File) error { f.Hosts[0].Owner = "ops"; return nil }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("a JSON-style file must stay JSON:\n%s\n%v", data, err)
	}
	host := back["hosts"].([]any)[0].(map[string]any)
	if host["owner"] != "ops" || host["created_at"] != "2026-10-11T00:00:00Z" || host["hardware"].(map[string]any)["memory"].(map[string]any)["total"] != float64(8<<30) {
		t.Fatalf("round trip lost data: %v", host)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	// A YAML file stays YAML.
	e2, p2 := newEditor(t)
	r2, _ := e2.Revision()
	if _, err := e2.Apply(r2, func(f *File) error { f.Hosts[1].Owner = "ops"; return nil }); err != nil {
		t.Fatal(err)
	}
	if d, _ := os.ReadFile(p2); json.Valid(d) {
		t.Fatalf("a YAML file turned into JSON:\n%s", d)
	}
}

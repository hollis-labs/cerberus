package legacyexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
	"github.com/hollis-labs/cerberus/internal/legacyimport/oldtree"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// pauseContent and pauseMode are the old pause writer's bytes and mode
// (origin/main:internal/pausectl/pausectl.go: os.WriteFile(path, []byte("paused\n"), 0644)).
const (
	pauseContent = "paused\n"
	pauseMode    = 0o644
)

// planPause is R10: the old markers become new's pause set, as a set: a marker new has and old
// lacks is created with the old writer's bytes, a marker old has and new lacks is removed (a
// resume under new is reversed too), and a marker both have is left as it is.
func (x *export) planPause(ctx context.Context) (*plan, error) {
	r := x.r
	targets, err := storage.PausedTargets(ctx, x.local)
	if err != nil {
		return nil, err
	}
	pr := &PauseReport{Targets: append([]string{}, targets...), Created: []string{}, Removed: []string{}}
	r.Pause = pr
	pl := &plan{post: Post{Files: map[string]manifest.File{}}}
	want := map[string]bool{}
	created := manifest.File{SHA256: oldtree.BytesSHA256([]byte(pauseContent)), Size: int64(len(pauseContent))}
	for _, t := range targets {
		if t == "" || strings.ContainsAny(t, "/\x00") {
			r.refuse("pause target %q cannot be an old pause marker (empty, or it names a path)", t)
			continue
		}
		name := oldtree.PauseFile(t)
		want[name] = true
		if f := x.manifestFile(name); !f.Absent {
			pl.post.Files[name] = f
			continue
		}
		pl.post.Files[name] = created
		pr.Created = append(pr.Created, name)
	}
	for _, rel := range sortedKeys(x.m.Files) {
		f := x.m.Files[rel]
		if (rel == "paused" || strings.HasPrefix(rel, "paused.")) && !f.Absent && !want[rel] {
			pl.post.Files[rel] = manifest.File{Absent: true}
			pr.Removed = append(pr.Removed, rel)
		}
	}
	pl.summary = fmt.Sprintf("pause set %v; created %v, removed %v", targets, pr.Created, pr.Removed)
	pl.apply = func(context.Context) error {
		for _, rel := range sortedKeys(pl.post.Files) {
			post := pl.post.Files[rel]
			p := filepath.Join(x.o.OldHome, rel)
			cur, err := fileState(p)
			if err != nil {
				return err
			}
			switch {
			case cur == post:
			case post.Absent:
				if cur != x.manifestFile(rel) {
					return fmt.Errorf("old's %s changed; it is not the marker the manifest recorded", rel)
				}
				if err := os.Remove(p); err != nil {
					return err
				}
			case !cur.Absent:
				return fmt.Errorf("old's %s is neither absent nor the old writer's marker", rel)
			default:
				f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, pauseMode) //nolint:gosec // the old pause marker, the old writer's mode
				if err != nil {
					return err
				}
				_, werr := f.WriteString(pauseContent)
				if err = errors.Join(werr, f.Sync(), f.Close()); err != nil {
					return err
				}
			}
		}
		return syncDir(x.o.OldHome)
	}
	pl.verify = func(context.Context) error {
		var p oldtree.Problems
		markers := oldtree.ReadPause(x.o.OldHome, &p)
		if len(p.Refusals) > 0 {
			return fmt.Errorf("old's pause markers: %s", strings.Join(p.Refusals, "; "))
		}
		got := make([]string, 0, len(markers))
		for _, mk := range markers {
			got = append(got, mk.Target)
		}
		want := append([]string{}, targets...)
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			return fmt.Errorf("old's pause markers are %v, new's pause set %v", got, want)
		}
		return nil
	}
	return pl, nil
}

// planApplied is R9: old's runtime/applied.json becomes its own map overlaid with new's applied
// digests (neither side ever deletes one), written as the old writer wrote it, and only when that
// adds or changes a digest. new's digests must be keyed with old's key.
func (x *export) planApplied(ctx context.Context) (*plan, error) {
	r := x.r
	ar := &AppliedReport{Added: []string{}, Changed: []string{}}
	r.Applied = ar
	empty := &plan{apply: noop, verify: noop, summary: "no applied digests on either side"}
	fp, hasKey, err := storage.AppliedKey(ctx, x.local)
	if err != nil {
		return nil, err
	}
	all, err := storage.AppliedAll(ctx, x.local)
	if err != nil {
		return nil, err
	}
	digester, err := audit.OpenExistingDigester(x.oldAudit)
	if err != nil {
		r.refuse("old's audit digest key: %v", err)
		return empty, nil
	}
	ar.KeyFingerprint = digester.Fingerprint()
	if (hasKey || len(all) > 0) && fp != digester.Fingerprint() {
		r.refuse("new's applied digests are keyed with %q, not old's audit/.digest_key (%s): old could not compare them", fp, digester.Fingerprint())
		return empty, nil
	}
	var p oldtree.Problems
	cur := oldtree.ReadApplied(x.o.OldHome, &p)
	for _, x2 := range p.Refusals {
		r.refuse("old's %s", x2)
	}
	if len(p.Refusals) > 0 {
		return empty, nil
	}
	merged := map[string]string{}
	for id, d := range cur.Digests {
		merged[id] = d
	}
	for _, id := range sortedKeys(all) {
		old, ok := merged[id]
		switch {
		case !ok:
			ar.Added = append(ar.Added, id)
		case old != all[id]:
			ar.Changed = append(ar.Changed, id)
		}
		merged[id] = all[id]
	}
	ar.Count = len(merged)
	dst := filepath.Join(x.o.OldHome, filepath.FromSlash(oldtree.AppliedRel))
	if len(ar.Added)+len(ar.Changed) == 0 {
		if !cur.Present {
			return empty, nil
		}
		now, ferr := fileState(dst)
		if ferr != nil {
			return nil, ferr
		}
		ar.SHA256 = now.SHA256
		return &plan{post: Post{Files: map[string]manifest.File{oldtree.AppliedRel: now}}, apply: noop, verify: noop,
			summary: fmt.Sprintf("%d digests; new added or changed none, the file is left as it is", ar.Count)}, nil
	}
	data, err := renderApplied(merged)
	if err != nil {
		return nil, err
	}
	post := manifest.File{SHA256: oldtree.BytesSHA256(data), Size: int64(len(data))}
	ar.SHA256 = post.SHA256
	pl := &plan{post: Post{Files: map[string]manifest.File{oldtree.AppliedRel: post}}}
	pl.summary = fmt.Sprintf("%d digests; added %v, changed %v", ar.Count, ar.Added, ar.Changed)
	pl.apply = func(context.Context) error {
		now, err := fileState(dst)
		if err != nil {
			return err
		}
		if now == post {
			return nil
		}
		if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err = replaceFile(dst, data, 0o600); err != nil {
			return err
		}
		ar.Written = true
		return nil
	}
	pl.verify = func(context.Context) error {
		var p oldtree.Problems
		got := oldtree.ReadApplied(x.o.OldHome, &p)
		if len(p.Refusals) > 0 || !reflect.DeepEqual(got.Digests, merged) {
			return fmt.Errorf("old's %s does not hold the merged digests (%s)", oldtree.AppliedRel, strings.Join(p.Refusals, "; "))
		}
		if now, err := fileState(dst); err != nil || now != post {
			return errors.Join(fmt.Errorf("old's %s is not the bytes written", oldtree.AppliedRel), err)
		}
		return nil
	}
	return pl, nil
}

// renderApplied is the old writer's rendering (origin/main:internal/cerbapi/applied_definitions.go:
// json.MarshalIndent(digests, "", "  "), no trailing newline).
func renderApplied(digests map[string]string) ([]byte, error) {
	return json.MarshalIndent(digests, "", "  ")
}

// renderReceipt is the old broker's rendering of a receipt file
// (origin/main:internal/credentialbroker/broker.go: json.Marshal(Receipt), no trailing newline):
// id, connector, operation, binding_digest, state, references ({} when empty), at in UTC.
func renderReceipt(rc storage.Receipt) ([]byte, error) {
	refs := rc.References
	if refs == nil {
		refs = map[string]string{}
	}
	return json.Marshal(oldtree.Receipt{ID: rc.IssuanceID, Connector: rc.Connector, Operation: rc.Operation, BindingDigest: rc.BindingDigest,
		State: rc.State, References: refs, At: rc.At.UTC()})
}

// planReceipts is R11: every receipt new recorded that has no file in the manifest becomes the
// old broker's create-once file (its reservation directory created 0700 when the id is new).
// After writing, the old tree parses back to exactly new's rows.
func (x *export) planReceipts(ctx context.Context) (*plan, error) {
	r := x.r
	rows, err := storage.ReceiptsAll(ctx, x.local)
	if err != nil {
		return nil, err
	}
	rr := &ReceiptsReport{Rows: len(rows), Written: []string{}, NewDirs: []string{}}
	r.Receipts = rr
	root := filepath.Join(x.o.OldHome, "credential-issuance")
	pl := &plan{post: Post{Files: map[string]manifest.File{}}}
	dirs := map[string]bool{}
	for _, d := range x.m.Dirs {
		dirs[d] = true
	}
	type write struct {
		rel, dir string
		data     []byte
	}
	var writes []write
	for _, rc := range rows {
		if !oldtree.IssuanceID.MatchString(rc.IssuanceID) || !oldtree.ReceiptStates[rc.State] {
			r.refuse("receipt %q %q cannot be an old receipt file (the id is not 32 lowercase hex, or the state is unknown)", rc.IssuanceID, rc.State)
			continue
		}
		dirRel := "credential-issuance/" + rc.IssuanceID
		rel := dirRel + "/" + rc.State + ".json"
		if !dirs[dirRel] {
			dirs[dirRel] = true
			rr.NewDirs = append(rr.NewDirs, dirRel)
		}
		if !x.manifestFile(rel).Absent {
			continue
		}
		data, err := renderReceipt(rc)
		if err != nil {
			return nil, err
		}
		pl.post.Files[rel] = manifest.File{SHA256: oldtree.BytesSHA256(data), Size: int64(len(data))}
		writes = append(writes, write{rel: rel, dir: dirRel, data: data})
		rr.Written = append(rr.Written, rel)
	}
	if len(rr.NewDirs) > 0 {
		pl.post.Dirs = sortedKeys(dirs)
	}
	pl.summary = fmt.Sprintf("%d rows; wrote %d files, %d new reservations", len(rows), len(writes), len(rr.NewDirs))
	pl.apply = func(context.Context) error {
		if len(writes) == 0 {
			return nil
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return err
		}
		for _, w := range writes {
			dir := filepath.Join(x.o.OldHome, filepath.FromSlash(w.dir))
			switch err := os.Mkdir(dir, 0o700); {
			case err == nil:
				if serr := syncDir(root); serr != nil {
					return serr
				}
			case !errors.Is(err, fs.ErrExist):
				return err
			}
			p := filepath.Join(x.o.OldHome, filepath.FromSlash(w.rel))
			if existing, err := os.ReadFile(p); err == nil { //nolint:gosec // a receipt file of the old tree
				if string(existing) != string(w.data) {
					return fmt.Errorf("old's %s exists and is not new's receipt", w.rel)
				}
				continue
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // as above
			if err != nil {
				return err
			}
			_, werr := f.Write(w.data)
			if err = errors.Join(werr, f.Sync(), f.Close()); err != nil {
				return err
			}
			if err = syncDir(dir); err != nil {
				return err
			}
		}
		return nil
	}
	pl.verify = func(context.Context) error {
		var p oldtree.Problems
		got := oldtree.ReadReceipts(x.o.OldHome, &p)
		if len(p.Refusals) > 0 {
			return fmt.Errorf("old's receipts do not parse: %s", strings.Join(p.Refusals, "; "))
		}
		for _, rc := range rows {
			rel := rc.IssuanceID + "/" + rc.State + ".json"
			f, ok := got.ByFile[rel]
			if !ok || !f.At.Equal(rc.At) || f.Connector != rc.Connector || f.Operation != rc.Operation || f.BindingDigest != rc.BindingDigest || !sameRefs(f.References, rc.References) {
				return fmt.Errorf("old's credential-issuance/%s does not equal new's receipt", rel)
			}
		}
		if len(got.ByFile) != len(rows) {
			return fmt.Errorf("old's tree holds %d receipt files, new %d receipts", len(got.ByFile), len(rows))
		}
		rr.Verified = len(rows)
		return nil
	}
	return pl, nil
}

func sameRefs(a, b map[string]string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// policyReport warns when new's policy (the config dir) differs from old's policy/: a rollback
// runs old's, and does not carry new's changes back (R13 is not reversed).
func (x *export) policyReport() {
	if x.o.ConfigDir == "" {
		return
	}
	newDir, oldDir := filepath.Join(x.o.ConfigDir, "policy"), filepath.Join(x.o.OldHome, "policy")
	a, errA := policyDigests(newDir)
	b, errB := policyDigests(oldDir)
	if errA != nil || errB != nil {
		return
	}
	var differ []string
	for _, k := range sortedKeys(a) {
		if b[k] != a[k] {
			differ = append(differ, k)
		}
	}
	for _, k := range sortedKeys(b) {
		if _, ok := a[k]; !ok {
			differ = append(differ, k)
		}
	}
	if len(differ) > 0 {
		x.r.warn("policy: new's %s differs from old's %s (%v); the rollback leaves both as they are, and old runs its own: re-apply on old any change that should stay", newDir, oldDir, differ)
	}
}

func policyDigests(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || strings.HasPrefix(d.Name(), "posture.yaml.bak-") {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		sum, err := oldtree.FileSHA256(p)
		out[filepath.ToSlash(rel)] = sum
		return err
	})
	return out, err
}

// stateFiles are the old build's state/ files the manifest covers (fixedFiles), which the new
// build keeps in <data>/state and the rollback copies back whole.
var stateFiles = []string{"state/hosts.json", "state/overview_snapshots.json"}

// planState copies new's state/hosts.json and state/overview_snapshots.json back whole, as the
// old writers write them (a temporary file renamed into place, 0600; origin/main's
// snapshot_recorder.go and hosts/state.go). A file new does not have leaves old's as it is.
func (x *export) planState(context.Context) (*plan, error) {
	sr := &StateFilesReport{Copied: map[string]string{}, Kept: []string{}}
	x.r.State = sr
	pl := &plan{post: Post{Files: map[string]manifest.File{}}}
	type copyFile struct {
		rel, src, dst string
		post          manifest.File
	}
	var copies []copyFile
	for _, rel := range stateFiles {
		src := filepath.Join(x.o.DataDir, filepath.FromSlash(rel))
		post, err := fileState(src)
		if err != nil {
			return nil, err
		}
		if post.Absent {
			sr.Kept = append(sr.Kept, rel)
			continue
		}
		pl.post.Files[rel] = post
		sr.Copied[rel] = post.SHA256
		copies = append(copies, copyFile{rel: rel, src: src, dst: filepath.Join(x.o.OldHome, filepath.FromSlash(rel)), post: post})
	}
	pl.summary = fmt.Sprintf("copied %v whole; kept old's %v", sortedKeys(sr.Copied), sr.Kept)
	pl.apply = func(context.Context) error {
		for _, c := range copies {
			cur, err := fileState(c.dst)
			if err != nil {
				return err
			}
			if cur == c.post {
				continue
			}
			if cur != x.manifestFile(c.rel) {
				return fmt.Errorf("old's %s is neither in its manifest state nor in its post state", c.rel)
			}
			data, err := os.ReadFile(c.src) //nolint:gosec // new's own state file
			if err != nil {
				return err
			}
			if oldtree.BytesSHA256(data) != c.post.SHA256 {
				return fmt.Errorf("new's %s changed during the run; stop the new build and run again", c.rel)
			}
			if err = os.MkdirAll(filepath.Dir(c.dst), 0o700); err != nil {
				return err
			}
			if err = replaceFile(c.dst, data, 0o600); err != nil {
				return err
			}
		}
		return nil
	}
	pl.verify = func(context.Context) error {
		for _, c := range copies {
			cur, err := fileState(c.dst)
			if err != nil || cur != c.post {
				return errors.Join(fmt.Errorf("old's %s is not new's after the copy", c.rel), err)
			}
			if fi, err := os.Stat(c.dst); err != nil || fi.Mode().Perm() != 0o600 {
				return errors.Join(fmt.Errorf("old's %s is not mode 0600", c.rel), err)
			}
		}
		return nil
	}
	return pl, nil
}

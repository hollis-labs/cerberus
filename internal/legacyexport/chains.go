package legacyexport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
	"github.com/hollis-labs/cerberus/internal/legacyimport/oldtree"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

// appendable is one append-only file: new's copy (src) continues the old tree's (dst) byte for
// byte from the manifest's state (base), and the old file becomes new's (post).
type appendable struct {
	rel, src, dst string
	base, post    manifest.File
}

// planAppendable proves the prefix: new's file's first base.Size bytes hash to the manifest's
// sha256. An empty refusal is a file that can be appended (or, absent in the manifest, created
// whole).
func (x *export) planAppendable(rel, src, dst string) (*appendable, string, error) {
	post, err := fileState(src)
	if err != nil {
		return nil, "", err
	}
	a := &appendable{rel: rel, src: src, dst: dst, base: x.manifestFile(rel), post: post}
	if a.base.Absent {
		return a, "", nil
	}
	if post.Size < a.base.Size {
		return a, fmt.Sprintf("new's %s is %d bytes, shorter than the %d the manifest records for old's: it does not continue old's file", rel, post.Size, a.base.Size), nil
	}
	prefix, err := oldtree.PrefixSHA256(src, a.base.Size)
	if err != nil {
		return nil, "", err
	}
	if prefix != a.base.SHA256 {
		return a, fmt.Sprintf("new's %s does not continue old's: its first %d bytes hash to %s, the manifest has %s", rel, a.base.Size, prefix, a.base.SHA256), nil
	}
	return a, "", nil
}

// suffix is how many bytes the step adds to the old file.
func (a *appendable) suffix() int64 {
	if a.base.Absent {
		return a.post.Size
	}
	return a.post.Size - a.base.Size
}

// apply brings the old file from its manifest state to its post state: the suffix appended
// (O_APPEND, fsynced), or the whole file created (O_EXCL, 0600, fsynced with its directory). A
// file already in its post state is left; one in neither state is an error. lock, when set, is
// held around the write.
func (a *appendable) apply(lock func(dst string) (func(), error)) error {
	if a.post.Absent || a.post == a.base {
		return nil
	}
	if lock != nil {
		unlock, err := lock(a.dst)
		if err != nil {
			return err
		}
		defer unlock()
	}
	cur, err := fileState(a.dst)
	if err != nil {
		return err
	}
	if cur == a.post {
		return nil
	}
	if cur != a.base {
		return fmt.Errorf("old's %s is neither in its manifest state nor in its post state (%s, %d bytes)", a.rel, cur.SHA256, cur.Size)
	}
	data, err := os.ReadFile(a.src) //nolint:gosec // new's own file
	if err != nil {
		return err
	}
	if oldtree.BytesSHA256(data) != a.post.SHA256 || int64(len(data)) != a.post.Size {
		return fmt.Errorf("new's %s changed during the run; stop the new build and run again", a.rel)
	}
	if a.base.Absent {
		if err = os.MkdirAll(filepath.Dir(a.dst), 0o700); err != nil {
			return err
		}
		f, ferr := os.OpenFile(a.dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a file of the old tree
		if ferr != nil {
			return ferr
		}
		_, werr := f.Write(data)
		if err = errors.Join(werr, f.Sync(), f.Close()); err != nil {
			return err
		}
		if err = syncDir(filepath.Dir(a.dst)); err != nil {
			return err
		}
	} else {
		f, ferr := os.OpenFile(a.dst, os.O_WRONLY|os.O_APPEND, 0) //nolint:gosec // a file of the old tree
		if ferr != nil {
			return ferr
		}
		_, werr := f.Write(data[a.base.Size:])
		if err = errors.Join(werr, f.Sync(), f.Close()); err != nil {
			return err
		}
	}
	after, err := fileState(a.dst)
	if err != nil {
		return err
	}
	if after != a.post {
		return fmt.Errorf("old's %s is %s (%d bytes) after the write, not new's %s (%d bytes)", a.rel, after.SHA256, after.Size, a.post.SHA256, a.post.Size)
	}
	return nil
}

// flockPath takes an exclusive flock on path (created 0600 when absent), the protocol the old
// build's writers use; the returned func releases it.
func flockPath(path string, create bool) (func(), error) {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0o600) //nolint:gosec // a lock or store file of the old tree
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX) //nolint:gosec // a file descriptor fits an int
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // as above
		_ = f.Close()
	}, nil
}

// monthFiles are the audit chain's month files in dir, sorted.
func monthFiles(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9][0-9]-[0-9][0-9].jsonl"))
	if err != nil {
		return nil, err
	}
	return files, nil
}

// lastHead is the chain's newest record as a head: its seq and hash, its month file and the
// offset its line starts at (where the sink that wrote it put the anchor).
func lastHead(dir string) (audit.Head, error) {
	files, err := monthFiles(dir)
	if err != nil || len(files) == 0 {
		return audit.Head{}, errors.Join(fmt.Errorf("%s holds no audit month file", dir), err)
	}
	last := files[len(files)-1]
	data, err := os.ReadFile(last) //nolint:gosec // an audit month file
	if err != nil {
		return audit.Head{}, err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return audit.Head{}, fmt.Errorf("%s does not end in a complete record", filepath.Base(last))
	}
	body := data[:len(data)-1]
	start := bytes.LastIndexByte(body, '\n') + 1
	var rec audit.Record
	if err := json.Unmarshal(body[start:], &rec); err != nil || rec.Seq == 0 || rec.Hash == "" {
		return audit.Head{}, fmt.Errorf("the last line of %s is not a chain record", filepath.Base(last))
	}
	return audit.Head{Seq: rec.Seq, Hash: rec.Hash, File: filepath.Base(last), Offset: int64(start)}, nil
}

// planAudit is R4/R5: new's chain verifies against new's anchor; each month file new has either
// continues the old one the manifest recorded (append the suffix) or is a month the old tree
// never had (a rollover: copy it whole; new's file_start already chains it). Month files new
// pruned stay in the old tree. .digest_key is never touched and must be the same key.
func (x *export) planAudit(context.Context) (*plan, error) {
	r := x.r
	ar := &AuditReport{Appended: map[string]int64{}, Created: map[string]int64{}}
	r.Audit = ar
	checked, verdict := oldtree.CheckChain(x.newAudit, auditchain.KeychainAnchor{Dir: x.newAudit})
	ar.NewVerify, ar.Records = verdict, len(checked.Records)
	if verdict != "ok" {
		r.refuse("new's audit chain does not verify against its anchor (%s): %s", x.newAudit, verdict)
		return &plan{apply: noop, verify: noop}, nil
	}
	newKey, err := oldtree.FileSHA256(filepath.Join(x.newAudit, ".digest_key"))
	if err != nil {
		r.refuse("new's audit/.digest_key: %v", err)
		return &plan{apply: noop, verify: noop}, nil
	}
	mk := x.manifestFile("audit/.digest_key")
	ar.DigestKeySHA256Equal = !mk.Absent && mk.SHA256 == newKey
	if !ar.DigestKeySHA256Equal {
		r.refuse("new's audit/.digest_key is not the key the manifest records for old's (%s, the manifest has %s): export-legacy never touches the key, and old could not check what new keyed", newKey, mk.SHA256)
	}

	files, err := monthFiles(x.newAudit)
	if err != nil {
		return nil, err
	}
	pl := &plan{post: Post{Files: map[string]manifest.File{}}}
	var steps []*appendable
	seen := map[string]bool{}
	for _, f := range files {
		rel := "audit/" + filepath.Base(f)
		seen[rel] = true
		a, refusal, perr := x.planAppendable(rel, f, filepath.Join(x.oldAudit, filepath.Base(f)))
		if perr != nil {
			return nil, perr
		}
		if refusal != "" {
			r.refuse("%s", refusal)
			continue
		}
		pl.post.Files[rel] = a.post
		steps = append(steps, a)
		if a.base.Absent {
			ar.Created[rel] = a.suffix()
		} else if a.suffix() > 0 {
			ar.Appended[rel] = a.suffix()
		}
	}
	for _, rel := range sortedKeys(x.m.Files) {
		if f := x.m.Files[rel]; !f.Absent && !seen[rel] && filepath.Dir(rel) == "audit" && filepath.Ext(rel) == ".jsonl" {
			ar.KeptInOld = append(ar.KeptInOld, rel)
		}
	}
	head, err := lastHead(x.newAudit)
	if err != nil {
		r.refuse("new's audit chain: %v", err)
		return &plan{apply: noop, verify: noop}, nil
	}
	x.newHead = head
	ar.Head, _ = json.Marshal(head)
	pl.summary = fmt.Sprintf("appended %v, created %v; the old chain verifies to seq %d", ar.Appended, ar.Created, head.Seq)
	pl.apply = func(context.Context) error {
		if err := os.MkdirAll(x.oldAudit, 0o700); err != nil {
			return err
		}
		unlock, err := flockPath(filepath.Join(x.oldAudit, ".lock"), true)
		if err != nil {
			return err
		}
		defer unlock()
		for _, a := range steps {
			if err := a.apply(nil); err != nil {
				return err
			}
		}
		return nil
	}
	pl.verify = func(context.Context) error {
		checked, verdict := oldtree.CheckChain(x.oldAudit, oldtree.MemoryAnchor{Head: head})
		ar.OldVerify = verdict
		if verdict != "ok" {
			return fmt.Errorf("the old tree's chain does not verify to new's head (seq %d): %s", head.Seq, verdict)
		}
		if n := len(checked.Records); n == 0 || checked.Records[n-1].Seq != head.Seq || checked.Records[n-1].Hash != head.Hash {
			return fmt.Errorf("the old tree's chain does not end at new's head (seq %d)", head.Seq)
		}
		return nil
	}
	return pl, nil
}

// planAnchor moves the old anchor to new's head, after the audit step verified the appended
// chain against that head (and only then).
func (x *export) planAnchor() *plan {
	o, r := x.o, x.r
	headJSON, _ := json.Marshal(x.newHead)
	ar := &AnchorReport{Account: o.OldAnchorAccount, Head: headJSON}
	r.Anchor = ar
	pl := &plan{post: Post{Anchor: &manifest.Anchor{Account: o.OldAnchorAccount, Head: headJSON}}}
	pl.summary = fmt.Sprintf("anchor for %s at seq %d", o.OldAnchorAccount, x.newHead.Seq)
	pl.apply = func(context.Context) error {
		if !x.state.Steps[StepAudit].done() {
			return errors.New("the audit step is not done: the anchor never moves before the appended chain verifies")
		}
		if _, verdict := oldtree.CheckChain(x.oldAudit, oldtree.MemoryAnchor{Head: x.newHead}); verdict != "ok" {
			return fmt.Errorf("the anchor was not moved: the old chain does not verify to new's head: %s", verdict)
		}
		_, cur, ok, err := oldtree.ReadAnchor(o.OldAnchorAccount)
		if err == nil && ok && cur == x.newHead {
			return nil
		}
		if err = secrets.KeyringSet(auditchain.AnchorService, o.OldAnchorAccount, string(headJSON)); err != nil {
			return fmt.Errorf("write the audit anchor for %s: %w", o.OldAnchorAccount, err)
		}
		ar.Moved = true
		return nil
	}
	pl.verify = func(context.Context) error {
		_, verdict := oldtree.CheckChain(x.oldAudit, auditchain.KeychainAnchor{Dir: o.OldAnchorAccount})
		ar.Verify = verdict
		if verdict != "ok" {
			return fmt.Errorf("the old chain does not verify against the moved anchor: %s", verdict)
		}
		_, cur, ok, err := oldtree.ReadAnchor(o.OldAnchorAccount)
		if err != nil || !ok || cur != x.newHead {
			return errors.Join(fmt.Errorf("the anchor for %s does not read back as new's head", o.OldAnchorAccount), err)
		}
		return nil
	}
	return pl
}

// planApprovals is R6: new's store reads whole, its owner-session seals verify (gate's check,
// run only on a whole chain, so opening the store writes nothing), and its file continues old's.
// After the append old's store reads to the same next seq.
func (x *export) planApprovals(context.Context) (*plan, error) {
	r := x.r
	rel := "approvals/" + approval.FileName
	newDir, oldDir := filepath.Join(x.o.DataDir, "approvals"), filepath.Join(x.o.OldHome, "approvals")
	src := filepath.Join(newDir, approval.FileName)
	cr := &ChainFileReport{}
	r.Approvals = cr
	empty := &plan{apply: noop, verify: noop, summary: "no approvals store on either side"}
	if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
		if !x.manifestFile(rel).Absent {
			r.refuse("new has no %s, but old's is in the manifest: new lost its approvals store", rel)
		}
		return empty, nil
	}
	batch, err := approval.ReadFrom(newDir, approval.Cursor{}, 0)
	switch {
	case err != nil:
		r.refuse("new's %s: %v", rel, err)
		return empty, nil //nolint:nilerr // a refusal, recorded on the report
	case batch.Stop != nil:
		r.refuse("new's %s does not read whole: %v", rel, batch.Stop)
		return empty, nil //nolint:nilerr // as above
	}
	before, err := oldtree.FileSHA256(src)
	if err != nil {
		return nil, err
	}
	digester, err := audit.OpenExistingDigester(x.newAudit)
	if err != nil {
		r.refuse("new's audit digest key: %v", err)
		return empty, nil
	}
	vr, err := gate.VerifyApprovals(newDir, digester)
	if err != nil {
		return nil, err
	}
	if after, _ := oldtree.FileSHA256(src); after != before {
		return nil, errors.New("verifying new's approvals changed the store; its chain is not intact")
	}
	cr.Legacy, cr.Verified = len(vr.Legacy), len(vr.Verified)
	cr.HeadSeq, cr.HeadHash = batch.Next.Seq, batch.Next.Hash
	if err = vr.Err(); err != nil {
		r.refuse("new's approvals: %v", err)
		return empty, nil
	}
	a, refusal, err := x.planAppendable(rel, src, filepath.Join(oldDir, approval.FileName))
	if err != nil {
		return nil, err
	}
	if refusal != "" {
		r.refuse("%s", refusal)
		return empty, nil
	}
	cr.Appended, cr.Created = a.suffix(), a.base.Absent
	pl := &plan{post: Post{Files: map[string]manifest.File{rel: a.post}}}
	pl.summary = fmt.Sprintf("%d bytes appended; next seq %d", a.suffix(), batch.Next.Seq)
	pl.apply = func(context.Context) error { return a.apply(nil) }
	pl.verify = func(context.Context) error {
		got, err := approval.ReadFrom(oldDir, approval.Cursor{}, 0)
		switch {
		case err != nil:
			return err
		case got.Stop != nil:
			return fmt.Errorf("old's %s does not read whole after the append: %w", rel, got.Stop)
		case got.Next.Seq != batch.Next.Seq || got.Next.Hash != batch.Next.Hash:
			return fmt.Errorf("old's %s reads to seq %d (%s), new's to %d (%s)", rel, got.Next.Seq, got.Next.Hash, batch.Next.Seq, batch.Next.Hash)
		}
		cr.NextSeq, cr.Verify = got.Next.Seq, "ok"
		return nil
	}
	return pl, nil
}

// planBrakes is R21's brakes/: new's file continues old's; after the append old's store folds to
// exactly new's state and problems.
func (x *export) planBrakes(context.Context) (*plan, error) {
	r := x.r
	rel := "brakes/" + brake.FileName
	newDir, oldDir := filepath.Join(x.o.DataDir, "brakes"), filepath.Join(x.o.OldHome, "brakes")
	src := filepath.Join(newDir, brake.FileName)
	cr := &ChainFileReport{}
	r.Brakes = cr
	empty := &plan{apply: noop, verify: noop, summary: "no brake store on either side"}
	if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
		if !x.manifestFile(rel).Absent {
			r.refuse("new has no %s, but old's is in the manifest: new lost its brake store", rel)
		}
		return empty, nil
	}
	newState, newProblems := brake.Store{Dir: newDir}.Load()
	a, refusal, err := x.planAppendable(rel, src, filepath.Join(oldDir, brake.FileName))
	if err != nil {
		return nil, err
	}
	if refusal != "" {
		r.refuse("%s", refusal)
		return empty, nil
	}
	cr.Appended, cr.Created = a.suffix(), a.base.Absent
	pl := &plan{post: Post{Files: map[string]manifest.File{rel: a.post}}}
	pl.summary = fmt.Sprintf("%d bytes appended; engaged %v", a.suffix(), newState.Engaged())
	pl.apply = func(context.Context) error {
		// The old brake store's writers flock the events file itself; an absent one is created
		// O_EXCL, so a writer racing it fails one side rather than interleaving.
		lock := func(dst string) (func(), error) { return flockPath(dst, false) }
		if a.base.Absent {
			lock = nil
		}
		return a.apply(lock)
	}
	pl.verify = func(context.Context) error {
		got, problems := brake.Store{Dir: oldDir}.Load()
		if !reflect.DeepEqual(got, newState) || fmt.Sprint(problems) != fmt.Sprint(newProblems) {
			return errors.New("old's brake store does not fold to new's state after the append")
		}
		cr.Verify = "ok"
		return nil
	}
	return pl, nil
}

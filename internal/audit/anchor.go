package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Head is a record the chain once reached: its seq and hash, and where its
// line starts, so it can be re-read without walking the log.
type Head struct {
	Seq    uint64 `json:"seq"`
	Hash   string `json:"hash"`
	File   string `json:"file"`
	Offset int64  `json:"offset"`
}

// An Anchor keeps the chain's head outside the log (M2). A hash chain
// alone proves only that its records agree with each other: cut the last
// records off at a line boundary, or rewrite the whole file with the
// hashes recomputed, and it verifies. Check compares the log against the
// anchored head, which neither survives.
//
// The daemon keeps it in the login keychain. That is outside ~/.cerberus,
// not outside the operator's account: a process running as the operator
// can rewrite the keychain item too, so the anchor catches a log edited as
// files, not an agent determined to forge both.
type Anchor interface {
	// Load is the anchored head; ok is false when none was ever stored.
	Load() (h Head, ok bool, err error)
	Store(h Head) error
}

type anchorHolder struct {
	dir string
	a   Anchor
}

var anchorPoint atomic.Pointer[anchorHolder]

// SetAnchor installs the anchor this process checks the audit directory dir
// against and advances as it writes there; a nil a removes it. Another
// directory is not anchored.
func SetAnchor(dir string, a Anchor) {
	if a == nil {
		anchorPoint.Store(nil)
		return
	}
	anchorPoint.Store(&anchorHolder{dir: filepath.Clean(dir), a: a})
}

func anchorFor(dir string) Anchor {
	if h := anchorPoint.Load(); h != nil && h.dir == filepath.Clean(dir) {
		return h.a
	}
	return nil
}

// AnchorInterval is how often an ordinary record advances the anchor. A
// record that changes what Cerberus enforces (anchoredAlways) advances it
// every time; so the records a truncation could drop unnoticed are at most
// this old, and never those.
var AnchorInterval = 30 * time.Second

// anchoredAlways are the kinds that advance the anchor on every write: the
// records the daemon folds state from.
var anchoredAlways = map[string]bool{
	KindBrakeChanged:       true,
	KindEnforcementChanged: true,
	KindEnrollmentChanged:  true,
	KindChainReanchored:    true,
	KindApprovalDecided:    true,
	KindGrantCreated:       true,
}

// anchorState is a sink's memory of when it last advanced the anchor.
type anchorState struct {
	mu   sync.Mutex
	last time.Time
}

// advance moves the anchor to head, the record just written, if it is due.
// It never moves an anchor whose record is no longer in the log: that is a
// log cut or rewritten under the anchor, and moving past it would launder
// the change. Only a reanchor, a person acknowledging it, moves it then.
func (st *anchorState) advance(dir string, head Head, kind string, now time.Time) error {
	a := anchorFor(dir)
	if a == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !anchoredAlways[kind] && now.Sub(st.last) < AnchorInterval {
		return nil
	}
	if kind != KindChainReanchored {
		old, ok, err := a.Load()
		if err != nil {
			return err
		}
		if ok {
			if problem := headProblem(dir, old); problem != "" {
				return fmt.Errorf("audit: the anchor was not advanced: %s", problem)
			}
		}
	}
	if err := a.Store(head); err != nil {
		return err
	}
	st.last = now
	return nil
}

// headProblem says what is wrong with the log at an anchored head, or "".
// A head in a month file a prune removed is not a problem.
func headProblem(dir string, h Head) string {
	path := filepath.Join(dir, filepath.Base(h.File))
	f, err := os.Open(path) //nolint:gosec // the audit directory's own file
	if err != nil {
		if os.IsNotExist(err) && prunedBefore(dir, h.File) {
			return ""
		}
		return fmt.Sprintf("%s, which holds anchored seq %d, is missing", filepath.Base(h.File), h.Seq)
	}
	defer func() { _ = f.Close() }()
	if _, seekErr := f.Seek(h.Offset, 0); seekErr != nil {
		return fmt.Sprintf("anchored seq %d is past the end of %s: the log was cut", h.Seq, filepath.Base(h.File))
	}
	r := bufio.NewReaderSize(f, 64*1024)
	line, err := r.ReadBytes('\n')
	if len(line) == 0 && err != nil {
		return fmt.Sprintf("anchored seq %d is past the end of %s: the log was cut", h.Seq, filepath.Base(h.File))
	}
	var rec Record
	if json.Unmarshal(line, &rec) != nil || rec.Seq != h.Seq || rec.Hash != h.Hash {
		return fmt.Sprintf("anchored seq %d is not where it was written in %s: the log was cut or rewritten", h.Seq, filepath.Base(h.File))
	}
	return ""
}

// prunedBefore reports whether file lies before every month file still in
// dir, which is where a prune leaves the files it removed. Verify is what
// checks that a recorded prune removed them.
func prunedBefore(dir, file string) bool {
	files, err := monthFiles(dir)
	if err != nil || len(files) == 0 {
		return false
	}
	return filepath.Base(file) < filepath.Base(files[0])
}

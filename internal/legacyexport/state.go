package legacyexport

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hollis-labs/cerberus/internal/legacyimport/hostdump"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
)

// StateRel is where a run records its progress, relative to the new data directory: never in the
// old tree, which must match the manifest or a step's recorded post-write state and nothing else.
// It is the one small write export-legacy makes into the data directory it otherwise leaves
// intact. Its location awaits the lead's ruling (the alternative is a --state path); this
// constant is the only place that names it.
const StateRel = "migration/export-state.json"

// StateVersion is the progress file's format.
const StateVersion = 1

// The steps, in the order a run takes them: the files, then the anchor (only once the appended
// chain verifies), then the class B host tables.
const (
	StepAudit     = "audit"
	StepApprovals = "approvals"
	StepBrakes    = "brakes"
	StepPause     = "pause"
	StepApplied   = "applied"
	StepReceipts  = "receipts"
	StepState     = "state"
	StepAnchor    = "anchor"
	StepTelemetry = "telemetry"
)

// fileSteps are the steps that write files of the old tree, in order.
var fileSteps = []string{StepAudit, StepApprovals, StepBrakes, StepPause, StepApplied, StepReceipts, StepState}

// Step statuses. started is a step whose writes began and did not finish (a crash): on the next
// run each of its files may be in its manifest state or its post state, and nothing else. failed
// is a step that stopped on an error, treated as started on the next run. pending is the class B
// step waiting for the Telemetry store (or for a conflict to be resolved); done is done.
const (
	StatusStarted = "started"
	StatusDone    = "done"
	StatusPending = "pending"
	StatusFailed  = "failed"
)

// State is the progress file.
type State struct {
	Version int `json:"version"`
	// Handoff is the D3 manifest's body sha256: the one window this progress belongs to.
	Handoff string `json:"handoff"`
	// OldHome, OldDB and OldAnchorAccount are the tree this progress was written to; a run against
	// another tree with the same handoff is refused.
	OldHome          string           `json:"old_home"`
	OldDB            string           `json:"old_db"`
	OldAnchorAccount string           `json:"old_anchor_account"`
	Steps            map[string]*Step `json:"steps"`
	Error            string           `json:"error,omitempty"`
}

// Step is one step's progress.
type Step struct {
	Status string    `json:"status"`
	At     time.Time `json:"at"`
	// Post is the state the step leaves the old tree in, recorded before it writes.
	Post  *Post  `json:"post,omitempty"`
	Error string `json:"error,omitempty"`
}

// Post is a step's post-write state: what manifest.Expect overrides once it is done.
type Post struct {
	Files   map[string]manifest.File `json:"files,omitempty"`
	Dirs    []string                 `json:"dirs,omitempty"`
	Anchor  *manifest.Anchor         `json:"anchor,omitempty"`
	DBState *hostdump.State          `json:"db_state,omitempty"`
}

// inFlight reports a step whose writes may have begun without finishing.
func (s *Step) inFlight() bool {
	return s != nil && (s.Status == StatusStarted || s.Status == StatusFailed)
}

func (s *Step) done() bool { return s != nil && s.Status == StatusDone }

// statePath is the progress file in dataDir.
func statePath(dataDir string) string { return filepath.Join(dataDir, filepath.FromSlash(StateRel)) }

// loadState reads the progress file; ok is false when there is none.
func loadState(dataDir string) (*State, bool, error) {
	data, err := os.ReadFile(statePath(dataDir)) //nolint:gosec // the data directory's own file
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var s State
	if err = json.Unmarshal(data, &s); err != nil {
		return nil, true, fmt.Errorf("%s does not parse: %w", statePath(dataDir), err)
	}
	if s.Version != StateVersion {
		return nil, true, fmt.Errorf("%s is version %d; this build reads %d", statePath(dataDir), s.Version, StateVersion)
	}
	if s.Steps == nil {
		s.Steps = map[string]*Step{}
	}
	return &s, true, nil
}

// save writes the progress file whole: a temporary file (0600) synced and renamed into place,
// then the directory synced.
func (s *State) save(dataDir string) error {
	p := statePath(dataDir)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return replaceFile(p, append(data, '\n'), 0o600)
}

// replaceFile writes data to p through p.tmp (mode, synced) renamed into place, and syncs the
// directory.
func replaceFile(p string, data []byte, mode os.FileMode) error {
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) //nolint:gosec // a path the caller owns
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if err = errors.Join(werr, f.Chmod(mode), f.Sync(), f.Close()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, p); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	return syncDir(filepath.Dir(p))
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // a directory the caller owns
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

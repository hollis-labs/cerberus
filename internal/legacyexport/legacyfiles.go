package legacyexport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
)

// LegacyFilesOptions are --check-legacy-files' inputs.
type LegacyFilesOptions struct {
	// OldHome is the old tree (the adopted root), where plugin-connectors.json stays in place.
	OldHome string
	// D3Copy is the D3 copy's directory: the `cp -a` snapshot of the old home at
	// <D3Copy>/cerberus-home, taken because the manifest excludes plugin-connectors.json.
	D3Copy string
	Now    func() time.Time
}

// D3HomeDir is the old home's copy inside the D3 copy directory.
const D3HomeDir = "cerberus-home"

// pluginStateFile is the in-place plugin state (R18): the new build edits it while it runs, and a
// rollback keeps those edits, so old must still read it.
const pluginStateFile = "plugin-connectors.json"

// CheckLegacyFiles is runbook step 0 of a rollback: plugin-connectors.json compared with its D3
// copy and, when it changed, parsed with the old build's own decoder (readPluginConnectorState,
// carried verbatim below) outside any daemon. A parse error stops the rollback. It reads only.
func CheckLegacyFiles(o LegacyFilesOptions) (*Report, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	r := newReport(now().UTC())
	r.OldHome = o.OldHome
	finish := func(result string, err error) (*Report, error) {
		r.Result = result
		if err != nil {
			r.Error = err.Error()
		}
		r.Finished = now().UTC()
		return r, err
	}
	for _, p := range []struct{ name, path string }{{"--old-home", o.OldHome}, {"--d3-copy", o.D3Copy}} {
		if p.path == "" || !filepath.IsAbs(p.path) {
			r.refuse("%s must be an absolute path (got %q)", p.name, p.path)
		}
	}
	if len(r.Refusals) > 0 {
		return finish(ResultRefused, ErrRefused)
	}
	copyHome := filepath.Join(o.D3Copy, D3HomeDir)
	if fi, err := os.Stat(copyHome); err != nil || !fi.IsDir() {
		r.refuse("the D3 copy has no %s directory (%v): --d3-copy is the directory the cp -a snapshot was taken into", D3HomeDir, err)
		return finish(ResultRefused, ErrRefused)
	}
	lf := &LegacyFilesReport{File: filepath.Join(o.OldHome, pluginStateFile), D3Copy: filepath.Join(copyHome, pluginStateFile)}
	r.LegacyFiles = lf
	cur, err := fileState(lf.File)
	if err != nil {
		return finish(ResultFailed, err)
	}
	was, err := fileState(lf.D3Copy)
	if err != nil {
		return finish(ResultFailed, err)
	}
	lf.SHA256, lf.D3SHA = cur.SHA256, was.SHA256
	lf.Changed = cur != was
	if !lf.Changed {
		return finish(ResultChecked, nil)
	}
	state, err := readPluginConnectorState(lf.File)
	if err != nil {
		lf.Error = err.Error()
		return finish(ResultFailed, fmt.Errorf("%s changed since the D3 copy and the old build's decoder refuses it: %w; stop the rollback and fix the file", lf.File, err))
	}
	lf.Decoded, lf.Entries, lf.Version = true, len(state.Entries), state.Version
	return finish(ResultChecked, nil)
}

// The old build's decoder of plugin-connectors.json and the structs it reads, carried verbatim
// from origin/main (internal/cerbapi/plugin_connector_state.go and the install options of
// plugin_connector_service.go): it accepts an empty file, refuses a version above 2, accepts
// version 1 entries without an id, and folds the legacy "trust" field. New's
// internal/admin/plugin_state.go is a port, not old's code; this is old's code, so a file it
// accepts is one the old daemon will start on. TestCarriedDecoderIsMains pins it to main's text
// (testdata/plugin_connector_state.main.go.txt).

// pluginStateVersion is the format of plugin-connectors.json written since
// install review. Version 1 (no version field) held pointers at plugin
// directories and no review; those entries still load, as review_pending.
const pluginStateVersion = 2

type pluginConnectorPersistedState struct {
	Version int                             `json:"version,omitempty"`
	Entries []pluginConnectorPersistedEntry `json:"entries"`
}

type pluginConnectorPersistedEntry struct {
	// ID is the plugin id. Absent in version 1 entries, whose id is read from
	// the plugin.yaml at PluginDir.
	ID string `json:"id,omitempty"`
	// PluginDir is where the plugin runs from: its reviewed copy in the
	// plugin store, a development install's source directory, or, for a
	// version 1 entry still pending review, the directory it was installed
	// from.
	PluginDir string `json:"plugin_dir"`
	// Source is the directory the reviewed bundle was installed from.
	Source  string               `json:"source,omitempty"`
	Options PluginInstallOptions `json:"options"`
	Loaded  bool                 `json:"loaded"`
	// BundleDigest is the accepted bundle digest; load refuses a bundle that
	// does not match it. Empty while review is pending.
	BundleDigest string `json:"bundle_digest,omitempty"`
	// Review is the review the operator accepted: what a later change is
	// shown as a diff against. Nil while review is pending.
	Review     *pluginhost.Review `json:"review,omitempty"`
	AcceptedAt time.Time          `json:"accepted_at,omitzero"`
	// LegacyTrust is the "trust" object written before P0-4. Only its
	// dev_mode is honored; the signing fields it may carry are ignored. It
	// is folded into Options on read and never written back.
	LegacyTrust *legacyInstallOptions `json:"trust,omitempty"`
}

func readPluginConnectorState(path string) (pluginConnectorPersistedState, error) {
	if path == "" {
		return pluginConnectorPersistedState{}, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // the operator's own state file
	if err != nil {
		if os.IsNotExist(err) {
			return pluginConnectorPersistedState{}, nil
		}
		return pluginConnectorPersistedState{}, fmt.Errorf("read plugin connector state: %w", err)
	}
	var state pluginConnectorPersistedState
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return pluginConnectorPersistedState{}, fmt.Errorf("decode plugin connector state: %w", err)
	}
	if state.Version > pluginStateVersion {
		return pluginConnectorPersistedState{}, fmt.Errorf("plugin connector state %s is version %d, newer than this Cerberus understands (%d); upgrade Cerberus", path, state.Version, pluginStateVersion)
	}
	for i := range state.Entries {
		state.Entries[i].Options = mergeLegacyOptions(state.Entries[i].Options, state.Entries[i].LegacyTrust)
		state.Entries[i].LegacyTrust = nil
	}
	return state, nil
}

// PluginInstallOptions are how a plugin is installed. There is no trust or
// signing option: Cerberus does not vet plugins, so nothing a caller could
// assert about a plugin changes what it may do. DevMode selects a development
// install, which is a restriction (see pluginhost.OriginDev).
type PluginInstallOptions struct {
	DevMode bool `json:"dev_mode,omitempty"`
}

// legacyInstallOptions reads the "trust" object older CLIs sent and older
// state files stored. Only dev_mode still means anything; catalog_signed,
// archive_signed and archive_sha256 were self-asserted and are ignored.
type legacyInstallOptions struct {
	DevMode bool `json:"dev_mode,omitempty"`
}

func mergeLegacyOptions(options PluginInstallOptions, legacy *legacyInstallOptions) PluginInstallOptions {
	if legacy != nil && legacy.DevMode {
		options.DevMode = true
	}
	return options
}

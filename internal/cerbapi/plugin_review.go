package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
)

// PluginReviewer runs a plugin install review: install, upgrade, the
// one-time review of a plugin installed before reviews existed, and
// accepting a changed bundle. It is in-process only — it is not on the
// Client interface, so the socket, the web console and MCP cannot reach it —
// and it refuses any caller surface other than in_process. The caller shows
// the review and collects the typed plugin id on the operator's terminal
// (docs/plans/live-systems-security-target.md, section 10 and Decision 3).
//
// It writes the reviewed entry into the state file; the serving process
// picks it up with ReloadManagedPlugin, by id.
type PluginReviewer struct {
	audit       audit.Sink
	logger      *slog.Logger
	statePath   string
	store       pluginhost.Store
	reservedIDs []string
	now         func() time.Time
}

// NewPluginReviewer constructs the review lane over the state file at
// statePath, whose plugin store is the plugins directory beside it. The audit
// sink is required: every review is an admin event.
func NewPluginReviewer(sink audit.Sink, statePath string, reservedIDs ...string) *PluginReviewer {
	if sink == nil {
		panic("cerbapi: NewPluginReviewer requires an audit sink")
	}
	return &PluginReviewer{
		audit:       sink,
		logger:      slog.Default(),
		statePath:   statePath,
		store:       pluginStoreFor(statePath),
		reservedIDs: append(append([]string(nil), hostServedIDs...), reservedIDs...),
		now:         time.Now,
	}
}

// Review kinds, recorded as the audit operation.
const (
	ReviewInstall       = "install"
	ReviewUpgrade       = "upgrade"
	ReviewMigrate       = "review"
	ReviewAcceptChanges = "accept_changes"
)

// ErrNothingToReview is a review of a plugin whose bundle already matches
// its accepted review.
var ErrNothingToReview = errors.New("nothing to review")

// PendingReview is a review prepared and waiting for the operator's
// confirmation. Discard it if it is not accepted.
type PendingReview struct {
	Kind   string
	Review pluginhost.Review
	// Previous is the review accepted before, for an upgrade or a changed
	// bundle; Changes is the diff against it.
	Previous   *pluginhost.Review
	AcceptedAt time.Time
	Changes    []string

	staged *pluginhost.Staged
	dev    bool
	source string
	entry  *pluginConnectorPersistedEntry
}

// Text is the review as the operator reads it: the diff first, when there
// is an accepted review to compare with, then the whole summary.
func (p *PendingReview) Text() string {
	var b strings.Builder
	if p.Previous != nil {
		fmt.Fprintf(&b, "Changes since the review accepted %s:\n", p.AcceptedAt.UTC().Format(time.RFC3339))
		for _, line := range p.Changes {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		b.WriteString("\n")
	}
	b.WriteString(p.Review.Render())
	return b.String()
}

// Prompt is what the operator types to accept.
func (p *PendingReview) Prompt() string {
	return fmt.Sprintf("Accept? Type the plugin id (%s) to confirm: ", p.Review.ID)
}

// PrepareInstall stages source and reviews it. A plugin already installed
// under the same id is an upgrade, shown as a diff against its accepted
// review; one installed before reviews existed gets the whole summary.
func (r *PluginReviewer) PrepareInstall(ctx context.Context, source string, dev bool) (*PendingReview, error) {
	if err := requireInProcess(ctx, "install"); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	staged, err := r.stage(abs, dev)
	if err != nil {
		return nil, err
	}
	pending := &PendingReview{Kind: ReviewInstall, staged: staged, dev: dev, source: abs}
	state, err := readPluginConnectorState(r.statePath)
	if err != nil {
		r.Discard(pending)
		return nil, err
	}
	if i := state.findEntry(staged.Spec.ID); i >= 0 {
		entry := state.Entries[i]
		pending.entry = &entry
		if !entry.reviewPending() {
			if entry.BundleDigest == staged.Digest && entry.Options.DevMode == dev {
				r.Discard(pending)
				return nil, fmt.Errorf("plugin %q is already installed at %s: %w", staged.Spec.ID, staged.Digest, ErrNothingToReview)
			}
			pending.Kind = ReviewUpgrade
			pending.Previous = entry.Review
			pending.AcceptedAt = entry.AcceptedAt
		}
	}
	if err := r.finishPrepare(pending); err != nil {
		r.Discard(pending)
		return nil, err
	}
	return pending, nil
}

// PrepareReview reviews an installed plugin again: the one-time review of a
// plugin installed before reviews existed, or a bundle that no longer
// matches its accepted review (--accept-changes). A plugin that matches its
// review has nothing to review.
func (r *PluginReviewer) PrepareReview(ctx context.Context, id string) (*PendingReview, error) {
	if err := requireInProcess(ctx, "review"); err != nil {
		return nil, err
	}
	state, err := readPluginConnectorState(r.statePath)
	if err != nil {
		return nil, err
	}
	i := state.findEntry(id)
	if i < 0 {
		return nil, fmt.Errorf("plugin %q is not installed; install it with `cerberus connectors plugin managed install <dir>`", id)
	}
	entry := state.Entries[i]
	pending := &PendingReview{Kind: ReviewMigrate, dev: entry.Options.DevMode, entry: &entry}
	if !entry.reviewPending() {
		current, digestErr := pluginhost.BundleDigest(entry.PluginDir)
		if digestErr == nil && current == entry.BundleDigest {
			return nil, fmt.Errorf("plugin %q matches its accepted review (%s): %w", id, entry.BundleDigest, ErrNothingToReview)
		}
		if digestErr != nil {
			return nil, fmt.Errorf("plugin %q: its installed bundle at %s cannot be read (%w); reinstall it with `cerberus connectors plugin managed install <dir>`", id, entry.PluginDir, digestErr)
		}
		pending.Kind = ReviewAcceptChanges
		pending.Previous = entry.Review
		pending.AcceptedAt = entry.AcceptedAt
	}
	// A pending entry points at the directory it was installed from; a
	// changed one at its store copy or its development source. Either way
	// that directory is what is reviewed, and a non-dev bundle is copied
	// into the store under its new digest.
	pending.source = entry.PluginDir
	staged, err := r.stage(entry.PluginDir, pending.dev)
	if err != nil {
		return nil, err
	}
	if staged.Spec.ID != id {
		r.store.Discard(staged)
		return nil, fmt.Errorf("the directory installed as %q now declares plugin %q; refusing", id, staged.Spec.ID)
	}
	pending.staged = staged
	if err := r.finishPrepare(pending); err != nil {
		r.Discard(pending)
		return nil, err
	}
	return pending, nil
}

func (r *PluginReviewer) stage(source string, dev bool) (*pluginhost.Staged, error) {
	if dev {
		// A development install runs from its source directory, so it is
		// reviewed in place. It still gets the digest every load compares.
		return pluginhost.Inspect(source)
	}
	return r.store.Stage(source)
}

// finishPrepare runs the install checks on the staged bundle and builds its
// review.
func (r *PluginReviewer) finishPrepare(p *PendingReview) error {
	spec := p.staged.Spec
	installer := pluginhost.DirectoryInstaller{ReservedIDs: r.reservedIDs}
	for _, reserved := range installer.ReservedIDs {
		if strings.EqualFold(reserved, spec.ID) {
			return &pluginhost.ReservedIDError{ID: spec.ID}
		}
	}
	if err := spec.Cerberus.Host.Check(pluginsdk.ContractVersion); err != nil {
		return fmt.Errorf("plugin %q cannot be installed: %w", spec.ID, err)
	}
	// A development install is reviewed in place, so its staged directory is
	// its source, which is what the developer-root check is about.
	policy := pluginPolicy(p.source, PluginInstallOptions{DevMode: p.dev})
	decision, err := policy.ValidateInstall(pluginhost.InstallCheck{
		SourcePath:       p.staged.Dir,
		EntrypointSHA256: p.staged.EntrypointSHA256,
		Manifest:         spec.Cerberus.Connector,
	})
	if err != nil {
		return err
	}
	source := p.source
	if p.entry != nil && p.entry.Source != "" && p.Kind == ReviewAcceptChanges {
		source = p.entry.Source
	}
	p.Review = pluginhost.BuildReview(p.staged, source, decision.Origin)
	if p.Previous != nil {
		p.Changes = pluginhost.Diff(*p.Previous, p.Review)
	}
	return nil
}

// Discard drops a review that was not accepted.
func (r *PluginReviewer) Discard(p *PendingReview) {
	if p != nil && !p.dev {
		r.store.Discard(p.staged)
	}
}

// Accept records and applies a review the operator confirmed by typing the
// plugin id. The record is written before anything changes — an unwritable
// log refuses the review — and a confirmation that does not match is
// recorded as a refusal and changes nothing.
func (r *PluginReviewer) Accept(ctx context.Context, p *PendingReview, typed string) (_ ManagedPluginConnectorState, retErr error) {
	if err := requireInProcess(ctx, p.Kind); err != nil {
		r.Discard(p)
		return ManagedPluginConnectorState{}, err
	}
	review := p.Review
	call, err := beginAudit(ctx, r.audit, r.logger, auditSpec{
		connector: "plugin", operation: p.Kind, op: pluginAdminOperation(p.Kind), known: true,
		config:       map[string]any{"id": review.ID, "plugin_dir": review.Source},
		acknowledged: strings.TrimSpace(typed) == review.ID,
		review: &audit.PluginReview{
			Kind: p.Kind, SummarySHA256: review.SummaryDigest(), BundleDigest: review.BundleDigest,
			Source: review.Source, Gaps: review.Gaps, Changes: p.Changes,
		},
	})
	if err != nil {
		r.Discard(p)
		return ManagedPluginConnectorState{}, externalConnectorError(ExternalConnectorOperationArgs{Connector: "plugin", Operation: p.Kind}, ExternalConnectorAuditUnavailable, err)
	}
	defer func() { call.finish(retErr) }()
	if strings.TrimSpace(typed) != review.ID {
		r.Discard(p)
		return ManagedPluginConnectorState{}, externalConnectorError(ExternalConnectorOperationArgs{Connector: "plugin", Operation: p.Kind}, ExternalConnectorAckRequired,
			fmt.Errorf("the confirmation did not match the plugin id %q; nothing was installed", review.ID))
	}

	runDir := p.staged.Dir
	if !p.dev {
		runDir, err = r.store.Commit(p.staged)
		if err != nil {
			return ManagedPluginConnectorState{}, err
		}
	}
	entry := pluginConnectorPersistedEntry{
		ID: review.ID, PluginDir: runDir, Source: review.Source,
		Options: PluginInstallOptions{DevMode: p.dev}, BundleDigest: review.BundleDigest,
		Review: &review, AcceptedAt: r.now().UTC(),
	}
	var previousDir string
	err = updatePluginConnectorState(r.statePath, func(st *pluginConnectorPersistedState) error {
		if i := st.findEntry(review.ID); i >= 0 {
			entry.Loaded = st.Entries[i].Loaded
			previousDir = st.Entries[i].PluginDir
			st.Entries[i] = entry
			return nil
		}
		st.Entries = append(st.Entries, entry)
		return nil
	})
	if err != nil {
		return ManagedPluginConnectorState{}, err
	}
	// Superseded store copies go; the operator's own directories stay.
	if previousDir != "" && previousDir != runDir && r.store.Owns(previousDir) {
		_ = os.RemoveAll(previousDir)
	}
	return ManagedPluginConnectorState{
		ID: review.ID, Version: review.Version, Path: runDir, Source: review.Source, Loaded: entry.Loaded,
		Origin: review.Origin, EntrypointSHA256: review.EntrypointSHA256, BundleDigest: review.BundleDigest,
		AcceptedAt: entry.AcceptedAt, Capabilities: review.Capabilities, ContractGaps: review.Gaps,
		ConfigFields: []string{}, MCPExpose: []string{}, ConfigProblems: []string{},
	}, nil
}

// requireInProcess refuses a review from any surface but the operator's
// own process. The interface already keeps other surfaces out; this is the
// check that holds if a caller is ever wired to it by mistake.
func requireInProcess(ctx context.Context, kind string) error {
	if surface := CallerSurfaceFrom(ctx); surface != SurfaceInProcess {
		return externalConnectorError(ExternalConnectorOperationArgs{Connector: "plugin", Operation: kind}, ExternalConnectorUnsupported,
			fmt.Errorf("a plugin %s is a review confirmed in your terminal and is not available from the %s surface; run `cerberus connectors plugin managed %s` there", kind, surface, reviewVerb(kind)))
	}
	return nil
}

func reviewVerb(kind string) string {
	switch kind {
	case ReviewMigrate:
		return "review <id>"
	case ReviewAcceptChanges:
		return "load <id> --accept-changes"
	default:
		return "install <dir>"
	}
}

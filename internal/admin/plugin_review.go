package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
)

// The plugin install review: install, upgrade, the one-time review of a plugin installed
// before reviews existed, and accepting a changed bundle. It is the old build's
// plugin_review.go (internal/cerbapi on main) with its review semantics unchanged: the bundle
// is staged and digested, the review is built from the staged copy without starting it, and
// accepting moves that copy to plugins/<id>/<digest-hex>/ in the adopted root and writes the
// entry, with its review, accepted_at and bundle_digest, into plugin-connectors.json under the
// old lock with an atomic 0600 rename. The intent record carries the review (audit.PluginReview).
//
// What changed is who accepts (the lead's ruling (a) on decision 0.1). The old build took the
// typed plugin id on the operator's terminal, in-process only. Here the owner accepts: a person
// on the console, typing the plugin id; or an operator's key at the CLI naming, with
// approval_id, an out-of-band approval the owner decided on the console (its owner-session
// seal verified on consume). A key without one is answered approval_pending with the approval
// it opened, bound to the review's plan by its hash; an MCP client is refused (428). There is
// no local-trust, in-process or --yes path.

// Review kinds, recorded as the audit operation.
const (
	ReviewInstall       = "install"
	ReviewUpgrade       = "upgrade"
	ReviewMigrate       = "review"
	ReviewAcceptChanges = "accept_changes"
)

// ErrNothingToReview is a review of a plugin whose bundle already matches its accepted review.
var ErrNothingToReview = errors.New("nothing to review")

// ReviewRequest is an install, upgrade or review: the directory to install from (install and
// upgrade), whether it is a development install, and the plugin id a person typed.
type ReviewRequest struct {
	PluginDir string `json:"plugin_dir,omitempty"`
	Dev       bool   `json:"dev,omitempty"`
	Typed     string `json:"typed,omitempty"`
}

// ReviewResult is an accepted review: what was accepted and the plugin after it.
type ReviewResult struct {
	Kind         string            `json:"kind"`
	Review       pluginhost.Review `json:"review"`
	Changes      []string          `json:"changes,omitempty"`
	PolicyChange []string          `json:"policy_change,omitempty"`
	// Proof is what authorized it: "typed" (a person on the console) or "approval:<id>".
	Proof string      `json:"proof"`
	State PluginState `json:"state"`
	// Warning is set when the review was accepted but the lane could not reload the plugin.
	Warning string `json:"warning,omitempty"`
}

// ReviewPreview is the review as a plan shows it (the plan's preview, kind "review"): what the
// owner reads before accepting, on the console or the CLI.
type ReviewPreview struct {
	Kind         string    `json:"kind"`
	ID           string    `json:"id"`
	Text         string    `json:"text"`
	Changes      []string  `json:"changes,omitempty"`
	PolicyChange []string  `json:"policy_change,omitempty"`
	AcceptedAt   time.Time `json:"previous_accepted_at,omitzero"`
}

// pendingReview is a review prepared and waiting for acceptance; discarded if not accepted.
type pendingReview struct {
	kind         string
	review       pluginhost.Review
	previous     *pluginhost.Review
	acceptedAt   time.Time
	changes      []string
	policyChange []string

	staged       *pluginhost.Staged
	dev          bool
	source       string
	entry        *persistedEntry
	policyFile   *policy.File
	policyTarget string
}

// text is the review as the owner reads it: the diff first, when there is an accepted review
// to compare with, then the whole summary.
func (r *pendingReview) text() string {
	var b strings.Builder
	if r.previous != nil {
		fmt.Fprintf(&b, "Changes since the review accepted %s:\n", r.acceptedAt.UTC().Format(time.RFC3339))
		for _, line := range r.changes {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		b.WriteString("\n")
	}
	b.WriteString(r.review.Render())
	if len(r.policyChange) > 0 {
		b.WriteString("\nSuggested policy\n")
		for _, line := range r.policyChange {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	return b.String()
}

// Install reviews the plugin in req.PluginDir and, accepted, installs it. A plugin already
// installed under the same id is an upgrade, shown as a diff against its accepted review.
func (p *Plugins) Install(ctx context.Context, req ReviewRequest, opts GateOpts) (ReviewResult, *gate.ConnectorPlan, error) {
	pending, err := p.prepareInstall(req.PluginDir, req.Dev)
	if err != nil {
		return ReviewResult{}, nil, err
	}
	return p.acceptReview(ctx, pending, req.Typed, opts)
}

// Upgrade is an install of an installed plugin, id, from req.PluginDir, which must declare it.
func (p *Plugins) Upgrade(ctx context.Context, id string, req ReviewRequest, opts GateOpts) (ReviewResult, *gate.ConnectorPlan, error) {
	pending, err := p.prepareInstall(req.PluginDir, req.Dev)
	if err != nil {
		return ReviewResult{}, nil, err
	}
	switch {
	case pending.review.ID != id:
		p.discard(pending)
		return ReviewResult{}, nil, InputError{fmt.Errorf("%s declares plugin %q, not %q; refusing", req.PluginDir, pending.review.ID, id)}
	case pending.entry == nil:
		p.discard(pending)
		return ReviewResult{}, nil, InputError{fmt.Errorf("plugin %q is not installed; install it first", id)}
	}
	return p.acceptReview(ctx, pending, req.Typed, opts)
}

// Review reviews an installed plugin again: the one-time review of a plugin installed before
// reviews existed, or a bundle that no longer matches its accepted review.
func (p *Plugins) Review(ctx context.Context, id string, req ReviewRequest, opts GateOpts) (ReviewResult, *gate.ConnectorPlan, error) {
	pending, err := p.prepareReview(id)
	if err != nil {
		return ReviewResult{}, nil, err
	}
	return p.acceptReview(ctx, pending, req.Typed, opts)
}

// prepareInstall stages source and reviews it.
func (p *Plugins) prepareInstall(source string, dev bool) (*pendingReview, error) {
	if strings.TrimSpace(source) == "" {
		return nil, InputError{errors.New("plugin_dir names the plugin directory to install")}
	}
	if !filepath.IsAbs(source) {
		return nil, InputError{fmt.Errorf("plugin_dir must be an absolute path, not %q", source)}
	}
	staged, err := p.stage(source, dev)
	if err != nil {
		return nil, InputError{err}
	}
	pending := &pendingReview{kind: ReviewInstall, staged: staged, dev: dev, source: filepath.Clean(source)}
	state, err := readPluginState(p.statePath)
	if err != nil {
		p.discard(pending)
		return nil, err
	}
	if i := state.findEntry(staged.Spec.ID); i >= 0 {
		entry := state.Entries[i]
		pending.entry = &entry
		if !entry.reviewPending() {
			if entry.BundleDigest == staged.Digest && entry.Options.DevMode == dev {
				p.discard(pending)
				return nil, fmt.Errorf("plugin %q is already installed at %s: %w", staged.Spec.ID, staged.Digest, ErrNothingToReview)
			}
			pending.kind = ReviewUpgrade
			pending.previous = entry.Review
			pending.acceptedAt = entry.AcceptedAt
		}
	}
	if err := p.finishPrepare(pending); err != nil {
		p.discard(pending)
		return nil, err
	}
	return pending, nil
}

// prepareReview reviews an installed plugin's directory again.
func (p *Plugins) prepareReview(id string) (*pendingReview, error) {
	state, err := readPluginState(p.statePath)
	if err != nil {
		return nil, err
	}
	i := state.findEntry(id)
	if i < 0 {
		return nil, InputError{fmt.Errorf("plugin %q is not installed; install it with `cerberus connectors plugin managed install <dir>`", id)}
	}
	entry := state.Entries[i]
	pending := &pendingReview{kind: ReviewMigrate, dev: entry.Options.DevMode, entry: &entry}
	if !entry.reviewPending() {
		current, digestErr := pluginhost.BundleDigest(entry.PluginDir)
		if digestErr == nil && current == entry.BundleDigest {
			return nil, fmt.Errorf("plugin %q matches its accepted review (%s): %w", id, entry.BundleDigest, ErrNothingToReview)
		}
		if digestErr != nil {
			return nil, fmt.Errorf("plugin %q: its installed bundle at %s cannot be read (%w); reinstall it with `cerberus connectors plugin managed install <dir>`", id, entry.PluginDir, digestErr)
		}
		pending.kind = ReviewAcceptChanges
		pending.previous = entry.Review
		pending.acceptedAt = entry.AcceptedAt
	}
	// A pending entry points at the directory it was installed from; a changed one at its
	// store copy or its development source. Either way that directory is what is reviewed,
	// and a non-dev bundle is copied into the store under its new digest.
	pending.source = entry.PluginDir
	staged, err := p.stage(entry.PluginDir, pending.dev)
	if err != nil {
		return nil, err
	}
	pending.staged = staged
	if staged.Spec.ID != id {
		p.discard(pending)
		return nil, fmt.Errorf("the directory installed as %q now declares plugin %q; refusing", id, staged.Spec.ID)
	}
	if err := p.finishPrepare(pending); err != nil {
		p.discard(pending)
		return nil, err
	}
	return pending, nil
}

func (p *Plugins) stage(source string, dev bool) (*pluginhost.Staged, error) {
	if dev {
		// A development install runs from its source directory, so it is reviewed in place.
		// It still gets the digest every load compares.
		return pluginhost.Inspect(source)
	}
	return p.store.Stage(source)
}

// finishPrepare runs the install checks on the staged bundle and builds its review.
func (p *Plugins) finishPrepare(r *pendingReview) error {
	spec := r.staged.Spec
	for _, reserved := range p.reservedIDs {
		if strings.EqualFold(reserved, spec.ID) {
			return InputError{&pluginhost.ReservedIDError{ID: spec.ID}}
		}
	}
	if err := spec.Cerberus.Host.Check(pluginsdk.ContractVersion); err != nil {
		return InputError{fmt.Errorf("plugin %q cannot be installed: %w", spec.ID, err)}
	}
	installPolicy := pluginhost.LocalInstallPolicy()
	if r.dev {
		// A development install is reviewed in place, so its staged directory is its source,
		// which is what the developer-root check is about.
		installPolicy = pluginhost.DeveloperInstallPolicy(r.source)
	}
	decision, err := installPolicy.ValidateInstall(pluginhost.InstallCheck{
		SourcePath:       r.staged.Dir,
		EntrypointSHA256: r.staged.EntrypointSHA256,
		Manifest:         spec.Cerberus.Connector,
	})
	if err != nil {
		return InputError{err}
	}
	source := r.source
	if r.entry != nil && r.entry.Source != "" && r.kind == ReviewAcceptChanges {
		source = r.entry.Source
	}
	r.review = pluginhost.BuildReview(r.staged, source, decision.Origin)
	if r.previous != nil {
		r.changes = pluginhost.Diff(*r.previous, r.review)
	}
	return p.preparePolicy(r)
}

// preparePolicy works out what accepting does to the plugin's provider profile: its suggested
// rules become a working policy file, shown as a diff against the one already there. The
// plugin's word is never applied by itself; a policy apply makes it live.
func (p *Plugins) preparePolicy(r *pendingReview) error {
	if p.policy.Dir == "" {
		return nil
	}
	id := r.review.ID
	proposed := policy.FromSuggested(id, r.review.SuggestedPolicy)
	rel := filepath.Join("providers", id+".yaml")
	existing, exists, err := p.policy.ReadWorking(rel)
	if err != nil {
		return fmt.Errorf("read the working policy file %s: %w", rel, err)
	}
	was, now := policy.RuleLines(existing), policy.RuleLines(proposed)
	if strings.Join(was, "\n") == strings.Join(now, "\n") {
		return nil
	}
	where := filepath.Join(p.policy.Dir, rel)
	if !exists {
		r.policyChange = append(r.policyChange, fmt.Sprintf("Accepting writes these %d suggested rule(s) to %s. It is a working file:", len(now), where),
			"nothing is enforced until you review it and run `cerberus policy apply`.")
		for _, line := range now {
			r.policyChange = append(r.policyChange, "+ "+line)
		}
	} else {
		r.policyChange = append(r.policyChange, fmt.Sprintf("Accepting replaces the rules in %s (a working file; `cerberus policy apply` makes it live):", where))
		r.policyChange = append(r.policyChange, lineDiff(was, now)...)
	}
	r.policyFile, r.policyTarget = &proposed, rel
	return nil
}

func lineDiff(was, now []string) []string {
	in := func(list []string, v string) bool {
		for _, s := range list {
			if s == v {
				return true
			}
		}
		return false
	}
	var out []string
	for _, l := range was {
		if !in(now, l) {
			out = append(out, "- "+l)
		}
	}
	for _, l := range now {
		if !in(was, l) {
			out = append(out, "+ "+l)
		}
	}
	return out
}

// discard drops a review that was not accepted.
func (p *Plugins) discard(r *pendingReview) {
	if r != nil && !r.dev {
		p.store.Discard(r.staged)
	}
}

// reviewSpec is the review's gate spec: connector "plugin", the review kind as the operation,
// the plugin by id and source, the review record, and the review's plan, which the owner's
// approval binds to by its hash.
func (p *Plugins) reviewSpec(r *pendingReview, typedOK bool, opts GateOpts) gate.Spec {
	review := r.review
	spec := gate.Spec{Connector: "plugin", Operation: r.kind, Op: pluginAdminOperation(r.kind), Known: true,
		Config:       map[string]any{"id": review.ID, "plugin_dir": review.Source},
		Acknowledged: typedOK || opts.ApprovalID != "",
		Review: &audit.PluginReview{Kind: r.kind, SummarySHA256: review.SummaryDigest(), BundleDigest: review.BundleDigest,
			Source: review.Source, Gaps: review.Gaps, Changes: r.changes},
		PlanOnly: opts.Plan, DryRun: opts.Plan, ConfirmedPlanHash: opts.ConfirmedPlanHash,
		PluginEntrypointSHA256: review.EntrypointSHA256}
	planSpec := spec
	digest := p.gate.Sink().Digest
	preview, _ := json.Marshal(ReviewPreview{Kind: r.kind, ID: review.ID, Text: r.text(), Changes: r.changes, PolicyChange: r.policyChange, AcceptedAt: r.acceptedAt})
	spec.Plan = func(context.Context) (plan.Plan, error) {
		tgt, _ := gate.AuditTarget(planSpec)
		previous := "(none)"
		if r.previous != nil {
			previous = r.previous.BundleDigest
		}
		return plan.Plan{Lane: plan.LaneAdmin, Connector: "plugin", Operation: r.kind, Effect: string(contract.EffectAdmin), Target: tgt,
			ArgsDigest: digest(planSpec.Config), PreviewKind: "review", Preview: preview, PluginEntrypointSHA256: review.EntrypointSHA256,
			Digests: map[string]string{"bundle": review.BundleDigest, "review": review.SummaryDigest(), "accepted": previous}}, nil
	}
	return spec
}

// acceptReview gates the review and, accepted by the owner, applies it. The intent, with the
// review record, is written before anything changes, so an unwritable log refuses the review;
// a refusal is recorded as its outcome and changes nothing.
func (p *Plugins) acceptReview(ctx context.Context, r *pendingReview, typed string, opts GateOpts) (_ ReviewResult, _ *gate.ConnectorPlan, retErr error) {
	ctx, scope := redact.EnsureScope(ctx)
	defer func() { retErr = scopeError(scope, retErr) }()
	person := gate.ConsolePerson(ctx)
	typedOK := person && strings.TrimSpace(typed) == r.review.ID
	spec := p.reviewSpec(r, typedOK, opts)
	call, err := p.gate.Begin(ctx, spec)
	if err != nil {
		p.discard(r)
		return ReviewResult{}, nil, err
	}
	if opts.Plan {
		shown, planErr := call.ShowPlan(ctx)
		call.Finish(planErr)
		p.discard(r)
		return ReviewResult{}, shown, planErr
	}
	proof, err := p.reviewProof(ctx, r, spec, typed, typedOK, person, opts.ApprovalID)
	if err != nil {
		call.Finish(err)
		p.discard(r)
		return ReviewResult{}, nil, err
	}
	out, err := p.applyReview(ctx, r)
	call.Finish(err)
	if err != nil {
		return ReviewResult{}, nil, err
	}
	out.Proof = proof
	return out, nil, nil
}

// reviewProof is what authorizes accepting: a person on the console typing the plugin id, or
// the owner's out-of-band approval named by approvalID (asked for when none is named). An MCP
// client, local trust and an in-process caller have neither.
func (p *Plugins) reviewProof(ctx context.Context, r *pendingReview, spec gate.Spec, typed string, typedOK, person bool, approvalID string) (string, error) {
	id := r.review.ID
	switch {
	case typedOK:
		return "typed", nil
	case person && approvalID == "":
		what := "installed"
		if r.kind != ReviewInstall {
			what = "accepted"
		}
		if strings.TrimSpace(typed) == "" {
			return "", gate.NewError("plugin", r.kind, gate.CodeAckRequired,
				redact.Guidance("type the plugin id (%s) to accept the review; nothing was %s", id, what))
		}
		return "", gate.NewError("plugin", r.kind, gate.CodeAckRequired,
			redact.Guidance("the confirmation did not match the plugin id %q; nothing was %s", id, what))
	}
	tgt, _ := gate.AuditTarget(spec)
	return p.gate.OwnerApproval(ctx, gate.OwnerApprovalRequest{Connector: "plugin", Operation: r.kind, Target: tgt,
		ArgsDigest: r.review.BundleDigest, ApprovalID: approvalID, What: fmt.Sprintf("a plugin %s of %s", r.kind, id),
		Rule: "plugin." + r.kind, Reason: "a plugin is installed, upgraded or reviewed by the owner on the console", Plan: spec.Plan})
}

// applyReview commits the reviewed bundle to the store, writes its entry, the suggested
// policy, and makes the lane's view match: the plugin is registered, reloaded when it was
// loaded, and a superseded store copy is removed.
func (p *Plugins) applyReview(ctx context.Context, r *pendingReview) (ReviewResult, error) {
	review := r.review
	runDir := r.staged.Dir
	if !r.dev {
		var err error
		if runDir, err = p.store.Commit(r.staged); err != nil {
			return ReviewResult{}, err
		}
	}
	entry := persistedEntry{ID: review.ID, PluginDir: runDir, Source: review.Source, Options: InstallOptions{DevMode: r.dev},
		BundleDigest: review.BundleDigest, Review: &review, AcceptedAt: time.Now().UTC()}
	var previousDir string
	err := updatePluginState(p.statePath, func(st *persistedState) error {
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
		return ReviewResult{}, err
	}
	out := ReviewResult{Kind: r.kind, Review: review, Changes: r.changes, PolicyChange: r.policyChange}
	if r.policyFile != nil {
		if err := p.policy.WriteWorking(r.policyTarget, *r.policyFile); err != nil {
			return ReviewResult{}, fmt.Errorf("the review was accepted, but the suggested policy was not written to %s: %w", filepath.Join(p.policy.Dir, r.policyTarget), err)
		}
	}
	st, reloadErr := p.reloadEntry(ctx, review.ID)
	if reloadErr != nil {
		out.Warning = redact.Text(fmt.Sprintf("the review was accepted and recorded, but %q was not reloaded: %v", review.ID, reloadErr))
		installed, _ := p.manager.Installed(review.ID)
		st = p.state(installed, p.manager.Loaded(review.ID))
	}
	// Superseded store copies go, once nothing runs from them; the operator's own directories
	// stay.
	if previousDir != "" && previousDir != runDir && p.store.Owns(previousDir) && reloadErr == nil {
		_ = os.RemoveAll(previousDir)
	}
	out.State = st
	return out, nil
}

// reloadEntry re-reads id's entry from the state file and makes the lane's view match it: the
// body of Reload, for a caller already inside a gated call.
func (p *Plugins) reloadEntry(ctx context.Context, id string) (PluginState, error) {
	state, err := readPluginState(p.statePath)
	if err != nil {
		return PluginState{}, err
	}
	i := state.findEntry(id)
	if i < 0 {
		return PluginState{}, redact.Guidance("plugin %q is not in plugin-connectors.json; install it first", id)
	}
	entry := state.Entries[i]
	next, err := p.inspect(entry)
	if err != nil {
		return PluginState{}, err
	}
	if next.ID != id {
		return PluginState{}, fmt.Errorf("the state entry for %q now holds plugin %q; refusing", id, next.ID)
	}
	wasLoaded := p.manager.Loaded(id)
	if wasLoaded || entry.Loaded {
		if err := p.manager.CheckBundle(next); err != nil {
			return PluginState{}, loadError(err)
		}
	}
	if wasLoaded {
		if err := p.manager.Unload(ctx, id); err != nil {
			return PluginState{}, err
		}
	}
	p.manager.RegisterInstalled(next)
	entry.ID = id
	p.setRecord(id, entry)
	if wasLoaded || entry.Loaded {
		if err := p.manager.Load(context.WithoutCancel(ctx), id); err != nil {
			_ = p.persist()
			return PluginState{}, loadError(err)
		}
	}
	if err := p.persist(); err != nil {
		return PluginState{}, err
	}
	return p.state(next, p.manager.Loaded(id)), nil
}

package cerbapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/configops"
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/registry"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

// Console writes are changes the web console makes to Cerberus's own state:
// a deploy profile saved or deleted, a provider's settings and credentials
// saved, a project config registered or deregistered, a config backup
// restored. Profiles carry shell commands and target labels, and labels
// decide which approval channel a call needs, so each is an admin
// operation, gated and recorded (M9).
//
// They belong to the serving process, as deploy-profile runs do
// (CERB-GAP-886): the daemon holds the approval broker, so a write policy
// wants approved can be asked for, decided and consumed where it runs, and
// the console confirms it in the same dialog as a resource verb.

// ConsoleConnector is the connector id console writes are recorded under.
const ConsoleConnector = "console"

// The console writes.
const (
	ConsoleProfileSave        = "profile_save"
	ConsoleProfileDelete      = "profile_delete"
	ConsoleProviderSave       = "provider_save"
	ConsoleRegistryRegister   = "registry_register"
	ConsoleRegistryDeregister = "registry_deregister"
	ConsoleConfigRestore      = "config_restore"
)

// ErrConsoleWriteNotFound is a console write whose subject does not exist.
var ErrConsoleWriteNotFound = errors.New("not found")

// ConsoleWriteInputError is a console write refused for its input.
type ConsoleWriteInputError struct{ Err error }

func (e ConsoleWriteInputError) Error() string { return e.Err.Error() }
func (e ConsoleWriteInputError) Unwrap() error { return e.Err }

func consoleInputError(format string, args ...any) error {
	return ConsoleWriteInputError{fmt.Errorf(format, args...)}
}

// ConsoleWriteRequest is one console write, as the console sends it.
type ConsoleWriteRequest struct {
	Operation string `json:"operation"`
	// ID is the profile (profile_delete), the provider (provider_save) or
	// the registry owner (registry_deregister).
	ID string `json:"id,omitempty"`
	// Profile is the profile profile_save writes.
	Profile *infra.DeploymentProfile `json:"profile,omitempty"`
	// Path is the config registry_register registers, or the backup
	// config_restore restores (empty: the config's own .bak).
	Path string `json:"path,omitempty"`
	// Values, Secrets and ClearSecrets are provider_save's settings and
	// credentials. Secret values travel only to the serving process, over
	// its socket, and are never recorded, shown or answered with.
	Values       map[string]string `json:"values,omitempty"`
	Secrets      map[string]string `json:"secrets,omitempty"`
	ClearSecrets []string          `json:"clear_secrets,omitempty"`
}

// ConsoleWriteResult is what a console write did.
type ConsoleWriteResult struct {
	Success bool `json:"success"`
	// Error is a restore that failed after the gate let it run: the outcome
	// record says so, and the console answers it as a failed restore.
	Error string `json:"error,omitempty"`
	// Count is how many projects registry_register registered.
	Count int `json:"count,omitempty"`
	// SecretsChanged is a provider_save that set or cleared a credential,
	// so a loaded plugin holding the old one needs a reload.
	SecretsChanged bool   `json:"secrets_changed,omitempty"`
	BackupPath     string `json:"backup_path,omitempty"`
	RestoredTo     string `json:"restored_to,omitempty"`
	PreRestorePath string `json:"pre_restore_path,omitempty"`
}

// WithConsoleSecretStore is the credential store provider_save writes.
func WithConsoleSecretStore(store secret.ReadWriter) InProcessOption {
	return func(c *InProcessClient) { c.consoleSecrets = store }
}

// consoleWriteOperation is a console write's contract: admin, local files.
func consoleWriteOperation(name string) contract.Operation {
	return contract.Operation{
		Name: name, Effect: contract.EffectAdmin,
		// The labels and paths are names, recorded in the clear: a relabeled
		// target is visible in the record, not only as a digest.
		Target:  contract.TargetDescriptor{Kind: "console", From: []string{"id", "env", "owner", "admin", "config_path", "backup_path"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites,
	}.Finalize()
}

// ConsoleWrite runs a console write through the gate: the verified
// caller's scopes, the brakes and policy, an approval where policy wants
// one, the intent recorded first and the outcome after.
func (c *InProcessClient) ConsoleWrite(ctx context.Context, req ConsoleWriteRequest, options ...MutationOption) (*ConsoleWriteResult, error) {
	opts := ApplyMutationOptions(options)
	w, err := c.consoleWrite(req)
	if err != nil {
		return nil, err
	}
	spec := w.spec(opts, c.runtime.audit)
	call, err := beginGated(ctx, c.runtime.audit, c.logger, spec)
	if err != nil {
		return nil, err
	}
	result, err := w.do(ctx)
	if result != nil && result.Error != "" {
		call.finish(errors.New(result.Error))
		return result, nil
	}
	call.finish(err)
	return result, err
}

// PlanConsoleWrite is a console write's plan and hash, as an approval of it
// binds it: the console's confirm step shows it and sends the hash back. It
// writes nothing and is recorded as a dry run.
func (c *InProcessClient) PlanConsoleWrite(ctx context.Context, req ConsoleWriteRequest, options ...MutationOption) (*ConnectorPlan, error) {
	w, err := c.consoleWrite(req)
	if err != nil {
		return nil, err
	}
	spec := w.spec(ApplyMutationOptions(options), c.runtime.audit)
	spec.planOnly, spec.dryRun, spec.approvalID, spec.confirmedPlanHash = true, true, "", ""
	call, err := beginGated(ctx, c.runtime.audit, slog.Default(), spec)
	if err != nil {
		return nil, err
	}
	shown, err := showPlan(ctx, spec)
	call.finish(err)
	return shown, err
}

// consoleWriteCall is one checked console write against this process's
// config: what it targets, what it records and how it runs.
type consoleWriteCall struct {
	req     ConsoleWriteRequest
	cfgPath string
	store   secret.ReadWriter
	target  map[string]any
	config  map[string]any
	// labels is the definition the target's labels come from; nil, or a
	// nil answer, is a target with none of its own, which reads as unknown.
	labels func() *config.ResourceDef
	// planned adds the write's own part to its plan: what it would change,
	// as observed now, so a plan confirmed against one state is stale
	// against another.
	// digest is the audit log's keyed digest, for a value that must be
	// bound but never shown, such as a credential.
	planned func(p *plan.Plan, digest func(any) string)
}

func (c *InProcessClient) consoleWrite(req ConsoleWriteRequest) (*consoleWriteCall, error) {
	if c.cfgPath == "" {
		return nil, errors.New("this Cerberus has no config path, so it has no state for the console to write")
	}
	w := &consoleWriteCall{req: req, cfgPath: c.cfgPath, store: c.consoleSecrets}
	switch req.Operation {
	case ConsoleProfileSave:
		return w, w.checkProfileSave()
	case ConsoleProfileDelete:
		return w, w.checkProfileDelete()
	case ConsoleProviderSave:
		return w, w.checkProviderSave()
	case ConsoleRegistryRegister:
		return w, w.checkRegistryRegister()
	case ConsoleRegistryDeregister:
		return w, w.checkRegistryDeregister()
	case ConsoleConfigRestore:
		w.checkConfigRestore()
		return w, nil
	}
	return nil, consoleInputError("unknown console write %q", req.Operation)
}

func (w *consoleWriteCall) spec(opts MutationOpts, sink audit.Sink) auditSpec {
	cfg := map[string]any{}
	for k, v := range w.target {
		cfg[k] = v
	}
	for k, v := range w.config {
		if _, taken := cfg[k]; !taken {
			cfg[k] = v
		}
	}
	spec := auditSpec{
		connector: ConsoleConnector, operation: w.req.Operation, op: consoleWriteOperation(w.req.Operation), known: true,
		// The console's own form is the acknowledgment: the operator pressed
		// Save, Delete, Register or Restore.
		acknowledged: true, config: cfg,
		approvalID: opts.ApprovalID, confirmedPlanHash: opts.ConfirmedPlanHash,
	}
	id, _ := w.target["id"].(string)
	// The target is named by its id either way, so the approver types what
	// is being changed; one with no labels of its own reads as unknown.
	spec.resources = func(name string) (*config.ResourceDef, bool) {
		if name != id {
			return nil, false
		}
		if w.labels != nil {
			if def := w.labels(); def != nil {
				return def, true
			}
		}
		return &config.ResourceDef{ID: id}, true
	}
	planSpec := spec
	spec.plan = func(context.Context) (plan.Plan, error) {
		tgt, _ := auditTarget(planSpec)
		p := plan.Plan{Lane: plan.LaneConsole, Connector: ConsoleConnector, Operation: w.req.Operation, Effect: string(contract.EffectAdmin),
			Target: tgt, ArgsDigest: sink.Digest(planSpec.config)}
		if w.planned != nil {
			w.planned(&p, sink.Digest)
		}
		return p, nil
	}
	return spec
}

// profileTarget names a deploy profile and its labels for the record, in
// the clear: relabeling a target is what changes its approval channel.
func profileTarget(p infra.DeploymentProfile) map[string]any {
	labels := p.ResourceDef().TargetLabels().Labels
	return map[string]any{"id": p.ID, "env": string(labels.Env), "owner": labels.Owner, "admin": labels.Admin.String()}
}

func (w *consoleWriteCall) savedProfile(id string) (infra.DeploymentProfile, bool) {
	state, err := infra.LoadState(w.cfgPath)
	if err != nil {
		return infra.DeploymentProfile{}, false
	}
	return state.Profile(id)
}

func (w *consoleWriteCall) checkProfileSave() error {
	if w.req.Profile == nil {
		return consoleInputError("profile_save needs a profile")
	}
	profile := *w.req.Profile
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.RepoPath) == "" {
		return consoleInputError("id, name, provider, and repo_path are required")
	}
	// Labels are checked as a resource's are: a misspelled env must not
	// quietly read as unknown.
	if problems := profile.ResourceDef().TargetLabels().Validate(); len(problems) > 0 {
		return consoleInputError("deployment profile labels: %s", strings.Join(problems, "; "))
	}
	w.target = profileTarget(profile)
	w.config = map[string]any{"profile": profile}
	// A new profile, or one whose labels stay as they are, is labeled by
	// itself. One whose labels change is labeled by neither: relabeling
	// is what moves a target between approval channels, so it is approved
	// as an unlabeled target is, out of band, in either direction.
	w.labels = func() *config.ResourceDef {
		incoming := profile.ResourceDef()
		if saved, ok := w.savedProfile(profile.ID); ok && !reflect.DeepEqual(saved.ResourceDef().TargetLabels(), incoming.TargetLabels()) {
			return nil
		}
		return &incoming
	}
	w.planned = func(p *plan.Plan, _ func(any) string) {
		p.Digests = map[string]string{"profile": canonicalDigest(profile)}
		if saved, ok := w.savedProfile(profile.ID); ok {
			p.State = "replaces the saved profile"
			p.Digests["saved"] = canonicalDigest(saved)
		} else {
			p.State = "adds a profile"
		}
	}
	return nil
}

func (w *consoleWriteCall) checkProfileDelete() error {
	id := strings.TrimSpace(w.req.ID)
	if id == "" {
		return consoleInputError("profile_delete needs an id")
	}
	w.target = map[string]any{"id": id}
	// A deleted profile is labeled as it was saved.
	w.labels = func() *config.ResourceDef {
		saved, ok := w.savedProfile(id)
		if !ok {
			return nil
		}
		def := saved.ResourceDef()
		return &def
	}
	w.planned = func(p *plan.Plan, _ func(any) string) {
		if saved, ok := w.savedProfile(id); ok {
			p.State = "deletes the saved profile"
			p.Digests = map[string]string{"saved": canonicalDigest(saved)}
		} else {
			p.State = "no such profile"
		}
	}
	return nil
}

func (w *consoleWriteCall) checkProviderSave() error {
	id := strings.TrimSpace(w.req.ID)
	if id == "" {
		return consoleInputError("provider_save needs a provider id")
	}
	// Recorded by name: which settings and which credentials changed. The
	// values are bound by keyed digest, so a confirmed plan holds the call
	// to the values it was confirmed with, and never carries one.
	valueKeys := make([]string, 0, len(w.req.Values))
	for key := range w.req.Values {
		valueKeys = append(valueKeys, key)
	}
	var setSecrets []string
	for key, value := range w.req.Secrets {
		if strings.TrimSpace(value) != "" {
			setSecrets = append(setSecrets, key)
		}
	}
	sort.Strings(valueKeys)
	sort.Strings(setSecrets)
	cleared := append([]string(nil), w.req.ClearSecrets...)
	sort.Strings(cleared)
	w.target = map[string]any{"id": id}
	w.config = map[string]any{"values": valueKeys, "secrets_set": setSecrets, "secrets_cleared": cleared}
	w.planned = func(p *plan.Plan, digest func(any) string) {
		p.Digests = map[string]string{"values": digest(w.req.Values), "secrets": digest(w.req.Secrets)}
	}
	return nil
}

func (w *consoleWriteCall) checkRegistryRegister() error {
	path := strings.TrimSpace(w.req.Path)
	if path == "" {
		return consoleInputError("path is required")
	}
	w.target = map[string]any{"id": path, "config_path": path}
	// What is registered is the file as it reads now.
	w.planned = func(p *plan.Plan, _ func(any) string) { p.Digests = map[string]string{"config": fileDigest(path)} }
	return nil
}

func (w *consoleWriteCall) checkRegistryDeregister() error {
	owner := strings.TrimSpace(w.req.ID)
	if owner == "" {
		return consoleInputError("owner is required")
	}
	w.target = map[string]any{"id": owner}
	return nil
}

func (w *consoleWriteCall) checkConfigRestore() {
	backup := strings.TrimSpace(w.req.Path)
	w.target = map[string]any{"id": w.cfgPath, "config_path": w.cfgPath, "backup_path": backup}
	if backup == "" {
		backup = w.cfgPath + ".bak"
	}
	// The restore replaces this config with that backup, both as they read
	// now.
	w.planned = func(p *plan.Plan, _ func(any) string) {
		p.Digests = map[string]string{"backup": fileDigest(backup), "config": fileDigest(w.cfgPath)}
	}
}

// do performs the write the gate let through.
func (w *consoleWriteCall) do(ctx context.Context) (*ConsoleWriteResult, error) {
	switch w.req.Operation {
	case ConsoleProfileSave:
		state, err := infra.LoadState(w.cfgPath)
		if err != nil {
			return nil, err
		}
		state.UpsertProfile(*w.req.Profile)
		return &ConsoleWriteResult{Success: true}, infra.SaveState(w.cfgPath, state)
	case ConsoleProfileDelete:
		state, err := infra.LoadState(w.cfgPath)
		if err != nil {
			return nil, err
		}
		if !state.DeleteProfile(strings.TrimSpace(w.req.ID)) {
			return nil, fmt.Errorf("deployment profile %w", ErrConsoleWriteNotFound)
		}
		return &ConsoleWriteResult{Success: true}, infra.SaveState(w.cfgPath, state)
	case ConsoleProviderSave:
		return w.saveProvider(ctx)
	case ConsoleRegistryRegister:
		reg, err := registry.ForConfig(w.cfgPath)
		if err != nil {
			return nil, err
		}
		entries, err := reg.Register(strings.TrimSpace(w.req.Path))
		if err != nil {
			return nil, ConsoleWriteInputError{err}
		}
		return &ConsoleWriteResult{Success: true, Count: len(entries)}, nil
	case ConsoleRegistryDeregister:
		reg, err := registry.ForConfig(w.cfgPath)
		if err != nil {
			return nil, err
		}
		if err := reg.Deregister(strings.TrimSpace(w.req.ID)); err != nil {
			return nil, ConsoleWriteInputError{err}
		}
		return &ConsoleWriteResult{Success: true}, nil
	case ConsoleConfigRestore:
		restored, err := configops.RestoreConfigBackup(w.cfgPath, strings.TrimSpace(w.req.Path))
		if err != nil {
			// A failed restore is an answer, success false: the outcome
			// record carries the error (ConsoleWrite).
			return &ConsoleWriteResult{Error: err.Error()}, nil //nolint:nilerr // see above
		}
		return &ConsoleWriteResult{Success: true, BackupPath: restored.BackupPath, RestoredTo: restored.RestoredTo, PreRestorePath: restored.PreRestorePath}, nil
	}
	return nil, consoleInputError("unknown console write %q", w.req.Operation)
}

func (w *consoleWriteCall) saveProvider(ctx context.Context) (*ConsoleWriteResult, error) {
	id := strings.TrimSpace(w.req.ID)
	credentials := len(w.req.ClearSecrets) > 0
	for _, value := range w.req.Secrets {
		credentials = credentials || strings.TrimSpace(value) != ""
	}
	if credentials && w.store == nil {
		// Checked before anything is written, so a save is not half done.
		return nil, errors.New("this Cerberus has no credential store to write; nothing was saved")
	}
	state, err := infra.LoadState(w.cfgPath)
	if err != nil {
		return nil, err
	}
	cfg := state.Providers[id]
	if cfg.Values == nil {
		cfg.Values = map[string]string{}
	}
	for key, value := range w.req.Values {
		cfg.Values[key] = strings.TrimSpace(value)
	}
	state.Providers[id] = cfg
	if err := infra.SaveState(w.cfgPath, state); err != nil {
		return nil, err
	}
	result := &ConsoleWriteResult{Success: true}
	for key, value := range w.req.Secrets {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := w.store.Set(ctx, id, key, value); err != nil {
			return nil, fmt.Errorf("store credential %s/%s: %w", id, key, err)
		}
		result.SecretsChanged = true
	}
	for _, key := range w.req.ClearSecrets {
		if err := w.store.Delete(ctx, id, key); err != nil {
			return nil, fmt.Errorf("clear credential %s/%s: %w", id, key, err)
		}
		result.SecretsChanged = true
	}
	return result, nil
}

// canonicalDigest is v's canonical JSON, hashed.
func canonicalDigest(v any) string {
	data, err := plan.Canonical(v)
	if err != nil {
		return "(cannot be encoded)"
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

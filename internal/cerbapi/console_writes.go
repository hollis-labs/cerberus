package cerbapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/configops"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/registry"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

// Console writes are changes the web console makes to Cerberus's own state:
// a connector's declared credentials saved, a project config registered or
// deregistered, a config backup restored. Each is an admin operation, gated
// and recorded (M9).
//
// They belong to the serving process (CERB-GAP-886): the daemon holds the
// approval broker, so a write policy
// wants approved can be asked for, decided and consumed where it runs, and
// the console confirms it in the same dialog as a resource verb.

// ConsoleConnector is the connector id console writes are recorded under.
const ConsoleConnector = "console"

// The console writes.
const (
	ConsoleProviderSave       = "provider_save"
	ConsoleRegistryRegister   = "registry_register"
	ConsoleRegistryDeregister = "registry_deregister"
	ConsoleConfigRestore      = "config_restore"
)

// ConsoleWriteInputError is a console write refused for its input.
type ConsoleWriteInputError struct{ Err error }

func (e ConsoleWriteInputError) Error() string { return e.Err.Error() }
func (e ConsoleWriteInputError) Unwrap() error { return e.Err }

func consoleInputError(format string, args ...any) error {
	return ConsoleWriteInputError{fmt.Errorf(format, args...)}
}

// consoleGuidance is a console write refused for its input, with a recovery
// instruction the redaction rules must not rewrite: its arguments are names.
func consoleGuidance(format string, args ...any) error {
	return ConsoleWriteInputError{redact.Guidance(format, args...)}
}

// ConsoleWriteRequest is one console write, as the console sends it.
type ConsoleWriteRequest struct {
	Operation string `json:"operation"`
	// ID is the connector (provider_save) or the registry owner
	// (registry_deregister).
	ID string `json:"id,omitempty"`
	// Path is the config registry_register registers, or the backup
	// config_restore restores (empty: the config's own .bak).
	Path string `json:"path,omitempty"`
	// Secrets and ClearSecrets are provider_save's credentials, each one a
	// secret the connector declares. Secret values travel only to the
	// serving process, over its socket, and are never recorded, shown or
	// answered with. Values is refused when set: the console used to save
	// provider settings to a file nothing read.
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
		// The ids and paths are names, recorded in the clear.
		Target:  contract.TargetDescriptor{Kind: "console", From: []string{"id", "config_path", "backup_path"}},
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
	// declared is the connectors provider_save may write credentials for:
	// the built-ins and installed plugins, with their declared secrets.
	declared func() []contract.Definition
	target   map[string]any
	config   map[string]any
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
	w := &consoleWriteCall{req: req, cfgPath: c.cfgPath, store: c.consoleSecrets, declared: c.connectorDefinitions}
	switch req.Operation {
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
		// Save, Register, Deregister or Restore.
		acknowledged: true, config: cfg,
		approvalID: opts.ApprovalID, confirmedPlanHash: opts.ConfirmedPlanHash,
	}
	id, _ := w.target["id"].(string)
	// The target is named by its id, so the approver types what is being
	// changed. A console write's target has no labels of its own, so it
	// reads as unknown.
	spec.resources = func(name string) (*config.ResourceDef, bool) {
		if name != id {
			return nil, false
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

func (w *consoleWriteCall) checkProviderSave() error {
	id := strings.TrimSpace(w.req.ID)
	if id == "" {
		return consoleInputError("provider_save needs a provider id")
	}
	// Only a secret a connector declares is written, under its connector's
	// id: an undeclared id or key would store a credential nothing reads,
	// under a name another plugin might later declare and be handed.
	var defs []contract.Definition
	if w.declared != nil {
		defs = w.declared()
	}
	// A secret resolved per resource is read as <id>/<resource-id>/<name>,
	// so saving it connector-wide would store it where nothing reads it.
	perResource := perResourceSecretNames(defs, id)
	for key := range w.req.Secrets {
		if perResource[key] {
			return consoleGuidance("connector %q reads %s per resource, as %s/<resource-id>/%s, which the console cannot set; run `cerberus secrets set %s/<resource-id>/%s`", id, key, id, key, id, key)
		}
	}
	for _, key := range w.req.ClearSecrets {
		if perResource[key] {
			return consoleGuidance("connector %q reads %s per resource, as %s/<resource-id>/%s, which the console cannot clear", id, key, id, key)
		}
	}
	names, ok := declaredSecretNames(defs, id)
	if !ok {
		return consoleGuidance("no installed connector %q declares a credential, so there is nothing to save for it; install the plugin first", id)
	}
	// Settings are not credentials, and the console no longer writes them:
	// a plugin's settings live in connector-config.yaml, which Cerberus
	// never writes.
	if len(w.req.Values) > 0 {
		return consoleGuidance("provider_save no longer saves settings; a plugin's settings go in connector-config.yaml, which the operator edits")
	}
	var undeclared []string
	var setSecrets []string
	for key, value := range w.req.Secrets {
		if !names[key] {
			undeclared = append(undeclared, key)
			continue
		}
		if strings.TrimSpace(value) != "" {
			setSecrets = append(setSecrets, key)
		}
	}
	for _, key := range w.req.ClearSecrets {
		if !names[key] {
			undeclared = append(undeclared, key)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		declared := make([]string, 0, len(names))
		for name := range names {
			declared = append(declared, name)
		}
		sort.Strings(declared)
		return consoleGuidance("connector %q does not declare the secrets %s (it declares %s); nothing was saved",
			id, strings.Join(undeclared, ", "), strings.Join(declared, ", "))
	}
	// Recorded by name: which credentials changed. The values are bound by
	// keyed digest, so a confirmed plan holds the call to the values it was
	// confirmed with, and never carries one.
	sort.Strings(setSecrets)
	cleared := append([]string(nil), w.req.ClearSecrets...)
	sort.Strings(cleared)
	w.target = map[string]any{"id": id}
	w.config = map[string]any{"secrets_set": setSecrets, "secrets_cleared": cleared}
	w.planned = func(p *plan.Plan, digest func(any) string) {
		p.Digests = map[string]string{"secrets": digest(w.req.Secrets)}
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

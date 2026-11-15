package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/credentialbroker"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secrets"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// PluginState is one managed plugin as the lane reports it (the old
// ManagedPluginConnectorState, same JSON).
type PluginState struct {
	ID               string    `json:"id"`
	Version          string    `json:"version"`
	Path             string    `json:"path"`
	Loaded           bool      `json:"loaded"`
	Origin           string    `json:"origin,omitempty"`
	EntrypointSHA256 string    `json:"entrypoint_sha256,omitempty"`
	Source           string    `json:"source,omitempty"`
	BundleDigest     string    `json:"bundle_digest,omitempty"`
	AcceptedAt       time.Time `json:"accepted_at,omitzero"`
	ReviewPending    bool      `json:"review_pending"`
	CredentialSplit  bool      `json:"credential_split,omitempty"`
	WriteInstance    bool      `json:"write_instance,omitempty"`
	GaveUp           string    `json:"gave_up,omitempty"`
	MissingSecrets   []string  `json:"missing_secrets,omitempty"`
	Capabilities     []string  `json:"capabilities,omitempty"`
	Granted          []string  `json:"granted,omitempty"`
	ContractGaps     []string  `json:"contract_gaps"`
	ConfigFields     []string  `json:"config_fields"`
	MCPExpose        []string  `json:"mcp_expose"`
	// MCPExposeByPosture marks an exposure that came from the permissive posture.
	MCPExposeByPosture bool     `json:"mcp_expose_by_posture,omitempty"`
	ConfigSHA256       string   `json:"config_sha256,omitempty"`
	ConfigProblems     []string `json:"config_problems"`
}

// PluginHealth is a plugin's health check.
type PluginHealth struct {
	ID      string `json:"id"`
	Loaded  bool   `json:"loaded"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message,omitempty"`
}

// PluginExecArgs is a plugin operation on the direct route.
type PluginExecArgs struct {
	PluginDir    string         `json:"plugin_dir,omitempty"`
	Operation    string         `json:"operation"`
	Config       map[string]any `json:"config,omitempty"`
	DryRun       bool           `json:"dry_run,omitempty"`
	Acknowledged bool           `json:"acknowledged,omitempty"`
	// ApprovalID is the approval the call runs under.
	ApprovalID string `json:"approval_id,omitempty"`
}

// GateOpts are a gated write's forms (docs/substrate-port/console-api-contract.md section 1):
// a retry under an approval, the plan alone, or a confirmed plan.
type GateOpts struct {
	ApprovalID        string
	Plan              bool
	ConfirmedPlanHash string
}

// PluginDirNotAccepted refuses a path where only an installed plugin's id is accepted:
// running a directory executes its entrypoint.
const PluginDirNotAccepted = "plugin_dir is not accepted here: address the installed plugin by id"

// Plugins is the managed plugin lane over the adopted plugin roots.
type Plugins struct {
	s         *Service
	gate      *gate.Gate
	issuance  *credentialbroker.Broker
	manager   *pluginhost.Manager
	statePath string
	store     pluginhost.Store
	// policy is the operator's policy directory, where a review writes suggested rules.
	policy      policy.Store
	warn        io.Writer
	reservedIDs []string
	logger      *slog.Logger

	mu      sync.Mutex
	records map[string]persistedEntry
}

// hostServedIDs are connector ids the host serves outside the connector registry, reserved for
// every managed lane (H-d).
var hostServedIDs = []string{"local", "policy", "brake", "approvals", "audit", "console", "pipeline", "mcp-http", "plugin", "secrets"}

func newPlugins(s *Service) (*Plugins, error) {
	o := s.o
	p := &Plugins{
		s:           s,
		gate:        s.gate,
		issuance:    credentialbroker.New(o.Paths.Issuance(), credentialbroker.WithRecorder(o.Receipts)),
		statePath:   o.Paths.PluginState(),
		store:       pluginhost.Store{Root: o.Paths.PluginStore()},
		policy:      policy.Store{Dir: o.Paths.PolicyDir()},
		warn:        o.Stderr,
		reservedIDs: append(append([]string(nil), hostServedIDs...), s.registry.BuiltInIDs()...),
		logger:      s.logger,
		records:     map[string]persistedEntry{},
	}
	cfg := o.ConnectorConfig
	if cfg.Entries == nil {
		cfg.Entries = map[string]pluginhost.PluginSettings{}
	}
	p.manager = pluginhost.NewManager(
		nil,
		pluginhost.SubprocessLauncher{
			Transport: pluginhost.StdioTransportFactory{Stderr: o.Stderr},
			Env:       pluginLaunchEnv(),
			Shim:      o.Shim,
		},
		o.HostVersion,
		pluginhost.WithRuntimeRoots(o.Paths.PluginData(), o.Paths.PluginCache()),
		pluginhost.WithSecretResolver(s.chain),
		pluginhost.WithCoreSecretResolver(s.core),
		pluginhost.WithConnectorConfig(func() (pluginhost.ConnectorConfig, error) { return cfg, nil }),
		pluginhost.WithLoadWarning(func(line string) { p.warnf("%s", line) }),
		pluginhost.WithChangedBundles(p.acceptChangedBundle),
		pluginhost.WithRestartObserver(p.onSupervision),
		pluginhost.WithCredentialBindings(func() (secrets.BindingFile, error) { return s.bindings(), nil }),
	)
	// The vault references route to this lane's secret backends, before any plugin loads.
	s.backends.Bind(p)
	if err := p.restore(context.Background(), false); err != nil {
		return nil, err
	}
	return p, nil
}

// RestorePlugins loads every plugin the state file marks loaded, as the old daemon did at
// start: secret backends first, then the rest, each load recorded as Cerberus acting on its own
// (automation, daemon_start) and never refused, not even in a lockdown. A plugin that cannot
// load is skipped and warned about, never fatal.
func (p *Plugins) RestorePlugins(ctx context.Context) error {
	return p.restore(ctx, true)
}

// Stop stops every running plugin process at exit (plan section 4.4, step 5). The state file
// is not written: stopping at exit is not unloading, so the next start restores the same set.
func (p *Plugins) Stop(ctx context.Context) {
	var stopped []string
	for _, ip := range p.manager.InstalledPlugins() {
		if !p.manager.Loaded(ip.ID) {
			continue
		}
		if err := p.manager.Unload(ctx, ip.ID); err != nil {
			p.logger.Warn("admin.plugins.stop_failed", "plugin", ip.ID, "error", redact.Text(err.Error()))
			continue
		}
		stopped = append(stopped, ip.ID)
	}
	p.logger.Info("admin.plugins.stop", "stopped", strings.Join(stopped, ","))
}

// LoadedPlugins lists the plugins whose processes are running.
func (p *Plugins) LoadedPlugins() []string {
	var ids []string
	for _, ip := range p.manager.InstalledPlugins() {
		if p.manager.Loaded(ip.ID) {
			ids = append(ids, ip.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// Reload re-reads one plugin's entry from the state file and makes the lane's view match it.
// It takes an id only, so it applies only what a review accepted, and a bundle that does not
// match its accepted digest is refused before the running plugin is touched.
func (p *Plugins) Reload(ctx context.Context, id string, opts GateOpts) (PluginState, *gate.ConnectorPlan, error) {
	return p.admin(ctx, "reload", id, opts, func() (PluginState, error) { return p.reloadEntry(ctx, id) })
}

// Load starts an installed plugin and records it loaded.
func (p *Plugins) Load(ctx context.Context, id string, opts GateOpts) (PluginState, *gate.ConnectorPlan, error) {
	return p.admin(ctx, "load", id, opts, func() (PluginState, error) {
		if err := p.manager.Load(context.WithoutCancel(ctx), id); err != nil {
			return PluginState{}, loadError(err)
		}
		installed, _ := p.manager.Installed(id)
		if err := p.persist(); err != nil {
			return PluginState{}, err
		}
		return p.state(installed, true), nil
	})
}

// Unload stops a plugin and records it unloaded.
func (p *Plugins) Unload(ctx context.Context, id string, opts GateOpts) (PluginState, *gate.ConnectorPlan, error) {
	return p.admin(ctx, "unload", id, opts, func() (PluginState, error) {
		if err := p.manager.Unload(ctx, id); err != nil {
			return PluginState{}, err
		}
		installed, _ := p.manager.Installed(id)
		if err := p.persist(); err != nil {
			return PluginState{}, err
		}
		return managedState(installed, false), nil
	})
}

// Uninstall unloads the plugin if needed and drops it from the managed set and the state file;
// only Cerberus's own store copy is removed.
func (p *Plugins) Uninstall(ctx context.Context, id string, opts GateOpts) (PluginState, *gate.ConnectorPlan, error) {
	return p.admin(ctx, "uninstall", id, opts, func() (PluginState, error) {
		installed, ok := p.manager.Installed(id)
		if !ok {
			return PluginState{}, fmt.Errorf("plugin %q is not installed", id)
		}
		if p.manager.Loaded(id) {
			if err := p.manager.Unload(ctx, id); err != nil {
				return PluginState{}, err
			}
		}
		if err := p.manager.Remove(id); err != nil {
			return PluginState{}, err
		}
		p.mu.Lock()
		entry := p.records[id]
		delete(p.records, id)
		p.mu.Unlock()
		if err := updatePluginState(p.statePath, func(st *persistedState) error {
			if i := st.findEntry(id); i >= 0 {
				st.Entries = append(st.Entries[:i], st.Entries[i+1:]...)
			}
			return nil
		}); err != nil {
			return PluginState{}, err
		}
		if p.store.Owns(entry.PluginDir) || p.store.Owns(installed.Path) {
			if err := p.store.Remove(id); err != nil {
				p.warnf("plugin %q uninstalled, but its store copy was not removed: %v", id, err)
			}
		}
		if err := p.persist(); err != nil {
			return PluginState{}, err
		}
		return managedState(installed, false), nil
	})
}

// List is every installed plugin.
func (p *Plugins) List(context.Context) []PluginState {
	plugins := p.manager.InstalledPlugins()
	out := make([]PluginState, 0, len(plugins))
	for _, ip := range plugins {
		out = append(out, p.state(ip, p.manager.Loaded(ip.ID)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Definitions is every installed plugin's connector definition.
func (p *Plugins) Definitions() []contract.Definition {
	plugins := p.manager.InstalledPlugins()
	out := make([]contract.Definition, 0, len(plugins))
	for _, ip := range plugins {
		out = append(out, contract.DefinitionFromManifest(ip.Manifest))
	}
	return out
}

// LiveDefinitions is every loaded plugin's connector definition.
func (p *Plugins) LiveDefinitions() []contract.Definition {
	plugins := p.manager.InstalledPlugins()
	out := make([]contract.Definition, 0, len(plugins))
	for _, ip := range plugins {
		if p.manager.Loaded(ip.ID) {
			out = append(out, contract.DefinitionFromManifest(ip.Manifest))
		}
	}
	return out
}

// Installed reports whether id is an installed plugin.
func (p *Plugins) Installed(id string) bool {
	_, ok := p.manager.Installed(id)
	return ok
}

// Loaded reports whether id's process is running.
func (p *Plugins) Loaded(id string) bool { return p.manager.Loaded(id) }

// Health asks a loaded plugin for its health.
func (p *Plugins) Health(ctx context.Context, id string) (PluginHealth, error) {
	h, err := p.manager.Health(ctx, id)
	if err != nil {
		return PluginHealth{}, err
	}
	return PluginHealth{ID: h.ID, Loaded: h.Loaded, Healthy: h.Healthy, Message: h.Message}, nil
}

// Execute runs a plugin operation on the direct route, through the gate as the admin lane runs
// it, with the admin lane's plugin plan.
func (p *Plugins) Execute(ctx context.Context, id string, args PluginExecArgs) (OperationResult, error) {
	ctx, scope := redact.EnsureScope(ctx)
	spec := gate.Spec{Connector: id, Operation: args.Operation, Config: args.Config, Acknowledged: args.Acknowledged, DryRun: args.DryRun,
		ApprovalID: args.ApprovalID, Resources: p.s.lookup()}
	if args.DryRun {
		spec.Preview = audit.PreviewPluginClaimed
	}
	spec.PluginConfigSHA256, spec.PluginEntrypointSHA256 = p.fingerprints(id)
	for _, def := range p.Definitions() {
		if def.ID == id {
			spec.Op, spec.Known = def.Operation(args.Operation)
			spec.Credentials = labeledCredentialNames(def, credentialScope(spec), p.s.bindings())
		}
	}
	planSpec := spec
	digest := p.gate.Sink().Digest
	spec.Plan = func(ctx context.Context) (plan.Plan, error) {
		tgt, _ := gate.AuditTarget(planSpec)
		pl := plan.Plan{Lane: plan.LaneAdmin, Connector: id, Operation: args.Operation, Effect: string(planSpec.Op.Effect), Target: tgt, ArgsDigest: digest(args.Config)}
		return pl, p.planPlugin(ctx, &pl, planSpec.Op, id, args.Config, args.Acknowledged)
	}
	ctx = withCredentialScope(ctx, spec)
	call, err := p.gate.Begin(ctx, spec)
	if err != nil {
		return OperationResult{}, err
	}
	res, err := p.execute(call.WithTelemetry(ctx), id, args)
	if err == nil && call.EgressApplies() {
		var shaped any
		if shaped, err = call.Shape(res.Data); err == nil {
			res.Data = shaped
		} else {
			res = OperationResult{}
		}
	}
	call.Finish(err)
	return res, scopeError(scope, err)
}

// execute runs a plugin operation unrecorded, for a caller that has begun the call: the
// credential issuance an issuing operation needs, prepared after the gate, then the call.
func (p *Plugins) execute(ctx context.Context, id string, args PluginExecArgs) (OperationResult, error) {
	opArgs := OperationArgs{Connector: id, Operation: args.Operation}
	issued, issueErr := p.prepareIssuance(ctx, id, args)
	if issueErr != nil {
		return OperationResult{}, pluginExecuteError(opArgs, issueErr)
	}
	out, err := p.manager.ExecuteOperation(ctx, pluginhost.OperationArgs{
		Issuance: issued, Connector: id, Operation: args.Operation, Config: args.Config, DryRun: args.DryRun, Acknowledged: args.Acknowledged,
	})
	var receipt *credentialbroker.Receipt
	if issued != nil {
		if issued.Receipt().State == "dispatching" {
			success := err == nil && out.OutputCap == nil
			if finishErr := issued.Finish(success); finishErr != nil {
				err = redact.Guidance("credential operation outcome could not be persisted; inspect the target and issuance record before retrying")
			}
		}
		r := issued.Receipt()
		receipt = &r
	}
	if err != nil {
		return OperationResult{}, pluginExecuteError(opArgs, err)
	}
	return OperationResult{Connector: out.Connector, CredentialIssuance: receipt, Operation: out.Operation, Data: out.Data}, nil
}

// prepareIssuance reserves and persists an issuing operation's credentials, after the gate:
// a preview or plan never reserves an id or generates a secret, and no value enters the
// arguments, a preview or the audit record.
func (p *Plugins) prepareIssuance(ctx context.Context, id string, args PluginExecArgs) (*credentialbroker.Dispatch, error) {
	if args.DryRun {
		return nil, nil
	}
	for _, def := range p.Definitions() {
		if def.ID != id {
			continue
		}
		op, ok := def.Operation(args.Operation)
		if !ok || len(op.IssuedSecrets) == 0 {
			return nil, nil
		}
		if err := p.manager.CheckOperation(pluginhost.OperationArgs{Connector: id, Operation: args.Operation, Config: args.Config, Acknowledged: args.Acknowledged}); err != nil {
			return nil, err
		}
		if err := op.CheckInputs(args.Config, true); err != nil {
			return nil, redact.GuidanceWrap(err, "credential issuance inputs refused")
		}
		issuanceID, _ := args.Config["issuance_id"].(string)
		configDigest, entrypointDigest := p.fingerprints(id)
		binding := credentialbroker.Binding{ID: issuanceID, Connector: id, Operation: args.Operation,
			Digest: p.gate.Sink().Digest(map[string]any{"connector": id, "operation": args.Operation, "arguments": args.Config, "config": configDigest, "entrypoint": entrypointDigest})}
		d, err := p.issuance.Prepare(ctx, binding, op.IssuedSecrets)
		if err != nil {
			return nil, redact.GuidanceWrap(err, "credential issuance refused before dispatch")
		}
		for name, value := range d.Values() {
			redact.ScopeFrom(ctx).Add(name, value)
		}
		return d, nil
	}
	return nil, nil
}

// admin runs a plugin lifecycle call through the gate: its plan alone, or the call, recorded.
func (p *Plugins) admin(ctx context.Context, operation, id string, opts GateOpts, run func() (PluginState, error)) (PluginState, *gate.ConnectorPlan, error) {
	ctx, scope := redact.EnsureScope(ctx)
	call, err := p.gate.Begin(ctx, p.adminSpec(operation, id, opts))
	if err != nil {
		return PluginState{}, nil, err
	}
	if opts.Plan {
		shown, planErr := call.ShowPlan(ctx)
		call.Finish(planErr)
		return PluginState{}, shown, scopeError(scope, planErr)
	}
	st, err := run()
	call.Finish(err)
	return st, nil, scopeError(scope, err)
}

// adminSpec is a plugin lifecycle call's gate spec: connector "plugin", the admin operation's
// contract, the plugin by id, and a plan binding the bundle it would act on.
func (p *Plugins) adminSpec(operation, id string, opts GateOpts) gate.Spec {
	spec := gate.Spec{Connector: "plugin", Operation: operation, Op: pluginAdminOperation(operation), Known: true,
		Config: map[string]any{"id": id}, ApprovalID: opts.ApprovalID, PlanOnly: opts.Plan, DryRun: opts.Plan,
		ConfirmedPlanHash: opts.ConfirmedPlanHash}
	planSpec := spec
	digest := p.gate.Sink().Digest
	spec.Plan = func(context.Context) (plan.Plan, error) {
		tgt, _ := gate.AuditTarget(planSpec)
		pl := plan.Plan{Lane: plan.LaneAdmin, Connector: "plugin", Operation: operation, Effect: string(contract.EffectAdmin), Target: tgt, ArgsDigest: digest(planSpec.Config)}
		installed, ok := p.manager.Installed(id)
		pl.Digests = map[string]string{"bundle": "(not installed)"}
		if ok {
			pl.Digests["bundle"] = orUnreviewed(installed.BundleDigest)
			pl.PluginEntrypointSHA256 = installed.EntrypointSHA256
		}
		if p.manager.Loaded(id) {
			pl.State = "loaded"
		} else {
			pl.State = "not loaded"
		}
		return pl, nil
	}
	return spec
}

func orUnreviewed(digest string) string {
	if digest == "" {
		return "(review pending)"
	}
	return digest
}

// pluginAdminOperation is the contract plugin lifecycle calls are recorded under: admin, since
// it changes what Cerberus can run.
func pluginAdminOperation(name string) contract.Operation {
	return contract.Operation{
		Name: name, Effect: contract.EffectAdmin,
		Target:  contract.TargetDescriptor{Kind: "plugin", From: []string{"id", "plugin_dir"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites,
	}.Finalize()
}

func managedState(ip pluginhost.InstalledPlugin, loaded bool) PluginState {
	declared := make([]string, 0, len(ip.Spec.Capabilities))
	for _, req := range ip.Spec.Capabilities {
		declared = append(declared, req.Name)
	}
	granted := ip.Granted
	if len(granted) == 0 {
		granted = pluginhost.GrantCapabilities(ip.Spec.Capabilities)
	}
	return PluginState{
		ID: ip.ID, Version: ip.Version, Path: ip.Path, Loaded: loaded, Origin: string(ip.Origin),
		EntrypointSHA256: ip.EntrypointSHA256, BundleDigest: ip.BundleDigest, ReviewPending: ip.ReviewPending,
		Capabilities: declared, ContractGaps: ip.Manifest.ContractGaps(), Granted: granted,
	}
}

func (p *Plugins) state(ip pluginhost.InstalledPlugin, loaded bool) PluginState {
	out := managedState(ip, loaded)
	p.mu.Lock()
	if entry, ok := p.records[ip.ID]; ok {
		out.Source = entry.Source
		out.AcceptedAt = entry.AcceptedAt
	}
	p.mu.Unlock()
	if loaded {
		out.MissingSecrets = p.manager.MissingSecrets(ip.ID)
		if settings, ok := p.manager.Settings(ip.ID); ok {
			out.ConfigFields = settings.Fields
			out.MCPExpose = settings.Expose
			if !settings.ExposeDeclared && p.gate.Policy().GlobalPosture() == policy.PosturePermissive {
				out.MCPExpose = pluginhost.ExposableOperations(ip)
				out.MCPExposeByPosture = true
			}
			out.ConfigSHA256 = settings.SHA256
		}
	}
	out.ConfigProblems = p.manager.ConfigProblems(ip.ID)
	out.GaveUp = p.manager.GaveUp(ip.ID)
	out.CredentialSplit, out.WriteInstance = p.manager.WriteInstanceRunning(ip.ID)
	if out.ContractGaps == nil {
		out.ContractGaps = []string{}
	}
	if out.ConfigFields == nil {
		out.ConfigFields = []string{}
	}
	if out.MCPExpose == nil {
		out.MCPExpose = []string{}
	}
	if out.ConfigProblems == nil {
		out.ConfigProblems = []string{}
	}
	return out
}

// inspect reads a state entry into the plugin it describes, without registering it.
func (p *Plugins) inspect(entry persistedEntry) (pluginhost.InstalledPlugin, error) {
	policyFor := pluginhost.LocalInstallPolicy()
	if entry.Options.DevMode {
		policyFor = pluginhost.DeveloperInstallPolicy(entry.PluginDir)
	}
	installer := pluginhost.DirectoryInstaller{Policy: policyFor, ReservedIDs: p.reservedIDs}
	installed, err := installer.Install(context.Background(), entry.PluginDir)
	if err != nil {
		return pluginhost.InstalledPlugin{}, err
	}
	installed.BundleDigest = entry.BundleDigest
	installed.ReviewPending = entry.reviewPending()
	return installed, nil
}

// register makes a state entry one of the lane's installed plugins.
func (p *Plugins) register(entry persistedEntry) (pluginhost.InstalledPlugin, error) {
	installed, err := p.inspect(entry)
	if err != nil {
		return pluginhost.InstalledPlugin{}, err
	}
	// One claimant per secret reference scheme.
	if b := installed.Spec.Cerberus.SecretBackend; b != nil {
		if other := p.manager.SchemeClaimant(b.Scheme); other != "" && other != installed.ID {
			return pluginhost.InstalledPlugin{}, fmt.Errorf("plugin %q claims the %s:// secret scheme, which plugin %q already claims; uninstall %q first",
				installed.ID, b.Scheme, other, other)
		}
	}
	p.manager.RegisterInstalled(installed)
	entry.ID = installed.ID
	p.setRecord(installed.ID, entry)
	return installed, nil
}

func (p *Plugins) setRecord(id string, entry persistedEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records[id] = entry
}

// persist records which plugins are loaded: only that, under the state file's lock, keeping
// every entry it does not know.
func (p *Plugins) persist() error {
	p.mu.Lock()
	ids := make([]string, 0, len(p.records))
	for id := range p.records {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	return updatePluginState(p.statePath, func(st *persistedState) error {
		for _, id := range ids {
			if i := st.findEntry(id); i >= 0 {
				st.Entries[i].ID = id
				st.Entries[i].Loaded = p.manager.Loaded(id)
			}
		}
		return nil
	})
}

// restore registers the state file's plugins and, when load is set, loads the ones marked
// loaded, in phases (secret backends first), each recorded as automation via daemon_start.
func (p *Plugins) restore(ctx context.Context, load bool) error {
	state, err := readPluginState(p.statePath)
	if err != nil {
		return err
	}
	var toLoad []pluginhost.InstalledPlugin
	for _, entry := range state.Entries {
		var installed pluginhost.InstalledPlugin
		if installed, err = p.registered(entry); err != nil {
			p.warnf("skipping plugin %q: %v\n  the registration is kept; reinstall or uninstall it to drop it", entry.PluginDir, err)
			continue
		}
		if !load && installed.ReviewPending {
			p.warnf("plugin %q was installed before install review and loads unchecked until reviewed", installed.ID)
		}
		if entry.Loaded && load && !p.manager.Loaded(installed.ID) {
			toLoad = append(toLoad, installed)
		}
	}
	phases := map[int][]pluginhost.InstalledPlugin{}
	var order []int
	for _, ip := range toLoad {
		n := restorePhase(ip)
		if _, ok := phases[n]; !ok {
			order = append(order, n)
		}
		phases[n] = append(phases[n], ip)
	}
	sort.Ints(order)
	for _, n := range order {
		var wg sync.WaitGroup
		for _, ip := range phases[n] {
			wg.Add(1)
			go func(ip pluginhost.InstalledPlugin) {
				defer wg.Done()
				// A restart starts new code, so the load is recorded, as Cerberus acting on its own.
				// It is never refused: an unwritable log is logged, and the plugin still loads.
				call, _ := p.gate.BeginAudit(ctx, restoreSpec(ip))
				loadErr := p.manager.Load(ctx, ip.ID)
				call.Finish(loadError(loadErr))
				if loadErr != nil {
					p.warnf("plugin %q installed but failed to load: %v", ip.ID, loadErr)
					return
				}
				p.logger.Info("admin.plugins.restored", "plugin", ip.ID, "bundle_digest", ip.BundleDigest)
			}(ip)
		}
		wg.Wait()
	}
	return nil
}

// registered is entry's plugin: registered now, or as registered at construction.
func (p *Plugins) registered(entry persistedEntry) (pluginhost.InstalledPlugin, error) {
	if entry.ID != "" {
		if ip, ok := p.manager.Installed(entry.ID); ok {
			return ip, nil
		}
	}
	return p.register(entry)
}

// restorePhase puts a secret backend first: other plugins resolve credentials through it.
func restorePhase(ip pluginhost.InstalledPlugin) int {
	if ip.Spec.Cerberus.SecretBackend != nil {
		return 0
	}
	return 1
}

// restoreSpec is the record of a plugin loaded at start: automation via daemon_start, with
// whether its bundle was checked against an accepted review.
func restoreSpec(ip pluginhost.InstalledPlugin) gate.Spec {
	checked := "bundle " + ip.BundleDigest + " checked against its accepted review"
	if ip.ReviewPending || ip.BundleDigest == "" {
		checked = "unchecked: its review is pending"
	}
	return gate.Spec{Connector: "plugin", Operation: "load", Op: pluginAdminOperation("load"), Known: true,
		Config: map[string]any{"id": ip.ID, "plugin_dir": ip.Path}, Automation: true, AutomationVia: "daemon_start",
		Reason: "restore at daemon start; " + checked, PluginEntrypointSHA256: ip.EntrypointSHA256}
}

// PluginChangedAcceptedByPosture is the outcome code of a load that let a changed bundle
// through because the global posture is permissive.
const PluginChangedAcceptedByPosture = "plugin_changed_accepted_by_posture"

// acceptChangedBundle is the permissive posture's "load, and warn" for a plugin that is not the
// bundle its review accepted, recorded first; under the secure posture it refuses.
func (p *Plugins) acceptChangedBundle(changed *pluginhost.ChangedError) bool {
	if p.gate.Policy().GlobalPosture() != policy.PosturePermissive {
		return false
	}
	if err := recordChangedBundleAccepted(p.gate.Sink(), changed); err != nil {
		p.warnf("plugin %q changed since its review and was NOT loaded: the permissive posture loads it only with an audit record, and the record could not be written: %s", changed.ID, redact.Text(err.Error()))
		return false
	}
	p.warnf("WARNING: plugin %q is not the bundle you reviewed (it is %s, the review was for %s); it is loading anyway because the posture is permissive",
		changed.ID, changed.Found, changed.Accepted)
	return true
}

func recordChangedBundleAccepted(sink audit.Sink, changed *pluginhost.ChangedError) error {
	if sink == nil {
		return errors.New("no audit sink")
	}
	rec := audit.Record{Kind: audit.KindIntent, OperationID: audit.NewID(),
		Principal: audit.Principal{Kind: audit.PrincipalAutomation, Surface: string(gate.SurfaceUnknown), Via: "posture"},
		Connector: "plugin", Operation: "load", Effect: string(contract.EffectAdmin),
		Target:  audit.Target{Kind: "plugin", Fields: map[string]string{"id": changed.ID, "accepted": changed.Accepted, "found": changed.Found}},
		Reason:  "the bundle is not the one its review accepted, and the global posture is permissive, which loads it with a warning",
		Posture: policy.PosturePermissive}
	if _, err := sink.Write(rec); err != nil {
		return err
	}
	rec.Kind, rec.ID = audit.KindOutcome, ""
	rec.Decision, rec.OutcomeCode = audit.DecisionAllowed, PluginChangedAcceptedByPosture
	_, err := sink.Write(rec)
	return err
}

// loadError codes a refused load: a bundle that is not the one reviewed is plugin_changed.
func loadError(err error) error {
	if errors.Is(err, pluginhost.ErrPluginChanged) {
		return gate.NewError("plugin", "load", gate.CodePluginChanged, err)
	}
	return err
}

// fingerprints are a loaded plugin's config and entrypoint hashes, for its records.
func (p *Plugins) fingerprints(id string) (cfg, entrypoint string) {
	if p == nil || !p.manager.Loaded(id) {
		return "", ""
	}
	if settings, ok := p.manager.Settings(id); ok {
		cfg = settings.SHA256
	}
	if installed, ok := p.manager.Installed(id); ok {
		entrypoint = installed.EntrypointSHA256
	}
	return cfg, entrypoint
}

// onSupervision records what the plugin supervisor did: each step is Cerberus acting on its
// own, and giving up also tells the operator.
func (p *Plugins) onSupervision(ev pluginhost.RestartEvent) {
	installed, _ := p.manager.Installed(ev.ID)
	spec := restoreSpec(installed)
	spec.Operation = "supervise"
	spec.Reason = fmt.Sprintf("plugin %s %s: %s", ev.ID, strings.ReplaceAll(ev.Kind, "_", " "), ev.Reason)
	if ev.Attempt > 0 {
		spec.Reason += fmt.Sprintf(" (restart %d of %d in 10m)", ev.Attempt, 3)
	}
	call, _ := p.gate.BeginAudit(context.Background(), spec)
	call.Finish(ev.Err)
	switch ev.Kind {
	case "stopped", "restart_failed":
		p.warnf("%s", spec.Reason)
	case "gave_up":
		msg := fmt.Sprintf("plugin %s was stopped and restarted 3 times in 10 minutes (%s); it stays unloaded until it is loaded again", ev.ID, ev.Reason)
		p.warnf("%s", msg)
		p.gate.Notify("Cerberus: plugin "+ev.ID+" stopped", msg)
	}
}

// Claims reports whether an installed plugin claims scheme as a secret backend.
func (p *Plugins) Claims(scheme string) bool { return p.manager.SchemeClaimant(scheme) != "" }

// ResolveSecret resolves a vault reference through the backend that claims its scheme: the
// credential chain's router, reached by no surface.
func (p *Plugins) ResolveSecret(ctx context.Context, ref string) (string, error) {
	return p.manager.ResolveSecret(ctx, ref)
}

// Backend names the plugin that resolves scheme, as id@version.
func (p *Plugins) Backend(scheme string) string { return p.manager.BackendLabel(scheme) }

func (p *Plugins) warnf(format string, args ...any) {
	if p == nil || p.warn == nil {
		return
	}
	_, _ = fmt.Fprintf(p.warn, "cerberus: managed plugin: "+format+"\n", args...)
}

// pluginLaunchEnv is the base allow-list every plugin subprocess inherits: no credential and no
// credential handle (CERB-GAP-837); those are unlocked by a declared capability.
func pluginLaunchEnv() []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true, "USER": true,
		"LOGNAME": true, "LANG": true, "LC_ALL": true, "XDG_RUNTIME_DIR": true}
	var out []string
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if allowed[key] || strings.HasPrefix(key, "GO_WANT_") {
			out = append(out, entry)
		}
	}
	return out
}

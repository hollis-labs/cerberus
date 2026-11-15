package admin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/hollis-labs/cerberus/internal/audit"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/credentialbroker"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// OperationArgs is one connector operation as a caller asked for it (the old
// ExternalConnectorOperationArgs, same JSON).
type OperationArgs struct {
	Connector    string         `json:"connector"`
	Operation    string         `json:"operation"`
	Config       map[string]any `json:"config,omitempty"`
	DryRun       bool           `json:"dry_run,omitempty"`
	Acknowledged bool           `json:"acknowledged,omitempty"`
	// ApprovalID is the approval the call runs under: the retry after it was approved.
	ApprovalID string `json:"approval_id,omitempty"`
	// Plan asks for the call's plan instead of running it.
	Plan bool `json:"plan,omitempty"`
	// ConfirmedPlanHash is the plan a person confirmed; only a confirm route sets it.
	ConfirmedPlanHash string `json:"-"`
	// BreakGlass is a person breaking glass on their own call; only a break-glass route sets it.
	BreakGlass *gate.BreakGlassRequest `json:"-"`
}

// OperationResult is what an operation answered (the old ExternalConnectorOperationResult).
type OperationResult struct {
	CredentialIssuance *credentialbroker.Receipt `json:"credential_issuance,omitempty"`
	Connector          string                    `json:"connector"`
	Operation          string                    `json:"operation"`
	Data               any                       `json:"data"`
}

// DryRunPreview is a built-in operation's host preview.
type DryRunPreview struct {
	DryRun    bool           `json:"dry_run"`
	Connector string         `json:"connector"`
	Operation string         `json:"operation"`
	Summary   string         `json:"summary"`
	Target    map[string]any `json:"target,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
}

// Definitions is every connector the lane serves: the built-ins and the installed plugins.
func (s *Service) Definitions() []contract.Definition {
	defs := make(map[string]contract.Definition)
	for _, def := range s.registry.Definitions() {
		defs[def.ID] = def
	}
	for _, def := range s.plugins.Definitions() {
		defs[def.ID] = def
	}
	return sortedDefinitions(defs)
}

// LiveDefinitions is the connectors that can be constructed now: a built-in whose probe
// passes (no network call), and a loaded plugin.
func (s *Service) LiveDefinitions(ctx context.Context) []contract.Definition {
	defs := make(map[string]contract.Definition)
	for _, def := range s.registry.Definitions() {
		if s.registry.Probe(ctx, def.ID) == nil {
			defs[def.ID] = def
		}
	}
	for _, def := range s.plugins.LiveDefinitions() {
		defs[def.ID] = def
	}
	return sortedDefinitions(defs)
}

// Execute runs one admin-lane operation through the gate, as the old Execute did: one resolve
// of each configured resource for the call (M10), the credential scope set before the gate,
// the intent written before anything else, a local path held to the transfer root, the plan
// shown or the operation run, egress on the result, and the outcome on every exit.
func (s *Service) Execute(ctx context.Context, args OperationArgs) (OperationResult, error) {
	ctx, scope := redact.EnsureScope(ctx)
	if args.Plan {
		args.DryRun, args.ApprovalID, args.ConfirmedPlanHash, args.BreakGlass = true, "", "", nil
	}
	ctx = withResourceLookup(ctx, onceLookup(s.lookup()))
	// The plan function reads the call's arguments through current: a plan request is shown
	// with its local path as confined, as the old build showed it.
	current := args
	spec := s.specIn(ctx, args, &current)
	ctx = withCredentialScope(ctx, spec)
	call, err := s.gate.Begin(ctx, spec)
	if err != nil {
		return OperationResult{}, err
	}
	if spec.Known {
		confined, confineErr := s.confineLocalPath(args, spec.Op, call.Intent().Principal)
		if confineErr != nil {
			call.Finish(confineErr)
			return OperationResult{}, scopeError(scope, confineErr)
		}
		args, current = confined, confined
	}
	if args.Plan {
		shown, planErr := call.ShowPlan(ctx)
		call.Finish(planErr)
		if planErr != nil {
			return OperationResult{}, scopeError(scope, planErr)
		}
		return OperationResult{Connector: args.Connector, Operation: args.Operation, Data: *shown}, nil
	}
	result, err := s.execute(call.WithTelemetry(ctx), args)
	err = noCredentialRefusal(args, err)
	if err == nil && call.EgressApplies() {
		// Egress policy on what comes back (P4-4); a plugin's preview gets the same (M8).
		var shaped any
		if shaped, err = call.Shape(result.Data); err == nil {
			result.Data = shaped
		} else {
			result = OperationResult{}
		}
	}
	call.Finish(err)
	return result, scopeError(scope, err)
}

// scopeError renders err through the request's scope, where the credentials resolved for the
// call are still known. A gate error's cause is wrapped in place, so it keeps its code.
func scopeError(scope *redact.Scope, err error) error {
	if err == nil {
		return nil
	}
	if coded, ok := err.(*gate.Error); ok { //nolint:errorlint // the top-level error only; a wrapped one is handled below
		if coded.Err != nil {
			coded.Err = scope.Error(coded.Err)
			scope.MarkRendered(coded.Err.Error())
		}
		scope.MarkRendered(coded.Error())
		return coded
	}
	wrapped := scope.Error(err)
	scope.MarkRendered(wrapped.Error())
	return wrapped
}

// specIn is the call's gate spec, its target labels looked up through ctx's resource lookup.
// planArgs, when set, is what the plan function reads at the time it runs.
func (s *Service) specIn(ctx context.Context, args OperationArgs, planArgs *OperationArgs) gate.Spec {
	spec := gate.Spec{Connector: args.Connector, Operation: args.Operation, Config: args.Config, Acknowledged: args.Acknowledged,
		DryRun: args.DryRun, Resources: s.lookupFor(ctx), ApprovalID: args.ApprovalID, PlanOnly: args.Plan,
		ConfirmedPlanHash: args.ConfirmedPlanHash, BreakGlass: args.BreakGlass}
	if planArgs == nil {
		planArgs = &args
	}
	spec.Plan = func(ctx context.Context) (plan.Plan, error) { return s.planOperation(ctx, *planArgs) }
	loaded := s.plugins.Loaded(args.Connector)
	if def, ok := s.definitionFor(args.Connector); ok {
		spec.Op, spec.Known = def.Operation(args.Operation)
		spec.Credentials = labeledCredentialNames(def, credentialScope(spec), s.bindings())
	}
	if loaded {
		spec.PluginConfigSHA256, spec.PluginEntrypointSHA256 = s.plugins.fingerprints(args.Connector)
	}
	// A built-in's dry run is a host preview: nothing is resolved. A plugin's reaches the plugin,
	// which holds its credentials.
	if args.DryRun && !loaded {
		spec.Credentials = nil
	}
	if args.DryRun && loaded {
		spec.Preview = audit.PreviewPluginClaimed
	}
	return spec
}

func (s *Service) execute(ctx context.Context, args OperationArgs) (OperationResult, error) {
	// The contract gate: declared, and the caller's config passes its key table, on the raw
	// request, before a configured resource is merged in and before anything is resolved.
	op, contractErr := s.declaredOperation(ctx, args)
	if contractErr != nil {
		return OperationResult{}, contractErr
	}
	var err error
	switch args.Connector {
	case "ssh":
		// An ssh target is always a configured resource; resolved first, so a dry run shows the
		// target that would be used.
		if args, err = s.resolveSSHTarget(ctx, args); err != nil {
			return OperationResult{}, err
		}
	case "docker":
		if args, err = s.resolveDockerResource(ctx, args); err != nil {
			return OperationResult{}, err
		}
	}
	if args.DryRun {
		if preview, ok, perr := s.dryRunPreview(args); ok || perr != nil {
			if perr != nil {
				return OperationResult{}, perr
			}
			return result(args, preview), nil
		}
		// A dry run never reaches a built-in's dispatch; a loaded plugin is the one exception,
		// for an operation its manifest declares a preview for (pluginhost refuses the rest).
		if !s.plugins.Loaded(args.Connector) {
			return OperationResult{}, previewUnsupportedError(args)
		}
	}
	if s.plugins.Loaded(args.Connector) {
		res, execErr := s.plugins.execute(ctx, args.Connector, PluginExecArgs{Operation: args.Operation, Config: args.Config, DryRun: args.DryRun, Acknowledged: args.Acknowledged})
		if execErr != nil {
			return OperationResult{}, pluginExecuteError(args, execErr)
		}
		return res, nil
	}
	// An installed-but-unloaded plugin is fatal only when nothing else serves the id: one that
	// shadows a built-in must not disable it.
	if s.plugins.Installed(args.Connector) && !s.registry.Configured(args.Connector) {
		return OperationResult{}, codedErr(args, gate.CodeUnavailable,
			fmt.Errorf("plugin connector %q is installed but not loaded; load it (POST /api/plugins/connectors/%s/load), or uninstall it", args.Connector, args.Connector))
	}
	// Acknowledgment before Resolve: resolving reads the credential, and a refusal must not
	// depend on having one.
	if ackErr := requireAcknowledgment(args, op); ackErr != nil {
		return OperationResult{}, ackErr
	}
	c, resolveErr := s.registry.Resolve(ctx, args.Connector)
	if resolveErr != nil {
		return OperationResult{}, codedErr(args, unavailableCode(resolveErr), resolveErr)
	}
	var out OperationResult
	switch args.Connector {
	case "docker":
		out, err = s.executeDocker(ctx, c, args)
	case "ssh":
		out, err = s.executeSSH(ctx, c, args)
	default:
		err = codedErr(args, gate.CodeUnsupported, nil)
	}
	if err = operationFailure(args, err); err != nil {
		return OperationResult{}, err
	}
	return out, nil
}

// declaredOperation finds the operation's contract and checks the caller's config against its
// key table. It fails closed: a connector or operation without a contract is refused.
func (s *Service) declaredOperation(ctx context.Context, args OperationArgs) (contract.Operation, error) {
	def, ok := s.definitionFor(args.Connector)
	if !ok {
		return contract.Operation{}, codedErr(args, gate.CodeUnsupported, fmt.Errorf("connector %q has no definition, so the contract of %q cannot be checked; refusing", args.Connector, args.Operation))
	}
	op, ok := def.Operation(args.Operation)
	if !ok {
		return contract.Operation{}, codedErr(args, gate.CodeUnsupported, redact.Guidance("connector %q does not declare operation %q, so its contract cannot be checked; refusing", args.Connector, args.Operation))
	}
	if err := op.CheckInputs(args.Config, gate.CallerSurfaceFrom(ctx) == gate.SurfaceInProcess); err != nil {
		return contract.Operation{}, codedErr(args, gate.CodeInvalidArgs, inputRefusal(args.Connector, err))
	}
	return op, nil
}

// inputHints add the connector's own recovery to a key-table refusal.
var inputHints = map[string]string{
	"ssh":    "an ssh operation takes a configured resource id, not connection settings; pass id=<resource-id> and set host, user and keys on the resource",
	"docker": "a docker operation over the API takes a configured docker resource (resource=<id>) or a local container name; ad-hoc targets (host, context, compose file) are refused",
}

func inputRefusal(connectorID string, err error) error {
	if hint, ok := inputHints[connectorID]; ok {
		return redact.Prose(fmt.Errorf("%w; %s", err, hint))
	}
	return redact.Prose(err)
}

// requireAcknowledgment gates on the operation's derived RequiresAck.
func requireAcknowledgment(args OperationArgs, op contract.Operation) error {
	if !op.RequiresAck || args.Acknowledged {
		return nil
	}
	if op.Effect.ReadOnly() && op.LocalFS == contract.LocalFSWrites {
		return codedErr(args, gate.CodeAckRequired, redact.Guidance("%s operation %q writes to the local filesystem and requires operator acknowledgment", op.Effect, args.Operation))
	}
	return codedErr(args, gate.CodeAckRequired, redact.Guidance("%s operation %q requires operator acknowledgment", op.Effect, args.Operation))
}

// operationFailure codes an error from past the gates: a coded error keeps its code, anything
// else came from the provider, the plugin or the tool behind the connector.
func operationFailure(args OperationArgs, err error) error {
	if err == nil {
		return nil
	}
	var coded *gate.Error
	if errors.As(err, &coded) {
		return err
	}
	return codedErr(args, gate.CodeOperationFailed, err)
}

func previewUnsupportedError(args OperationArgs) error {
	return codedErr(args, gate.CodePreviewUnsupported, redact.Guidance("operation %q has no dry-run preview; nothing was executed. Run it without dry_run to execute", args.Operation))
}

func (s *Service) definitionFor(id string) (contract.Definition, bool) {
	for _, def := range s.Definitions() {
		if def.ID == id {
			return def, true
		}
	}
	return contract.Definition{}, false
}

// dryRunPreview is a built-in's host preview, where it has one.
func (s *Service) dryRunPreview(args OperationArgs) (DryRunPreview, bool, error) {
	if args.Connector != "ssh" {
		return DryRunPreview{}, false, nil
	}
	reach := func() (map[string]any, error) {
		host, err := requiredString(args.Config, "host")
		if err != nil {
			return nil, codedErr(args, gate.CodeInvalidArgs, err)
		}
		return map[string]any{"host": host, "user": stringFromConfig(args.Config, "user", "root"), "port": intFromConfig(args.Config, "port", 22)}, nil
	}
	required := func(key string) (string, error) {
		v, err := requiredString(args.Config, key)
		if err != nil {
			return "", codedErr(args, gate.CodeInvalidArgs, err)
		}
		return v, nil
	}
	switch args.Operation {
	case "exec":
		target, err := reach()
		if err != nil {
			return DryRunPreview{}, true, err
		}
		command, err := required("command")
		if err != nil {
			return DryRunPreview{}, true, err
		}
		return dryRunPreview(args, "Would execute a remote SSH command.", target, map[string]any{"command": command}), true, nil
	case "put":
		target, err := reach()
		if err != nil {
			return DryRunPreview{}, true, err
		}
		localPath, err := required("local_path")
		if err != nil {
			return DryRunPreview{}, true, err
		}
		remotePath, err := required("remote_path")
		if err != nil {
			return DryRunPreview{}, true, err
		}
		target["remote_path"] = remotePath
		warnings := []string{}
		info, statErr := os.Stat(localPath)
		switch {
		case statErr != nil:
			warnings = append(warnings, fmt.Sprintf("local file %s cannot be read: %v", localPath, statErr))
		case info.IsDir():
			warnings = append(warnings, fmt.Sprintf("%s is a directory; ssh put transfers a single file", localPath))
		}
		input := map[string]any{"local_path": localPath}
		if statErr == nil && !info.IsDir() {
			input["bytes"] = info.Size()
			input["mode"] = info.Mode().Perm().String()
		}
		return dryRunPreview(args, "Would upload a local file over SFTP, replacing the remote file if it exists.", target, input, warnings...), true, nil
	case "put_dir":
		target, err := reach()
		if err != nil {
			return DryRunPreview{}, true, err
		}
		localPath, err := required("local_path")
		if err != nil {
			return DryRunPreview{}, true, err
		}
		remotePath, err := required("remote_path")
		if err != nil {
			return DryRunPreview{}, true, err
		}
		target["remote_path"] = remotePath
		preview := sshconn.PreviewDirUpload(localPath)
		return dryRunPreview(args, "Would upload a local directory tree over SFTP, replacing remote files that already exist.", target, map[string]any{
			"local_path": localPath, "files": preview.Files, "dirs": preview.Dirs, "symlinks": preview.Symlinks,
			"skipped": preview.Skipped, "bytes": preview.Bytes,
		}, preview.Warnings...), true, nil
	case "stop":
		target, err := reach()
		if err != nil {
			return DryRunPreview{}, true, err
		}
		return dryRunPreview(args, "Would shut down a remote host over SSH.", target, map[string]any{"command": "sudo shutdown -h now"}), true, nil
	}
	return DryRunPreview{}, false, nil
}

func (s *Service) executeDocker(ctx context.Context, c contract.Connector, args OperationArgs) (OperationResult, error) {
	docker, ok := c.(*dockerconn.Connector)
	if !ok {
		return OperationResult{}, codedErr(args, gate.CodeUnavailable, fmt.Errorf("registered connector has type %T", c))
	}
	// The host is chosen per call, so the connector never holds one between calls.
	target, err := dockerconn.TargetFromConfig(args.Config)
	if err != nil {
		return OperationResult{}, codedErr(args, gate.CodeInvalidArgs, err)
	}
	docker = docker.WithTarget(target)
	switch args.Operation {
	case "list_containers":
		containers, err := docker.ListContainers(ctx)
		return result(args, containers), err
	case "logs":
		name, err := requiredString(args.Config, "container")
		if err != nil {
			return OperationResult{}, codedErr(args, gate.CodeInvalidArgs, err)
		}
		logs, err := docker.Logs(ctx, name, intFromConfig(args.Config, "lines", 50))
		return result(args, logs), err
	case "start":
		res := containerResource(args)
		return result(args, nil), docker.Start(ctx, &res)
	case "stop":
		res := containerResource(args)
		return result(args, nil), docker.Stop(ctx, &res)
	case "destroy":
		res := containerResource(args)
		return result(args, nil), docker.Destroy(ctx, &res)
	case "status":
		res := containerResource(args)
		state, err := docker.Status(ctx, &res)
		return result(args, state), err
	}
	return OperationResult{}, codedErr(args, gate.CodeUnsupported, nil)
}

func (s *Service) executeSSH(ctx context.Context, c contract.Connector, args OperationArgs) (OperationResult, error) {
	ssh, ok := c.(*sshconn.Connector)
	if !ok {
		return OperationResult{}, codedErr(args, gate.CodeUnavailable, fmt.Errorf("registered connector has type %T", c))
	}
	res := resource.Resource{
		ID:        stringFromConfig(args.Config, "id", "ssh"),
		Name:      stringFromConfig(args.Config, "name", stringFromConfig(args.Config, "host", "ssh")),
		Type:      resource.Server,
		Connector: "ssh",
		Config:    args.Config,
	}
	paths := func(first, second string) (string, string, error) {
		a, err := requiredString(args.Config, first)
		if err != nil {
			return "", "", codedErr(args, gate.CodeInvalidArgs, err)
		}
		b, err := requiredString(args.Config, second)
		if err != nil {
			return "", "", codedErr(args, gate.CodeInvalidArgs, err)
		}
		return a, b, nil
	}
	switch args.Operation {
	case "status":
		status, err := ssh.HostStatusJSON(ctx, &res)
		return result(args, status), err
	case "exec":
		command, err := requiredString(args.Config, "command")
		if err != nil {
			return OperationResult{}, codedErr(args, gate.CodeInvalidArgs, err)
		}
		out, err := ssh.Exec(ctx, &res, command)
		return result(args, out), err
	case "put":
		localPath, remotePath, err := paths("local_path", "remote_path")
		if err != nil {
			return OperationResult{}, err
		}
		out, err := ssh.Put(ctx, &res, localPath, remotePath)
		return result(args, out), err
	case "get":
		remotePath, localPath, err := paths("remote_path", "local_path")
		if err != nil {
			return OperationResult{}, err
		}
		out, err := ssh.Get(ctx, &res, remotePath, localPath)
		return result(args, out), err
	case "put_dir":
		localPath, remotePath, err := paths("local_path", "remote_path")
		if err != nil {
			return OperationResult{}, err
		}
		out, err := ssh.PutDir(ctx, &res, localPath, remotePath)
		return result(args, out), err
	case "get_dir":
		remotePath, localPath, err := paths("remote_path", "local_path")
		if err != nil {
			return OperationResult{}, err
		}
		out, err := ssh.GetDir(ctx, &res, remotePath, localPath)
		return result(args, out), err
	case "stop":
		return result(args, nil), ssh.Stop(ctx, &res)
	}
	return OperationResult{}, codedErr(args, gate.CodeUnsupported, nil)
}

func containerResource(args OperationArgs) resource.Resource {
	return resource.Resource{
		ID:        stringFromConfig(args.Config, "id", stringFromConfig(args.Config, "container", args.Connector)),
		Name:      stringFromConfig(args.Config, "name", stringFromConfig(args.Config, "container", args.Connector)),
		Type:      resource.Container,
		Connector: args.Connector,
		Config:    args.Config,
	}
}

func result(args OperationArgs, data any) OperationResult {
	return OperationResult{Connector: args.Connector, Operation: args.Operation, Data: data}
}

func dryRunPreview(args OperationArgs, summary string, target, input map[string]any, warnings ...string) DryRunPreview {
	preview := DryRunPreview{DryRun: true, Connector: args.Connector, Operation: args.Operation, Summary: summary, Target: target, Input: input}
	if len(warnings) > 0 {
		preview.Warnings = append([]string(nil), warnings...)
	}
	return preview
}

// pluginExecuteError codes a plugin call's failure: the plugin's own code first, then the
// host's refusals, then operation_failed.
func pluginExecuteError(args OperationArgs, err error) error {
	var coded *gate.Error
	if errors.As(err, &coded) {
		return err
	}
	var pluginCoded *pluginhost.CodedError
	if errors.As(err, &pluginCoded) {
		return codedErr(args, gate.ErrorCode(pluginCoded.Code), err)
	}
	var missing *pluginhost.MissingSecretsError
	if errors.As(err, &missing) {
		return codedErr(args, gate.CodeCredentialMissing, err)
	}
	if errors.Is(err, pluginhost.ErrPreviewUnsupported) {
		return codedErr(args, gate.CodePreviewUnsupported, redact.Prose(err))
	}
	var inputErr *contract.InputError
	if errors.As(err, &inputErr) {
		return codedErr(args, gate.CodeInvalidArgs, redact.Prose(err))
	}
	if errors.Is(err, pluginhost.ErrAckRequired) {
		return codedErr(args, gate.CodeAckRequired, redact.Prose(err))
	}
	if errors.Is(err, pluginhost.ErrOperationUndeclared) {
		return codedErr(args, gate.CodeUnsupported, err)
	}
	var deadline *pluginhost.DeadlineError
	if errors.As(err, &deadline) {
		return codedErr(args, gate.CodeDeadlineExceeded, redact.Prose(err))
	}
	var tooLarge *pluginhost.OutputTooLargeError
	if errors.As(err, &tooLarge) {
		return codedErr(args, gate.CodeOutputTooLarge, redact.Prose(err))
	}
	if errors.Is(err, pluginhost.ErrNotLoaded) {
		return codedErr(args, gate.CodeUnavailable, err)
	}
	if errors.Is(err, pluginhost.ErrPluginChanged) {
		return codedErr(args, gate.CodePluginChanged, err)
	}
	return codedErr(args, gate.CodeOperationFailed, err)
}

func codedErr(args OperationArgs, code gate.ErrorCode, err error) error {
	return gate.NewError(args.Connector, args.Operation, code, err)
}

func unavailableCode(err error) gate.ErrorCode {
	if err == nil {
		return gate.CodeUnavailable
	}
	msg := strings.ToLower(redact.Text(err.Error()))
	if strings.Contains(msg, "token") || strings.Contains(msg, "credential") || strings.Contains(msg, "secret") || strings.Contains(msg, "api key") {
		return gate.CodeCredentialMissing
	}
	return gate.CodeUnavailable
}

func requiredString(cfg map[string]any, key string) (string, error) {
	value, _ := cfg[key].(string)
	if value == "" {
		return "", fmt.Errorf("missing %q", key)
	}
	return value, nil
}

func stringFromConfig(cfg map[string]any, key, fallback string) string {
	value, _ := cfg[key].(string)
	if value == "" {
		return fallback
	}
	return value
}

func intFromConfig(cfg map[string]any, key string, fallback int) int {
	switch value := cfg[key].(type) {
	case int:
		if value >= 0 {
			return value
		}
	case int64:
		if value >= 0 {
			return int(value)
		}
	case float64:
		if value >= 0 {
			return int(value)
		}
	case string:
		var out int
		if _, err := fmt.Sscanf(value, "%d", &out); err == nil && out >= 0 {
			return out
		}
	}
	return fallback
}

func sortedDefinitions(defs map[string]contract.Definition) []contract.Definition {
	out := make([]contract.Definition, 0, len(defs))
	for _, def := range defs {
		out = append(out, def)
	}
	slices.SortFunc(out, func(a, b contract.Definition) int { return strings.Compare(a.ID, b.ID) })
	return out
}

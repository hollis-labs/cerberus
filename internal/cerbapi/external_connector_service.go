package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/connector"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	ghconn "github.com/hollis-labs/cerberus/internal/connector/github"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
	gmcp "github.com/hollis-labs/go-mcp/server"
)

type ExternalConnectorErrorCode string

const (
	ExternalConnectorUnavailable       ExternalConnectorErrorCode = "connector_unavailable"
	ExternalConnectorCredentialMissing ExternalConnectorErrorCode = "credential_missing"
	ExternalConnectorUnsupported       ExternalConnectorErrorCode = "operation_unsupported"
	ExternalConnectorInvalidArgs       ExternalConnectorErrorCode = "invalid_args"
	ExternalConnectorAckRequired       ExternalConnectorErrorCode = "acknowledgment_required"
	// ExternalConnectorPreviewUnsupported is a dry run of an operation that
	// has no preview. The operation is refused, never executed.
	ExternalConnectorPreviewUnsupported ExternalConnectorErrorCode = "preview_unsupported"
	// ExternalConnectorOperationFailed is a connector operation — built-in or
	// plugin — that passed every gate and then failed in the provider, the
	// plugin or the tool it drives, for a reason the host did not classify. It
	// is not a refusal and not a fault in Cerberus. Coding it is also what
	// routes a plugin's own text through ExternalConnectorError's redaction.
	ExternalConnectorOperationFailed ExternalConnectorErrorCode = "operation_failed"
	// ExternalConnectorAuditUnavailable is an operation refused because its
	// audit record could not be written (Decision 8). Nothing ran.
	ExternalConnectorAuditUnavailable ExternalConnectorErrorCode = "audit_unavailable"
)

// externalConnectorErrorCodes is the whole vocabulary, for tests that hold
// each code intact through redaction.
var externalConnectorErrorCodes = []ExternalConnectorErrorCode{
	ExternalConnectorUnavailable,
	ExternalConnectorCredentialMissing,
	ExternalConnectorUnsupported,
	ExternalConnectorInvalidArgs,
	ExternalConnectorAckRequired,
	ExternalConnectorPreviewUnsupported,
	ExternalConnectorOperationFailed,
	ExternalConnectorAuditUnavailable,
}

// ExternalConnectorErrorCodes returns the whole vocabulary, for tests on the
// far side of a surface that must hold every code.
func ExternalConnectorErrorCodes() []ExternalConnectorErrorCode {
	return slices.Clone(externalConnectorErrorCodes)
}

type ExternalConnectorError struct {
	Code      ExternalConnectorErrorCode
	Connector string
	Operation string
	Err       error
}

func (e *ExternalConnectorError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s %s: %s", e.Connector, e.Operation, e.Code)
	}
	return redact.Text(fmt.Sprintf("%s %s: %s: %v", e.Connector, e.Operation, e.Code, e.Err))
}

func (e *ExternalConnectorError) Unwrap() error {
	return e.Err
}

type ExternalConnectorOperationArgs struct {
	Connector    string         `json:"connector"`
	Operation    string         `json:"operation"`
	Config       map[string]any `json:"config,omitempty"`
	DryRun       bool           `json:"dry_run,omitempty"`
	Acknowledged bool           `json:"acknowledged,omitempty"`
}

type ExternalConnectorOperationResult struct {
	Connector string `json:"connector"`
	Operation string `json:"operation"`
	Data      any    `json:"data"`
}

type ExternalConnectorDryRunPreview struct {
	DryRun    bool           `json:"dry_run"`
	Connector string         `json:"connector"`
	Operation string         `json:"operation"`
	Summary   string         `json:"summary"`
	Target    map[string]any `json:"target,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
}

type ExternalConnectorService struct {
	registry       *connector.Registry
	managedPlugins *ManagedPluginConnectorService
	resources      ResourceLookup
	audit          audit.Sink
	logger         *slog.Logger
}

// NewExternalConnectorService constructs the admin lane. The audit sink is
// required: every operation writes its intent and outcome to it, and there
// is no silent no-op. Outside tests it is constructed only in internal/app,
// which opens the real sink (TestServicesAreConstructedOnlyInApp).
func NewExternalConnectorService(sink audit.Sink, registry *connector.Registry, managedPlugins ...*ManagedPluginConnectorService) *ExternalConnectorService {
	if sink == nil {
		panic("cerbapi: NewExternalConnectorService requires an audit sink")
	}
	var managed *ManagedPluginConnectorService
	if len(managedPlugins) > 0 {
		managed = managedPlugins[0]
	}
	return &ExternalConnectorService{
		registry:       registry,
		managedPlugins: managed,
		audit:          sink,
		logger:         slog.Default(),
	}
}

func (s *ExternalConnectorService) Definitions() []contract.Definition {
	if s == nil {
		return nil
	}
	defs := make(map[string]contract.Definition)
	if s.registry != nil {
		for _, def := range s.registry.Definitions() {
			defs[def.ID] = def
		}
	}
	if s.managedPlugins != nil {
		for _, def := range s.managedPlugins.Definitions() {
			defs[def.ID] = def
		}
	}
	return sortedDefinitions(defs)
}

func (s *ExternalConnectorService) LiveDefinitions() []contract.Definition {
	return s.LiveDefinitionsContext(context.Background())
}

// LiveDefinitionsContext returns the connectors that can actually be
// constructed right now. It probes rather than checking registration: a
// registered factory whose docker binary is missing, or whose API token is
// unset, is not live. Probing costs a secret read per credentialed connector
// and no network calls.
func (s *ExternalConnectorService) LiveDefinitionsContext(ctx context.Context) []contract.Definition {
	if s == nil {
		return nil
	}
	defs := make(map[string]contract.Definition)
	if s.registry != nil {
		for _, def := range s.registry.Definitions() {
			if s.registry.Probe(ctx, def.ID) == nil {
				defs[def.ID] = def
			}
		}
	}
	if s.managedPlugins != nil {
		for _, def := range s.managedPlugins.LiveDefinitions() {
			defs[def.ID] = def
		}
	}
	return sortedDefinitions(defs)
}

// Execute runs one admin-lane operation. It writes the intent record before
// anything else — before the contract gate, so a refusal is recorded too —
// and the outcome record on every exit. When the intent cannot be written,
// a non-read is refused and nothing runs (Decision 8).
func (s *ExternalConnectorService) Execute(ctx context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	if s == nil {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, errors.New("connector registry is not configured"))
	}
	call, err := beginAudit(ctx, s.audit, s.logger, s.auditSpec(args))
	if err != nil {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorAuditUnavailable, err)
	}
	result, err := s.execute(call.withTelemetry(ctx), args)
	call.finish(err)
	return result, err
}

// auditSpec describes a request for its records, from the contract when the
// operation declares one. It checks nothing: the gates in execute do that.
func (s *ExternalConnectorService) auditSpec(args ExternalConnectorOperationArgs) auditSpec {
	spec := auditSpec{connector: args.Connector, operation: args.Operation, config: args.Config, acknowledged: args.Acknowledged, dryRun: args.DryRun}
	if def, ok := s.definitionFor(args.Connector); ok {
		spec.op, spec.known = def.Operation(args.Operation)
		spec.credentials = credentialNames(def)
	}
	if s.managedPlugins != nil && s.managedPlugins.Loaded(args.Connector) {
		spec.pluginConfigSHA256, spec.pluginEntrypointSHA256 = s.managedPlugins.fingerprints(args.Connector)
	}
	// A built-in's dry run is a host preview: nothing is resolved. A
	// plugin's reaches the plugin, which holds its credentials.
	if args.DryRun && (s.managedPlugins == nil || !s.managedPlugins.Loaded(args.Connector)) {
		spec.credentials = nil
	}
	return spec
}

func (s *ExternalConnectorService) execute(ctx context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	progressToken := fmt.Sprintf("connector:%s:%s", args.Connector, args.Operation)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Starting connector operation %s.%s", args.Connector, args.Operation))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Validating connector operation")

	if s == nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: connector registry is not configured", args.Connector, args.Operation))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Connector registry unavailable")
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, errors.New("connector registry is not configured"))
	}
	// The contract gate: the operation must be declared, and the caller's
	// config must pass its key table. Both run on the raw request, before a
	// configured resource is merged in and before anything is resolved, so a
	// refusal never depends on having a credential.
	op, contractErr := s.declaredOperation(ctx, args)
	if contractErr != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(contractErr.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Operation refused")
		return ExternalConnectorOperationResult{}, contractErr
	}
	// An SSH target is always a configured resource. Resolve it first, so a
	// dry-run preview shows the target that would really be used.
	if args.Connector == "ssh" {
		resolved, err := s.resolveSSHTarget(args)
		if err != nil {
			gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(err.Error())))
			gmcp.NotifyProgress(ctx, progressToken, 2, 2, "SSH target refused")
			return ExternalConnectorOperationResult{}, err
		}
		args = resolved
	}
	// A docker operation may name a configured resource by id; its target
	// (host, context, compose file) then comes from the resource.
	if args.Connector == "docker" {
		resolved, err := s.resolveDockerResource(args)
		if err != nil {
			gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(err.Error())))
			gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Docker resource refused")
			return ExternalConnectorOperationResult{}, err
		}
		args = resolved
	}
	if args.DryRun {
		if preview, ok, err := s.dryRunPreview(args); ok || err != nil {
			if err != nil {
				gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(err.Error())))
				gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Dry run failed")
				return ExternalConnectorOperationResult{}, err
			}
			gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Connector dry run completed for %s.%s", args.Connector, args.Operation))
			gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Dry run completed")
			return externalConnectorResult(args, preview), nil
		}
		// A dry run never reaches a built-in connector's real dispatch. A
		// loaded managed plugin is the one exception, and only for an
		// operation its manifest declares supports_dry: pluginhost refuses
		// the rest with the same code before calling the plugin.
		if s.managedPlugins == nil || !s.managedPlugins.Loaded(args.Connector) {
			err := previewUnsupportedError(args)
			gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(err.Error())))
			gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Dry run unsupported")
			return ExternalConnectorOperationResult{}, err
		}
	}
	if s.managedPlugins != nil && s.managedPlugins.Loaded(args.Connector) {
		gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Executing managed plugin connector %s.%s", args.Connector, args.Operation))
		gmcp.NotifyProgress(ctx, progressToken, 1, 2, "Executing managed plugin connector")
		// The admin lane has written this call's records; the managed
		// service's own entry point is for its direct route.
		result, execErr := s.managedPlugins.execute(ctx, args.Connector, PluginConnectorExecArgs{
			Operation:    args.Operation,
			Config:       args.Config,
			DryRun:       args.DryRun,
			Acknowledged: args.Acknowledged,
		})
		if execErr != nil {
			return ExternalConnectorOperationResult{}, managedPluginExecuteError(args, execErr)
		}
		return result, nil
	}
	// An installed-but-unloaded plugin is only fatal when nothing else can serve
	// the id. A plugin that shadows a built-in must not disable it: unloading
	// the plugin previously left `cerberus docker ps` permanently broken, with
	// no uninstall command and hand-editing the state file as the only recovery.
	if s.managedPlugins != nil && s.managedPlugins.Installed(args.Connector) && !s.managedPlugins.Loaded(args.Connector) {
		if s.registry == nil || !s.registry.Configured(args.Connector) {
			gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: plugin connector is installed but not loaded", args.Connector, args.Operation))
			gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Connector unavailable")
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable,
				fmt.Errorf("plugin connector %q is installed but not loaded; run `cerberus connectors plugin managed load %s`, or `... uninstall %s` to drop it", args.Connector, args.Connector, args.Connector))
		}
		gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Plugin connector %q is installed but not loaded; using the built-in connector", args.Connector))
	}
	if s.registry == nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: connector registry is not configured", args.Connector, args.Operation))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Connector registry unavailable")
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, errors.New("connector registry is not configured"))
	}

	// The gate runs before Resolve. Resolving a connector reads its
	// credential, and a refusal that depends on having one reports
	// credential_missing for a call that was never going to run: an un-acked
	// droplet stop said "no token" instead of "not acknowledged".
	if err := requireAcknowledgment(args, op); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Acknowledgment required")
		return ExternalConnectorOperationResult{}, err
	}
	c, resolveErr := s.registry.Resolve(ctx, args.Connector)
	if resolveErr != nil {
		err := resolveErr
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Connector unavailable")
		return ExternalConnectorOperationResult{}, externalConnectorError(args, unavailableCode(err), err)
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Executing connector operation %s.%s", args.Connector, args.Operation))
	gmcp.NotifyProgress(ctx, progressToken, 1, 2, "Executing connector operation")

	var (
		result ExternalConnectorOperationResult
		err    error
	)
	switch args.Connector {
	case "docker":
		result, err = s.executeDocker(ctx, c, args)
	case "github":
		result, err = s.executeGitHub(ctx, c, args)
	case "ssh":
		result, err = s.executeSSH(ctx, c, args)
	default:
		err = externalConnectorError(args, ExternalConnectorUnsupported, nil)
	}
	err = operationFailure(args, err)
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Connector operation failed")
		return ExternalConnectorOperationResult{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Connector operation %s.%s completed", args.Connector, args.Operation))
	gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Connector operation completed")
	return result, nil
}

// declaredOperation finds the operation's contract and checks the caller's
// config against its key table. It fails closed: a connector with no
// definition, or an operation its definition does not declare, is refused
// rather than treated as safe.
func (s *ExternalConnectorService) declaredOperation(ctx context.Context, args ExternalConnectorOperationArgs) (contract.Operation, error) {
	def, ok := s.definitionFor(args.Connector)
	if !ok {
		return contract.Operation{}, externalConnectorError(args, ExternalConnectorUnsupported, fmt.Errorf("connector %q has no definition, so the contract of %q cannot be checked; refusing", args.Connector, args.Operation))
	}
	op, ok := def.Operation(args.Operation)
	if !ok {
		return contract.Operation{}, externalConnectorError(args, ExternalConnectorUnsupported, fmt.Errorf("connector %q does not declare operation %q, so its contract cannot be checked; refusing", args.Connector, args.Operation))
	}
	if err := op.CheckInputs(args.Config, CallerSurfaceFrom(ctx) == SurfaceInProcess); err != nil {
		return contract.Operation{}, externalConnectorError(args, ExternalConnectorInvalidArgs, inputRefusal(args.Connector, err))
	}
	return op, nil
}

// inputHints add the connector's own recovery to a key-table refusal: how
// to name a target it takes only as a configured resource.
var inputHints = map[string]string{
	"ssh":    "an ssh operation takes a configured resource id, not connection settings; pass id=<resource-id> (see `cerberus resource list`) and set host, user and keys on the resource",
	"docker": "over the socket, the web console and MCP a docker operation takes a configured docker resource (resource=<id>, see `cerberus resource list`) or a local container name; ad-hoc targets (--host, --context, -f) run only from your shell",
}

func inputRefusal(connectorID string, err error) error {
	if hint, ok := inputHints[connectorID]; ok {
		return fmt.Errorf("%w; %s", err, hint)
	}
	return err
}

// requireAcknowledgment gates on the operation's derived RequiresAck: every
// effect class except read and read_sensitive (Decision 14), and any
// operation that writes to the local filesystem.
func requireAcknowledgment(args ExternalConnectorOperationArgs, op contract.Operation) error {
	if !op.RequiresAck || args.Acknowledged {
		return nil
	}
	if op.Effect.ReadOnly() && op.LocalFS == contract.LocalFSWrites {
		return externalConnectorError(args, ExternalConnectorAckRequired, fmt.Errorf("%s operation %q writes to the local filesystem and requires operator acknowledgment", op.Effect, args.Operation))
	}
	return externalConnectorError(args, ExternalConnectorAckRequired, fmt.Errorf("%s operation %q requires operator acknowledgment", op.Effect, args.Operation))
}

// operationFailure codes an error from past the gates. A coded error keeps its
// code; anything else came from the provider, the plugin or the tool behind
// the connector.
func operationFailure(args ExternalConnectorOperationArgs, err error) error {
	if err == nil {
		return nil
	}
	var coded *ExternalConnectorError
	if errors.As(err, &coded) {
		return err
	}
	return externalConnectorError(args, ExternalConnectorOperationFailed, err)
}

func previewUnsupportedError(args ExternalConnectorOperationArgs) error {
	return externalConnectorError(args, ExternalConnectorPreviewUnsupported, fmt.Errorf("operation %q has no dry-run preview; nothing was executed. Run it without --dry-run to execute", args.Operation))
}

func (s *ExternalConnectorService) definitionFor(id string) (contract.Definition, bool) {
	for _, def := range s.Definitions() {
		if def.ID == id {
			return def, true
		}
	}
	return contract.Definition{}, false
}

func (s *ExternalConnectorService) dryRunPreview(args ExternalConnectorOperationArgs) (ExternalConnectorDryRunPreview, bool, error) {
	switch args.Connector {
	case "ssh":
		switch args.Operation {
		case "exec":
			host, err := requiredString(args.Config, "host")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			command, err := requiredString(args.Config, "command")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would execute a remote SSH command.", map[string]any{
				"host": host,
				"user": stringFromConfig(args.Config, "user", "root"),
				"port": intFromConfig(args.Config, "port", 22),
			}, map[string]any{
				"command": command,
			}), true, nil
		case "put":
			host, err := requiredString(args.Config, "host")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			localPath, err := requiredString(args.Config, "local_path")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			remotePath, err := requiredString(args.Config, "remote_path")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
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
			return dryRunPreview(args, "Would upload a local file over SFTP, replacing the remote file if it exists.", map[string]any{
				"host":        host,
				"user":        stringFromConfig(args.Config, "user", "root"),
				"port":        intFromConfig(args.Config, "port", 22),
				"remote_path": remotePath,
			}, input, warnings...), true, nil
		case "put_dir":
			host, err := requiredString(args.Config, "host")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			localPath, err := requiredString(args.Config, "local_path")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			remotePath, err := requiredString(args.Config, "remote_path")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			// File count and total bytes are what an operator needs to decide
			// whether to proceed: a preview that only named the two paths
			// would not distinguish a compose directory from a build tree.
			preview := sshconn.PreviewDirUpload(localPath)
			return dryRunPreview(args, "Would upload a local directory tree over SFTP, replacing remote files that already exist.", map[string]any{
				"host":        host,
				"user":        stringFromConfig(args.Config, "user", "root"),
				"port":        intFromConfig(args.Config, "port", 22),
				"remote_path": remotePath,
			}, map[string]any{
				"local_path": localPath,
				"files":      preview.Files,
				"dirs":       preview.Dirs,
				"symlinks":   preview.Symlinks,
				"skipped":    preview.Skipped,
				"bytes":      preview.Bytes,
			}, preview.Warnings...), true, nil
		case "stop":
			host, err := requiredString(args.Config, "host")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would shut down a remote host over SSH.", map[string]any{
				"host": host,
				"user": stringFromConfig(args.Config, "user", "root"),
				"port": intFromConfig(args.Config, "port", 22),
			}, map[string]any{
				"command": "sudo shutdown -h now",
			}), true, nil
		}
	}
	return ExternalConnectorDryRunPreview{}, false, nil
}

func (s *ExternalConnectorService) AvailabilityError(connector string) error {
	if s == nil || s.registry == nil {
		return errors.New("connector registry is not configured")
	}
	return s.registry.UnavailableError(connector)
}

func (s *ExternalConnectorService) executeDocker(ctx context.Context, c contract.Connector, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	docker, ok := c.(*dockerconn.Connector)
	if !ok {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, fmt.Errorf("registered connector has type %T", c))
	}

	// Host selection is resolved here, per call, so the same operations reach a
	// remote daemon without the connector — or the Cerberus daemon around it —
	// holding a host between calls.
	target, err := dockerconn.TargetFromConfig(args.Config)
	if err != nil {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
	}
	docker = docker.WithTarget(target)

	switch args.Operation {
	case "list_containers":
		containers, err := docker.ListContainers(ctx)
		return externalConnectorResult(args, containers), err
	case "logs":
		name, err := requiredString(args.Config, "container")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		logs, err := docker.Logs(ctx, name, intFromConfig(args.Config, "lines", 50))
		return externalConnectorResult(args, logs), err
	case "start":
		res := externalResource(args)
		err := docker.Start(ctx, &res)
		return externalConnectorResult(args, nil), err
	case "stop":
		res := externalResource(args)
		err := docker.Stop(ctx, &res)
		return externalConnectorResult(args, nil), err
	case "destroy":
		res := externalResource(args)
		err := docker.Destroy(ctx, &res)
		return externalConnectorResult(args, nil), err
	case "status":
		res := externalResource(args)
		state, err := docker.Status(ctx, &res)
		return externalConnectorResult(args, state), err
	default:
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnsupported, nil)
	}
}

func (s *ExternalConnectorService) executeGitHub(ctx context.Context, c contract.Connector, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	github, ok := c.(*ghconn.Connector)
	if !ok {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, fmt.Errorf("registered connector has type %T", c))
	}

	owner, repo, err := ownerRepoFromConfig(args.Config)
	if err != nil {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
	}

	switch args.Operation {
	case "status":
		status, err := github.RepoStatus(ctx, owner, repo)
		return externalConnectorResult(args, status), err
	case "list_releases":
		releases, err := github.ListReleases(ctx, owner, repo, intFromConfig(args.Config, "limit", 10))
		return externalConnectorResult(args, releases), err
	case "list_workflow_runs":
		runs, err := github.ListWorkflowRuns(ctx, owner, repo, intFromConfig(args.Config, "limit", 10))
		return externalConnectorResult(args, runs), err
	default:
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnsupported, nil)
	}
}

func (s *ExternalConnectorService) executeSSH(ctx context.Context, c contract.Connector, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	ssh, ok := c.(*sshconn.Connector)
	if !ok {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, fmt.Errorf("registered connector has type %T", c))
	}

	res := resource.Resource{
		ID:        stringFromConfig(args.Config, "id", "ssh"),
		Name:      stringFromConfig(args.Config, "name", stringFromConfig(args.Config, "host", "ssh")),
		Type:      resource.Server,
		Connector: "ssh",
		Config:    args.Config,
	}
	switch args.Operation {
	case "status":
		status, err := ssh.HostStatusJSON(ctx, &res)
		return externalConnectorResult(args, status), err
	case "exec":
		command, err := requiredString(args.Config, "command")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		result, err := ssh.Exec(ctx, &res, command)
		return externalConnectorResult(args, result), err
	case "put":
		localPath, err := requiredString(args.Config, "local_path")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		remotePath, err := requiredString(args.Config, "remote_path")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		result, err := ssh.Put(ctx, &res, localPath, remotePath)
		return externalConnectorResult(args, result), err
	case "get":
		remotePath, err := requiredString(args.Config, "remote_path")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		localPath, err := requiredString(args.Config, "local_path")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		result, err := ssh.Get(ctx, &res, remotePath, localPath)
		return externalConnectorResult(args, result), err
	case "put_dir":
		localPath, err := requiredString(args.Config, "local_path")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		remotePath, err := requiredString(args.Config, "remote_path")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		result, err := ssh.PutDir(ctx, &res, localPath, remotePath)
		return externalConnectorResult(args, result), err
	case "get_dir":
		remotePath, err := requiredString(args.Config, "remote_path")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		localPath, err := requiredString(args.Config, "local_path")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		result, err := ssh.GetDir(ctx, &res, remotePath, localPath)
		return externalConnectorResult(args, result), err
	case "stop":
		err := ssh.Stop(ctx, &res)
		return externalConnectorResult(args, nil), err
	default:
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnsupported, nil)
	}
}

func externalResource(args ExternalConnectorOperationArgs) resource.Resource {
	return resource.Resource{
		ID:        stringFromConfig(args.Config, "id", stringFromConfig(args.Config, "container", args.Connector)),
		Name:      stringFromConfig(args.Config, "name", stringFromConfig(args.Config, "container", args.Connector)),
		Type:      resource.Container,
		Connector: args.Connector,
		Config:    args.Config,
	}
}

func externalConnectorResult(args ExternalConnectorOperationArgs, data any) ExternalConnectorOperationResult {
	return ExternalConnectorOperationResult{
		Connector: args.Connector,
		Operation: args.Operation,
		Data:      data,
	}
}

func dryRunPreview(args ExternalConnectorOperationArgs, summary string, target, input map[string]any, warnings ...string) ExternalConnectorDryRunPreview {
	preview := ExternalConnectorDryRunPreview{
		DryRun:    true,
		Connector: args.Connector,
		Operation: args.Operation,
		Summary:   summary,
		Target:    target,
		Input:     input,
	}
	if len(warnings) > 0 {
		preview.Warnings = append([]string(nil), warnings...)
	}
	return preview
}

func managedPluginExecuteError(args ExternalConnectorOperationArgs, err error) error {
	var coded *ExternalConnectorError
	if errors.As(err, &coded) {
		return err
	}
	// The plugin's own diagnosis first: it knows what failed.
	var pluginCoded *pluginhost.CodedError
	if errors.As(err, &pluginCoded) {
		return externalConnectorError(args, ExternalConnectorErrorCode(pluginCoded.Code), err)
	}
	var missing *pluginhost.MissingSecretsError
	if errors.As(err, &missing) {
		return externalConnectorError(args, ExternalConnectorCredentialMissing, err)
	}
	if errors.Is(err, pluginhost.ErrPreviewUnsupported) {
		return externalConnectorError(args, ExternalConnectorPreviewUnsupported, err)
	}
	var inputErr *contract.InputError
	if errors.As(err, &inputErr) {
		return externalConnectorError(args, ExternalConnectorInvalidArgs, err)
	}
	if errors.Is(err, pluginhost.ErrAckRequired) {
		return externalConnectorError(args, ExternalConnectorAckRequired, err)
	}
	if errors.Is(err, pluginhost.ErrOperationUndeclared) {
		return externalConnectorError(args, ExternalConnectorUnsupported, err)
	}
	if errors.Is(err, pluginhost.ErrNotLoaded) {
		return externalConnectorError(args, ExternalConnectorUnavailable, err)
	}
	return externalConnectorError(args, ExternalConnectorOperationFailed, err)
}

func externalConnectorError(args ExternalConnectorOperationArgs, code ExternalConnectorErrorCode, err error) error {
	return &ExternalConnectorError{
		Code:      code,
		Connector: args.Connector,
		Operation: args.Operation,
		Err:       err,
	}
}

func unavailableCode(err error) ExternalConnectorErrorCode {
	if err == nil {
		return ExternalConnectorUnavailable
	}
	msg := strings.ToLower(redact.Text(err.Error()))
	if strings.Contains(msg, "token") || strings.Contains(msg, "credential") || strings.Contains(msg, "secret") || strings.Contains(msg, "api key") {
		return ExternalConnectorCredentialMissing
	}
	return ExternalConnectorUnavailable
}

func ownerRepoFromConfig(cfg map[string]any) (string, string, error) {
	owner, err := requiredString(cfg, "owner")
	if err != nil {
		return "", "", err
	}
	repo, err := requiredString(cfg, "repo")
	if err != nil {
		return "", "", err
	}
	return owner, repo, nil
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

func boolFromConfig(cfg map[string]any, key string) bool {
	value, _ := cfg[key].(bool)
	return value
}

func sortedDefinitions(defs map[string]contract.Definition) []contract.Definition {
	out := make([]contract.Definition, 0, len(defs))
	for _, def := range defs {
		out = append(out, def)
	}
	slices.SortFunc(out, func(a, b contract.Definition) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
	})
	return out
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

func requiredInt(cfg map[string]any, key string) (int, error) {
	value := intFromConfig(cfg, key, -1)
	if value < 0 {
		return 0, fmt.Errorf("missing %q", key)
	}
	return value, nil
}

func requiredStringSlice(cfg map[string]any, key string) ([]string, error) {
	raw, ok := cfg[key]
	if !ok {
		return nil, fmt.Errorf("%s is required", key)
	}
	switch values := raw.(type) {
	case []string:
		out := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" {
				out = append(out, value)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%s must not be empty", key)
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			value, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s entries must be strings", key)
			}
			value = strings.TrimSpace(value)
			if value != "" {
				out = append(out, value)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%s must not be empty", key)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be a string array", key)
	}
}

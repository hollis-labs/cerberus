package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/connector"
	cfconn "github.com/hollis-labs/cerberus/internal/connector/cloudflare"
	doconn "github.com/hollis-labs/cerberus/internal/connector/digitalocean"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	forgeconn "github.com/hollis-labs/cerberus/internal/connector/forge"
	ghconn "github.com/hollis-labs/cerberus/internal/connector/github"
	ncconn "github.com/hollis-labs/cerberus/internal/connector/namecheap"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
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
)

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
}

func NewExternalConnectorService(registry *connector.Registry, managedPlugins ...*ManagedPluginConnectorService) *ExternalConnectorService {
	var managed *ManagedPluginConnectorService
	if len(managedPlugins) > 0 {
		managed = managedPlugins[0]
	}
	return &ExternalConnectorService{
		registry:       registry,
		managedPlugins: managed,
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

func (s *ExternalConnectorService) Execute(ctx context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	progressToken := fmt.Sprintf("connector:%s:%s", args.Connector, args.Operation)
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Starting connector operation %s.%s", args.Connector, args.Operation))
	gmcp.NotifyProgress(ctx, progressToken, 0, 2, "Validating connector operation")

	if s == nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: connector registry is not configured", args.Connector, args.Operation))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Connector registry unavailable")
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, errors.New("connector registry is not configured"))
	}
	// Refuse before credential resolution, dry-run previews, or plugin dispatch.
	if args.Connector == "namecheap" && (args.Operation == "create_dns_record" || args.Operation == "delete_dns_record") {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnsupported, ncconn.ErrUnsafePerRecordWrite)
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
	}
	if s.managedPlugins != nil && s.managedPlugins.Loaded(args.Connector) {
		gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Executing managed plugin connector %s.%s", args.Connector, args.Operation))
		gmcp.NotifyProgress(ctx, progressToken, 1, 2, "Executing managed plugin connector")
		return s.managedPlugins.Execute(ctx, args.Connector, PluginConnectorExecArgs{
			Operation:    args.Operation,
			Config:       args.Config,
			DryRun:       args.DryRun,
			Acknowledged: args.Acknowledged,
		})
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

	c, resolveErr := s.registry.Resolve(ctx, args.Connector)
	if resolveErr != nil {
		err := resolveErr
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Connector unavailable")
		return ExternalConnectorOperationResult{}, externalConnectorError(args, unavailableCode(err), err)
	}
	if err := s.requireAcknowledgment(args); err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Acknowledgment required")
		return ExternalConnectorOperationResult{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Executing connector operation %s.%s", args.Connector, args.Operation))
	gmcp.NotifyProgress(ctx, progressToken, 1, 2, "Executing connector operation")

	var (
		result ExternalConnectorOperationResult
		err    error
	)
	switch args.Connector {
	case "cloudflare":
		result, err = s.executeCloudflare(ctx, c, args)
	case "digitalocean":
		result, err = s.executeDigitalOcean(ctx, c, args)
	case "docker":
		result, err = s.executeDocker(ctx, c, args)
	case "forge":
		result, err = s.executeForge(ctx, c, args)
	case "github":
		result, err = s.executeGitHub(ctx, c, args)
	case "namecheap":
		result, err = s.executeNamecheap(ctx, c, args)
	case "ssh":
		result, err = s.executeSSH(ctx, c, args)
	default:
		err = externalConnectorError(args, ExternalConnectorUnsupported, nil)
	}
	if err != nil {
		gmcp.NotifyMessage(ctx, "error", fmt.Sprintf("Connector operation %s.%s failed: %s", args.Connector, args.Operation, redact.Text(err.Error())))
		gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Connector operation failed")
		return ExternalConnectorOperationResult{}, err
	}
	gmcp.NotifyMessage(ctx, "info", fmt.Sprintf("Connector operation %s.%s completed", args.Connector, args.Operation))
	gmcp.NotifyProgress(ctx, progressToken, 2, 2, "Connector operation completed")
	return result, nil
}

func (s *ExternalConnectorService) requireAcknowledgment(args ExternalConnectorOperationArgs) error {
	def, ok := s.definitionFor(args.Connector)
	if !ok {
		return nil
	}
	for _, op := range def.Operations {
		if op.Name == args.Operation && op.Destructive && !args.Acknowledged {
			return externalConnectorError(args, ExternalConnectorAckRequired, fmt.Errorf("destructive operation %q requires operator acknowledgment", args.Operation))
		}
	}
	return nil
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
	case "cloudflare":
		switch args.Operation {
		case "create_zone":
			accountID, err := requiredString(args.Config, "account_id")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			name, err := requiredString(args.Config, "name")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			zoneType := stringFromConfig(args.Config, "type", cfconn.ZoneTypeFull)
			return dryRunPreview(args, "Would create a Cloudflare zone.", map[string]any{
				"account_id": accountID,
				"name":       name,
			}, map[string]any{
				"type": zoneType,
			}), true, nil
		case "create_dns_record":
			zoneID, err := requiredString(args.Config, "zone_id")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			recordType, err := requiredString(args.Config, "type")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			name, err := requiredString(args.Config, "name")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			content, err := requiredString(args.Config, "content")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would create a Cloudflare DNS record.", map[string]any{
				"zone_id": zoneID,
				"name":    name,
				"type":    recordType,
			}, map[string]any{
				"content":  content,
				"ttl":      intFromConfig(args.Config, "ttl", 1),
				"proxied":  boolFromConfig(args.Config, "proxied"),
				"priority": intPointerFromConfig(args.Config, "priority"),
			}), true, nil
		case "delete_dns_record":
			zoneID, err := requiredString(args.Config, "zone_id")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			recordID, err := requiredString(args.Config, "record_id")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would delete a Cloudflare DNS record.", map[string]any{
				"zone_id":   zoneID,
				"record_id": recordID,
			}, nil), true, nil
		}
	case "namecheap":
		switch args.Operation {
		case "set_dns_record_set":
			domainName, set, err := namecheapRecordSetArgs(args.Config)
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would replace every Namecheap DNS host record and explicitly set email routing.", map[string]any{"domain": domainName}, map[string]any{"email_type": set.EmailType, "records": set.Records}, "All omitted records will be deleted. getHosts can omit existing records; supply a complete authoritative set."), true, nil
		case "create_dns_record", "delete_dns_record":
			return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorUnsupported, ncconn.ErrUnsafePerRecordWrite)
		case "set_custom_nameservers":
			domain, err := requiredString(args.Config, "domain")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			nameservers, err := requiredStringSlice(args.Config, "nameservers")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would switch a Namecheap domain to custom nameservers.", map[string]any{
				"domain": domain,
			}, map[string]any{
				"nameservers": nameservers,
			}, "Changing registrar nameservers moves DNS authority away from Namecheap's default nameservers for this domain."), true, nil
		}
	case "forge":
		switch args.Operation {
		case "deploy_site":
			serverID, siteID, err := serverSiteIDs(args.Config)
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would trigger a Forge site deployment.", map[string]any{
				"server_id": serverID,
				"site_id":   siteID,
			}, nil), true, nil
		case "exec_site_command":
			serverID, siteID, err := serverSiteIDs(args.Config)
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			command, err := requiredString(args.Config, "command")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would execute a Forge site command.", map[string]any{
				"server_id": serverID,
				"site_id":   siteID,
			}, map[string]any{
				"command": command,
			}), true, nil
		}
	case "digitalocean":
		switch args.Operation {
		case "create_droplet":
			name, err := requiredString(args.Config, "name")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			region, err := requiredString(args.Config, "region")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			size, err := requiredString(args.Config, "size")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			image, err := requiredString(args.Config, "image")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would create a DigitalOcean droplet.", map[string]any{
				"name":   name,
				"region": region,
				"size":   size,
				"image":  image,
			}, map[string]any{
				"ssh_keys":  args.Config["ssh_keys"],
				"user_data": stringFromConfig(args.Config, "user_data", ""),
			}), true, nil
		case "stop", "destroy":
			dropletID, err := requiredInt(args.Config, "droplet_id")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			summary := "Would power off a DigitalOcean droplet."
			if args.Operation == "destroy" {
				summary = "Would destroy a DigitalOcean droplet."
			}
			return dryRunPreview(args, summary, map[string]any{
				"droplet_id": dropletID,
			}, nil), true, nil
		}
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

func (s *ExternalConnectorService) executeCloudflare(ctx context.Context, c contract.Connector, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	cloudflare, ok := c.(*cfconn.Connector)
	if !ok {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, fmt.Errorf("registered connector has type %T", c))
	}

	switch args.Operation {
	case "list_zones":
		zones, err := cloudflare.ListZones(ctx)
		return externalConnectorResult(args, zones), err
	case "create_zone":
		accountID, err := requiredString(args.Config, "account_id")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		name, err := requiredString(args.Config, "name")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		zone, err := cloudflare.CreateZone(ctx, accountID, name, stringFromConfig(args.Config, "type", cfconn.ZoneTypeFull))
		if err != nil {
			return externalConnectorResult(args, nil), err
		}
		return externalConnectorResult(args, zone), nil
	case "list_dns_records":
		zoneID, err := requiredString(args.Config, "zone_id")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		records, err := cloudflare.ListDNSRecords(ctx, zoneID)
		return externalConnectorResult(args, records), err
	case "create_dns_record":
		zoneID, err := requiredString(args.Config, "zone_id")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		record, err := cloudflare.CreateDNSRecord(ctx, zoneID, cfconn.DNSRecord{
			Type:     stringFromConfig(args.Config, "type", ""),
			Name:     stringFromConfig(args.Config, "name", ""),
			Content:  stringFromConfig(args.Config, "content", ""),
			TTL:      intFromConfig(args.Config, "ttl", 1),
			Proxied:  boolFromConfig(args.Config, "proxied"),
			Priority: intPointerFromConfig(args.Config, "priority"),
		})
		if err != nil {
			return externalConnectorResult(args, nil), err
		}
		return externalConnectorResult(args, record), nil
	case "delete_dns_record":
		zoneID, err := requiredString(args.Config, "zone_id")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		recordID, err := requiredString(args.Config, "record_id")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		err = cloudflare.DeleteDNSRecord(ctx, zoneID, recordID)
		return externalConnectorResult(args, nil), err
	default:
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnsupported, nil)
	}
}

func (s *ExternalConnectorService) executeDigitalOcean(ctx context.Context, c contract.Connector, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	digitalocean, ok := c.(*doconn.Connector)
	if !ok {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, fmt.Errorf("registered connector has type %T", c))
	}

	switch args.Operation {
	case "list_droplets":
		droplets, err := digitalocean.ListDroplets(ctx)
		return externalConnectorResult(args, droplets), err
	case "get_droplet":
		dropletID, err := requiredInt(args.Config, "droplet_id")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		droplet, err := digitalocean.GetDroplet(ctx, dropletID)
		return externalConnectorResult(args, droplet), err
	case "create_droplet":
		res := externalResource(args)
		err := digitalocean.Create(ctx, &res)
		if err != nil {
			return externalConnectorResult(args, nil), err
		}
		dropletID, err := dropletIDFromConfig(res.Config)
		if err != nil {
			return externalConnectorResult(args, map[string]any{"droplet_id": res.Config["droplet_id"]}), nil
		}
		droplet, err := digitalocean.GetDroplet(ctx, dropletID)
		if err != nil {
			return externalConnectorResult(args, map[string]any{"droplet_id": dropletID}), nil
		}
		return externalConnectorResult(args, droplet), nil
	case "start":
		res := externalResource(args)
		err := digitalocean.Start(ctx, &res)
		return externalConnectorResult(args, nil), err
	case "stop":
		res := externalResource(args)
		err := digitalocean.Stop(ctx, &res)
		return externalConnectorResult(args, nil), err
	case "destroy":
		res := externalResource(args)
		err := digitalocean.Destroy(ctx, &res)
		return externalConnectorResult(args, nil), err
	case "status":
		res := externalResource(args)
		state, err := digitalocean.Status(ctx, &res)
		return externalConnectorResult(args, state), err
	default:
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnsupported, nil)
	}
}

func (s *ExternalConnectorService) executeForge(ctx context.Context, c contract.Connector, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	forge, ok := c.(*forgeconn.Connector)
	if !ok {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, fmt.Errorf("registered connector has type %T", c))
	}

	switch args.Operation {
	case "list_servers":
		servers, err := forge.ListServers(ctx)
		return externalConnectorResult(args, servers), err
	case "get_server":
		serverID, err := requiredInt(args.Config, "server_id")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		server, err := forge.GetServer(ctx, serverID)
		return externalConnectorResult(args, server), err
	case "list_sites":
		serverID, err := requiredInt(args.Config, "server_id")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		sites, err := forge.ListSites(ctx, serverID)
		return externalConnectorResult(args, sites), err
	case "get_deployment_script":
		serverID, siteID, err := serverSiteIDs(args.Config)
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		script, err := forge.GetDeploymentScript(ctx, serverID, siteID)
		return externalConnectorResult(args, script), err
	case "update_deployment_script":
		serverID, siteID, err := serverSiteIDs(args.Config)
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		content, err := requiredString(args.Config, "content")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		err = forge.UpdateDeploymentScript(ctx, serverID, siteID, content, boolFromConfig(args.Config, "auto_source"))
		return externalConnectorResult(args, nil), err
	case "deploy_site":
		serverID, siteID, err := serverSiteIDs(args.Config)
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		err = forge.DeploySite(ctx, serverID, siteID)
		return externalConnectorResult(args, nil), err
	case "exec_site_command":
		serverID, siteID, err := serverSiteIDs(args.Config)
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		command, err := requiredString(args.Config, "command")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		result, err := forge.ExecuteSiteCommand(ctx, serverID, siteID, command)
		return externalConnectorResult(args, result), err
	default:
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnsupported, nil)
	}
}

func (s *ExternalConnectorService) executeNamecheap(ctx context.Context, c contract.Connector, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	namecheap, ok := c.(*ncconn.Connector)
	if !ok {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, fmt.Errorf("registered connector has type %T", c))
	}

	switch args.Operation {
	case "get_dns_record_set":
		domainName, err := requiredString(args.Config, "domain")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		set, err := namecheap.GetDNSRecordSet(ctx, domainName)
		return externalConnectorResult(args, set), err
	case "set_dns_record_set":
		domainName, set, err := namecheapRecordSetArgs(args.Config)
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		err = namecheap.SetDNSRecordSet(ctx, domainName, set)
		return externalConnectorResult(args, set), err
	case "list_domains":
		domains, err := namecheap.ListDomains(ctx)
		return externalConnectorResult(args, domains), err
	case "get_domain_status":
		domainName, err := requiredString(args.Config, "domain")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		status, err := namecheap.GetDomainStatus(ctx, domainName)
		return externalConnectorResult(args, status), err
	case "list_dns_records":
		domainName, err := requiredString(args.Config, "domain")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		records, err := namecheap.ListDNSRecords(ctx, domainName)
		return externalConnectorResult(args, records), err
	case "create_dns_record":
		domainName, err := requiredString(args.Config, "domain")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		record := ncconn.DNSRecord{
			Type:   stringFromConfig(args.Config, "type", ""),
			Host:   stringFromConfig(args.Config, "host", ""),
			Value:  stringFromConfig(args.Config, "value", ""),
			TTL:    intFromConfig(args.Config, "ttl", 0),
			MXPref: intFromConfig(args.Config, "mx_pref", 0),
		}
		created, err := namecheap.CreateDNSRecord(ctx, domainName, record)
		return externalConnectorResult(args, created), err
	case "delete_dns_record":
		domainName, err := requiredString(args.Config, "domain")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		recordID, err := requiredInt(args.Config, "record_id")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		err = namecheap.DeleteDNSRecord(ctx, domainName, recordID)
		return externalConnectorResult(args, nil), err
	case "set_custom_nameservers":
		domainName, err := requiredString(args.Config, "domain")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		nameservers, err := requiredStringSlice(args.Config, "nameservers")
		if err != nil {
			return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
		}
		result, err := namecheap.SetCustomNameservers(ctx, domainName, nameservers)
		return externalConnectorResult(args, result), err
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

func dropletIDFromConfig(cfg map[string]any) (int, error) {
	return requiredInt(cfg, "droplet_id")
}

func intPointerFromConfig(cfg map[string]any, key string) *int {
	value := intFromConfig(cfg, key, -1)
	if value < 0 {
		return nil
	}
	return &value
}

func serverSiteIDs(cfg map[string]any) (int, int, error) {
	serverID, err := requiredInt(cfg, "server_id")
	if err != nil {
		return 0, 0, err
	}
	siteID, err := requiredInt(cfg, "site_id")
	if err != nil {
		return 0, 0, err
	}
	return serverID, siteID, nil
}

func namecheapRecordSetArgs(config map[string]any) (string, ncconn.DNSRecordSet, error) {
	domainName, err := requiredString(config, "domain")
	if err != nil {
		return "", ncconn.DNSRecordSet{}, err
	}
	emailType, err := requiredString(config, "email_type")
	if err != nil {
		return "", ncconn.DNSRecordSet{}, err
	}
	raw, exists := config["records"]
	if !exists || raw == nil {
		return "", ncconn.DNSRecordSet{}, errors.New("records must explicitly contain the complete authoritative host record array")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return "", ncconn.DNSRecordSet{}, err
	}
	var records []ncconn.DNSRecord
	if err = json.Unmarshal(data, &records); err != nil {
		return "", ncconn.DNSRecordSet{}, fmt.Errorf("records: %w", err)
	}
	for _, record := range records {
		if record.Type == "" || record.Host == "" || record.Value == "" {
			return "", ncconn.DNSRecordSet{}, errors.New("each record requires type, host and value")
		}
	}
	set := ncconn.DNSRecordSet{EmailType: emailType, Records: records}
	return domainName, set, set.Validate()
}

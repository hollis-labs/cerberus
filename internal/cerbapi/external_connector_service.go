package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/chrispian/cerberus/internal/connector"
	cfconn "github.com/chrispian/cerberus/internal/connector/cloudflare"
	dockerconn "github.com/chrispian/cerberus/internal/connector/docker"
	forgeconn "github.com/chrispian/cerberus/internal/connector/forge"
	ghconn "github.com/chrispian/cerberus/internal/connector/github"
	ncconn "github.com/chrispian/cerberus/internal/connector/namecheap"
	sshconn "github.com/chrispian/cerberus/internal/connector/ssh"
	contract "github.com/chrispian/cerberus/pkg/connector"
	"github.com/chrispian/cerberus/pkg/resource"
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
	return fmt.Sprintf("%s %s: %s: %v", e.Connector, e.Operation, e.Code, e.Err)
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
	if s == nil {
		return nil
	}
	defs := make(map[string]contract.Definition)
	if s.registry != nil {
		for _, def := range s.registry.Definitions() {
			if _, ok := s.registry.Get(def.ID); ok {
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
	if s == nil {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, errors.New("connector registry is not configured"))
	}
	if args.DryRun {
		if preview, ok, err := s.dryRunPreview(args); ok || err != nil {
			if err != nil {
				return ExternalConnectorOperationResult{}, err
			}
			return externalConnectorResult(args, preview), nil
		}
	}
	if s.managedPlugins != nil && s.managedPlugins.Loaded(args.Connector) {
		return s.managedPlugins.Execute(ctx, args.Connector, PluginConnectorExecArgs{
			Operation:    args.Operation,
			Config:       args.Config,
			DryRun:       args.DryRun,
			Acknowledged: args.Acknowledged,
		})
	}
	if s.managedPlugins != nil && s.managedPlugins.Installed(args.Connector) && !s.managedPlugins.Loaded(args.Connector) {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, fmt.Errorf("plugin connector %q is installed but not loaded", args.Connector))
	}
	if s.registry == nil {
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnavailable, errors.New("connector registry is not configured"))
	}

	c, ok := s.registry.Get(args.Connector)
	if !ok {
		err := s.registry.UnavailableError(args.Connector)
		return ExternalConnectorOperationResult{}, externalConnectorError(args, unavailableCode(err), err)
	}
	if err := s.requireAcknowledgment(args); err != nil {
		return ExternalConnectorOperationResult{}, err
	}

	switch args.Connector {
	case "cloudflare":
		return s.executeCloudflare(ctx, c, args)
	case "docker":
		return s.executeDocker(ctx, c, args)
	case "forge":
		return s.executeForge(ctx, c, args)
	case "github":
		return s.executeGitHub(ctx, c, args)
	case "namecheap":
		return s.executeNamecheap(ctx, c, args)
	case "ssh":
		return s.executeSSH(ctx, c, args)
	default:
		return ExternalConnectorOperationResult{}, externalConnectorError(args, ExternalConnectorUnsupported, nil)
	}
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
		case "create_dns_record":
			domain, err := requiredString(args.Config, "domain")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			recordType, err := requiredString(args.Config, "type")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			host, err := requiredString(args.Config, "host")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			value, err := requiredString(args.Config, "value")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would create a Namecheap DNS record.", map[string]any{
				"domain": domain,
				"host":   host,
				"type":   recordType,
			}, map[string]any{
				"value":   value,
				"ttl":     intFromConfig(args.Config, "ttl", 0),
				"mx_pref": intFromConfig(args.Config, "mx_pref", 0),
			}, "Namecheap DNS writes replace the full host-record set for the domain; concurrent edits can race."), true, nil
		case "delete_dns_record":
			domain, err := requiredString(args.Config, "domain")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			recordID, err := requiredInt(args.Config, "record_id")
			if err != nil {
				return ExternalConnectorDryRunPreview{}, true, externalConnectorError(args, ExternalConnectorInvalidArgs, err)
			}
			return dryRunPreview(args, "Would delete a Namecheap DNS record.", map[string]any{
				"domain":    domain,
				"record_id": recordID,
			}, nil, "Namecheap DNS writes replace the full host-record set for the domain; concurrent edits can race."), true, nil
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
	msg := strings.ToLower(err.Error())
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

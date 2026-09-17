package cerbapi

import (
	"encoding/json"

	cf "github.com/hollis-labs/cerberus/internal/connector/cloudflare"
	do "github.com/hollis-labs/cerberus/internal/connector/digitalocean"
	docker "github.com/hollis-labs/cerberus/internal/connector/docker"
	forge "github.com/hollis-labs/cerberus/internal/connector/forge"
	gh "github.com/hollis-labs/cerberus/internal/connector/github"
	nc "github.com/hollis-labs/cerberus/internal/connector/namecheap"
	ssh "github.com/hollis-labs/cerberus/internal/connector/ssh"
)

// decodeConnectorPayload restores the service's DTOs across JSON transport.
// This preserves CLI field order, integer precision, timestamps and type
// assertions instead of forcing every caller to reinterpret generic maps.
func decodeConnectorPayload(args ExternalConnectorOperationArgs, raw json.RawMessage) (any, error) {
	if args.DryRun {
		var preview ExternalConnectorDryRunPreview
		if json.Unmarshal(raw, &preview) == nil && preview.DryRun {
			return preview, nil
		}
	}
	switch args.Connector + "/" + args.Operation {
	case "cloudflare/list_zones":
		return decodePayload[[]cf.Zone](raw)
	case "cloudflare/create_zone":
		return decodePayload[*cf.Zone](raw)
	case "cloudflare/list_dns_records":
		return decodePayload[[]cf.DNSRecord](raw)
	case "cloudflare/create_dns_record":
		return decodePayload[*cf.DNSRecord](raw)
	case "digitalocean/list_droplets":
		return decodePayload[[]do.DropletStatus](raw)
	case "digitalocean/get_droplet":
		return decodePayload[*do.DropletStatus](raw)
	case "digitalocean/create_droplet":
		var reference struct {
			DropletID *int `json:"droplet_id"`
		}
		if json.Unmarshal(raw, &reference) == nil && reference.DropletID != nil {
			return raw, nil // creation succeeded but the follow-up read failed
		}
		return decodePayload[*do.DropletStatus](raw)
	case "docker/list_containers":
		return decodePayload[[]docker.Container](raw)
	case "forge/list_servers":
		return decodePayload[[]forge.Server](raw)
	case "forge/get_server":
		return decodePayload[*forge.Server](raw)
	case "forge/list_sites":
		return decodePayload[[]forge.Site](raw)
	case "forge/exec_site_command":
		return decodePayload[*forge.SiteCommand](raw)
	case "github/status":
		return decodePayload[*gh.RepoStatus](raw)
	case "github/list_releases":
		return decodePayload[[]gh.Release](raw)
	case "github/list_workflow_runs":
		return decodePayload[[]gh.WorkflowRun](raw)
	case "namecheap/list_domains":
		return decodePayload[[]nc.Domain](raw)
	case "namecheap/get_domain_status":
		return decodePayload[*nc.DomainStatus](raw)
	case "namecheap/list_dns_records":
		return decodePayload[[]nc.DNSRecord](raw)
	case "namecheap/get_dns_record_set":
		return decodePayload[*nc.DNSRecordSet](raw)
	case "namecheap/set_dns_record_set":
		return decodePayload[nc.DNSRecordSet](raw)
	case "namecheap/set_custom_nameservers":
		return decodePayload[*nc.DomainNameserverUpdate](raw)
	case "ssh/exec":
		return decodePayload[*ssh.ExecResult](raw)
	case "ssh/put", "ssh/get":
		return decodePayload[*ssh.TransferResult](raw)
	case "ssh/put_dir", "ssh/get_dir":
		return decodePayload[*ssh.DirTransferResult](raw)
	case "docker/logs", "forge/get_deployment_script", "ssh/status", "docker/status", "digitalocean/status":
		return decodePayload[string](raw)
	default:
		return raw, nil // retain unknown/plugin payloads without losing fields
	}
}

func decodePayload[T any](raw json.RawMessage) (any, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

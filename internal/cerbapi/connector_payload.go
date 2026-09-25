package cerbapi

import (
	"encoding/json"

	docker "github.com/hollis-labs/cerberus/internal/connector/docker"
	gh "github.com/hollis-labs/cerberus/internal/connector/github"
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
	case "docker/list_containers":
		return decodePayload[[]docker.Container](raw)
	case "github/status":
		return decodePayload[*gh.RepoStatus](raw)
	case "github/list_releases":
		return decodePayload[[]gh.Release](raw)
	case "github/list_workflow_runs":
		return decodePayload[[]gh.WorkflowRun](raw)
	case "ssh/exec":
		return decodePayload[*ssh.ExecResult](raw)
	case "ssh/put", "ssh/get":
		return decodePayload[*ssh.TransferResult](raw)
	case "ssh/put_dir", "ssh/get_dir":
		return decodePayload[*ssh.DirTransferResult](raw)
	case "docker/logs", "ssh/status", "docker/status":
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

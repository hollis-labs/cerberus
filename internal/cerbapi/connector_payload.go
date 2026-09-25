package cerbapi

import (
	"context"
	"encoding/json"
	"fmt"

	docker "github.com/hollis-labs/cerberus/internal/connector/docker"
	gh "github.com/hollis-labs/cerberus/internal/connector/github"
	ssh "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/redact"
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

// RenderInProcessResult gives a result the in-process lane produced what the
// socket gives one it serves: the payload marshaled through the request's
// scope — its resolved credentials, sensitive keys and the regex net — and
// decoded back into the same DTO, so the CLI prints the same thing on either
// lane. Before it, the in-process lane printed ssh exec output and docker
// logs with no redaction at all.
func RenderInProcessResult(ctx context.Context, args ExternalConnectorOperationArgs, result ExternalConnectorOperationResult) (ExternalConnectorOperationResult, error) {
	if result.Data == nil {
		return result, nil
	}
	raw, err := redact.ScopeFrom(ctx).Marshal(result.Data)
	if err != nil {
		return ExternalConnectorOperationResult{}, fmt.Errorf("render %s %s payload: %w", args.Connector, args.Operation, err)
	}
	data, err := decodeConnectorPayload(args, raw)
	if err != nil {
		return ExternalConnectorOperationResult{}, fmt.Errorf("decode %s %s payload: %w", args.Connector, args.Operation, err)
	}
	result.Data = data
	return result, nil
}

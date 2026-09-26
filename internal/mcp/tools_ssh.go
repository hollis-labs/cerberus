package mcp

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// NewCerberusSSHExecTool creates the cerberus_ssh_exec tool.
func NewCerberusSSHExecTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_ssh_exec",
		Description: "Run a command on an SSH resource.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id":  map[string]interface{}{"type": "string", "description": "SSH resource ID."},
			"command":      map[string]interface{}{"type": "string", "description": "Command to run on the remote host."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "resource_id", "command"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID := stringArg(args, "resource_id")
			command := stringArg(args, "command")
			result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
				Connector:    "ssh",
				Operation:    "exec",
				Config:       sshToolConfig(resourceID, command),
				DryRun:       boolArg(args, "dry_run"),
				Acknowledged: boolArg(args, "acknowledged"),
				ApprovalID:   stringArg(args, argApprovalID),
			})
			if err != nil {
				return connectorFailure(ctx, err)
			}
			return marshalConnectorData(result.Data)
		},
	})
}

// NewCerberusSSHStatusTool creates the cerberus_ssh_status tool.
func NewCerberusSSHStatusTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_ssh_status",
		Description: "Check connectivity and host info for an SSH resource.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id": map[string]interface{}{"type": "string", "description": "SSH resource ID."},
		}, "resource_id"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			resourceID := stringArg(args, "resource_id")
			result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
				Connector: "ssh",
				Operation: "status",
				Config:    sshToolConfig(resourceID, ""),
			})
			if err != nil {
				return connectorFailure(ctx, err)
			}
			return marshalConnectorData(result.Data)
		},
	})
}

// NewCerberusSSHPutTool creates the cerberus_ssh_put tool.
func NewCerberusSSHPutTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_ssh_put",
		Description: "Upload a local file to an SSH resource over SFTP, replacing the remote file if it exists.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id":  map[string]interface{}{"type": "string", "description": "SSH resource ID."},
			"local_path":   map[string]interface{}{"type": "string", "description": "Local file to upload."},
			"remote_path":  map[string]interface{}{"type": "string", "description": "Destination path on the remote host."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "resource_id", "local_path", "remote_path"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return runSSHTransfer(ctx, client, "put", args,
				boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	})
}

// NewCerberusSSHGetTool creates the cerberus_ssh_get tool.
func NewCerberusSSHGetTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_ssh_get",
		Description: "Download a file from an SSH resource over SFTP, overwriting local_path if it exists. Requires acknowledged=true.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id":  map[string]interface{}{"type": "string", "description": "SSH resource ID."},
			"remote_path":  map[string]interface{}{"type": "string", "description": "File to download from the remote host."},
			"local_path":   map[string]interface{}{"type": "string", "description": "Local destination path."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge overwriting local_path. Required: the download writes to the local filesystem."},
		}, "resource_id", "remote_path", "local_path"),
		// Not read-only: the download overwrites local_path, which the caller
		// chooses. Which local paths a caller may write is P1/P2 path policy.
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return runSSHTransfer(ctx, client, "get", args, false, boolArg(args, "acknowledged"))
		},
	})
}

// NewCerberusSSHPutDirTool creates the cerberus_ssh_put_dir tool.
func NewCerberusSSHPutDirTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_ssh_put_dir",
		Description: "Recursively upload a local directory tree to an SSH resource over SFTP, replacing remote files that already exist. Permission bits are carried and a symlink pointing outside the tree is refused. Every byte is copied every time — there is no delta transfer.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id":  map[string]interface{}{"type": "string", "description": "SSH resource ID."},
			"local_path":   map[string]interface{}{"type": "string", "description": "Local directory to upload."},
			"remote_path":  map[string]interface{}{"type": "string", "description": "Destination directory on the remote host."},
			"dry_run":      map[string]interface{}{"type": "boolean", "description": "Preview only. Reports file count and total bytes without transferring."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge this change."},
		}, "resource_id", "local_path", "remote_path"),
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return runSSHTransfer(ctx, client, "put_dir", args,
				boolArg(args, "dry_run"), boolArg(args, "acknowledged"))
		},
	})
}

// NewCerberusSSHGetDirTool creates the cerberus_ssh_get_dir tool.
func NewCerberusSSHGetDirTool(client cerbapi.Client) Tool {
	return contractTool(Tool{
		Name:        "cerberus_ssh_get_dir",
		Description: "Recursively download a directory tree from an SSH resource over SFTP into a local directory, overwriting local files that already exist. Requires acknowledged=true.",
		InputSchema: objectSchema(map[string]interface{}{
			"resource_id":  map[string]interface{}{"type": "string", "description": "SSH resource ID."},
			"remote_path":  map[string]interface{}{"type": "string", "description": "Directory to download from the remote host."},
			"local_path":   map[string]interface{}{"type": "string", "description": "Local destination directory."},
			"acknowledged": map[string]interface{}{"type": "boolean", "description": "Acknowledge overwriting local files. Required: the download writes to the local filesystem."},
		}, "resource_id", "remote_path", "local_path"),
		// Not read-only: the download overwrites local_path, which the caller
		// chooses. Which local paths a caller may write is P1/P2 path policy.
		Handler: func(ctx context.Context, args map[string]interface{}) (any, error) {
			return runSSHTransfer(ctx, client, "get_dir", args, false, boolArg(args, "acknowledged"))
		},
	})
}

// runSSHTransfer backs all four transfer tools; they differ only in direction,
// in whether the transfer is recursive, and in whether the operation needs an
// acknowledgment. All four take the same two path arguments.
func runSSHTransfer(ctx context.Context, client cerbapi.Client, operation string, args map[string]interface{}, dryRun, acknowledged bool) (any, error) {
	opConfig := sshToolConfig(stringArg(args, "resource_id"), "")
	opConfig["local_path"] = stringArg(args, "local_path")
	opConfig["remote_path"] = stringArg(args, "remote_path")

	result, err := client.ExecuteConnectorOperation(ctx, cerbapi.ExternalConnectorOperationArgs{
		Connector:    "ssh",
		Operation:    operation,
		Config:       opConfig,
		DryRun:       dryRun,
		Acknowledged: acknowledged,
		ApprovalID:   stringArg(args, argApprovalID),
	})
	if err != nil {
		return connectorFailure(ctx, err)
	}
	return marshalConnectorData(result.Data)
}

// sshToolConfig names the target by resource id only. The process serving
// the call resolves the id against its own config — so a resource registered
// after this MCP subprocess started is still reachable — and refuses any
// connection field an agent tries to add.
func sshToolConfig(resourceID, command string) map[string]any {
	cfg := map[string]any{"id": resourceID}
	if command != "" {
		cfg["command"] = command
	}
	return cfg
}

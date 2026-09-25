package mcp

import (
	"context"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// capturingSSHClient records what an SSH tool sends to the serving process.
type capturingSSHClient struct {
	fakeSocketProgressClient
	args cerbapi.ExternalConnectorOperationArgs
}

func (c *capturingSSHClient) ExecuteConnectorOperation(ctx context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	c.args = args
	return cerbapi.ExternalConnectorOperationResult{Connector: args.Connector, Operation: args.Operation}, nil
}

// Every SSH tool names its target by resource id and sends nothing about the
// connection: the serving process resolves the id and refuses anything else.
// Extra arguments an agent adds are not forwarded either.
func TestSSHToolsSendOnlyTheResourceID(t *testing.T) {
	allowed := map[string]bool{"id": true, "command": true, "local_path": true, "remote_path": true}
	for _, tc := range []struct {
		name string
		tool func(cerbapi.Client) Tool
		args map[string]interface{}
	}{
		{"exec", NewCerberusSSHExecTool, map[string]interface{}{"command": "uptime"}},
		{"status", NewCerberusSSHStatusTool, nil},
		{"put", NewCerberusSSHPutTool, map[string]interface{}{"local_path": "/tmp/a", "remote_path": "/srv/a"}},
		{"get", NewCerberusSSHGetTool, map[string]interface{}{"remote_path": "/srv/a", "local_path": "/tmp/a"}},
		{"put_dir", NewCerberusSSHPutDirTool, map[string]interface{}{"local_path": "/tmp/d", "remote_path": "/srv/d"}},
		{"get_dir", NewCerberusSSHGetDirTool, map[string]interface{}{"remote_path": "/srv/d", "local_path": "/tmp/d"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &capturingSSHClient{}
			args := map[string]interface{}{"resource_id": "muctlvaig", "host": "evil.example", "key_file": "/tmp/k", "allow_insecure_host_key": true}
			for k, v := range tc.args {
				args[k] = v
			}
			if _, err := tc.tool(client).Handler(context.Background(), args); err != nil {
				t.Fatalf("handler: %v", err)
			}
			if client.args.Config["id"] != "muctlvaig" {
				t.Fatalf("config = %#v, want id muctlvaig", client.args.Config)
			}
			for key := range client.args.Config {
				if !allowed[key] {
					t.Errorf("tool sent %q; only the id and operation fields may travel", key)
				}
			}
		})
	}
}

package mcp

import (
	"context"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// capturingDockerClient records the operation args a Docker tool sends, so a
// test can assert the mapping from tool arguments to connector config.
type capturingDockerClient struct {
	fakeSocketProgressClient
	args cerbapi.ExternalConnectorOperationArgs
}

func (c *capturingDockerClient) ExecuteConnectorOperation(ctx context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	c.args = args
	return c.fakeSocketProgressClient.ExecuteConnectorOperation(ctx, args)
}

// Docker tools never send an ad-hoc target: no DOCKER_HOST, docker context or
// compose file, even when an agent adds them to the arguments. The socket
// would refuse them; a remote daemon or a stack is a declared resource.
func TestDockerToolsNeverSendAdHocTargets(t *testing.T) {
	cases := []struct {
		name string
		tool func(cerbapi.Client) Tool
		args map[string]interface{}
	}{
		{"ps", NewCerberusDockerPSTool, map[string]interface{}{}},
		{"logs", NewCerberusDockerLogsTool, map[string]interface{}{"container": "web"}},
		{"up", NewCerberusDockerUpTool, map[string]interface{}{"container_name": "web"}},
		{"down", NewCerberusDockerDownTool, map[string]interface{}{"container_name": "web"}},
		{"up by resource", NewCerberusDockerUpTool, map[string]interface{}{"resource_id": "mtbf-monitor"}},
		{"down by resource", NewCerberusDockerDownTool, map[string]interface{}{"resource_id": "mtbf-monitor"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &capturingDockerClient{}
			args := map[string]interface{}{
				"docker_host":    "ssh://cburks@muctlvaig",
				"docker_context": "azure-dev",
				"compose_file":   "/tmp/evil.yml",
			}
			for key, value := range tc.args {
				args[key] = value
			}
			if _, err := tc.tool(client).Handler(context.Background(), args); err != nil {
				t.Fatalf("handler: %v", err)
			}
			for _, key := range []string{"host", "context", "docker_host", "docker_context", "compose_file"} {
				if _, ok := client.args.Config[key]; ok {
					t.Errorf("tool sent %q: %#v", key, client.args.Config)
				}
			}
		})
	}
}

func TestDockerLifecycleToolsSendAResourceByID(t *testing.T) {
	for name, tool := range map[string]func(cerbapi.Client) Tool{"up": NewCerberusDockerUpTool, "down": NewCerberusDockerDownTool} {
		client := &capturingDockerClient{}
		if _, err := tool(client).Handler(context.Background(), map[string]interface{}{"resource_id": "mtbf-monitor"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(client.args.Config) != 1 || client.args.Config["resource"] != "mtbf-monitor" {
			t.Fatalf("%s: config = %#v, want only resource=mtbf-monitor", name, client.args.Config)
		}
	}
}

func TestDockerToolSchemasOfferNoAdHocTargets(t *testing.T) {
	client := &capturingDockerClient{}
	tools := map[string]Tool{
		"ps":   NewCerberusDockerPSTool(client),
		"logs": NewCerberusDockerLogsTool(client),
		"up":   NewCerberusDockerUpTool(client),
		"down": NewCerberusDockerDownTool(client),
	}
	for name, tool := range tools {
		schema, ok := tool.InputSchema.(map[string]interface{})
		if !ok {
			t.Fatalf("%s: input schema is %T", name, tool.InputSchema)
		}
		properties, ok := schema["properties"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s: input schema has no properties: %#v", name, schema)
		}
		for _, key := range []string{"docker_host", "docker_context", "compose_file"} {
			if _, ok := properties[key]; ok {
				t.Errorf("%s: schema still offers %s", name, key)
			}
		}
		// additionalProperties is false, so a strict client cannot send an
		// unadvertised parameter either.
		if schema["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties = %#v, want false", name, schema["additionalProperties"])
		}
	}
	for _, name := range []string{"up", "down"} {
		properties := tools[name].InputSchema.(map[string]interface{})["properties"].(map[string]interface{})
		if _, ok := properties["resource_id"]; !ok {
			t.Errorf("%s: schema does not offer resource_id", name)
		}
	}
}

// Tools over operations that need acknowledgment advertise `acknowledged`
// and forward it: docker up and down are lifecycle, and so is droplet start
// (Decision 14); ssh get and get_dir write to the local filesystem.
func TestLifecycleToolsForwardAcknowledged(t *testing.T) {
	for _, tc := range []struct {
		tool func(cerbapi.Client) Tool
		args map[string]interface{}
	}{
		{NewCerberusDockerUpTool, map[string]interface{}{"container_name": "web"}},
		{NewCerberusDockerDownTool, map[string]interface{}{"resource_id": "stack"}},
		{NewCerberusDropletStartTool, map[string]interface{}{"droplet_id": float64(42)}},
		{NewCerberusSSHGetTool, map[string]interface{}{"resource_id": "box", "remote_path": "/a", "local_path": "/tmp/a"}},
		{NewCerberusSSHGetDirTool, map[string]interface{}{"resource_id": "box", "remote_path": "/a", "local_path": "/tmp/a"}},
	} {
		client := &capturingDockerClient{}
		tool := tc.tool(client)
		props, _ := tool.InputSchema.(map[string]interface{})["properties"].(map[string]interface{})
		if _, ok := props["acknowledged"]; !ok {
			t.Errorf("%s does not advertise acknowledged", tool.Name)
		}
		tc.args["acknowledged"] = true
		_, _ = tool.Handler(context.Background(), tc.args)
		if !client.args.Acknowledged {
			t.Errorf("%s did not forward acknowledged", tool.Name)
		}
	}
}

// capturingRuntimeClient records the options each runtime mutation receives.
type capturingRuntimeClient struct {
	fakeSocketProgressClient
	acked map[string]bool
}

func (c *capturingRuntimeClient) record(name string, opts []cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	c.acked[name] = cerbapi.ApplyMutationOptions(opts).Acknowledged
	return &cerbapi.OpResult{Success: true}, nil
}
func (c *capturingRuntimeClient) ReloadResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return c.record("reload", o)
}
func (c *capturingRuntimeClient) StopResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return c.record("stop", o)
}
func (c *capturingRuntimeClient) DeployResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return c.record("deploy", o)
}
func (c *capturingRuntimeClient) ApplyResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return c.record("apply", o)
}
func (c *capturingRuntimeClient) SyncResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return c.record("sync", o)
}
func (c *capturingRuntimeClient) RemoveResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return c.record("remove", o)
}
func (c *capturingRuntimeClient) RunPipeline(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.PipelineRunResult, error) {
	c.acked["pipeline"] = cerbapi.ApplyMutationOptions(o).Acknowledged
	return &cerbapi.PipelineRunResult{Success: true}, nil
}

// Every resource mutation tool and pipeline run advertises acknowledged and
// forwards exactly what the agent sent.
func TestRuntimeToolsForwardAcknowledged(t *testing.T) {
	for _, tc := range []struct {
		tool func(cerbapi.Client) Tool
		key  string
		args map[string]interface{}
	}{
		{NewCerberusResourceReloadTool, "reload", map[string]interface{}{"resource_id": "svc"}},
		{NewCerberusResourceStopTool, "stop", map[string]interface{}{"resource_id": "svc"}},
		{NewCerberusResourceDeployTool, "deploy", map[string]interface{}{"resource_id": "svc"}},
		{NewCerberusResourceEnsureFreshTool, "deploy", map[string]interface{}{"resource_id": "svc", "force": true}},
		{NewCerberusResourceApplyTool, "apply", map[string]interface{}{"resource_id": "svc"}},
		{NewCerberusResourceSyncTool, "sync", map[string]interface{}{"resource_id": "svc"}},
		{NewCerberusResourceRemoveTool, "remove", map[string]interface{}{"resource_id": "svc"}},
		{NewCerberusPipelineRunTool, "pipeline", map[string]interface{}{"pipeline_id": "p"}},
	} {
		for _, ack := range []bool{false, true} {
			client := &capturingRuntimeClient{acked: map[string]bool{}}
			tool := tc.tool(client)
			props, _ := tool.InputSchema.(map[string]interface{})["properties"].(map[string]interface{})
			if _, ok := props["acknowledged"]; !ok {
				t.Errorf("%s does not advertise acknowledged", tool.Name)
			}
			args := map[string]interface{}{"acknowledged": ack}
			for k, v := range tc.args {
				args[k] = v
			}
			_, _ = tool.Handler(context.Background(), args)
			if got, ok := client.acked[tc.key]; !ok || got != ack {
				t.Errorf("%s acknowledged=%v forwarded %v (called %v)", tool.Name, ack, got, ok)
			}
		}
	}
}

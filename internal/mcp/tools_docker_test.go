package mcp

import (
	"context"
	"strings"
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

func TestDockerToolsForwardHostSelectionToTheConnector(t *testing.T) {
	cases := []struct {
		name string
		tool func(cerbapi.Client) Tool
		args map[string]interface{}
	}{
		{"ps", NewCerberusDockerPSTool, map[string]interface{}{}},
		{"logs", NewCerberusDockerLogsTool, map[string]interface{}{"container": "web"}},
		{"up", NewCerberusDockerUpTool, map[string]interface{}{"container_name": "web"}},
		{"down", NewCerberusDockerDownTool, map[string]interface{}{"container_name": "web"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &capturingDockerClient{}
			args := map[string]interface{}{"docker_host": "ssh://cburks@muctlvaig"}
			for key, value := range tc.args {
				args[key] = value
			}

			if _, err := tc.tool(client).Handler(context.Background(), args); err != nil {
				t.Fatalf("handler: %v", err)
			}
			if got := client.args.Config["host"]; got != "ssh://cburks@muctlvaig" {
				t.Fatalf("config host = %#v, want the requested host", got)
			}
		})
	}
}

func TestDockerToolsOmitHostSelectionWhenNoneIsAskedFor(t *testing.T) {
	client := &capturingDockerClient{}
	if _, err := NewCerberusDockerPSTool(client).Handler(context.Background(), map[string]interface{}{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	// A nil config is what every caller sent before host selection existed;
	// sending an empty host would make "default" an explicit choice the
	// connector has to unpick.
	if _, ok := client.args.Config["host"]; ok {
		t.Fatalf("config gained a host key without one being asked for: %#v", client.args.Config)
	}
}

func TestDockerToolsAdvertiseHostSelection(t *testing.T) {
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
		for _, key := range []string{"docker_host", "docker_context"} {
			if _, ok := properties[key]; !ok {
				t.Errorf("%s: schema does not advertise %s", name, key)
			}
		}
		// additionalProperties is false on these schemas, so a parameter the
		// handler reads but the schema omits is a parameter a strict client
		// cannot send.
		if schema["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties = %#v, want false", name, schema["additionalProperties"])
		}
		if !strings.Contains(tool.Description, "remote Docker host") {
			t.Errorf("%s: description does not mention remote hosts: %q", name, tool.Description)
		}
	}
}

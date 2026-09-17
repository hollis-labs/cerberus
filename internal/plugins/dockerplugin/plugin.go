package dockerplugin

import (
	"context"
	"encoding/json"
	"fmt"

	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	plugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/cerberus/pkg/resource"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type connectorFactory func() (*dockerconn.Connector, error)

type Plugin struct {
	newConnector connectorFactory
	connector    *dockerconn.Connector
}

var _ subprocess.Plugin = (*Plugin)(nil)
var _ subprocess.HealthChecker = (*Plugin)(nil)
var _ subprocess.MCPHandler = (*Plugin)(nil)

func New() *Plugin {
	return &Plugin{newConnector: dockerconn.New}
}

func NewWithConnector(connector *dockerconn.Connector) *Plugin {
	return &Plugin{
		newConnector: func() (*dockerconn.Connector, error) { return connector, nil },
		connector:    connector,
	}
}

func (p *Plugin) Init(context.Context, subprocess.InitParams) (subprocess.InitResult, error) {
	def := dockerconn.Definition()
	return subprocess.InitResult{
		ID:          def.ID,
		Name:        "Cerberus Docker Connector",
		Version:     def.Version,
		Description: "Cerberus Docker connector subprocess plugin",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	if p.connector != nil {
		return subprocess.LoadResult{}, nil
	}
	connector, err := p.newConnector()
	if err != nil {
		return subprocess.LoadResult{}, err
	}
	p.connector = connector
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	p.connector = nil
	return nil
}

func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	if p.connector == nil {
		return subprocess.HealthStatus{OK: false, Message: "not loaded"}, nil
	}
	return subprocess.HealthStatus{OK: true, Message: "ready"}, nil
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	if p.connector == nil {
		return subprocess.MCPCallResult{}, fmt.Errorf("docker plugin is not loaded")
	}

	// The tool schemas come from dockerconn.Definition(), which advertises host
	// selection on every operation, so the handler has to honor it or the
	// schema lies.
	target, err := dockerconn.TargetFromConfig(req.Arguments)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	connector := p.connector.WithTarget(target)

	switch req.ToolName {
	case plugin.ToolNameForOperation("docker", "list_containers"):
		containers, err := connector.ListContainers(ctx)
		return marshalResult(containers, err)
	case plugin.ToolNameForOperation("docker", "logs"):
		name, err := requiredString(req.Arguments, "container")
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		logs, err := connector.Logs(ctx, name, intArg(req.Arguments, "lines", 50))
		return marshalResult(logs, err)
	case plugin.ToolNameForOperation("docker", "start"):
		err := connector.Start(ctx, resourceFromArgs(req.Arguments))
		return marshalResult(nil, err)
	case plugin.ToolNameForOperation("docker", "stop"):
		err := connector.Stop(ctx, resourceFromArgs(req.Arguments))
		return marshalResult(nil, err)
	case plugin.ToolNameForOperation("docker", "destroy"):
		err := connector.Destroy(ctx, resourceFromArgs(req.Arguments))
		return marshalResult(nil, err)
	case plugin.ToolNameForOperation("docker", "status"):
		state, err := connector.Status(ctx, resourceFromArgs(req.Arguments))
		return marshalResult(state, err)
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
	}
}

func resourceFromArgs(args map[string]interface{}) *resource.Resource {
	container := stringArg(args, "container")
	return &resource.Resource{
		ID:        stringArgDefault(args, "id", container),
		Name:      stringArgDefault(args, "name", container),
		Type:      resource.Container,
		Connector: "docker",
		Config:    args,
	}
}

func requiredString(args map[string]interface{}, key string) (string, error) {
	value := stringArg(args, key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func stringArg(args map[string]interface{}, key string) string {
	return stringArgDefault(args, key, "")
}

func stringArgDefault(args map[string]interface{}, key, fallback string) string {
	if value, ok := args[key].(string); ok && value != "" {
		return value
	}
	return fallback
}

func intArg(args map[string]interface{}, key string, fallback int) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return fallback
	}
}

func marshalResult(data any, err error) (subprocess.MCPCallResult, error) {
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	if data == nil {
		return subprocess.MCPCallResult{Content: json.RawMessage("null")}, nil
	}
	content, err := json.Marshal(data)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{Content: content}, nil
}

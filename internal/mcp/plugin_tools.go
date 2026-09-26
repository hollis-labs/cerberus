package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/plugin"
)

// Plugin operations reach MCP as generated tools, one per operation the
// operator exposed in connector-config.yaml (`<id>: mcp: expose: [...]`).
// Exposure is default-deny: a loaded plugin with nothing listed serves no
// tools, so installing a plugin never silently widens what an agent can call.
// Enabling one is a file edit and a `managed load`, never an MCP or socket
// call.
//
// A generated tool is named plugin.ToolNameForOperation(id, op), takes the
// manifest's input schema plus dry_run and acknowledged where the contract
// calls for them, gets its hints from the operation's effective contract (a
// gap reads as exec), and runs through the admin lane like every hand-written
// connector tool.

// PluginToolRefreshInterval is how often a served tool list is reconciled
// with the daemon's loaded plugins and their exposure. The daemon is a
// separate process from `cerberus mcp`, so a change there is noticed by
// polling; a client learns of it through notifications/tools/list_changed.
const PluginToolRefreshInterval = 15 * time.Second

// validToolName is MCP's tool-name grammar.
var validToolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Host-added arguments. They are not operation arguments, so a manifest that
// declares one of them cannot have a generated tool: the call would be
// ambiguous.
const (
	argDryRun       = "dry_run"
	argAcknowledged = "acknowledged"
)

// PluginToolSet is the generated tools the daemon's state calls for, and the
// exposures it refused.
type PluginToolSet struct {
	Tools   []Tool
	Refused []string
}

// PluginTools builds the generated tools for every loaded managed plugin's
// exposed operations. reserved holds the names already served by hand-written
// tools; a generated tool that would take one of them is refused rather than
// shadowing it.
func PluginTools(ctx context.Context, client cerbapi.Client, reserved map[string]bool) (PluginToolSet, error) {
	plugins, err := client.ListManagedPlugins(ctx)
	if err != nil {
		return PluginToolSet{}, fmt.Errorf("list managed plugins: %w", err)
	}
	var exposing []cerbapi.ManagedPluginConnectorState
	for _, p := range plugins {
		if p.Loaded && len(p.MCPExpose) > 0 {
			exposing = append(exposing, p)
		}
	}
	if len(exposing) == 0 {
		return PluginToolSet{}, nil
	}
	defs, err := client.ListConnectors(ctx)
	if err != nil {
		return PluginToolSet{}, fmt.Errorf("list connectors: %w", err)
	}
	byID := make(map[string]contract.Definition, len(defs))
	for _, def := range defs {
		byID[def.ID] = def
	}

	var set PluginToolSet
	for _, p := range exposing {
		def, ok := byID[p.ID]
		if !ok {
			set.Refused = append(set.Refused, fmt.Sprintf("plugin %q is loaded but has no definition; exposing nothing", p.ID))
			continue
		}
		for _, name := range p.MCPExpose {
			op, ok := def.Operation(name)
			if !ok {
				set.Refused = append(set.Refused, fmt.Sprintf("plugin %q exposes %q, which its definition does not declare", p.ID, name))
				continue
			}
			tool, err := pluginTool(client, p.ID, op)
			if err != nil {
				set.Refused = append(set.Refused, err.Error())
				continue
			}
			if reserved[tool.Name] {
				set.Refused = append(set.Refused, fmt.Sprintf("plugin %q operation %q would be served as %s, which a built-in tool already is; not generated", p.ID, name, tool.Name))
				continue
			}
			set.Tools = append(set.Tools, tool)
		}
	}
	sort.Slice(set.Tools, func(i, j int) bool { return set.Tools[i].Name < set.Tools[j].Name })
	return set, nil
}

// pluginTool builds the tool for one exposed operation. op is the
// definition's effective contract, so an operation with no effect reads as
// exec and its hints say so.
func pluginTool(client cerbapi.Client, connectorID string, op contract.Operation) (Tool, error) {
	name := plugin.ToolNameForOperation(connectorID, op.Name)
	if !validToolName.MatchString(name) {
		return Tool{}, fmt.Errorf("plugin %q operation %q would be served as %q, which is not a valid MCP tool name; not generated", connectorID, op.Name, name)
	}
	schema := cloneJSON(op.InputSchema)
	if schema == nil {
		schema = emptyObjectSchema()
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		schema["properties"] = props
	}
	for _, reservedArg := range []string{argDryRun, argAcknowledged, argApprovalID} {
		if _, clash := props[reservedArg]; clash {
			return Tool{}, fmt.Errorf("plugin %q operation %q declares an argument named %q, which the host adds itself; not generated", connectorID, op.Name, reservedArg)
		}
	}
	if op.SupportsDry {
		props[argDryRun] = map[string]any{"type": "boolean", "description": "Preview without changing anything. The preview is the plugin's claim; Cerberus does not verify it."}
	}
	if op.RequiresAck {
		props[argAcknowledged] = map[string]any{"type": "boolean", "description": fmt.Sprintf("Acknowledge this %s operation. Required unless dry_run.", op.Effect)}
	}

	description := op.Description
	if description == "" {
		description = op.Name
	}
	operation := op.Name
	tool := Tool{
		Name:        name,
		Description: fmt.Sprintf("%s (plugin %s, effect %s)", description, connectorID, op.Effect),
		InputSchema: schema,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			cfg := make(map[string]any, len(args))
			for key, value := range args {
				if key == argDryRun || key == argAcknowledged || key == argApprovalID {
					continue
				}
				cfg[key] = value
			}
			return executeConnectorMCP(ctx, client, connectorID, operation, cfg, boolArg(args, argDryRun), boolArg(args, argAcknowledged), stringArg(args, argApprovalID))
		},
	}
	return withRequestScope(withApprovalArg(WithHints(tool, op), op)), nil
}

// ReservedToolNames is the set of names the hand-written tools occupy.
func ReservedToolNames(tools []Tool) map[string]bool {
	names := make(map[string]bool, len(tools))
	for _, t := range tools {
		names[t.Name] = true
	}
	return names
}

// ServePluginTools starts keeping srv's generated plugin tools in step with
// client until ctx ends, alongside the hand-written tools already registered
// from AllTools(client). It returns at once; the first reconcile runs in the
// background, so a daemon that is not up yet does not delay the server.
func ServePluginTools(ctx context.Context, srv *Server, client cerbapi.Client, logf func(string, ...any)) *PluginToolSync {
	syncer := NewPluginToolSync(srv, client, ReservedToolNames(AllTools(client)), logf)
	go syncer.Run(ctx, PluginToolRefreshInterval)
	return syncer
}

// PluginToolSync keeps a server's generated tools in step with the daemon.
type PluginToolSync struct {
	server   *Server
	client   cerbapi.Client
	reserved map[string]bool
	logf     func(format string, args ...any)

	mu      sync.Mutex
	served  map[string]string // tool name -> definition fingerprint
	refused map[string]bool   // refusals already logged
}

// NewPluginToolSync reconciles server's generated tools against client.
// reserved is the hand-written tool names (ReservedToolNames(AllTools(...))).
// logf receives one line per change and per new refusal; it may be nil.
func NewPluginToolSync(server *Server, client cerbapi.Client, reserved map[string]bool, logf func(string, ...any)) *PluginToolSync {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &PluginToolSync{server: server, client: client, reserved: reserved, logf: logf, served: map[string]string{}, refused: map[string]bool{}}
}

// Reconcile adds the tools the daemon's state now calls for, replaces any
// whose definition changed, and removes the rest. When the daemon cannot be
// asked, the served set is left as it is rather than emptied: a daemon
// restart must not make a client's tools flicker away and back.
func (s *PluginToolSync) Reconcile(ctx context.Context) error {
	set, err := PluginTools(ctx, s.client, s.reserved)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, reason := range set.Refused {
		if !s.refused[reason] {
			s.refused[reason] = true
			s.logf("mcp plugin tool refused: %s", reason)
		}
	}

	want := make(map[string]Tool, len(set.Tools))
	for _, tool := range set.Tools {
		want[tool.Name] = tool
	}
	var stale []string
	for name := range s.served {
		if _, keep := want[name]; !keep {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		s.server.RemoveTools(stale...)
		for _, name := range stale {
			delete(s.served, name)
		}
		s.logf("mcp plugin tools removed: %v", stale)
	}
	for _, tool := range set.Tools {
		fingerprint := toolFingerprint(tool)
		if s.served[tool.Name] == fingerprint {
			continue
		}
		if _, replacing := s.served[tool.Name]; replacing {
			s.server.RemoveTools(tool.Name)
		}
		s.server.RegisterTool(tool)
		s.served[tool.Name] = fingerprint
		s.logf("mcp plugin tool served: %s", tool.Name)
	}
	return nil
}

// Run reconciles now and then every interval until ctx ends. A failed
// reconcile is logged once per distinct error and retried on the next tick.
func (s *PluginToolSync) Run(ctx context.Context, interval time.Duration) {
	var lastErr string
	tick := func() {
		err := s.Reconcile(ctx)
		switch {
		case err != nil && err.Error() != lastErr:
			lastErr = err.Error()
			s.logf("mcp plugin tools not refreshed: %v", err)
		case err == nil:
			lastErr = ""
		}
	}
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// Served names the generated tools currently registered.
func (s *PluginToolSync) Served() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.served))
	for name := range s.served {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func toolFingerprint(t Tool) string {
	data, _ := json.Marshal(struct {
		Name, Description string
		Schema            any
		R, D, I, O        bool
	}{t.Name, t.Description, t.InputSchema, t.ReadOnlyHint, t.DestructiveHint, t.IdempotentHint, t.OpenWorldHint})
	return string(data)
}

// cloneJSON deep-copies a JSON-shaped schema, so adding the host's arguments
// never writes into the definition it came from.
func cloneJSON(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

// Command echoplugin is a synthetic third-party plugin for tests: a standalone binary built
// against the public plugin SDK only, as an out-of-tree plugin is. It echoes each call's tool
// and arguments, reports the names (never the values) of credentials the host issued to it,
// and marks its data directory when it loads and unloads, so a test can see the host stop it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

// issuedPrefix is pkg/plugin's IssuedValuePrefix, spelled out: the plugin uses the public
// wire contract, not the host's packages.
const issuedPrefix = "cerberus.issued."

type echo struct {
	id      string
	dataDir string
	issued  []string
	// token says whether the host handed the plugin its declared token: a flag, never the value.
	token bool
}

func (e *echo) Init(_ context.Context, p sdk.InitParams) (sdk.InitResult, error) {
	e.dataDir = p.DataDir
	e.token = p.Config["token"] != ""
	for k := range p.Config {
		if strings.HasPrefix(k, issuedPrefix) {
			e.issued = append(e.issued, strings.TrimPrefix(k, issuedPrefix))
		}
	}
	sort.Strings(e.issued)
	return sdk.InitResult{ID: e.id, Name: "Echo", Version: "1.0.0", Description: "synthetic test plugin",
		Protocol: sdk.ProtocolVersion, CapabilityContract: 1}, nil
}

func (e *echo) mark(name string) {
	if e.dataDir != "" {
		_ = os.WriteFile(filepath.Join(e.dataDir, name), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	}
}

func (e *echo) Load(context.Context) (sdk.LoadResult, error) {
	if len(e.issued) == 0 {
		e.mark("loaded")
	}
	return sdk.LoadResult{}, nil
}

func (e *echo) Unload(context.Context) error {
	if len(e.issued) == 0 {
		e.mark("unloaded")
	}
	return nil
}

func (e *echo) Health(context.Context) (sdk.HealthStatus, error) {
	return sdk.HealthStatus{OK: true, Message: "ready"}, nil
}

func (e *echo) MCPCallTool(_ context.Context, req sdk.MCPCallRequest) (sdk.MCPCallResult, error) {
	if strings.HasSuffix(req.ToolName, "_fail") {
		return sdk.MCPCallResult{}, errors.New("echo failure")
	}
	content, err := json.Marshal(map[string]any{"tool": req.ToolName, "args": req.Arguments, "issued": e.issued, "token_set": e.token})
	if err != nil {
		return sdk.MCPCallResult{}, err
	}
	return sdk.MCPCallResult{Content: content}, nil
}

func main() {
	id := "echo"
	if len(os.Args) > 1 {
		id = os.Args[1]
	}
	if err := sdk.Serve(&echo{id: id}); err != nil {
		os.Exit(1)
	}
}

// Package mcp is Cerberus's MCP tool set on Substrate's mcp module: every cerberus_* tool of
// the old build, with the same name, description, input schema and hints, served over HTTP
// /mcp in the serving process and over stdio by `cerberus mcp` (docs/substrate-port-plan.md
// sections 4.1, 4.3 and 6).
//
// The tools call the serving process's /api through a Backend: in-process inside serve, over
// loopback HTTP with the CLI key file from `cerberus mcp`, so one process supervises, as the old
// stdio client dialed the daemon. A tool whose backend has not landed yet answers that it is not
// available in this build, naming the slice that brings it.
package mcp

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// mainCatalog is the old build's tool catalog: every tool AllTools(nil) served on main
// (internal/mcp at f3f8181), as names, descriptions, input schemas (after withApprovalArg) and
// hints, dumped from main's own definitions. It is the parity contract: the tools are built from
// it, not re-typed. Regenerate on a checkout of main: a test in its internal/mcp that marshals, for
// each tool of AllTools(nil), name, title, description, InputSchema, OutputSchema and the four hints
// with the JSON names of Def, indented two spaces.
//
//go:embed main-catalog.json
var mainCatalog []byte

// Def is one tool's definition as the old build declared it.
type Def struct {
	Name            string `json:"name"`
	Title           string `json:"title,omitempty"`
	Description     string `json:"description"`
	InputSchema     any    `json:"input_schema"`
	OutputSchema    any    `json:"output_schema,omitempty"`
	ReadOnlyHint    bool   `json:"read_only_hint"`
	DestructiveHint bool   `json:"destructive_hint"`
	IdempotentHint  bool   `json:"idempotent_hint"`
	OpenWorldHint   bool   `json:"open_world_hint"`
}

// Catalog is the old build's tools, in its registration order.
func Catalog() ([]Def, error) {
	var defs []Def
	if err := json.Unmarshal(mainCatalog, &defs); err != nil {
		return nil, fmt.Errorf("mcp: the embedded catalog: %w", err)
	}
	return defs, nil
}

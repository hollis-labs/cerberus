package plugin

import (
	"fmt"
	"sort"
	"strings"
)

// ContractVersion is the plugin contract this Cerberus implements: the shape
// of plugin.yaml, the operation contract and the Init channel. A plugin
// declares the range of contract versions it was built for (HostRange), and a
// host outside that range refuses it at install and at load. It changes only
// when a plugin built for the old contract would be misread by the new host.
const ContractVersion = 1

// HostRange is the range of Cerberus contract versions a plugin works with.
// Zero means no bound on that side. A plugin that declares no range is a gap
// in the install review, not a refusal.
type HostRange struct {
	MinContract int `json:"min_contract,omitempty" yaml:"min_contract,omitempty"`
	MaxContract int `json:"max_contract,omitempty" yaml:"max_contract,omitempty"`
}

// Declared reports whether the plugin declared a range at all.
func (r HostRange) Declared() bool { return r.MinContract != 0 || r.MaxContract != 0 }

// Check reports whether contract falls inside the range.
func (r HostRange) Check(contract int) error {
	if r.MinContract != 0 && contract < r.MinContract {
		return fmt.Errorf("the plugin needs Cerberus contract %d or newer, and this Cerberus implements %d; upgrade Cerberus", r.MinContract, contract)
	}
	if r.MaxContract != 0 && contract > r.MaxContract {
		return fmt.Errorf("the plugin was built for Cerberus contract %d at most, and this Cerberus implements %d; rebuild the plugin against the current pkg/plugin", r.MaxContract, contract)
	}
	return nil
}

// Requirement is what a suggested policy rule asks the operator to require
// before an operation runs.
type Requirement string

const (
	// RequireAck asks for acknowledgment even where the effect would not.
	RequireAck Requirement = "ack"
	// RequireApproval asks for out-of-band human approval on every call.
	RequireApproval Requirement = "approval"
	// RequireApprovalForAgents asks for approval when an agent is the caller.
	RequireApprovalForAgents Requirement = "approval_for_agents"
	// RequireDeny asks that the operation not run at all by default.
	RequireDeny Requirement = "deny"
)

var requirements = []Requirement{RequireAck, RequireApproval, RequireApprovalForAgents, RequireDeny}

// SuggestedRule is one rule a plugin suggests for its own operations, for
// example "delete_pod needs out-of-band approval". The install review shows
// it; the host never applies it on the plugin's word (I10). Accepting or
// editing it is the operator's decision, made in policy the operator owns.
type SuggestedRule struct {
	Operation string      `json:"operation" yaml:"operation"`
	Require   Requirement `json:"require" yaml:"require"`
	Reason    string      `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// Surfaces is where the plugin suggests its operations should be reachable.
// MCP lists the operations it suggests exposing to agents; the operator still
// opts each one in, in connector-config.yaml. CLIOnly lists operations that
// must never reach MCP: a plugin narrowing itself is honored even if the
// operator's file exposes one.
type Surfaces struct {
	MCP     []string `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	CLIOnly []string `json:"cli_only,omitempty" yaml:"cli_only,omitempty"`
}

// TelemetryDeclaration names the structured event kinds a plugin reports
// for one operation (see AttachTelemetry). The install review uses it to say
// which operations' audit records will carry only the host's view.
type TelemetryDeclaration struct {
	Operation string   `json:"operation" yaml:"operation"`
	Events    []string `json:"events" yaml:"events"`
}

// validateDeclarations checks the section 10 declarations against the
// connector's operations. An absent declaration is a gap the review reports;
// a declaration naming an operation the plugin does not have, or a value
// outside the vocabulary, is a problem, because the host could only guess.
func (b CerberusPluginBlock) validateDeclarations() []string {
	ops := map[string]bool{}
	for _, op := range b.Connector.Operations {
		ops[op.Name] = true
	}
	var problems []string
	known := func(where, name string) {
		if !ops[name] {
			problems = append(problems, fmt.Sprintf("%s names operation %q, which the connector does not declare", where, name))
		}
	}
	if b.Host.MinContract < 0 || b.Host.MaxContract < 0 {
		problems = append(problems, "host contract versions cannot be negative")
	}
	if b.Host.MinContract != 0 && b.Host.MaxContract != 0 && b.Host.MinContract > b.Host.MaxContract {
		problems = append(problems, fmt.Sprintf("host min_contract %d is above max_contract %d", b.Host.MinContract, b.Host.MaxContract))
	}
	for _, rule := range b.SuggestedPolicy {
		known("suggested_policy", rule.Operation)
		if !containsRequirement(rule.Require) {
			problems = append(problems, fmt.Sprintf("suggested_policy for %q requires %q, which is not one of %s", rule.Operation, rule.Require, joinRequirements()))
		}
	}
	cliOnly := map[string]bool{}
	for _, name := range b.Surfaces.CLIOnly {
		known("surfaces.cli_only", name)
		cliOnly[name] = true
	}
	for _, name := range b.Surfaces.MCP {
		known("surfaces.mcp", name)
		if cliOnly[name] {
			problems = append(problems, fmt.Sprintf("operation %q is in both surfaces.mcp and surfaces.cli_only", name))
		}
	}
	for _, decl := range b.Telemetry {
		known("telemetry", decl.Operation)
		if len(decl.Events) == 0 {
			problems = append(problems, fmt.Sprintf("telemetry for %q names no event kinds", decl.Operation))
		}
	}
	return problems
}

func containsRequirement(r Requirement) bool {
	for _, known := range requirements {
		if r == known {
			return true
		}
	}
	return false
}

func joinRequirements() string {
	names := make([]string, len(requirements))
	for i, r := range requirements {
		names[i] = string(r)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

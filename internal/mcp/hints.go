package mcp

import (
	"fmt"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	cfconn "github.com/hollis-labs/cerberus/internal/connector/cloudflare"
	doconn "github.com/hollis-labs/cerberus/internal/connector/digitalocean"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	forgeconn "github.com/hollis-labs/cerberus/internal/connector/forge"
	ghconn "github.com/hollis-labs/cerberus/internal/connector/github"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	ncconn "github.com/hollis-labs/cerberus/internal/connector/namecheap"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/pipeline"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// This file is the only place in internal/mcp that sets a tool annotation.
// TestNoHandWrittenHints fails on a hint written anywhere else.

// toolOperations binds each tool to the operation it runs. The tool's
// annotations are derived from that operation's contract, so the MCP hint an
// agent sees and the gate the call meets come from one declaration.
var toolOperations = map[string]opRef{
	"cerberus_health":             {cerbapi.ControlPlaneDefinition, cerbapi.OpHealth},
	"cerberus_project_list":       {cerbapi.ControlPlaneDefinition, cerbapi.OpProjectList},
	"cerberus_connector_list":     {cerbapi.ControlPlaneDefinition, cerbapi.OpConnectorList},
	"cerberus_connector_describe": {cerbapi.ControlPlaneDefinition, cerbapi.OpConnectorDescribe},

	"cerberus_resource_list":         {localconn.Definition, localconn.OpList},
	"cerberus_resource_status":       {localconn.Definition, localconn.OpStatus},
	"cerberus_resource_inspect":      {localconn.Definition, localconn.OpInspect},
	"cerberus_resource_doctor":       {localconn.Definition, localconn.OpDoctor},
	"cerberus_resource_logs":         {localconn.Definition, localconn.OpLogs},
	"cerberus_resource_reload":       {localconn.Definition, localconn.OpReload},
	"cerberus_resource_stop":         {localconn.Definition, localconn.OpStop},
	"cerberus_resource_deploy":       {localconn.Definition, localconn.OpDeploy},
	"cerberus_resource_ensure_fresh": {localconn.Definition, localconn.OpEnsureFresh},
	"cerberus_resource_sync":         {localconn.Definition, localconn.OpSync},
	"cerberus_resource_apply":        {localconn.Definition, localconn.OpApply},
	"cerberus_resource_remove":       {localconn.Definition, localconn.OpRemove},

	"cerberus_pipeline_list": {pipeline.Definition, pipeline.OpList},
	"cerberus_pipeline_run":  {pipeline.Definition, pipeline.OpRun},

	"cerberus_github_status":   {ghconn.Definition, "status"},
	"cerberus_github_releases": {ghconn.Definition, "list_releases"},
	"cerberus_github_runs":     {ghconn.Definition, "list_workflow_runs"},

	"cerberus_ssh_exec":    {sshconn.Definition, "exec"},
	"cerberus_ssh_status":  {sshconn.Definition, "status"},
	"cerberus_ssh_put":     {sshconn.Definition, "put"},
	"cerberus_ssh_get":     {sshconn.Definition, "get"},
	"cerberus_ssh_put_dir": {sshconn.Definition, "put_dir"},
	"cerberus_ssh_get_dir": {sshconn.Definition, "get_dir"},

	"cerberus_domain_list":        {ncconn.Definition, "list_domains"},
	"cerberus_domain_status":      {ncconn.Definition, "get_domain_status"},
	"cerberus_nameservers_set":    {ncconn.Definition, "set_custom_nameservers"},
	"cerberus_dns_list":           {ncconn.Definition, "list_dns_records"},
	"cerberus_get_dns_record_set": {ncconn.Definition, "get_dns_record_set"},
	"cerberus_set_dns_record_set": {ncconn.Definition, "set_dns_record_set"},
	"cerberus_dns_create":         {namecheapDisabled, "create_dns_record"},
	"cerberus_dns_delete":         {namecheapDisabled, "delete_dns_record"},

	"cerberus_forge_servers": {forgeconn.Definition, "list_servers"},
	"cerberus_forge_server":  {forgeconn.Definition, "get_server"},
	"cerberus_forge_sites":   {forgeconn.Definition, "list_sites"},
	"cerberus_forge_deploy":  {forgeconn.Definition, "deploy_site"},
	"cerberus_forge_exec":    {forgeconn.Definition, "exec_site_command"},

	"cerberus_cloudflare_zones":       {cfconn.Definition, "list_zones"},
	"cerberus_cloudflare_zone_create": {cfconn.Definition, "create_zone"},
	"cerberus_cloudflare_dns_list":    {cfconn.Definition, "list_dns_records"},
	"cerberus_cloudflare_dns_create":  {cfconn.Definition, "create_dns_record"},
	"cerberus_cloudflare_dns_delete":  {cfconn.Definition, "delete_dns_record"},

	"cerberus_docker_ps":      {dockerconn.Definition, "list_containers"},
	"cerberus_docker_logs":    {dockerconn.Definition, "logs"},
	"cerberus_docker_up":      {dockerconn.Definition, "start"},
	"cerberus_docker_down":    {dockerconn.Definition, "stop"},
	"cerberus_docker_destroy": {dockerconn.Definition, "destroy"},

	"cerberus_droplet_list":    {doconn.Definition, "list_droplets"},
	"cerberus_droplet_get":     {doconn.Definition, "get_droplet"},
	"cerberus_droplet_create":  {doconn.Definition, "create_droplet"},
	"cerberus_droplet_start":   {doconn.Definition, "start"},
	"cerberus_droplet_stop":    {doconn.Definition, "stop"},
	"cerberus_droplet_destroy": {doconn.Definition, "destroy"},
}

type opRef struct {
	definition func() contract.Definition
	operation  string
}

func namecheapDisabled() contract.Definition {
	return contract.Definition{ID: "namecheap", Operations: ncconn.DisabledOperations()}
}

// ToolOperation is the contract of the operation a tool runs, from the
// binding table. ok is false for a tool with no binding.
func ToolOperation(name string) (contract.Operation, bool) {
	ref, ok := toolOperations[name]
	if !ok {
		return contract.Operation{}, false
	}
	return ref.definition().Operation(ref.operation)
}

// WithHints sets a tool's four annotations from an operation's contract. It is
// the one way a tool gets its hints: the built-in tools below go through it,
// and a tool generated from a plugin manifest should too, passing the
// manifest operation's effective contract (ManifestOperation.Operation()).
func WithHints(t Tool, op contract.Operation) Tool {
	h := contract.HintsFor(op)
	t.ReadOnlyHint = h.ReadOnly
	t.DestructiveHint = h.Destructive
	t.IdempotentHint = h.Idempotent
	t.OpenWorldHint = h.OpenWorld
	return t
}

// contractTool gives a built-in tool the hints of the operation the binding
// table names for it. A tool with no binding, or a binding to an operation
// its definition does not declare, is a programming error, caught the first
// time the tool is built — which every test that lists the tools does.
func contractTool(t Tool) Tool {
	op, ok := ToolOperation(t.Name)
	if !ok {
		panic(fmt.Sprintf("mcp: tool %q has no contract binding in toolOperations", t.Name))
	}
	return WithHints(t, op)
}

package mcp

import "github.com/hollis-labs/cerberus/internal/cerbapi"

// AllTools is every Cerberus MCP tool, in registration order. `cerberus mcp`,
// mcp-http and the daemon's stdio server all serve exactly this list, and
// conformance enumerates it, so a tool cannot be served without its hints
// being checked against its operation's contract.
func AllTools(client cerbapi.Client) []Tool {
	tools := []Tool{
		NewCerberusHealthTool(client),
		NewCerberusProjectListTool(client),
		NewCerberusResourceListTool(client),
		NewCerberusResourceStatusTool(client),
		NewCerberusResourceInspectTool(client),
		NewCerberusResourceDoctorTool(client),
		NewCerberusResourceLogsTool(client),
		NewCerberusResourceReloadTool(client),
		NewCerberusResourceStopTool(client),
		NewCerberusResourceDeployTool(client),
		NewCerberusResourceEnsureFreshTool(client),
		NewCerberusResourceSyncTool(client),
		NewCerberusResourceApplyTool(client),
		NewCerberusResourceRemoveTool(client),
		NewCerberusPipelineListTool(client),
		NewCerberusPipelineRunTool(client),
		NewCerberusConnectorListTool(client),
		NewCerberusConnectorDescribeTool(client),
		NewCerberusGithubStatusTool(client),
		NewCerberusGithubReleasesTool(client),
		NewCerberusGithubRunsTool(client),
		NewCerberusSSHExecTool(client),
		NewCerberusSSHStatusTool(client),
		NewCerberusSSHPutTool(client),
		NewCerberusSSHGetTool(client),
		NewCerberusSSHPutDirTool(client),
		NewCerberusSSHGetDirTool(client),
		NewCerberusDomainListTool(client),
		NewCerberusDomainStatusTool(client),
		NewCerberusNameserversSetTool(client),
		NewCerberusDNSListTool(client),
		NewCerberusDNSCreateTool(client),
		NewCerberusDNSDeleteTool(client),
		NewCerberusForgeServersTool(client),
		NewCerberusForgeServerTool(client),
		NewCerberusForgeSitesTool(client),
		NewCerberusForgeDeployTool(client),
		NewCerberusForgeExecTool(client),
		NewCerberusDockerPSTool(client),
		NewCerberusDockerLogsTool(client),
		NewCerberusDockerUpTool(client),
		NewCerberusDockerDownTool(client),
		NewCerberusDockerDestroyTool(client),
	}
	return append(tools, NewCerberusDNSRecordSetTools(client)...)
}

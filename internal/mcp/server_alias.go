package mcp

import gmcp "github.com/hollis-labs/go-mcp/server"

type Tool = gmcp.Tool
type ToolHandler = gmcp.ToolHandler
type Server = gmcp.Server

func NewServer(name, version string) *gmcp.Server {
	return gmcp.NewServer(name, version)
}

func emptyObjectSchema() map[string]interface{} {
	return gmcp.EmptyObjectSchema()
}

func objectSchema(properties map[string]interface{}, required ...string) map[string]interface{} {
	return gmcp.ObjectSchema(properties, required...)
}

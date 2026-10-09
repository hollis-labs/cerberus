package main

import (
	"os"

	"github.com/hollis-labs/cerberus/internal/plugins/dockerplugin"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

func main() {
	if err := subprocess.Serve(dockerplugin.New()); err != nil {
		os.Exit(1)
	}
}

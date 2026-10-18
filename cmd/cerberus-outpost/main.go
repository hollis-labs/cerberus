package main

import (
	"fmt"
	"os"

	"github.com/hollis-labs/cerberus/internal/outpostexecutor"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

var version = "dev"

func main() {
	if err := subprocess.Serve(&outpostexecutor.Plugin{Version: version}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

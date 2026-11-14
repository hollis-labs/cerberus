// Command cerberus is the Cerberus control plane, built on Substrate. The shell (config,
// logging, health, HTTP host, database, CLI root) comes from the manifest; this package and
// internal/ are the app. The port lands in slices (docs/substrate-port-plan.md, section 11);
// this is slice 1, the skeleton.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/substrate/boot"
	"github.com/hollis-labs/substrate/modules/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "cerberus:", err)
		stop()
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	started, err := boot.StartWith(ctx, substrateApp, substrateModules, substrateConfigJSON, bootOptions(getenv))
	if err != nil {
		return err
	}
	defer func() { _ = started.Stop(context.Background()) }()

	c := &cerberus{app: started.App}
	rootCmd := cli.Root(started.App)
	serve := c.serveCmd(ctx)
	// `cerberus` with no command serves, like `cerberus serve`.
	rootCmd.RunE = func(cmd *cobra.Command, args []string) error { return serve.RunE(cmd, args) }
	rootCmd.Args = cobra.NoArgs
	rootCmd.AddCommand(serve)
	return rootCmd.ExecuteContext(ctx)
}

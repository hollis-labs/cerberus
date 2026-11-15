// Command cerberus is the Cerberus control plane, built on Substrate. The shell (config,
// logging, health, HTTP host, database, CLI root) comes from the manifest; this package and
// internal/ are the app. The port lands in slices (docs/substrate-port-plan.md, section 11);
// this is slice 1, the skeleton.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/substrate/boot"
	"github.com/hollis-labs/substrate/modules/cli"
	"github.com/hollis-labs/substrate/modules/database"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/storage"
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
	// The database module applies the embedded schema when it opens the file.
	database.UseMigrationFS(storage.Migrations())
	started, err := boot.StartWith(ctx, substrateApp, substrateModules, substrateConfigJSON, bootOptions(getenv))
	if err != nil {
		return err
	}
	defer func() { _ = started.Stop(context.Background()) }()

	c := &cerberus{app: started.App, home: home(getenv)}
	rootCmd := cli.Root(started.App)
	serve := c.serveCmd(ctx)
	// `cerberus` with no command serves, like `cerberus serve`.
	rootCmd.RunE = func(cmd *cobra.Command, args []string) error { return serve.RunE(cmd, args) }
	rootCmd.Args = cobra.NoArgs
	rootCmd.AddCommand(serve)
	rootCmd.AddCommand(c.readCommands()...)
	guardKeyCreate(rootCmd)
	return rootCmd.ExecuteContext(ctx)
}

// guardKeyCreate refuses `cerberus identity key create` with a scope no machine credential may
// hold (api.RefusedKeyScope): the offline path gets the same rule as the HTTP one.
func guardKeyCreate(root *cobra.Command) {
	create, _, err := root.Find([]string{"identity", "key", "create"})
	if err != nil || create == nil || create.Name() != "create" {
		return
	}
	prev := create.PreRunE
	create.PreRunE = func(cmd *cobra.Command, args []string) error {
		scopes, _ := cmd.Flags().GetStringSlice("scope")
		for _, s := range scopes {
			if why := api.RefusedKeyScope(s); why != "" {
				return errors.New(why)
			}
		}
		if prev != nil {
			return prev(cmd, args)
		}
		return nil
	}
}

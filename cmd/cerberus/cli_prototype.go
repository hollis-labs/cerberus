package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/plugins/dockerplugin"
)

// prototypeCommand is the old `connectors write-plugin-prototype <connector> <dir>`
// (cmd/cerberus cmd_connectors.go on main): a developer tool that writes a plugin prototype
// directory for a built-in connector, with --build-binary building the plugin from the source
// tree the command runs in. It touches neither the server nor any state, as old's did not.
func prototypeCommand() *cobra.Command {
	var buildBinary bool
	cmd := &cobra.Command{
		Use:   "write-plugin-prototype <connector> <dir>",
		Short: "Write a connector plugin prototype directory",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sourceDir, err := os.Getwd()
			if err != nil {
				return err
			}
			if args[0] != "docker" {
				return fmt.Errorf("unsupported connector prototype %q", args[0])
			}
			if buildBinary {
				err = dockerplugin.WritePrototypeWithBinary(cmd.Context(), args[1], sourceDir)
			} else {
				err = dockerplugin.WritePrototype(args[1])
			}
			if err != nil {
				return err
			}
			if buildBinary {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Wrote Docker plugin prototype with binary to %s\n", args[1])
			} else {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Wrote Docker plugin prototype to %s\n", args[1])
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&buildBinary, "build-binary", false, "build and stage the plugin binary into the prototype directory")
	return cmd
}

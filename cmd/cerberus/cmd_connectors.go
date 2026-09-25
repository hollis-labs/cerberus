package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/plugins/dockerplugin"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/spf13/cobra"
)

var connectorsCmd = &cobra.Command{
	Use:   "connectors",
	Short: "List connector discovery metadata",
	RunE: func(cmd *cobra.Command, args []string) error {
		defs, live, err := connectorDefinitions(cmd.Context())
		if err != nil {
			return err
		}
		printConnectorDefinitions(cmd.OutOrStdout(), defs, live)
		return nil
	},
}

var connectorsDescribeCmd = &cobra.Command{
	Use:   "describe <connector-id>",
	Short: "Show detailed connector discovery metadata",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		defs, live, err := connectorDefinitions(cmd.Context())
		if err != nil {
			return err
		}
		desc, err := connectorDescriptionByID(defs, live, args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), desc)
	},
}

var connectorsPrototypeCmd = &cobra.Command{
	Use:   "write-plugin-prototype <connector> <dir>",
	Short: "Write a connector plugin prototype directory",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		sourceDir, err := os.Getwd()
		if err != nil {
			return err
		}
		switch args[0] {
		case "docker":
			if connectorsPrototypeBuildBinary {
				err = dockerplugin.WritePrototypeWithBinary(context.Background(), args[1], sourceDir)
			} else {
				err = dockerplugin.WritePrototype(args[1])
			}
			if err != nil {
				return err
			}
			if connectorsPrototypeBuildBinary {
				fmt.Fprintf(cmd.OutOrStdout(), "Wrote Docker plugin prototype with binary to %s\n", args[1])
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Wrote Docker plugin prototype to %s\n", args[1])
			}
			return nil
		default:
			return fmt.Errorf("unsupported connector prototype %q", args[0])
		}
	},
}

var connectorsPrototypeBuildBinary bool

func init() {
	connectorsPrototypeCmd.Flags().BoolVar(&connectorsPrototypeBuildBinary, "build-binary", false, "build and stage the plugin binary into the prototype directory")
	connectorsCmd.AddCommand(connectorsDescribeCmd)
	connectorsCmd.AddCommand(connectorsPrototypeCmd)
}

type connectorDescription struct {
	contract.Definition
	Live bool `json:"live"`
}

func connectorDefinitions(ctx context.Context) ([]contract.Definition, map[string]bool, error) {
	local := app.NewExternalConnectorService(cfgPath)
	localDefs := local.Definitions()
	localLive := make(map[string]bool, len(localDefs))
	for _, def := range localDefs {
		localLive[def.ID] = connectorLive(local, def.ID)
	}

	client, err := newManagedPluginSocketClient()
	if err != nil {
		return localDefs, localLive, nil
	}

	defs, err := client.ListConnectors(ctx)
	if err != nil {
		var unreachable *cerbapi.DaemonUnreachableError
		if errors.As(err, &unreachable) {
			return localDefs, localLive, nil
		}
		return nil, nil, err
	}

	// Liveness has to come from the daemon, because the daemon is what runs the
	// operation. This process has the user's shell PATH and credentials; the
	// daemon has launchd's. Reporting the CLI's view here is how a connector
	// could read LIVE=yes while every call against it failed. Fall back to the
	// local view only if the daemon cannot answer.
	live := make(map[string]bool, len(defs))
	liveIDs, liveErr := client.ListLiveConnectors(ctx)
	if liveErr == nil {
		for _, def := range defs {
			live[def.ID] = false
		}
		for _, id := range liveIDs {
			live[id] = true
		}
	} else {
		for id, ok := range localLive {
			live[id] = ok
		}
	}
	plugins, err := client.ListManagedPlugins(ctx)
	if err == nil {
		for _, plugin := range plugins {
			live[plugin.ID] = plugin.Loaded
		}
	}
	for _, def := range defs {
		if _, ok := live[def.ID]; !ok {
			live[def.ID] = false
		}
	}
	return defs, live, nil
}

func connectorLive(svc *cerbapi.ExternalConnectorService, id string) bool {
	for _, def := range svc.LiveDefinitions() {
		if def.ID == id {
			return true
		}
	}
	return false
}

func printConnectorDefinitions(out io.Writer, defs []contract.Definition, live map[string]bool) {
	if len(defs) == 0 {
		fmt.Fprintln(out, "No connector metadata registered.")
		return
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTYPES\tLIVE\tOPERATIONS")
	fmt.Fprintln(w, "--\t-----\t----\t----------")
	for _, def := range defs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n",
			def.ID,
			strings.Join(def.ResourceTypes, ","),
			yesNo(live[def.ID]),
			len(def.Operations),
		)
	}
	_ = w.Flush()
}

func connectorDescriptionByID(defs []contract.Definition, live map[string]bool, id string) (connectorDescription, error) {
	for _, def := range defs {
		if def.ID == id {
			return connectorDescription{
				Definition: def,
				Live:       live[id],
			}, nil
		}
	}
	return connectorDescription{}, fmt.Errorf("connector %q not found", id)
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

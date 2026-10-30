package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

var componentCmd = &cobra.Command{
	Use:   "component",
	Short: "Spare and installed parts recorded in hosts.yaml (read-only)",
}

var componentFilterOpts struct {
	kind  string
	host  string
	spare bool
}

var componentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List components (RAM, disks, CPUs, other), installed and spare",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newHostClient(cmd)
		if err != nil {
			return err
		}
		comps, err := cerbapi.ComponentClientOf(client).ListComponents(cmd.Context(),
			cerbapi.ComponentFilter{Kind: componentFilterOpts.kind, Host: componentFilterOpts.host, Spare: componentFilterOpts.spare})
		if err != nil {
			return err
		}
		if comps == nil {
			comps = []hosts.Component{}
		}
		if hostWantsJSON() {
			return writeJSON(cmd.OutOrStdout(), comps)
		}
		return printComponentTable(cmd.OutOrStdout(), comps)
	},
}

var componentGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one component",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newHostClient(cmd)
		if err != nil {
			return err
		}
		c, err := cerbapi.ComponentClientOf(client).GetComponent(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if hostWantsJSON() {
			return writeJSON(cmd.OutOrStdout(), c)
		}
		return printComponentTable(cmd.OutOrStdout(), []hosts.Component{c})
	},
}

func printComponentTable(out io.Writer, comps []hosts.Component) error {
	if len(comps) == 0 {
		_, err := fmt.Fprintln(out, "No components match. Components are declared in ~/.cerberus/hosts.yaml (see docs/hosts.md).")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tSIZE\tTYPE\tSPEED\tSERIAL\tLOCATION")
	for _, c := range comps {
		loc := "spare"
		if !c.Location.Spare {
			loc = c.Location.Host + "/" + c.Location.Slot
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Kind, hostCell(c.Size > 0, humanBytes(int64(c.Size))),
			hostCell(c.Type != "", c.Type), hostCell(c.Speed != "", c.Speed), hostCell(c.Serial != "", c.Serial), loc)
	}
	return w.Flush()
}

func init() {
	f := componentListCmd.Flags()
	f.StringVar(&componentFilterOpts.kind, "kind", "", "ram|disk|cpu|other")
	f.StringVar(&componentFilterOpts.host, "host", "", "only components located on this host")
	f.BoolVar(&componentFilterOpts.spare, "spare", false, "only spare components")
	componentCmd.PersistentFlags().BoolVar(&hostJSONFlag, "json", false, "print JSON")
	componentCmd.PersistentFlags().StringVarP(&hostOutput, "output", "o", outputFormatText, "output format: text|json")
	componentCmd.AddCommand(componentListCmd, componentGetCmd)
	rootCmd.AddCommand(componentCmd)
}

package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/spf13/cobra"
)

var resourceCmd = &cobra.Command{
	Use:   "resource",
	Short: "Resource management",
}

var resourceListProject string

var resourceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List resources",
	Long:  "Lists all resources defined in the config. Use --project to filter by project.",
	RunE: func(cmd *cobra.Command, args []string) error {
		v2, err := config.LoadUnified(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tTYPE\tPROJECT\tCONNECTOR\tTAGS")
		fmt.Fprintln(w, "--\t----\t----\t-------\t---------\t----")

		count := 0
		for _, r := range v2.Resources {
			if resourceListProject != "" && r.Project != resourceListProject {
				continue
			}
			tags := "-"
			if len(r.Tags) > 0 {
				tags = strings.Join(r.Tags, ", ")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.ID, r.Name, r.Type, r.Project, r.Connector, tags)
			count++
		}
		w.Flush() //nolint:errcheck

		if count == 0 {
			fmt.Println("No resources found.")
		}
		return nil
	},
}

var resourceShowCmd = &cobra.Command{
	Use:   "show <resource-id>",
	Short: "Show resource details",
	Long:  "Shows detailed information about a specific resource.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		v2, err := config.LoadUnified(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		id := args[0]
		for _, r := range v2.Resources {
			if r.ID == id {
				fmt.Printf("ID:        %s\n", r.ID)
				fmt.Printf("Name:      %s\n", r.Name)
				fmt.Printf("Type:      %s\n", r.Type)
				fmt.Printf("Project:   %s\n", r.Project)
				fmt.Printf("Connector: %s\n", r.Connector)
				if len(r.Tags) > 0 {
					fmt.Printf("Tags:      %s\n", strings.Join(r.Tags, ", "))
				}
				if len(r.DependsOn) > 0 {
					fmt.Printf("Depends:   %s\n", strings.Join(r.DependsOn, ", "))
				}
				if len(r.Config) > 0 {
					fmt.Println("Config:")
					for k, v := range r.Config {
						fmt.Printf("  %s: %v\n", k, v)
					}
				}
				return nil
			}
		}

		return fmt.Errorf("resource %q not found", id)
	},
}

func init() {
	resourceListCmd.Flags().StringVar(&resourceListProject, "project", "", "filter by project ID")
	resourceCmd.AddCommand(resourceListCmd)
	resourceCmd.AddCommand(resourceShowCmd)
}

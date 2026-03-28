package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/app"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pipeline"
	"github.com/spf13/cobra"
)

var pipelineCmd = &cobra.Command{
	Use:   "pipeline",
	Short: "Pipeline management",
}

var pipelineListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available pipelines",
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		if len(a.Config.Pipelines) == 0 {
			fmt.Println("No pipelines defined. Add pipelines to your config under the 'pipelines:' key.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTAGES\tDESCRIPTION")
		fmt.Fprintln(w, "--\t----\t------\t-----------")
		for _, p := range a.Config.Pipelines {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", p.ID, p.Name, len(p.Stages), p.Description)
		}
		return w.Flush()
	},
}

var pipelineRunCmd = &cobra.Command{
	Use:   "run <pipeline-id>",
	Short: "Run a pipeline",
	Long:  "Executes a pipeline defined in the config. Stages run in dependency order with parallel execution where possible.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		pipelineID := args[0]

		// Find pipeline def
		for _, pd := range a.Config.Pipelines {
			if pd.ID != pipelineID {
				continue
			}

			// Resolve config def into executable pipeline
			p, err := pipeline.Resolve(pd, a.Services, a.Local)
			if err != nil {
				return fmt.Errorf("resolve pipeline: %w", err)
			}

			// Set up cancellation
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			fmt.Printf("Running pipeline: %s (%s)\n", p.Name, p.ID)

			env := &domain.PipelineEnv{
				Values: make(map[string]any),
			}

			exec := pipeline.NewExecutor(nil)
			result, err := exec.Run(ctx, p, env)
			if err != nil {
				return fmt.Errorf("pipeline execution: %w", err)
			}

			// Print results
			fmt.Println()
			for _, sr := range result.Stages {
				var status string
				switch sr.Status {
				case domain.StateFailed:
					status = "FAILED"
				case domain.StateStopped:
					status = "skipped"
				default:
					status = "ok"
				}
				fmt.Printf("  %-20s %s (%s)\n", sr.Name, status, sr.Duration)
				if sr.Error != "" {
					fmt.Printf("    error: %s\n", sr.Error)
				}
			}
			fmt.Printf("\nPipeline %s: %s (%s)\n", pipelineID, result.Status, result.Duration)

			if result.Status == domain.StateFailed {
				return fmt.Errorf("pipeline failed: %s", result.Error)
			}
			return nil
		}

		return fmt.Errorf("pipeline %q not found in config", pipelineID)
	},
}

var pipelineShowCmd = &cobra.Command{
	Use:   "show <pipeline-id>",
	Short: "Show pipeline details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		pipelineID := args[0]
		for _, pd := range a.Config.Pipelines {
			if pd.ID == pipelineID {
				data, err := json.MarshalIndent(pd, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}
		}
		return fmt.Errorf("pipeline %q not found", pipelineID)
	},
}

func init() {
	pipelineCmd.AddCommand(pipelineListCmd)
	pipelineCmd.AddCommand(pipelineRunCmd)
	pipelineCmd.AddCommand(pipelineShowCmd)
}

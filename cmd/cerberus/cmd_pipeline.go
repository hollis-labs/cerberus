package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/hollis-labs/cerberus/internal/redact"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/spf13/cobra"
)

var pipelineCmd = &cobra.Command{
	Use:   "pipeline",
	Short: "Pipeline management",
	Long: `Pipeline definition and execution commands.

A pipeline is an ordered set of stages (each with one or more actions) defined
under 'pipelines:' in the v2 config. Stages run in dependency order with
parallel execution where possible. Pipelines are the right tool for orchestrating
multi-step deploys, releases, and operational workflows across resources.

Subcommands:
  list   list available pipelines
  show   show a pipeline definition (stages, actions, dependencies)
  run    execute a pipeline by ID`,
}

var pipelineListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available pipelines",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newPipelineClient(cmd)
		if err != nil {
			return err
		}
		list, err := client.ListPipelines(cmd.Context())
		if err != nil {
			return err
		}
		return printPipelineList(os.Stdout, list)
	},
}

var pipelineRunCmd = &cobra.Command{
	Use:   "run <pipeline-id>",
	Short: "Run a pipeline",
	Long:  "Executes a pipeline defined in the config. Stages run in dependency order with parallel execution where possible.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newPipelineClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ack, _ := cmd.Flags().GetBool("ack")
		return runPipelineCommand(ctx, client, args[0], os.Stdout, cerbapi.WithAcknowledged(ack))
	},
}

var pipelineShowCmd = &cobra.Command{
	Use:   "show <pipeline-id>",
	Short: "Show pipeline details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newPipelineClient(cmd)
		if err != nil {
			return err
		}
		detail, err := client.GetPipeline(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if detail == nil {
			return fmt.Errorf("pipeline %q not found", args[0])
		}
		return printPipelineDetail(os.Stdout, detail)
	},
}

func printPipelineList(out io.Writer, list []cerbapi.PipelineInfo) error {
	if len(list) == 0 {
		_, err := fmt.Fprintln(out, "No pipelines defined. Add pipelines to your config under the 'pipelines:' key.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSTAGES\tDESCRIPTION")
	fmt.Fprintln(w, "--\t----\t------\t-----------")
	for _, p := range list {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", p.ID, p.Name, p.Stages, p.Description)
	}
	return w.Flush()
}

func printPipelineDetail(out io.Writer, detail *cerbapi.PipelineDetail) error {
	data, err := redact.MarshalIndent(detail.Definition, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}

func runPipelineCommand(ctx context.Context, client pipelineClient, id string, out io.Writer, opts ...cerbapi.MutationOption) error {
	detail, err := client.GetPipeline(ctx, id)
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("pipeline %q not found in config", id)
	}
	if detail.ValidationError != "" {
		return errors.New(detail.ValidationError)
	}
	fmt.Fprintf(out, "Running pipeline: %s (%s)\n", detail.Definition.Name, detail.Definition.ID)
	result, err := client.RunPipeline(ctx, id, opts...)
	if err != nil {
		return err
	}
	if !result.Success {
		return errors.New(result.Error)
	}
	execution, err := result.Execution()
	if err != nil {
		return fmt.Errorf("decode pipeline result: %w", err)
	}
	return printPipelineExecution(out, id, execution)
}

func printPipelineExecution(out io.Writer, id string, result *cerbapi.PipelineExecution) error {
	fmt.Fprintln(out)
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
		fmt.Fprintf(out, "  %-20s %s (%s)\n", sr.Name, status, sr.Duration)
		if sr.Error != "" {
			fmt.Fprintf(out, "    error: %s\n", sr.Error)
		}
	}
	fmt.Fprintf(out, "\nPipeline %s: %s (%s)\n", id, result.Status, result.Duration)
	if result.Status == domain.StateFailed {
		return fmt.Errorf("pipeline failed: %s", result.Error)
	}
	return nil
}

func init() {
	pipelineCmd.AddCommand(pipelineListCmd)
	// A run is exec: a stage can be a shell action (Decision 11).
	pipelineRunCmd.Flags().Bool("ack", false, "acknowledge the run; a pipeline stage can run shell commands, so every run requires it")
	pipelineCmd.AddCommand(pipelineRunCmd)
	pipelineCmd.AddCommand(pipelineShowCmd)
}

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/scheduling"
	"github.com/spf13/cobra"
)

// newScheduleCommand takes the same shared service as MCP and HTTP. Production
// uses the daemon socket only: no mutation fallback or private source of truth.
func newScheduleCommand(resolve func(*cobra.Command) (scheduling.Service, error)) *cobra.Command {
	cmd := &cobra.Command{SilenceErrors: true, SilenceUsage: true, Use: "schedule", Short: "Manage durable schedules (engine remains inactive)", Long: `Manage durable jobs through one daemon-owned service. This does not start the
scheduler or authorize effects. run-now refuses without exact-fire authority.
Use JSON job files with cron, interval (nanoseconds), or at (RFC3339), and an
IANA location. Job/app names are selectors, never permission. See docs/scheduling.md.`}
	var jsonOutput bool
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "print the shared JSON response (errors also use JSON)")
	for _, operation := range scheduling.Operations {
		op := operation
		cliName := strings.ReplaceAll(op, "_", "-")
		var r scheduling.Call
		var input string
		var after string
		child := &cobra.Command{Use: cliName, Short: "Scheduled job " + cliName, Args: cobra.NoArgs, RunE: func(child *cobra.Command, _ []string) (retErr error) {
			defer func() {
				if retErr != nil && jsonOutput {
					var coded *scheduling.Error
					if !errors.As(retErr, &coded) {
						coded = &scheduling.Error{Code: scheduling.ErrorCode(retErr), Message: redact.ErrorText(retErr)}
					}
					if encodeErr := json.NewEncoder(child.ErrOrStderr()).Encode(map[string]any{"error": coded}); encodeErr != nil {
						retErr = encodeErr
					} else {
						retErr = &reportedCommandError{err: retErr}
					}
				}
			}()
			r.Operation = op
			if after != "" {
				parsed, err := time.Parse(time.RFC3339, after)
				if err != nil {
					return scheduling.Refusal("invalid", "after must be RFC3339")
				}
				r.After = parsed
			}
			if op == "create" || op == "update" || op == "dry_run" {
				var reader io.Reader
				var file *os.File
				if input == "-" {
					reader = child.InOrStdin()
				} else {
					var err error
					root, openErr := os.OpenRoot(filepath.Dir(input))
					if openErr != nil {
						return openErr
					}
					file, err = root.Open(filepath.Base(input))
					if file != nil {
						defer func() { _ = file.Close() }()
					}
					closeErr := root.Close()
					if closeErr != nil {
						return closeErr
					}
					if err != nil {
						return err
					}
					reader = file
				}
				var j scheduling.Job
				data, readErr := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
				if readErr != nil {
					return readErr
				}
				if len(data) > 1<<20 {
					return scheduling.Refusal("invalid", "job file exceeds the 1 MiB limit")
				}
				decoder := json.NewDecoder(bytes.NewReader(data))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&j); err != nil {
					return scheduling.Refusal("invalid", "job file does not match the scheduling job JSON schema")
				}
				var extra any
				if decoder.Decode(&extra) != io.EOF {
					return scheduling.Refusal("invalid", "job file must contain one JSON object")
				}
				r.Job = &j
			}
			service, err := resolve(child)
			if err != nil {
				return err
			}
			out, err := service.Schedule(child.Context(), r)
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(child.OutOrStdout())
			if !jsonOutput {
				encoder.SetIndent("", "  ")
			}
			return encoder.Encode(out)
		}}
		child.Flags().StringVar(&r.OwnerApp, "app", "", "application namespace selector")
		child.Flags().StringVar(&r.ID, "id", "", "job ID selector")
		switch op {
		case "create", "update", "dry_run":
			child.Flags().StringVar(&input, "file", "-", "JSON job file, or - for stdin")
		}
		switch op {
		case "create":
			child.Flags().StringVar(&r.IdempotencyKey, "idempotency-key", "", "required durable create idempotency key")
		case "update", "delete", "pause", "resume":
			child.Flags().StringVar(&r.Revision, "revision", "", "required revision from get; stale edits refuse")
		case "run_now":
			child.Flags().StringVar(&r.RequestID, "request-id", "", "required durable manual-run request ID")
		case "history":
			child.Flags().IntVar(&r.Limit, "limit", 100, "history limit, 1..1000")
		case "dry_run":
			child.Flags().IntVar(&r.Limit, "limit", 5, "next fire count, 1..100")
			child.Flags().StringVar(&after, "after", "", "RFC3339 dry-run starting instant (default now)")
		case "list":
			child.Flags().StringVar(&r.State, "state", "", "enabled, paused or completed filter")
		case "logs":
			child.Flags().StringVar(&r.FireID, "fire-id", "", "run ID; logs currently report unavailable")
		}
		switch op {
		case "create", "update", "delete", "pause", "resume", "run_now":
			child.Flags().BoolVar(&r.Acknowledged, "ack", false, "acknowledge this mutation; never execution authority")
			child.Flags().StringVar(&r.ApprovalID, "approval-id", "", "existing shared policy approval ID")
		}
		cmd.AddCommand(child)
	}
	return cmd
}
func init() {
	rootCmd.AddCommand(newScheduleCommand(func(cmd *cobra.Command) (scheduling.Service, error) {
		if explicitConfig() || cmd.Flag("db").Changed {
			return nil, fmt.Errorf("schedule uses the serving daemon's database; --config and --db overrides are not supported")
		}
		client, err := newResourceSocketClient(cerbapi.WithClientTimeout(0))
		return client, err
	}))
}

// reportedCommandError marks a complete JSON error already written by a CLI
// adapter. main still exits nonzero, but does not append a second prose line.
type reportedCommandError struct{ err error }

func (e *reportedCommandError) Error() string { return e.err.Error() }
func (e *reportedCommandError) Unwrap() error { return e.err }

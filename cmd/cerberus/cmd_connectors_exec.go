package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/spf13/cobra"
)

// connectorExecFlags are the argument flags shared by every command that runs
// one connector operation by name.
type connectorExecFlags struct {
	args     []string
	jsonArgs []string
	input    string
	dryRun   bool
	ack      bool
	approval string
	plan     bool
}

func (f *connectorExecFlags) register(cmd *cobra.Command) {
	f.registerArgs(cmd)
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "preview the operation without executing it, where the operation supports a preview")
	cmd.Flags().BoolVar(&f.ack, "ack", false, "acknowledge an operation the gate requires acknowledgment for")
}

// registerArgs registers the flags that build an operation's arguments.
func (f *connectorExecFlags) registerArgs(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&f.args, "arg", nil, "operation argument as key=value, typed by the operation's input schema; repeat a key to build an array")
	cmd.Flags().StringArrayVar(&f.jsonArgs, "arg-json", nil, "operation argument as key=<JSON value>, for objects, arrays of objects, or exact types")
	cmd.Flags().StringVar(&f.input, "input", "", "JSON object of operation arguments, from a file or - for stdin; --arg and --arg-json override its keys")
}

var connectorsExecFlags connectorExecFlags

var connectorsExecCmd = &cobra.Command{
	Use:   "exec <connector-id> <operation>",
	Short: "Run any connector operation, built-in or plugin, through the admin lane",
	Long: `Run one connector operation by name. Built-in connectors and loaded plugins
take the same path, so dry-run, acknowledgment and redaction apply exactly as
they do for the connector's own commands.

Arguments are typed from the operation's input schema, which
'cerberus connectors describe <connector-id>' prints:

  --arg ttl=300                      integer, number and boolean values are parsed
  --arg nameservers=ns1 --arg nameservers=ns2
                                     a repeated key builds an array
  --arg-json records='[{"type":"A","host":"@","value":"203.0.113.10"}]'
                                     any JSON value
  --input args.json                  a JSON object of arguments, or - for stdin`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		defs, _, err := connectorDefinitions(cmd.Context())
		if err != nil {
			return err
		}
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()
		return runConnectorExec(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin(), svc, defs, args[0], args[1], connectorsExecFlags)
	},
}

var connectorsPlanFlags connectorExecFlags

var connectorsPlanCmd = &cobra.Command{
	Use:   "plan <connector-id> <operation>",
	Short: "Show the plan an approval of a connector operation would bind to",
	Long: `Compute and print the plan for one connector operation, and its hash,
without running it: the operation, its resolved target and labels, the keyed
digest of its arguments, its dry-run preview where it has one, and for a
plugin which binary and config would run.

This is the same function an approval is asked for and used with, so the hash
printed is the one an approval of these arguments would record. Asking for a
plan is recorded like a dry run, and it runs the operation's preview; for a
plugin, that is a call to the plugin. Arguments are given as for
'cerberus connectors exec'.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		defs, _, err := connectorDefinitions(cmd.Context())
		if err != nil {
			return err
		}
		svc, closeFn, err := newExternalConnectorService(cmd.Context())
		if err != nil {
			return err
		}
		defer closeFn()
		flags := connectorsPlanFlags
		flags.plan = true
		return runConnectorExec(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin(), svc, defs, args[0], args[1], flags)
	},
}

func init() {
	connectorsExecFlags.register(connectorsExecCmd)
	connectorsExecCmd.Flags().StringVar(&connectorsExecFlags.approval, "approval", "", "run under this approved approval id, with exactly the arguments it was approved for")
	connectorsCmd.AddCommand(connectorsExecCmd)
	connectorsPlanFlags.registerArgs(connectorsPlanCmd)
	connectorsPlanCmd.Flags().BoolVar(&connectorsPlanFlags.ack, "ack", false, "plan the call as it would be sent with --ack")
	connectorsCmd.AddCommand(connectorsPlanCmd)
}

// runConnectorExec types the arguments against the operation's schema, where
// the operation is known, and runs it through the executor's admin lane.
//
// Every attempted operation reaches the admin lane, which refuses what it must
// and records the attempt in the audit log. So nothing about the operation is
// refused here: an unknown connector or operation, an argument the schema does
// not declare, or a value that does not fit its type is sent as given, and
// Execute refuses and records it. What this side knows becomes a hint on
// errOut, printed before sending, so the operator still learns about a typo
// without the attempt going unrecorded. Only a command line that cannot be
// turned into a request at all, such as `--arg` without `=` or unparseable
// JSON, is refused locally, because there is no operation to record.
func runConnectorExec(ctx context.Context, out, errOut io.Writer, stdin io.Reader, svc connectorExecutor, defs []contract.Definition, connectorID, operation string, flags connectorExecFlags) error {
	var hints []string
	op, err := findConnectorOperation(defs, connectorID, operation)
	if err != nil {
		hints = append(hints, err.Error()+"; sending it anyway, so the refusal is recorded")
		op = contract.Operation{Name: operation}
	}
	cfg, argHints, err := connectorExecConfig(op, flags, stdin)
	if err != nil {
		return fmt.Errorf("%s %s: %w", connectorID, operation, err)
	}
	hints = append(hints, argHints...)
	for _, hint := range hints {
		fmt.Fprintf(errOut, "hint: %s %s: %s\n", connectorID, operation, hint)
	}
	result, err := svc.Execute(ctx, cerbapi.ExternalConnectorOperationArgs{
		Connector:    connectorID,
		Operation:    operation,
		Config:       cfg,
		DryRun:       flags.dryRun,
		Acknowledged: flags.ack,
		ApprovalID:   flags.approval,
		Plan:         flags.plan,
	})
	if err != nil {
		return err
	}
	return writeJSON(out, result)
}

func findConnectorOperation(defs []contract.Definition, connectorID, operation string) (contract.Operation, error) {
	ids := make([]string, 0, len(defs))
	for _, def := range defs {
		ids = append(ids, def.ID)
		if def.ID != connectorID {
			continue
		}
		names := make([]string, 0, len(def.Operations))
		for _, op := range def.Operations {
			if op.Name == operation {
				return op, nil
			}
			names = append(names, op.Name)
		}
		return contract.Operation{}, fmt.Errorf("connector %q has no operation %q; it declares: %s", connectorID, operation, strings.Join(names, ", "))
	}
	sort.Strings(ids)
	return contract.Operation{}, fmt.Errorf("connector %q not found; known connectors: %s (a plugin must be installed and loaded)", connectorID, strings.Join(ids, ", "))
}

// connectorExecConfig builds the operation's config from --input, then
// --arg-json, then --arg, each overriding the one before it by key. A value
// is typed from the schema where it fits; where it does not, it is sent as
// written and described in a hint, so the admin lane refuses and records it.
// It errors only for a command line that cannot form a request.
func connectorExecConfig(op contract.Operation, flags connectorExecFlags, stdin io.Reader) (map[string]any, []string, error) {
	cfg := map[string]any{}
	var hints []string
	if flags.input != "" {
		data, err := readExecInput(flags.input, stdin)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, nil, fmt.Errorf("--input must be a JSON object of arguments: %w", err)
		}
	}
	properties, _ := op.InputSchema["properties"].(map[string]any)

	for _, item := range flags.jsonArgs {
		key, raw, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return nil, nil, fmt.Errorf("invalid --arg-json %q, want key=<JSON value>", item)
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, nil, fmt.Errorf("--arg-json %s: %w", key, err)
		}
		cfg[key] = value
	}

	repeated := map[string][]any{}
	scalars := map[string]bool{}
	for _, item := range flags.args {
		key, raw, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return nil, nil, fmt.Errorf("invalid --arg %q, want key=value", item)
		}
		schema, _ := properties[key].(map[string]any)
		if schemaType(schema) == "array" {
			items, _ := schema["items"].(map[string]any)
			value, hint := coerceArg(key, raw, items)
			if hint != "" {
				hints = append(hints, hint)
			}
			repeated[key] = append(repeated[key], value)
			continue
		}
		value, hint := coerceArg(key, raw, schema)
		if hint != "" {
			hints = append(hints, hint)
		}
		if scalars[key] {
			// Sent as the list it was given, so the lane refuses it rather
			// than this side silently keeping one of the values.
			hints = append(hints, fmt.Sprintf("--arg %s given more than once, but it is not an array; sending every value", key))
			repeated[key] = append(repeated[key], value)
			continue
		}
		scalars[key] = true
		cfg[key] = value
		repeated[key] = []any{value}
	}
	for key, values := range repeated {
		if !scalars[key] || len(values) > 1 {
			cfg[key] = values
		}
	}

	// The operation's key table, the same check the admin lane runs. Its
	// verdict is a hint: the lane refuses and records the call. This is the
	// operator's own shell, so local-only inputs pass; over the socket the
	// daemon still refuses them.
	if op.InputSchema != nil {
		if err := op.Finalize().CheckInputs(cfg, true); err != nil {
			accepted := make([]string, 0, len(op.Inputs))
			for _, in := range op.Finalize().Inputs {
				accepted = append(accepted, in.Name)
			}
			sort.Strings(accepted)
			hints = append(hints, fmt.Sprintf("%v; the operation accepts: %s", err, strings.Join(accepted, ", ")))
		}
	}
	return cfg, hints, nil
}

func readExecInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(path) //nolint:gosec // the operator names the file
}

// schemaType is a property's JSON Schema type. A union such as
// ["string","null"] reads as its first non-null member.
func schemaType(schema map[string]any) string {
	switch t := schema["type"].(type) {
	case string:
		return t
	case []any:
		for _, member := range t {
			if s, ok := member.(string); ok && s != "null" {
				return s
			}
		}
	case []string:
		for _, s := range t {
			if s != "null" {
				return s
			}
		}
	}
	return ""
}

// coerceArg parses one --arg value into the type its schema declares. An
// undeclared type stays a string, which is what the operation received before
// arguments were typed at all. A value that does not fit is kept as written
// and described in the returned hint; the admin lane refuses it.
func coerceArg(key, raw string, schema map[string]any) (any, string) {
	var (
		value any
		err   error
	)
	switch schemaType(schema) {
	case "integer":
		value, err = strconv.Atoi(strings.TrimSpace(raw))
	case "number":
		value, err = strconv.ParseFloat(strings.TrimSpace(raw), 64)
	case "boolean":
		value, err = strconv.ParseBool(strings.TrimSpace(raw))
	case "object", "array":
		return raw, fmt.Sprintf("--arg %s takes a structured value; pass it as --arg-json %s=<JSON>; sending the text as given", key, key)
	default:
		value = raw
	}
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) {
			err = numErr.Err
		}
		return raw, fmt.Sprintf("--arg %s=%q: want %s: %v; sending the text as given", key, raw, schemaType(schema), err)
	}
	if enum, ok := schema["enum"]; ok && !enumContains(enum, value) {
		return value, fmt.Sprintf("--arg %s=%q: not one of %v", key, raw, enum)
	}
	return value, ""
}

// enumContains compares printed values: a schema that crossed the socket was
// JSON-decoded, so its numbers are float64 while a parsed --arg is an int.
func enumContains(enum any, value any) bool {
	values, ok := enum.([]any)
	if !ok {
		if strs, isStrings := enum.([]string); isStrings {
			for _, s := range strs {
				values = append(values, s)
			}
		} else {
			return true
		}
	}
	want := fmt.Sprint(value)
	return slices.ContainsFunc(values, func(v any) bool { return fmt.Sprint(v) == want })
}

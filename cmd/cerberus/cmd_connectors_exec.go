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
}

func (f *connectorExecFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&f.args, "arg", nil, "operation argument as key=value, typed by the operation's input schema; repeat a key to build an array")
	cmd.Flags().StringArrayVar(&f.jsonArgs, "arg-json", nil, "operation argument as key=<JSON value>, for objects, arrays of objects, or exact types")
	cmd.Flags().StringVar(&f.input, "input", "", "JSON object of operation arguments, from a file or - for stdin; --arg and --arg-json override its keys")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "preview the operation without executing it, where the operation supports a preview")
	cmd.Flags().BoolVar(&f.ack, "ack", false, "acknowledge an operation the gate requires acknowledgment for")
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
		return runConnectorExec(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(), svc, defs, args[0], args[1], connectorsExecFlags)
	},
}

func init() {
	connectorsExecFlags.register(connectorsExecCmd)
	connectorsCmd.AddCommand(connectorsExecCmd)
}

// runConnectorExec resolves the operation's schema, types the arguments
// against it, and runs the operation through the executor's admin lane.
func runConnectorExec(ctx context.Context, out io.Writer, stdin io.Reader, svc connectorExecutor, defs []contract.Definition, connectorID, operation string, flags connectorExecFlags) error {
	op, err := findConnectorOperation(defs, connectorID, operation)
	if err != nil {
		return err
	}
	cfg, err := connectorExecConfig(op, flags, stdin)
	if err != nil {
		return fmt.Errorf("%s %s: %w", connectorID, operation, err)
	}
	result, err := svc.Execute(ctx, cerbapi.ExternalConnectorOperationArgs{
		Connector:    connectorID,
		Operation:    operation,
		Config:       cfg,
		DryRun:       flags.dryRun,
		Acknowledged: flags.ack,
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
// --arg-json, then --arg, each overriding the one before it by key.
func connectorExecConfig(op contract.Operation, flags connectorExecFlags, stdin io.Reader) (map[string]any, error) {
	cfg := map[string]any{}
	if flags.input != "" {
		data, err := readExecInput(flags.input, stdin)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("--input must be a JSON object of arguments: %w", err)
		}
	}
	properties, _ := op.InputSchema["properties"].(map[string]any)
	closed := op.InputSchema["additionalProperties"] == false

	for _, item := range flags.jsonArgs {
		key, raw, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --arg-json %q, want key=<JSON value>", item)
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, fmt.Errorf("--arg-json %s: %w", key, err)
		}
		cfg[key] = value
	}

	repeated := map[string][]any{}
	for _, item := range flags.args {
		key, raw, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --arg %q, want key=value", item)
		}
		schema, _ := properties[key].(map[string]any)
		if schemaType(schema) == "array" {
			items, _ := schema["items"].(map[string]any)
			value, err := coerceArg(key, raw, items)
			if err != nil {
				return nil, err
			}
			repeated[key] = append(repeated[key], value)
			continue
		}
		if _, dup := repeated[key]; dup {
			return nil, fmt.Errorf("--arg %s given more than once, but it is not an array", key)
		}
		value, err := coerceArg(key, raw, schema)
		if err != nil {
			return nil, err
		}
		cfg[key] = value
		repeated[key] = nil
	}
	for key, values := range repeated {
		if values != nil {
			cfg[key] = values
		}
	}

	if closed {
		var unknown []string
		for key := range cfg {
			if _, ok := properties[key]; !ok {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			accepted := make([]string, 0, len(properties))
			for key := range properties {
				accepted = append(accepted, key)
			}
			sort.Strings(accepted)
			return nil, fmt.Errorf("unknown argument %s; the operation accepts: %s", strings.Join(unknown, ", "), strings.Join(accepted, ", "))
		}
	}
	return cfg, nil
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
// arguments were typed at all.
func coerceArg(key, raw string, schema map[string]any) (any, error) {
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
		return nil, fmt.Errorf("--arg %s takes a structured value; pass it as --arg-json %s=<JSON>", key, key)
	default:
		value = raw
	}
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) {
			err = numErr.Err
		}
		return nil, fmt.Errorf("--arg %s=%q: want %s: %w", key, raw, schemaType(schema), err)
	}
	if enum, ok := schema["enum"]; ok && !enumContains(enum, value) {
		return nil, fmt.Errorf("--arg %s=%q: not one of %v", key, raw, enum)
	}
	return value, nil
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

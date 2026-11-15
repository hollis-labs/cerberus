package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/api"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The admin lane's verbs on the CLI, over the serving Cerberus's admin routes with the CLI's key:
// `secrets set` (old cmd_secrets.go), `connectors exec|plan` (old cmd_connectors_exec.go) and
// `connectors plugin managed list|load|unload|uninstall|health` (old cmd_connectors_plugin.go),
// with the old flags and output. Every write is gated: an approval policy asks for is the
// owner's on the console, and the same command with --approval <id> runs it once approved.

// errSecretsNotInteractive is old's refusal off a terminal.
var errSecretsNotInteractive = errors.New("a secret is stored only from an interactive terminal, where you paste it and it is not shown; run this in your terminal, not from a script or an agent")

// secretNamePart is old's check on each half of <service>/<key>, made here too so a bad name
// is refused before the value is pasted; the server checks it again.
var secretNamePart = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// readSecretValue is old's secretsReadValue: the pasted value read in raw mode with no echo, so
// a long value (a Keeper configuration, a 1Password service account token) is not cut at the
// terminal's line limit; Enter ends it, Ctrl-C cancels, backspace edits.
func readSecretValue(in *os.File, out io.Writer) (string, error) {
	fd := int(in.Fd()) //nolint:gosec // a file descriptor fits an int
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer func() { _ = term.Restore(fd, state) }()
	return readRawValue(in, out)
}

// readRawValue reads the bytes of a value typed in raw mode, up to Enter.
func readRawValue(in io.Reader, out io.Writer) (string, error) {
	var value []byte
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if err != nil {
			return "", err
		}
		if n == 0 {
			continue
		}
		switch buf[0] {
		case '\r', '\n':
			_, _ = fmt.Fprint(out, "\r\n")
			return strings.TrimSpace(string(value)), nil
		case 3: // ctrl-C
			_, _ = fmt.Fprint(out, "\r\n")
			return "", errors.New("canceled; nothing was stored")
		case 127, 8: // backspace
			if len(value) > 0 {
				value = value[:len(value)-1]
			}
		default:
			value = append(value, buf[0])
		}
	}
}

// secretsCommands is `secrets set`. readValue reads the pasted value (raw mode on the terminal).
func secretsCommands(client func() (*apiClient, error), isTerminal func() bool, readValue func(io.Writer) (string, error)) *cobra.Command {
	var approvalID string
	set := &cobra.Command{
		Use:   "set <service>/<key>",
		Short: "Store one credential in the OS credential store (interactive)",
		Long: `Store one credential in the OS credential store (the macOS Keychain, the
Windows Credential Manager or the Linux Secret Service), under Cerberus's
service, where keyring://<service>/<key> and every connector's fallback read it.

It is how you store a secret backend's own credential, which must come from the
OS credential store and never from another vault. Set it before you install the
plugin that reads it:

    cerberus secrets set keeper/ksm_config
    cerberus secrets set onepassword/service_account_token

You paste the value; it is not shown, and it is never an argument, so it stays
out of your shell history and the process list. It runs only on an interactive
terminal. The serving Cerberus writes it, as an admin write through its gate,
recorded in the audit log as secrets.set by name, never by value. Where policy
asks for an approval, the owner approves it on the console; then run this again
with --approval <id> and paste the same value.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isTerminal() {
				return errSecretsNotInteractive
			}
			service, key, ok := strings.Cut(strings.TrimSpace(args[0]), "/")
			if !ok {
				return fmt.Errorf("%q is not <service>/<key>, for example keeper/ksm_config", args[0])
			}
			if !secretNamePart.MatchString(service) || !secretNamePart.MatchString(key) {
				return fmt.Errorf("%q is not a secret name: use <connector or plugin id>/<secret name>, lowercase, for example keeper/ksm_config", service+"/"+key)
			}
			cl, err := client()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			path := "/secrets/" + url.PathEscape(service) + "/" + url.PathEscape(key)
			var presence api.SecretPresence
			if err = cl.get(cmd.Context(), path, &presence); err != nil {
				return err
			}
			if presence.Present {
				_, _ = fmt.Fprintf(out, "%s/%s already has a value; pasting replaces it.\n", service, key)
			}
			_, _ = fmt.Fprintf(out, "Paste the value for %s/%s (it is not shown), then press Enter: ", service, key)
			value, err := readValue(out)
			if err != nil {
				return err
			}
			if value == "" {
				return errors.New("the value is empty; nothing was stored")
			}
			var res admin.SecretSetResult
			err = cl.post(cmd.Context(), path, struct {
				Value      string `json:"value"`
				ApprovalID string `json:"approval_id,omitempty"`
			}{value, approvalID}, &res)
			if err != nil {
				return pendingHint(err, approvalID)
			}
			_, _ = fmt.Fprintf(out, "Stored %s/%s (%d characters) in the OS credential store.\n", service, key, len(value))
			_, _ = fmt.Fprintf(out, "A plugin reads its credentials when it loads. If %s is loaded, run `cerberus connectors plugin managed unload %s`, then `cerberus connectors plugin managed load %s`.\n", service, service, service)
			return nil
		},
	}
	set.Flags().StringVar(&approvalID, "approval", "", "store under this approval, once the owner has approved it on the console (paste the same value)")
	secrets := &cobra.Command{Use: "secrets", Short: "Store credentials in the OS credential store"}
	secrets.AddCommand(set)
	return secrets
}

// managedPluginCommands are `connectors plugin managed list|load|unload|uninstall|health`, the
// old JSON output over GET and POST /api/plugins/connectors….
func managedPluginCommands(client func() (*apiClient, error)) []*cobra.Command {
	list := &cobra.Command{
		Use:   "list",
		Short: "List managed plugins",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			plugins, err := managedPlugins(cmd.Context(), cl)
			if err != nil {
				return err
			}
			if plugins == nil {
				plugins = []admin.PluginState{}
			}
			return printJSON(cmd.OutOrStdout(), plugins)
		},
	}
	health := &cobra.Command{
		Use:   "health <plugin-id>",
		Short: "Check health of a managed plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			var out any
			if err = cl.get(cmd.Context(), "/plugins/connectors/"+url.PathEscape(args[0])+"/health", &out); err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), out)
		},
	}
	cmds := []*cobra.Command{list, health}
	for _, a := range []struct{ action, short string }{
		{"load", "Load a managed plugin (a changed bundle is accepted with review <id>)"},
		{"unload", "Unload a managed plugin"},
		{"uninstall", "Remove a managed plugin, unloading it first if needed"},
	} {
		var approvalID string
		c := &cobra.Command{
			Use:   a.action + " <plugin-id>",
			Short: a.short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := client()
				if err != nil {
					return err
				}
				var st admin.PluginState
				err = cl.post(cmd.Context(), "/plugins/connectors/"+url.PathEscape(args[0])+"/"+a.action, struct {
					ApprovalID string `json:"approval_id,omitempty"`
				}{approvalID}, &st)
				if err != nil {
					return pendingHint(err, approvalID)
				}
				return printJSON(cmd.OutOrStdout(), st)
			},
		}
		c.Flags().StringVar(&approvalID, "approval", "", "run under this approval, once the owner has approved it on the console")
		cmds = append(cmds, c)
	}
	return cmds
}

// connectorExecFlags are old's argument flags for running one connector operation by name.
type connectorExecFlags struct {
	args, jsonArgs []string
	input          string
	dryRun, ack    bool
	approval       string
	plan           bool
}

func (f *connectorExecFlags) registerArgs(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&f.args, "arg", nil, "operation argument as key=value, typed by the operation's input schema; repeat a key to build an array")
	cmd.Flags().StringArrayVar(&f.jsonArgs, "arg-json", nil, "operation argument as key=<JSON value>, for objects, arrays of objects, or exact types")
	cmd.Flags().StringVar(&f.input, "input", "", "JSON object of operation arguments, from a file or - for stdin; --arg and --arg-json override its keys")
}

// connectorExecCommands are `connectors exec` and `connectors plan`.
func connectorExecCommands(client func() (*apiClient, error)) []*cobra.Command {
	var execFlags, planFlags connectorExecFlags
	exec := &cobra.Command{
		Use:   "exec <connector-id> <operation>",
		Short: "Run any connector operation, built-in or plugin, through the admin lane",
		Long: `Run one connector operation by name on the serving Cerberus. Built-in
connectors and loaded plugins take the same path, so dry-run, acknowledgment and
redaction apply exactly as they do for the connector's own commands.

Arguments are typed from the operation's input schema:

  --arg ttl=300                      integer, number and boolean values are parsed
  --arg nameservers=ns1 --arg nameservers=ns2
                                     a repeated key builds an array
  --arg-json records='[{"type":"A","host":"@","value":"203.0.113.10"}]'
                                     any JSON value
  --input args.json                  a JSON object of arguments, or - for stdin

Where policy asks for an approval, the owner approves it on the console; then
run this again with --approval <id> and the same arguments.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnectorExec(cmd, client, args[0], args[1], execFlags)
		},
	}
	execFlags.registerArgs(exec)
	exec.Flags().BoolVar(&execFlags.dryRun, "dry-run", false, "preview the operation without executing it, where the operation supports a preview")
	exec.Flags().BoolVar(&execFlags.ack, "ack", false, "acknowledge an operation the gate requires acknowledgment for")
	exec.Flags().StringVar(&execFlags.approval, "approval", "", "run under this approved approval id, with exactly the arguments it was approved for")

	plan := &cobra.Command{
		Use:   "plan <connector-id> <operation>",
		Short: "Show the plan an approval of a connector operation would bind to",
		Long: `Compute and print the plan for one connector operation, and its hash,
without running it. This is the same function an approval is asked for and used
with, so the hash printed is the one an approval of these arguments would
record. Asking for a plan is recorded like a dry run, and it runs the
operation's preview; for a plugin, that is a call to the plugin. Arguments are
given as for 'cerberus connectors exec'.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := planFlags
			f.plan = true
			return runConnectorExec(cmd, client, args[0], args[1], f)
		},
	}
	planFlags.registerArgs(plan)
	plan.Flags().BoolVar(&planFlags.ack, "ack", false, "plan the call as it would be sent with --ack")
	return []*cobra.Command{exec, plan}
}

// runConnectorExec is old's runConnectorExec over HTTP: the arguments typed against the
// operation's schema where the server declares it, the hints printed on stderr, and the call
// sent as given, so the admin lane refuses and records what it must.
func runConnectorExec(cmd *cobra.Command, client func() (*apiClient, error), connectorID, operation string, flags connectorExecFlags) error {
	cl, err := client()
	if err != nil {
		return err
	}
	var defs []contract.Definition
	var hints []string
	if derr := cl.get(cmd.Context(), "/connectors", &defs); derr != nil {
		hints = append(hints, "the connector definitions could not be read ("+derr.Error()+"); arguments are sent as written")
	}
	op, ferr := findConnectorOperation(defs, connectorID, operation)
	if ferr != nil {
		hints = append(hints, ferr.Error()+"; sending it anyway, so the refusal is recorded")
		op = contract.Operation{Name: operation}
	}
	cfg, argHints, err := connectorExecConfig(op, flags, cmd.InOrStdin())
	if err != nil {
		return fmt.Errorf("%s %s: %w", connectorID, operation, err)
	}
	for _, h := range append(hints, argHints...) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "hint: %s %s: %s\n", connectorID, operation, h)
	}
	body := admin.OperationArgs{Connector: connectorID, Operation: operation, Config: cfg, DryRun: flags.dryRun,
		Acknowledged: flags.ack, ApprovalID: flags.approval, Plan: flags.plan}
	var res any
	err = cl.post(cmd.Context(), "/connectors/"+url.PathEscape(connectorID)+"/operations/"+url.PathEscape(operation), body, &res)
	if err != nil {
		return pendingHint(err, flags.approval)
	}
	return printJSON(cmd.OutOrStdout(), res)
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

// connectorExecConfig is old's: --input, then --arg-json, then --arg, each overriding the one
// before it by key; a value typed from the schema where it fits, sent as written with a hint
// where it does not. It errors only for a command line that cannot form a request.
func connectorExecConfig(op contract.Operation, flags connectorExecFlags, stdin io.Reader) (map[string]any, []string, error) {
	cfg := map[string]any{}
	var hints []string
	if flags.input != "" {
		var data []byte
		var err error
		if flags.input == "-" {
			data, err = io.ReadAll(stdin)
		} else {
			data, err = os.ReadFile(flags.input) //nolint:gosec // the operator names the file
		}
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

// schemaType is a property's JSON Schema type; a union reads as its first non-null member.
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

// coerceArg parses one --arg value into the type its schema declares (old's).
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

// enumContains compares printed values: a schema that crossed HTTP was JSON-decoded.
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

// secretsCommand is `secrets` against the serving Cerberus. The connector and managed plugin
// verbs join `connectors` in pluginCommands (cli_plugins.go).
func (c *cerberus) secretsCommand() *cobra.Command {
	return secretsCommands(func() (*apiClient, error) { return newAPIClient(c.app, c.home) }, stdIsTerminal,
		func(out io.Writer) (string, error) { return readSecretValue(os.Stdin, out) })
}

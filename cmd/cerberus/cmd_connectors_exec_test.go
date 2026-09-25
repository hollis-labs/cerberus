package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// execTestDefinition has one operation of each argument shape the typing has
// to handle, so no test depends on a real connector's schema.
func execTestDefinition() contract.Definition {
	return contract.Definition{
		ID: "dnsdemo",
		Operations: []contract.Operation{
			{
				Name: "set_records",
				InputSchema: contract.ObjectSchema(map[string]any{
					"domain":      contract.StringSchema("Domain."),
					"ttl":         contract.IntegerSchema("TTL."),
					"weight":      map[string]any{"type": "number"},
					"proxied":     map[string]any{"type": "boolean"},
					"email_type":  map[string]any{"type": "string", "enum": []any{"MX", "NONE"}},
					"nameservers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"ports":       map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
					"records":     map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
					"labels":      map[string]any{"type": "object"},
				}, "domain"),
				Destructive: true,
				SupportsDry: true,
			},
			{
				Name:        "open_op",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
	}
}

func execTestOperation(t *testing.T, name string) contract.Operation {
	t.Helper()
	op, err := findConnectorOperation([]contract.Definition{execTestDefinition()}, "dnsdemo", name)
	if err != nil {
		t.Fatalf("findConnectorOperation: %v", err)
	}
	return op
}

func TestConnectorExecConfigTypesArgumentsFromTheSchema(t *testing.T) {
	cfg, err := connectorExecConfig(execTestOperation(t, "set_records"), connectorExecFlags{
		args: []string{
			"domain=example.com",
			"ttl=300",
			"weight=0.5",
			"proxied=true",
			"email_type=MX",
			"nameservers=ns1.example.net",
			"nameservers=ns2.example.net",
			"ports=80",
			"ports=443",
		},
		jsonArgs: []string{
			`records=[{"type":"A","host":"@","value":"203.0.113.10"}]`,
			`labels={"team":"infra"}`,
		},
	}, nil)
	if err != nil {
		t.Fatalf("connectorExecConfig: %v", err)
	}
	want := map[string]any{
		"domain":      "example.com",
		"ttl":         300,
		"weight":      0.5,
		"proxied":     true,
		"email_type":  "MX",
		"nameservers": []any{"ns1.example.net", "ns2.example.net"},
		"ports":       []any{80, 443},
		"records":     []any{map[string]any{"type": "A", "host": "@", "value": "203.0.113.10"}},
		"labels":      map[string]any{"team": "infra"},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("cfg = %#v\nwant %#v", cfg, want)
	}
}

// --input is the base, --arg-json overrides it, and --arg overrides both.
func TestConnectorExecConfigLayersInputThenJSONThenArgs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "args.json")
	if err := os.WriteFile(path, []byte(`{"domain":"from-input.example","ttl":60,"proxied":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := connectorExecConfig(execTestOperation(t, "set_records"), connectorExecFlags{
		input:    path,
		jsonArgs: []string{`ttl=120`},
		args:     []string{"domain=from-arg.example"},
	}, nil)
	if err != nil {
		t.Fatalf("connectorExecConfig: %v", err)
	}
	if cfg["domain"] != "from-arg.example" || cfg["ttl"] != float64(120) || cfg["proxied"] != true {
		t.Fatalf("cfg = %#v", cfg)
	}

	cfg, err = connectorExecConfig(execTestOperation(t, "set_records"), connectorExecFlags{input: "-"},
		strings.NewReader(`{"domain":"stdin.example"}`))
	if err != nil || cfg["domain"] != "stdin.example" {
		t.Fatalf("stdin input: cfg = %#v, err = %v", cfg, err)
	}
}

// Every refusal happens before the operation is sent, and names what to do.
func TestConnectorExecConfigRefusals(t *testing.T) {
	cases := []struct {
		name  string
		op    string
		flags connectorExecFlags
		want  string
	}{
		{"undeclared argument on a closed schema", "set_records", connectorExecFlags{args: []string{"domain=x", "zone=y"}}, "refusing fields (zone): the operation does not declare them; the operation accepts: domain, email_type"},
		{"missing a required argument", "set_records", connectorExecFlags{args: []string{"ttl=5"}}, "missing required fields (domain)"},
		{"not an integer", "set_records", connectorExecFlags{args: []string{"ttl=five"}}, `--arg ttl="five": want integer`},
		{"not a boolean", "set_records", connectorExecFlags{args: []string{"proxied=maybe"}}, `--arg proxied="maybe": want boolean`},
		{"outside the enum", "set_records", connectorExecFlags{args: []string{"email_type=FWD"}}, `not one of`},
		{"object through --arg", "set_records", connectorExecFlags{args: []string{"labels=team"}}, "pass it as --arg-json labels=<JSON>"},
		{"array of objects through --arg", "set_records", connectorExecFlags{args: []string{"records=A"}}, "pass it as --arg-json records=<JSON>"},
		{"a scalar repeated", "set_records", connectorExecFlags{args: []string{"domain=a", "domain=b"}}, "--arg domain given more than once"},
		{"malformed --arg", "set_records", connectorExecFlags{args: []string{"domain"}}, "want key=value"},
		{"malformed --arg-json", "set_records", connectorExecFlags{jsonArgs: []string{"records=[oops"}}, "--arg-json records"},
		{"--input that is not an object", "set_records", connectorExecFlags{input: "-"}, "--input must be a JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := connectorExecConfig(execTestOperation(t, tc.op), tc.flags, strings.NewReader(`[1,2]`))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// A schema that does not close itself accepts what it does not name, as a
// string: typing only ever narrows what the operation received before.
func TestConnectorExecConfigOpenSchemaKeepsUnknownArguments(t *testing.T) {
	cfg, err := connectorExecConfig(execTestOperation(t, "open_op"), connectorExecFlags{args: []string{"anything=42"}}, nil)
	if err != nil || cfg["anything"] != "42" {
		t.Fatalf("cfg = %#v, err = %v", cfg, err)
	}
}

type recordingExecutor struct {
	got cerbapi.ExternalConnectorOperationArgs
}

func (r *recordingExecutor) Execute(_ context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	r.got = args
	return cerbapi.ExternalConnectorOperationResult{Connector: args.Connector, Operation: args.Operation, Data: map[string]any{"ok": true}}, nil
}

func TestRunConnectorExecSendsTypedArgsDryRunAndAck(t *testing.T) {
	exec := &recordingExecutor{}
	var out bytes.Buffer
	err := runConnectorExec(context.Background(), &out, nil, exec, []contract.Definition{execTestDefinition()}, "dnsdemo", "set_records",
		connectorExecFlags{args: []string{"domain=example.com", "ttl=300"}, dryRun: true, ack: true})
	if err != nil {
		t.Fatalf("runConnectorExec: %v", err)
	}
	if exec.got.Connector != "dnsdemo" || exec.got.Operation != "set_records" || !exec.got.DryRun || !exec.got.Acknowledged {
		t.Fatalf("args = %#v", exec.got)
	}
	if exec.got.Config["ttl"] != 300 {
		t.Fatalf("ttl = %#v, want the integer 300", exec.got.Config["ttl"])
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["connector"] != "dnsdemo" {
		t.Fatalf("output = %s (%v)", out.String(), err)
	}
}

func TestRunConnectorExecNamesWhatExists(t *testing.T) {
	defs := []contract.Definition{execTestDefinition(), {ID: "another"}}
	err := runConnectorExec(context.Background(), &bytes.Buffer{}, nil, &recordingExecutor{}, defs, "missing", "x", connectorExecFlags{})
	if err == nil || !strings.Contains(err.Error(), "known connectors: another, dnsdemo") {
		t.Fatalf("unknown connector: err = %v", err)
	}
	err = runConnectorExec(context.Background(), &bytes.Buffer{}, nil, &recordingExecutor{}, defs, "dnsdemo", "nope", connectorExecFlags{})
	if err == nil || !strings.Contains(err.Error(), "it declares: set_records, open_op") {
		t.Fatalf("unknown operation: err = %v", err)
	}
}

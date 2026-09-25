package cerbapi

import (
	"context"
	"github.com/hollis-labs/cerberus/internal/audit"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// wrongValue is a value of the wrong JSON type for schema's type, or nil when
// the schema declares none.
func wrongValue(schema map[string]any) any {
	switch schema["type"] {
	case "string", "integer", "number", "boolean", "array":
		return map[string]any{"not": "this"}
	case "object":
		return "not an object"
	}
	return nil
}

// The type sweep, the way P1-1 swept keys: for every declared operation and
// every typed input, a value of the wrong type with no credential is refused
// as invalid_args, naming the key and never the value, and nothing is
// resolved. In-process, so local-only inputs reach the type check too.
func TestTypeRefusalsNeverNeedACredential(t *testing.T) {
	resolves := 0
	svc := NewExternalConnectorService(audit.NewMemory(), resolveCountingRegistry(&resolves))
	svc.SetResourceLookup(sshTestLookup())
	ctx := WithCallerSurface(context.Background(), SurfaceInProcess)
	checked := 0
	for _, def := range svc.Definitions() {
		for _, op := range def.Operations {
			for _, in := range op.Inputs {
				wrong := wrongValue(in.Schema)
				if wrong == nil {
					continue
				}
				cfg := sweepConfig(def.ID, op)
				cfg[in.Name] = wrong
				resolves = 0
				_, err := svc.Execute(ctx, ExternalConnectorOperationArgs{Connector: def.ID, Operation: op.Name, Config: cfg, Acknowledged: true})
				checked++
				if code := connectorErrorCode(err); code != ExternalConnectorInvalidArgs || !strings.Contains(err.Error(), "wrong types ("+in.Name+" wants") {
					t.Errorf("%s.%s %s: err = %v, want a wrong-type refusal naming it", def.ID, op.Name, in.Name, err)
				}
				if resolves != 0 {
					t.Errorf("%s.%s %s: resolved before refusing", def.ID, op.Name, in.Name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("the sweep checked nothing")
	}
}

// The runtime's gate checks types the same way: a resource mutation, a
// pipeline run or a profile run with a mistyped input is refused before the
// acknowledgment is even considered.
func TestRuntimeGateChecksTypes(t *testing.T) {
	for _, def := range RuntimeDefinitions() {
		for _, op := range def.Operations {
			for _, in := range op.Inputs {
				wrong := wrongValue(in.Schema)
				if wrong == nil {
					continue
				}
				cfg := map[string]any{"id": "x", in.Name: wrong}
				err := runtimeGate(context.Background(), def, op.Name, cfg, MutationOpts{})
				if code := connectorErrorCode(err); code != ExternalConnectorInvalidArgs {
					t.Errorf("%s.%s %s: err = %v, want invalid_args", def.ID, op.Name, in.Name, err)
				}
			}
		}
	}
}

// What the executors accept still passes: integers as JSON numbers, Go ints
// and decimal strings; enums by value.
func TestTypeCheckAcceptsWhatExecutorsAccept(t *testing.T) {
	op := contract.Operation{Name: "op", Inputs: []contract.Input{
		contract.Field("n", contract.IntegerSchema("N.")),
		contract.Field("kind", map[string]any{"type": "string", "enum": []any{"MX", "NONE"}}),
	}}.Finalize()
	for _, v := range []any{42, int64(42), float64(42), "42"} {
		if err := op.CheckInputs(map[string]any{"n": v}, true); err != nil {
			t.Errorf("integer %T %v refused: %v", v, v, err)
		}
	}
	for _, v := range []any{4.5, "four", true} {
		if err := op.CheckInputs(map[string]any{"n": v}, true); err == nil {
			t.Errorf("integer accepted %T %v", v, v)
		}
	}
	if err := op.CheckInputs(map[string]any{"kind": "FWD"}, true); err == nil || !strings.Contains(err.Error(), "kind wants one of its declared values") {
		t.Errorf("enum: %v", err)
	}
}

// A wrong-type refusal names keys, never values, in a shape redaction leaves
// alone — even for keys named like credentials.
func TestTypeRefusalSurvivesRedaction(t *testing.T) {
	op := contract.Operation{Name: "op", Inputs: []contract.Input{
		contract.Field("token", contract.StringSchema("T.")), contract.Field("password", contract.IntegerSchema("P.")),
	}}.Finalize()
	err := op.CheckInputs(map[string]any{"token": 7, "password": "hunter2-SENTINEL"}, true)
	if err == nil {
		t.Fatal("accepted")
	}
	msg := err.Error()
	if strings.Contains(msg, "hunter2") {
		t.Fatalf("the refusal carries a value: %q", msg)
	}
	if got := redact.Text(msg); got != msg {
		t.Fatalf("redaction changed the refusal:\n got %q\nwant %q", got, msg)
	}
}

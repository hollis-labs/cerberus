package connector

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func contractOp(effect Effect) Operation {
	return Operation{
		Name:    "op",
		Effect:  effect,
		Target:  TargetDescriptor{Kind: "test.thing", From: []string{"id"}},
		Preview: PreviewNone,
		Output:  OutputStructured,
		Cost:    CostNone,
		LocalFS: LocalFSNone,
		Inputs: []Input{
			RequiredField("id", StringSchema("ID.")),
			Field("note", StringSchema("Note.")),
			Field("host", StringSchema("Host.")).LocalOnly(),
		},
	}
}

// Acknowledgment follows the effect (Decision 14): only the two read classes
// go without it, and an unknown or missing effect fails closed.
func TestEffectRequiresAck(t *testing.T) {
	want := map[Effect]bool{
		EffectRead: false, EffectReadSensitive: false,
		EffectWrite: true, EffectLifecycle: true, EffectDestructive: true, EffectExec: true, EffectAdmin: true,
		"": true, "bogus": true,
	}
	for effect, needs := range want {
		if got := effect.RequiresAck(); got != needs {
			t.Errorf("%q.RequiresAck() = %v, want %v", effect, got, needs)
		}
	}
}

// Finalize derives every compatibility field from the contract, so none of
// them can be set to disagree with it.
func TestFinalizeDerivesFromTheContract(t *testing.T) {
	for _, tc := range []struct {
		effect      Effect
		preview     PreviewKind
		destructive bool
		ack         bool
		dry         bool
	}{
		{EffectRead, PreviewNone, false, false, false},
		{EffectWrite, PreviewHost, false, true, true},
		{EffectLifecycle, PreviewNone, false, true, false},
		{EffectDestructive, PreviewServer, true, true, true},
		{EffectExec, PreviewHost, false, true, true},
	} {
		op := contractOp(tc.effect)
		op.Preview = tc.preview
		// A hand-set flag is overwritten, never trusted.
		op.Destructive, op.RequiresAck, op.SupportsDry = !tc.destructive, !tc.ack, !tc.dry
		got := op.Finalize()
		if got.Destructive != tc.destructive || got.RequiresAck != tc.ack || got.SupportsDry != tc.dry {
			t.Errorf("%s/%s: destructive=%v ack=%v dry=%v, want %v %v %v", tc.effect, tc.preview,
				got.Destructive, got.RequiresAck, got.SupportsDry, tc.destructive, tc.ack, tc.dry)
		}
	}
}

// The advertised schema is built from the caller-scope inputs only: a local
// input is accepted from the operator's shell and never advertised.
// Writing to the local filesystem needs acknowledgment whatever the effect:
// a read_sensitive download still overwrites a path the caller chose.
func TestLocalWritesRequireAck(t *testing.T) {
	op := contractOp(EffectReadSensitive)
	op.LocalFS = LocalFSWrites
	if !op.Finalize().RequiresAck {
		t.Fatal("local_fs writes did not require ack")
	}
	op.LocalFS = LocalFSReads
	if op.Finalize().RequiresAck {
		t.Fatal("local_fs reads on a read op required ack")
	}
}

func TestFinalizeAdvertisesCallerInputsOnly(t *testing.T) {
	op := contractOp(EffectRead).Finalize()
	props, _ := op.InputSchema["properties"].(map[string]any)
	if _, ok := props["host"]; ok {
		t.Fatal("local-only input advertised")
	}
	if _, ok := props["id"]; !ok {
		t.Fatal("caller input missing from the schema")
	}
	if req, _ := op.InputSchema["required"].([]string); !reflect.DeepEqual(req, []string{"id"}) {
		t.Fatalf("required = %v, want [id]", req)
	}
	if op.InputSchema["additionalProperties"] != false {
		t.Fatal("schema built from a key table must be closed")
	}
}

func TestFinalizeIsIdempotent(t *testing.T) {
	built := contractOp(EffectWrite)
	plugin := Operation{Name: "p", Effect: EffectRead, InputSchema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": StringSchema("A.")},
		"required":   []any{"a"},
	}}
	for _, op := range []Operation{built, plugin} {
		once := op.Finalize()
		if twice := once.Finalize(); !reflect.DeepEqual(once, twice) {
			t.Fatalf("%s: Finalize not idempotent:\n once %+v\ntwice %+v", op.Name, once, twice)
		}
	}
	// A plugin's schema is its source: Finalize leaves it as written.
	if got := plugin.Finalize().InputSchema; !reflect.DeepEqual(got, plugin.InputSchema) {
		t.Fatalf("plugin schema rewritten: %v", got)
	}
}

func TestCheckInputs(t *testing.T) {
	op := contractOp(EffectRead).Finalize()
	for _, tc := range []struct {
		name   string
		config map[string]any
		local  bool
		want   []string // substrings; nil means accepted
	}{
		{"accepted", map[string]any{"id": "x", "note": "n"}, false, nil},
		{"local input from the shell", map[string]any{"id": "x", "host": "h"}, true, nil},
		{"local input from a remote surface", map[string]any{"id": "x", "host": "h"}, false, []string{"refusing fields (host)", "your own shell"}},
		{"undeclared", map[string]any{"id": "x", "key_file": "/k", "allow": true}, true, []string{"refusing fields (allow, key_file)", "does not declare"}},
		{"missing required", map[string]any{"note": "n"}, true, []string{"missing required fields (id)"}},
		{"empty string is missing", map[string]any{"id": "  "}, true, []string{"missing required fields (id)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := op.CheckInputs(tc.config, tc.local)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var ierr *InputError
			if !errors.As(err, &ierr) {
				t.Fatalf("err = %v, want *InputError", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%q missing %q", err.Error(), want)
				}
			}
		})
	}
}

func TestCheckInputsOneOf(t *testing.T) {
	op := Operation{Name: "start", Inputs: []Input{Field("resource", nil), Field("container", nil)}, OneOf: [][]string{{"resource", "container"}}}
	if err := op.CheckInputs(map[string]any{}, true); err == nil || !strings.Contains(err.Error(), "one of (resource, container) is required") {
		t.Fatalf("err = %v, want one-of refusal", err)
	}
	if err := op.CheckInputs(map[string]any{"container": "web"}, false); err != nil {
		t.Fatalf("container accepted? %v", err)
	}
}

// A plugin schema that does not close its properties accepts keys it does
// not name; a closed one refuses them.
func TestInputsFromSchemaOpenness(t *testing.T) {
	closed := Operation{Name: "c", InputSchema: ObjectSchema(map[string]any{"a": StringSchema("A.")})}.Finalize()
	if err := closed.CheckInputs(map[string]any{"b": 1}, true); err == nil {
		t.Fatal("closed schema accepted an undeclared key")
	}
	open := Operation{Name: "o", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}.Finalize()
	if err := open.CheckInputs(map[string]any{"b": 1}, true); err != nil {
		t.Fatalf("open schema refused: %v", err)
	}
}

func TestValidateRequiresTheWholeContract(t *testing.T) {
	if err := contractOp(EffectRead).Finalize().Validate(); err != nil {
		t.Fatalf("complete contract refused: %v", err)
	}
	bad := Operation{Name: "bad", Inputs: []Input{
		Field("dup", nil), Field("dup", nil), RequiredField("h", nil).LocalOnly(),
	}, Target: TargetDescriptor{From: []string{"nope"}}, OneOf: [][]string{{"ghost"}}}
	err := bad.Validate()
	if err == nil {
		t.Fatal("empty contract accepted")
	}
	for _, want := range []string{"effect", "preview", "output", "cost", "local_fs", "target.kind",
		`input "dup" is declared twice`, `"h" is required but local-only`, `target field "nope"`, `one_of field "ghost"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validation missed %q: %v", want, err)
		}
	}
}

// A manifest without an effect is a gap, not a refusal: the host reads it as
// exec. A value outside the vocabulary is a refusal.
func TestManifestContractGapsAndRefusals(t *testing.T) {
	m := Manifest{
		APIVersion: ManifestAPIVersion, Kind: "Connector", ID: "p", Version: "1", ResourceTypes: []string{"x"},
		Operations: []ManifestOperation{
			{Name: "legacy_read", InputSchema: ObjectSchema(map[string]any{})},
			{Name: "declared", Effect: EffectRead, InputSchema: ObjectSchema(map[string]any{})},
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("a gap refused the manifest: %v", err)
	}
	gaps := m.ContractGaps()
	if len(gaps) != 1 || !strings.Contains(gaps[0], `"legacy_read" declares no effect: treated as exec`) {
		t.Fatalf("gaps = %v", gaps)
	}
	def := DefinitionFromManifest(m)
	if op, _ := def.Operation("legacy_read"); op.Effect != EffectExec || !op.RequiresAck {
		t.Fatalf("gap read as %+v, want exec with ack", op)
	}
	if op, _ := def.Operation("declared"); op.RequiresAck {
		t.Fatal("declared read requires ack")
	}

	m.Operations[1].Effect = "sorta_read"
	m.Operations[1].Preview = "maybe"
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), `effect "sorta_read"`) || !strings.Contains(err.Error(), `preview "maybe"`) {
		t.Fatalf("err = %v, want unknown-vocabulary refusals", err)
	}

	full := Manifest{Operations: []ManifestOperation{{Name: "declared", Effect: EffectRead}}}
	if gaps := full.ContractGaps(); gaps == nil || len(gaps) != 0 {
		t.Fatalf("no gaps must be an empty list, not nil: %#v", gaps)
	}
}

// The pre-contract flags still mean what they meant when a manifest has no
// effect: supports_dry is a plugin preview, destructive refuses a dev
// install. With an effect declared they are ignored.
func TestManifestLegacyFlags(t *testing.T) {
	legacy := ManifestOperation{Name: "scale", Destructive: true, SupportsDry: true}
	if legacy.EffectivePreview() != PreviewPlugin || !legacy.IsDestructive() {
		t.Fatalf("legacy flags lost: preview %s destructive %v", legacy.EffectivePreview(), legacy.IsDestructive())
	}
	declared := ManifestOperation{Name: "scale", Effect: EffectLifecycle, Destructive: true}
	if declared.IsDestructive() {
		t.Fatal("legacy destructive overrode the declared effect")
	}
}

// A manifest generated from a definition round-trips its contract, and
// writes the pre-contract flags so an older host still gates what this one
// gates.
func TestManifestRoundTripsTheContract(t *testing.T) {
	def := Finalize(Definition{ID: "t", Version: "1", ResourceTypes: []string{"x"}, Operations: []Operation{contractOp(EffectWrite)}})
	m := ManifestFromDefinition(def)
	if op := m.Operations[0]; op.Effect != EffectWrite || !op.Destructive || !op.RequiresAck {
		t.Fatalf("manifest op %+v: want effect write and both legacy flags set", op)
	}
	back, _ := DefinitionFromManifest(m).Operation("op")
	orig, _ := def.Operation("op")
	if back.Effect != orig.Effect || back.RequiresAck != orig.RequiresAck || !reflect.DeepEqual(back.Target, orig.Target) ||
		back.Preview != orig.Preview || back.Output != orig.Output || back.Cost != orig.Cost || back.LocalFS != orig.LocalFS {
		t.Fatalf("round trip lost the contract:\n got %+v\nwant %+v", back, orig)
	}
}

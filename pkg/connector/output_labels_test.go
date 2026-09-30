package connector

import (
	"reflect"
	"strings"
	"testing"
)

// Labels become pointers through properties, items and additionalProperties,
// with RFC 6901 escaping; an unknown label or an empty list is refused.
func TestOutputLabelPointers(t *testing.T) {
	schema := map[string]any{
		"x-cerberus-label": "untrusted",
		"properties": map[string]any{
			"rows": map[string]any{"items": map[string]any{"properties": map[string]any{
				"email": map[string]any{"x-cerberus-label": []any{"personal", "untrusted"}},
			}}},
			"by/id": map[string]any{"additionalProperties": map[string]any{"x-cerberus-label": "untrusted"}},
			"count": map[string]any{"type": "integer"},
		},
	}
	got, err := OutputLabelPointers(schema)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"": {"untrusted"}, "/rows/*/email": {"personal", "untrusted"}, "/by~1id/*": {"untrusted"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []any{"secret", []any{}, []any{"untrusted", 3}, 7} {
		if _, err := OutputLabelPointers(map[string]any{"x-cerberus-label": bad}); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

// A manifest with a bad output label does not validate, and a good one
// carries its schema into the operation's contract.
func TestManifestOutputSchema(t *testing.T) {
	m := Manifest{APIVersion: ManifestAPIVersion, Kind: "Connector", ID: "demo", Version: "1", ResourceTypes: []string{"demo"},
		Operations: []ManifestOperation{{Name: "list", Effect: EffectRead, InputSchema: ObjectSchema(map[string]any{}), OutputSchema: map[string]any{"x-cerberus-label": "secret"}}}}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "output_schema") {
		t.Fatalf("a bad label validated: %v", err)
	}
	m.Operations[0].OutputSchema = map[string]any{"properties": map[string]any{"msg": map[string]any{"x-cerberus-label": "untrusted"}}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	op := m.Operations[0].Operation()
	if _, ok := op.OutputSchema["properties"].(map[string]any)["msg"]; !ok {
		t.Fatalf("output schema lost: %v", op.OutputSchema)
	}
}

// Labels set on a Go definition survive into the manifest it writes
// (plugin.yaml, via write-dist) and back into the host's contract.
func TestOutputSchemaRoundTrips(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{"msg": map[string]any{"type": "string", "x-cerberus-label": "untrusted"}}}
	def := Definition{ID: "demo", Version: "1", ResourceTypes: []string{"demo"}, Operations: []Operation{
		{Name: "list", Effect: EffectRead, Output: OutputStructured, OutputSchema: schema, InputSchema: ObjectSchema(map[string]any{})},
	}}
	m := ManifestFromDefinition(def)
	if !reflect.DeepEqual(m.Operations[0].OutputSchema, schema) {
		t.Fatalf("manifest lost the output schema: %v", m.Operations[0].OutputSchema)
	}
	if back := DefinitionFromManifest(m); !reflect.DeepEqual(back.Operations[0].OutputSchema, schema) {
		t.Fatalf("definition lost the output schema: %v", back.Operations[0].OutputSchema)
	}
}

type labelRow struct {
	Name  string   `json:"name" cerb:"personal"`
	Email string   `json:"email,omitempty" cerb:"personal,untrusted"`
	Tags  []string `json:"tags" cerb:"untrusted"`
	Count int      `json:"count"`
}

type labelEmbedded struct {
	Note string `json:"note" cerb:"untrusted"`
}

type labelResult struct {
	labelEmbedded
	Rows  []labelRow          `json:"rows"`
	ByID  map[string]labelRow `json:"by_id"`
	Plain struct {
		N int `json:"n"`
	} `json:"plain"`
}

// A schema derived from a labeled Go type reads back as the pointers the
// tags mean; an unlabeled type is a reviewed empty object; a bad tag is an
// error.
func TestOutputSchemaFor(t *testing.T) {
	got, err := OutputLabelPointers(OutputSchemaOf[labelResult]())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"/note":           {"untrusted"},
		"/rows/*/name":    {"personal"},
		"/rows/*/email":   {"personal", "untrusted"},
		"/rows/*/tags/*":  {"untrusted"},
		"/by_id/*/name":   {"personal"},
		"/by_id/*/email":  {"personal", "untrusted"},
		"/by_id/*/tags/*": {"untrusted"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pointers:\n got %v\nwant %v", got, want)
	}
	type plain struct {
		N int `json:"n"`
	}
	if s := OutputSchemaOf[[]plain](); s["type"] != "array" || s["items"] != nil {
		t.Fatalf("an unlabeled type: %v", s)
	}
	type bad struct {
		N int `json:"n" cerb:"untrusted"`
	}
	if _, err := OutputSchemaFor(reflect.TypeFor[bad]()); err == nil {
		t.Fatal("a label on an int was accepted")
	}
}

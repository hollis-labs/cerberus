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

package connector

import "testing"

func TestObjectSchemaIncludesRequiredOnlyWhenProvided(t *testing.T) {
	withoutRequired := ObjectSchema(map[string]any{})
	if _, ok := withoutRequired["required"]; ok {
		t.Fatal("required should be omitted when no fields are provided")
	}

	withRequired := ObjectSchema(map[string]any{}, "owner", "repo")
	required, ok := withRequired["required"].([]string)
	if !ok {
		t.Fatalf("required has type %T, want []string", withRequired["required"])
	}
	if len(required) != 2 || required[0] != "owner" || required[1] != "repo" {
		t.Fatalf("required = %#v, want [owner repo]", required)
	}
}

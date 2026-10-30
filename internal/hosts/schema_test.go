package hosts

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestSchemaMatchesGoTypes keeps schema/hosts.yaml.schema.json and the Go
// types from drifting: at every object, the schema's property names equal the
// struct's yaml keys.
func TestSchemaMatchesGoTypes(t *testing.T) {
	data, err := os.ReadFile("../../schema/hosts.yaml.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	defs, _ := root["$defs"].(map[string]any)
	var walk func(path string, node map[string]any, typ reflect.Type)
	walk = func(path string, node map[string]any, typ reflect.Type) {
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if ref, ok := node["$ref"].(string); ok {
			target, ok := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
			if !ok {
				t.Errorf("%s: unresolved %s", path, ref)
				return
			}
			walk(path, target, typ)
			return
		}
		if _, ok := node["oneOf"]; ok { // size
			return
		}
		switch typ.Kind() {
		case reflect.Struct:
			if typ == reflect.TypeOf(time.Time{}) {
				return
			}
			props, _ := node["properties"].(map[string]any)
			fields := map[string]reflect.Type{}
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				name := strings.Split(f.Tag.Get("yaml"), ",")[0]
				if name == "" || name == "-" {
					continue
				}
				fields[name] = f.Type
			}
			var want, got []string
			for k := range fields {
				want = append(want, k)
			}
			for k := range props {
				got = append(got, k)
			}
			sort.Strings(want)
			sort.Strings(got)
			if strings.Join(want, ",") != strings.Join(got, ",") {
				t.Errorf("%s: Go yaml keys %v, schema properties %v", path, want, got)
				return
			}
			for k, ft := range fields {
				walk(path+"."+k, props[k].(map[string]any), ft)
			}
		case reflect.Slice:
			if items, ok := node["items"].(map[string]any); ok {
				walk(path+"[]", items, typ.Elem())
			}
		}
	}
	walk("hosts.yaml", root, reflect.TypeOf(File{}))
}

// The slot kinds in the schema are exactly the ones the validator accepts.
func TestSchemaSlotKindsMatchTheValidator(t *testing.T) {
	data, err := os.ReadFile("../../schema/hosts.yaml.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	got := root.Defs["storage_slot"].Properties["kind"].Enum
	want := append([]string(nil), StorageSlotKinds...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("schema kinds %v, validator kinds %v", got, want)
	}
}

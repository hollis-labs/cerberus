package connector

import (
	"fmt"
	"reflect"
	"strings"
)

// LabelTag is the struct tag a Go DTO labels its fields with, the same
// convention the host's own DTOs use:
//
//	Message string `json:"message" cerb:"untrusted"`
//	Email   string `json:"email" cerb:"personal,untrusted"`
const LabelTag = "cerb"

// OutputSchemaFor derives an operation's output_schema from the Go type it
// returns, reading LabelTag: a plugin labels its DTO fields and sets
// Operation.OutputSchema to this, so the manifest cannot drift from the
// type. The schema goes only as deep as the labels; a type that labels
// nothing yields {"type": "object"} (or array), which says the result was
// reviewed and has nothing to mark.
func OutputSchemaFor(t reflect.Type) (map[string]any, error) {
	var problems []string
	schema := outputSchemaOf(t, map[reflect.Type]bool{}, &problems)
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s: %s", t, strings.Join(problems, "; "))
	}
	return schema, nil
}

// OutputSchemaOf is OutputSchemaFor for T, panicking on a bad label: it is
// meant for a Definition literal, where conformance catches it at test time.
func OutputSchemaOf[T any]() map[string]any {
	schema, err := OutputSchemaFor(reflect.TypeFor[T]())
	if err != nil {
		panic(err)
	}
	return schema
}

func outputSchemaOf(t reflect.Type, seen map[reflect.Type]bool, problems *[]string) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string"}
		}
		out := map[string]any{"type": "array"}
		if items := outputSchemaOf(t.Elem(), seen, problems); labeled(items) {
			out["items"] = items
		}
		return out
	case reflect.Map:
		out := map[string]any{"type": "object"}
		if extra := outputSchemaOf(t.Elem(), seen, problems); labeled(extra) {
			out["additionalProperties"] = extra
		}
		return out
	case reflect.Struct:
		out := map[string]any{"type": "object"}
		if seen[t] {
			return out
		}
		seen[t] = true
		defer delete(seen, t)
		props := map[string]any{}
		structProps(t, props, seen, problems)
		if len(props) > 0 {
			out["properties"] = props
		}
		return out
	case reflect.String:
		return map[string]any{"type": "string"}
	default:
		return map[string]any{}
	}
}

func structProps(t reflect.Type, props map[string]any, seen map[reflect.Type]bool, problems *[]string) {
	for i := range t.NumField() {
		f := t.Field(i)
		// An embedded struct's exported fields are promoted by encoding/json
		// even when the embedded type itself is unexported.
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Tag.Get("json") == "-" {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if name == "" && f.Anonymous && ft.Kind() == reflect.Struct {
			structProps(ft, props, seen, problems) // inlined, as encoding/json does
			continue
		}
		if name == "" {
			name = f.Name
		}
		if raw, ok := f.Tag.Lookup(LabelTag); ok {
			labels, err := outputLabels(splitLabels(raw))
			if err != nil {
				*problems = append(*problems, fmt.Sprintf("field %s: %v", f.Name, err))
				continue
			}
			node, ok := labeledText(f.Type, labels)
			if !ok {
				*problems = append(*problems, fmt.Sprintf("field %s is %s, which holds no text to label", f.Name, f.Type))
				continue
			}
			props[name] = node
			continue
		}
		if sub := outputSchemaOf(f.Type, seen, problems); labeled(sub) {
			props[name] = sub
		}
	}
}

func splitLabels(raw string) []any {
	var out []any
	for _, part := range strings.Split(raw, ",") {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

// labeledText is the schema for a labeled field: the label on the text
// itself, which for a list or map of strings is every element.
func labeledText(t reflect.Type, labels []string) (map[string]any, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string", OutputLabelKey: labels}, true
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", OutputLabelKey: labels}, true
		}
		items, ok := labeledText(t.Elem(), labels)
		return map[string]any{"type": "array", "items": items}, ok
	case reflect.Map:
		extra, ok := labeledText(t.Elem(), labels)
		return map[string]any{"type": "object", "additionalProperties": extra}, ok
	default:
		return nil, false
	}
}

// labeled is whether a schema node carries a label anywhere in it.
func labeled(node map[string]any) bool {
	if node == nil {
		return false
	}
	if _, ok := node[OutputLabelKey]; ok {
		return true
	}
	for _, key := range []string{"items", "additionalProperties"} {
		if sub, ok := node[key].(map[string]any); ok && labeled(sub) {
			return true
		}
	}
	if props, ok := node["properties"].(map[string]any); ok {
		for _, p := range props {
			if sub, ok := p.(map[string]any); ok && labeled(sub) {
				return true
			}
		}
	}
	return false
}

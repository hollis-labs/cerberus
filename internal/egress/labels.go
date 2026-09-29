// Package egress labels what Cerberus returns (I7, section 7 of the
// live-systems plan): which parts of a result are text Cerberus did not
// compose, and which carry personal data. The DTO decides what exists; the
// label says what it is, so a surface can mark it (P4-2) and policy can shape
// it (P4-4).
//
// Labels are struct tags on DTO fields:
//
//	Stdout string `json:"stdout" cerb:"untrusted"`
//	Email  string `json:"email"  cerb:"personal"`
//
// A label goes only on a field that holds text: a string, a []byte, or a
// slice or map of strings. Anything else is a declaration error, reported by
// Fields and caught by conformance.
package egress

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Label is what a field is, for egress.
type Label string

const (
	// Untrusted is text Cerberus did not compose: workload and command
	// output, vendor messages, names and descriptions anyone who can push
	// can set. A client should present it as data, never as instructions
	// (ASI06).
	Untrusted Label = "untrusted"
	// Personal is personal data: a name, an email address, a phone number.
	Personal Label = "personal"
)

// Labels is the vocabulary.
var Labels = []Label{Untrusted, Personal}

// TagKey is the struct tag labels are read from.
const TagKey = "cerb"

// Field is a labeled place in a result: a JSON pointer (RFC 6901), in which
// a "*" segment stands for every element of an array or map. The empty
// pointer is the whole result.
type Field struct {
	Pointer string  `json:"pointer"`
	Labels  []Label `json:"labels"`
}

// Fields are the labeled places in a value of type t, in pointer order.
func Fields(t reflect.Type) ([]Field, error) {
	w := walker{seen: map[reflect.Type]bool{}}
	w.walk(t, "")
	if len(w.errs) > 0 {
		return nil, fmt.Errorf("%s: %s", t, strings.Join(w.errs, "; "))
	}
	sort.Slice(w.out, func(i, j int) bool { return w.out[i].Pointer < w.out[j].Pointer })
	return w.out, nil
}

// Has is whether any field in fields carries label.
func Has(fields []Field, label Label) bool {
	for _, f := range fields {
		for _, l := range f.Labels {
			if l == label {
				return true
			}
		}
	}
	return false
}

type walker struct {
	seen map[reflect.Type]bool
	out  []Field
	errs []string
}

func (w *walker) walk(t reflect.Type, at string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return // []byte is a leaf
		}
		w.walk(t.Elem(), at+"/*")
	case reflect.Struct:
		// A recursive type is walked once per path, which is enough to find
		// its labels.
		if w.seen[t] {
			return
		}
		w.seen[t] = true
		defer delete(w.seen, t)
		for i := range t.NumField() {
			w.field(t.Field(i), at)
		}
	default:
		// A scalar: nothing inside it to label.
	}
}

func (w *walker) field(f reflect.StructField, at string) {
	if !f.IsExported() {
		return
	}
	name, inline := jsonName(f)
	if name == "-" {
		return
	}
	path := at + "/" + escape(name)
	if inline {
		path = at
	}
	if raw, ok := f.Tag.Lookup(TagKey); ok {
		labels, err := parse(raw)
		switch {
		case err != nil:
			w.errs = append(w.errs, fmt.Sprintf("field %s: %v", f.Name, err))
		case !holdsText(f.Type):
			w.errs = append(w.errs, fmt.Sprintf("field %s is %s, which holds no text to label", f.Name, f.Type))
		default:
			w.out = append(w.out, Field{Pointer: textPointer(f.Type, path), Labels: labels})
		}
		return
	}
	w.walk(f.Type, path)
}

// jsonName is the name encoding/json gives f, and whether f's fields are
// inlined into its parent (an untagged embedded struct).
func jsonName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" && tag == "-" {
		return "-", false
	}
	if name == "" {
		t := f.Type
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if f.Anonymous && t.Kind() == reflect.Struct {
			return "", true
		}
		return f.Name, false
	}
	return name, false
}

func parse(raw string) ([]Label, error) {
	var out []Label
	for _, part := range strings.Split(raw, ",") {
		l := Label(strings.TrimSpace(part))
		known := false
		for _, v := range Labels {
			known = known || v == l
		}
		if !known {
			return nil, fmt.Errorf("label %q is not one of %v", l, Labels)
		}
		out = append(out, l)
	}
	return out, nil
}

func holdsText(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return true
	case reflect.Slice, reflect.Array:
		return t.Elem().Kind() == reflect.Uint8 || holdsText(t.Elem())
	case reflect.Map:
		return holdsText(t.Elem())
	default:
		return false
	}
}

// textPointer is the pointer to the text a labeled field holds: the field,
// or every element of it.
func textPointer(t reflect.Type, path string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return path
		}
		return textPointer(t.Elem(), path+"/*")
	case reflect.Map:
		return textPointer(t.Elem(), path+"/*")
	default:
		return path
	}
}

// escape is RFC 6901's escaping of a pointer segment.
func escape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

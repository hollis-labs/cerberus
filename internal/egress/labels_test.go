package egress

import (
	"reflect"
	"strings"
	"testing"
)

type inner struct {
	Line  string   `json:"line" cerb:"untrusted"`
	Email string   `json:"email,omitempty" cerb:"personal,untrusted"`
	Tags  []string `json:"tags" cerb:"untrusted"`
	Count int      `json:"count"`
}

type Embedded struct {
	Note string `json:"note" cerb:"untrusted"`
}

type outer struct {
	Embedded
	Items   []inner           `json:"items"`
	ByName  map[string]*inner `json:"by_name"`
	Raw     []byte            `json:"raw" cerb:"untrusted"`
	Skipped string            `json:"-" cerb:"untrusted"`
	Slash   string            `json:"a/b" cerb:"untrusted"`
	Self    *outer            `json:"self,omitempty"`
	private string            //nolint:unused // proves an unexported field is skipped
}

// Labels become JSON pointers the way encoding/json would lay the value
// out: through slices and maps (*), embedded structs inlined, `-` skipped,
// and RFC 6901 escaping.
func TestFieldsArePointers(t *testing.T) {
	fields, err := Fields(reflect.TypeFor[outer]())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range fields {
		var ls []string
		for _, l := range f.Labels {
			ls = append(ls, string(l))
		}
		got[f.Pointer] = strings.Join(ls, ",")
	}
	want := map[string]string{
		"/note": "untrusted", "/raw": "untrusted", "/a~1b": "untrusted",
		"/items/*/line": "untrusted", "/items/*/email": "personal,untrusted", "/items/*/tags/*": "untrusted",
		"/by_name/*/line": "untrusted", "/by_name/*/email": "personal,untrusted", "/by_name/*/tags/*": "untrusted",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fields:\n got %v\nwant %v", got, want)
	}
	if !Has(fields, Personal) || Has([]Field{{Pointer: "/x", Labels: []Label{Untrusted}}}, Personal) {
		t.Fatal("Has")
	}
}

// A label that is not in the vocabulary, or on a field with no text, is a
// declaration error.
func TestBadLabelsAreErrors(t *testing.T) {
	type unknown struct {
		S string `json:"s" cerb:"secret"`
	}
	type notText struct {
		N int `json:"n" cerb:"untrusted"`
	}
	for name, typ := range map[string]reflect.Type{"unknown label": reflect.TypeFor[unknown](), "not text": reflect.TypeFor[notText]()} {
		if _, err := Fields(typ); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if fields, err := Fields(reflect.TypeFor[string]()); err != nil || len(fields) != 0 {
		t.Fatalf("a bare string has no fields of its own: %v %v", fields, err)
	}
}

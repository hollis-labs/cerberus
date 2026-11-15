package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The legacy types marshal byte for byte as the old build's types did. testdata/golden holds the
// bytes the old internal/cerbapi types produced (on main), zero-valued and with every field set by
// the same deterministic filler as below; regenerate by running that filler over the old types.
// OpResult.Plan is left out on both sides: observe mode never returns a plan.
func TestLegacyTypesMatchTheOldWireFormat(t *testing.T) {
	types := map[string]func() any{
		"ResourceInfo": func() any { return &ResourceInfo{} }, "ResourceRuntimeStatus": func() any { return &ResourceRuntimeStatus{} },
		"ResourceInspect": func() any { return &ResourceInspect{} }, "ResourceDoctor": func() any { return &ResourceDoctor{} },
		"LogLines": func() any { return &LogLines{} }, "OpResult": func() any { return &OpResult{} },
		"DaemonHealth": func() any { return &DaemonHealth{} }, "HostView": func() any { return &HostView{} },
	}
	for name, mk := range types {
		for _, variant := range []string{"zero", "full"} {
			v := mk()
			if variant == "full" {
				fill(reflect.ValueOf(v).Elem(), name, 0)
			}
			want, err := os.ReadFile(filepath.Join("testdata", "golden", name+"."+variant+".json")) //nolint:gosec // fixed test data names
			if err != nil {
				t.Fatal(err)
			}
			if got := wire(v); !bytes.Equal(got, want) {
				t.Errorf("%s (%s):\n got %s\nwant %s", name, variant, got, want)
			}
		}
	}
}

// fill sets every exported field of v to a deterministic value derived from its name, so the
// same filler run over two types with the same JSON shape yields the same bytes.
func fill(v reflect.Value, name string, depth int) {
	if depth > 6 {
		return
	}
	switch v.Kind() { //nolint:exhaustive // the legacy types use only these kinds
	case reflect.String:
		v.SetString(strings.ToLower(name))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(len(name)))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(len(name)))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(len(name)) + 0.5)
	case reflect.Ptr:
		p := reflect.New(v.Type().Elem())
		fill(p.Elem(), name, depth+1)
		v.Set(p)
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0), name, depth+1)
		v.Set(s)
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return
		}
		m := reflect.MakeMap(v.Type())
		e := reflect.New(v.Type().Elem()).Elem()
		fill(e, name, depth+1)
		m.SetMapIndex(reflect.ValueOf("k").Convert(v.Type().Key()), e)
		v.Set(m)
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() || f.Name == "Plan" {
				continue
			}
			fill(v.Field(i), f.Name, depth+1)
		}
	}
}

// wire is the old socket's bytes for v: json.Encoder, compact, HTML-escaped, newline.
func wire(v any) []byte {
	var b bytes.Buffer
	_ = json.NewEncoder(&b).Encode(v)
	return b.Bytes()
}

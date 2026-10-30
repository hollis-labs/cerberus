package cerbapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const openAPIFile = "../../schema/hosts.openapi.json"

// The committed document is the generated one. Regenerate with
// UPDATE_OPENAPI=1 go test ./internal/cerbapi -run TestOpenAPIMatchesTheCommittedFile.
// It reads nothing from the network.
func TestOpenAPIMatchesTheCommittedFile(t *testing.T) {
	doc, err := HostsOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	doc = append(doc, '\n')
	if os.Getenv("UPDATE_OPENAPI") == "1" {
		if err := os.WriteFile(openAPIFile, doc, 0o644); err != nil { //nolint:gosec // a committed schema file
			t.Fatal(err)
		}
		return
	}
	have, err := os.ReadFile(openAPIFile)
	if err != nil {
		t.Fatalf("%v (generate it with UPDATE_OPENAPI=1)", err)
	}
	if !bytes.Equal(have, doc) {
		t.Fatal("schema/hosts.openapi.json is out of date with the Go types and routes; regenerate it with UPDATE_OPENAPI=1 go test ./internal/cerbapi -run TestOpenAPIMatchesTheCommittedFile")
	}
	again, _ := HostsOpenAPI()
	if !bytes.Equal(append(again, '\n'), doc) {
		t.Fatal("the generated document is not deterministic")
	}
}

func parsedOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	doc, err := HostsOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOpenAPIIsWellFormedAndScopesAreTheEnforcedOnes(t *testing.T) {
	m := parsedOpenAPI(t)
	if m["openapi"] != "3.1.0" {
		t.Fatalf("version %v", m["openapi"])
	}
	schemas := m["components"].(map[string]any)["schemas"].(map[string]any)
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				name := strings.TrimPrefix(ref, "#/components/schemas/")
				if def, ok := schemas[name]; !ok || def == nil {
					t.Errorf("unresolved $ref %s", ref)
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(m)
	paths := m["paths"].(map[string]any)
	for _, r := range HostRoutes() {
		op, ok := paths["/api"+r.Path].(map[string]any)[strings.ToLower(r.Method)].(map[string]any)
		if !ok {
			t.Errorf("%s %s is not in the document", r.Method, r.Path)
			continue
		}
		if got, _ := op["x-cerberus-scope"].(string); got != RouteScope(r) {
			t.Errorf("%s %s documents scope %q, the daemon enforces %q", r.Method, r.Path, got, RouteScope(r))
		}
	}
	want := map[string]string{
		"GET /hosts/{name}":          "cerberus:hosts.read",
		"PATCH /hosts/{name}":        "cerberus:hosts.write",
		"DELETE /hosts/{name}":       "cerberus:operate",
		"POST /components/{id}/move": "cerberus:hosts.write",
	}
	for _, r := range HostRoutes() {
		if w, ok := want[r.Method+" "+r.Path]; ok && !strings.HasPrefix(RouteScope(r), w) {
			t.Errorf("%s %s needs %q, got %q", r.Method, r.Path, w, RouteScope(r))
		}
	}
	// Strings from a host or an operator are marked untrusted.
	comp := schemas["Component"].(map[string]any)["properties"].(map[string]any)
	if comp["serial"].(map[string]any)["x-cerberus-untrusted"] != true {
		t.Errorf("Component.serial is not marked untrusted: %v", comp["serial"])
	}
	// Absent is not zero: ceilings and storage slots are nullable on HostView.
	hv := schemas["HostView"].(map[string]any)["properties"].(map[string]any)
	if _, ok := hv["ceilings"].(map[string]any)["anyOf"]; !ok {
		t.Errorf("HostView.ceilings must be nullable: %v", hv["ceilings"])
	}
}

// Every documented route reaches a real handler, and every host operation is
// documented or deliberately not.
func TestRouteTableMatchesTheHandlers(t *testing.T) {
	hostH := HostHTTP(unavailableHostClient{}, "/hosts")
	compH := ComponentHTTP(unavailableHostClient{}, "/components")
	for _, r := range HostRoutes() {
		path := strings.NewReplacer("{name}", "box-a", "{id}", "ram-1").Replace(r.Path)
		var body *strings.Reader
		if r.Request != nil {
			body = strings.NewReader("{}")
		} else {
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(r.Method, path, body)
		if r.Request != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		if strings.HasPrefix(r.Path, "/components") {
			compH.ServeHTTP(rec, req)
		} else {
			hostH.ServeHTTP(rec, req)
		}
		if rec.Code == http.StatusNotFound && (strings.Contains(rec.Body.String(), "no such host route") || strings.Contains(rec.Body.String(), "no such component route")) {
			t.Errorf("%s %s is documented but no handler serves it", r.Method, r.Path)
		}
		if r.Static && rec.Code != http.StatusOK {
			t.Errorf("%s %s = %d", r.Method, r.Path, rec.Code)
		}
	}
	documented := map[string]bool{}
	for _, r := range HostRoutes() {
		documented[r.Operation] = true
	}
	for _, op := range HostDefinition().Operations {
		if !documented[op.Name] && !unpublishedHostOperations[op.Name] {
			t.Errorf("host operation %s has no route in HostRoutes and is not in unpublishedHostOperations", op.Name)
		}
	}
}

func TestRequestTypesCoverTheWireBody(t *testing.T) {
	fields := func(v any) map[string]bool {
		out := map[string]bool{}
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
			if name != "" && name != "-" {
				out[name] = true
			}
		}
		return out
	}
	wire := fields(writeBody{})
	union := map[string]bool{}
	for _, v := range []any{hostUpdateRequest{}, writeRequest{}, componentCreateRequest{}, componentUpdateRequest{}, componentMoveRequest{}, inventoryRequest{}} {
		for k := range fields(v) {
			union[k] = true
			if !wire[k] {
				t.Errorf("request field %q is documented but the handler's body does not accept it", k)
			}
		}
	}
	var missing []string
	for k := range wire {
		if !union[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the handler accepts %v, which no documented request carries", missing)
	}
}

func TestOpenAPIIsServed(t *testing.T) {
	h := HostHTTP(unavailableHostClient{}, "/api/hosts")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hosts/_openapi", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m["openapi"] != "3.1.0" {
		t.Fatalf("%v %v", m["openapi"], err)
	}
}

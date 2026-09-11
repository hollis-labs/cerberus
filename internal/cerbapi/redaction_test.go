package cerbapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSocketJSONRedactsDiagnostics(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, 200, map[string]any{"config": map[string]any{"env": map[string]string{"API_KEY": "socket-sentinel"}}, "command": []string{"server", "--password", "argv-sentinel"}, "raw": "Authorization: Bearer bearer-sentinel", "state": "running"})
	if !json.Valid(w.Body.Bytes()) {
		t.Fatal("invalid JSON")
	}
	for _, secret := range []string{"socket-sentinel", "argv-sentinel", "bearer-sentinel"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("credential leaked")
		}
	}
	if !strings.Contains(w.Body.String(), "running") {
		t.Fatal("lost status")
	}
}

package webui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebJSONRedactsDiagnostics(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, 200, map[string]any{"config": map[string]any{"env": map[string]string{"TOKEN": "web-sentinel"}}, "state": "running"})
	if !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "web-sentinel") {
		t.Fatal("invalid or unsafe diagnostic JSON")
	}
	if !strings.Contains(w.Body.String(), "running") {
		t.Fatal("lost status")
	}
}

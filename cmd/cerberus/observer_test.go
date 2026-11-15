package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// fakeUnits answers `systemctl --user show` from a table and refuses every other command.
type fakeUnits map[string]string

func (f fakeUnits) CombinedOutput(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) < 3 || args[0] != "--user" || args[1] != "show" {
		return nil, fmt.Errorf("unexpected command %v", args)
	}
	if out, ok := f[args[2]]; ok {
		return []byte(out), nil
	}
	return []byte("LoadState=not-found\nActiveState=inactive\nSubState=dead\n"), nil
}

// L3 through the adapter carries the unit's systemd_* fields; a dev session and an
// unsupervised handle leave them omitted.
func TestStatusThroughLaneObserver(t *testing.T) {
	home := t.TempDir()
	resources := []config.ResourceDef{
		{ID: "api", Project: "demo", Type: "process", Connector: "local",
			Config: map[string]any{"mode": "os_service", "supervisor": "systemd_user", "command": []any{"/bin/true"}}},
		{ID: "web-dev", Project: "web", Type: "process", Connector: "local",
			Config: map[string]any{"mode": "dev_session", "command": []any{"/bin/sleep", "60"}}},
		{ID: "box-ssh", Type: "server", Connector: "ssh"},
	}
	list := func() ([]config.ResourceDef, error) { return resources, nil }
	units := fakeUnits{"com.hollis-labs.cerberus.demo.api.service": "LoadState=loaded\nActiveState=active\nSubState=running\n" +
		"UnitFileState=enabled\nResult=success\nMainPID=4242\nNRestarts=2\n"}
	lane, err := supervise.New(supervise.Options{Mode: settings.ModeObserve, Resources: list, Home: home, StateDir: home, Runner: units})
	if err != nil {
		t.Fatal(err)
	}
	a, err := api.New(api.Options{Resources: list, Observer: laneObserver{lane: lane}, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	status := func(id string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		a.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/resources/"+id+"/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body)
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	got := status("api")
	want := map[string]any{
		"status": "running", "service_name": "com.hollis-labs.cerberus.demo.api.service",
		"systemd_load_state": "loaded", "systemd_active_state": "active", "systemd_sub_state": "running",
		"systemd_unit_file_state": "enabled", "systemd_result": "success",
		"systemd_pid": float64(4242), "systemd_restarts": float64(2),
		"systemd_diagnosis": "service is loaded and running",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("api %s = %v, want %v", k, got[k], v)
		}
	}
	if h, _ := got["systemd_highlights"].([]any); len(h) == 0 {
		t.Errorf("api systemd_highlights = %v", got["systemd_highlights"])
	}
	if _, ok := got["systemd_exit_status"]; ok {
		t.Errorf("api systemd_exit_status present for a unit that never exited")
	}
	for _, id := range []string{"web-dev", "box-ssh"} {
		for k := range status(id) {
			if strings.HasPrefix(k, "systemd_") {
				t.Errorf("%s carries %s", id, k)
			}
		}
	}
}

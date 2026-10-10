package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	cmcp "github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/scheduling"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

// This fixture owns its DB, audit, caller and loopback server; no daemon,
// engine, credentials, provider or resource is opened.
func TestScheduleCrossSurfaceContract(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	core, err := scheduling.New(context.Background(), db, nil, nil, scheduling.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sink, err := audit.OpenFileSink(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := cerbapi.NewScheduleService(core, sink)
	contextFor := func(ctx context.Context) context.Context {
		return cerbapi.WithPrincipal(cerbapi.BeginRequest(ctx, cerbapi.SurfaceWeb), cerbapi.WebSessionPrincipal("fixture-session"))
	}
	handler := cerbapi.ScheduleHTTP(service, "/schedules/v1/")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(contextFor(r.Context())))
	}))
	defer server.Close()
	httpCall := func(req scheduling.Call) scheduling.Result {
		t.Helper()
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		response, err := server.Client().Post(server.URL+"/schedules/v1/"+req.Operation, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var envelope struct {
			Result scheduling.Result `json:"result"`
			Error  any               `json:"error"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			t.Fatalf("HTTP %d: %+v", response.StatusCode, envelope)
		}
		return envelope.Result
	}
	cliCall := func(args []string, input []byte) scheduling.Result {
		t.Helper()
		cmd := newScheduleCommand(func(*cobra.Command) (scheduling.Service, error) { return service, nil })
		cmd.SetContext(contextFor(context.Background()))
		cmd.SetArgs(append([]string{"--json"}, args...))
		cmd.SetIn(bytes.NewReader(input))
		var output bytes.Buffer
		cmd.SetOut(&output)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var result scheduling.Result
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	tools := cmcp.ScheduleTools(service)
	mcpCall := func(req scheduling.Call) scheduling.Result {
		t.Helper()
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		var args map[string]any
		if err = json.Unmarshal(data, &args); err != nil {
			t.Fatal(err)
		}
		delete(args, "operation")
		for _, tool := range tools {
			if tool.Name == "cerberus_schedule_"+req.Operation {
				out, err := tool.Handler(contextFor(context.Background()), args)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(out)
				if err != nil {
					t.Fatal(err)
				}
				var result scheduling.Result
				if err = json.Unmarshal(encoded, &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
		}
		t.Fatal("tool missing")
		return scheduling.Result{}
	}
	for _, surface := range []string{"cli", "mcp", "http"} {
		t.Run(surface, func(t *testing.T) {
			job := scheduling.Job{ID: surface, Name: "Fixture", OwnerApp: "fixture", Timing: scheduling.Timing{At: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Location: "UTC"}, Target: scheduling.Target{Kind: scheduling.ResourceStart, ID: "fake"}, Timeout: time.Second, Enabled: true}
			create := scheduling.Call{Operation: "create", Job: &job, IdempotencyKey: surface, Acknowledged: true}
			var created scheduling.Result
			switch surface {
			case "cli":
				input, err := json.Marshal(job)
				if err != nil {
					t.Fatal(err)
				}
				created = cliCall([]string{"create", "--ack", "--idempotency-key", surface}, input)
			case "mcp":
				created = mcpCall(create)
			case "http":
				created = httpCall(create)
			}
			get := scheduling.Call{Operation: "get", OwnerApp: job.OwnerApp, ID: job.ID}
			gotCLI := cliCall([]string{"get", "--app", job.OwnerApp, "--id", job.ID}, nil)
			gotMCP := mcpCall(get)
			gotHTTP := httpCall(get)
			if !reflect.DeepEqual(created.Job, gotCLI.Job) || !reflect.DeepEqual(created.Job, gotMCP.Job) || !reflect.DeepEqual(created.Job, gotHTTP.Job) {
				t.Fatalf("surface drift: %+v %+v %+v %+v", created, gotCLI, gotMCP, gotHTTP)
			}
		})
	}
}
func TestScheduleCLIHelpAndNoFallback(t *testing.T) {
	cmd := newScheduleCommand(func(*cobra.Command) (scheduling.Service, error) { return scheduling.NewService(nil, nil), nil })
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"run-now", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte("request-id")) {
		t.Fatal(output.String())
	}
}

func TestScheduleCLIJSONErrorIsOneReportedDocument(t *testing.T) {
	cmd := newScheduleCommand(func(*cobra.Command) (scheduling.Service, error) { return scheduling.NewService(nil, nil), nil })
	cmd.SetArgs([]string{"--json", "list"})
	var output bytes.Buffer
	cmd.SetErr(&output)
	err := cmd.Execute()
	var reported *reportedCommandError
	if !errors.As(err, &reported) {
		t.Fatalf("JSON error not marked as reported: %v", err)
	}
	var body struct {
		Error scheduling.Error `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &body); err != nil {
		t.Fatal(output.String(), err)
	}
	if body.Error.Code != "unavailable" {
		t.Fatal(body)
	}
}

func TestScheduleCLIOverLimitNeverResolvesService(t *testing.T) {
	for _, suffix := range []string{"", "{}"} {
		called := false
		cmd := newScheduleCommand(func(*cobra.Command) (scheduling.Service, error) {
			called = true
			return scheduling.NewService(nil, nil), nil
		})
		cmd.SetArgs([]string{"create", "--ack", "--idempotency-key", "fixture"})
		cmd.SetIn(strings.NewReader(`{"id":"fixture"}` + strings.Repeat(" ", 1<<20) + suffix))
		err := cmd.Execute()
		if scheduling.ErrorCode(err) != "invalid" || called {
			t.Fatalf("oversized job error=%v service resolved=%v", err, called)
		}
	}
}

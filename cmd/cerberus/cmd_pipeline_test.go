package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
)

type fixturePipelineClient struct {
	detail *cerbapi.PipelineDetail
	raw    []byte
	runErr error
	calls  *int
}

func (f fixturePipelineClient) ListPipelines(context.Context) ([]cerbapi.PipelineInfo, error) {
	return nil, nil
}
func (f fixturePipelineClient) GetPipeline(context.Context, string) (*cerbapi.PipelineDetail, error) {
	return f.detail, nil
}
func (f fixturePipelineClient) RunPipeline(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.PipelineRunResult, error) {
	if f.calls != nil {
		*f.calls++
	}
	if f.runErr != nil {
		return nil, f.runErr
	}
	return &cerbapi.PipelineRunResult{Success: true, Raw: f.raw}, nil
}

func TestPipelineRunOutput(t *testing.T) {
	client := fixturePipelineClient{
		detail: &cerbapi.PipelineDetail{Definition: config.PipelineDef{ID: "release", Name: "Release"}},
		raw:    []byte(`{"pipeline_id":"release","status":"failed","stages":[{"name":"build","status":"running","duration_ms":2000000},{"name":"verify","status":"failed","duration_ms":3000000,"error":"bad exit"},{"name":"deploy","status":"stopped","duration_ms":0}],"duration_ms":5000000,"error":"bad exit"}`),
	}
	var out bytes.Buffer
	err := runPipelineCommand(context.Background(), client, "release", &out)
	if err == nil || err.Error() != "pipeline failed: bad exit" {
		t.Fatalf("error = %v", err)
	}
	want := "Pipeline: Release (release)\n\n  build                ok (2ms)\n  verify               FAILED (3ms)\n    error: bad exit\n  deploy               skipped (0s)\n\nPipeline release: failed (5ms)\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

// A refused run prints the refusal and nothing else: no line claims the
// pipeline started.
func TestRefusedPipelineRunAnnouncesNothing(t *testing.T) {
	client := fixturePipelineClient{
		detail: &cerbapi.PipelineDetail{Definition: config.PipelineDef{ID: "release", Name: "Release"}},
		runErr: errors.New(`daemon: pipeline run: acknowledgment_required: exec operation "run" on "release" requires operator acknowledgment`),
	}
	var out bytes.Buffer
	if err := runPipelineCommand(context.Background(), client, "release", &out); err == nil {
		t.Fatal("refused run returned no error")
	}
	if out.Len() != 0 {
		t.Fatalf("a refused run printed %q", out.String())
	}
}

// An invalid or unknown pipeline still goes to the runtime, which refuses it
// before anything executes and records the attempt. What the CLI's lookup
// found rides along as a hint, and nothing claims the pipeline started.
func TestPipelineRunSendsInvalidAndUnknownPipelinesToTheRuntime(t *testing.T) {
	for name, detail := range map[string]*cerbapi.PipelineDetail{
		"invalid": {ValidationError: "resolve pipeline: missing resource"},
		"unknown": nil,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			client := fixturePipelineClient{detail: detail, runErr: errors.New("runtime refused"), calls: &calls}
			var out bytes.Buffer
			err := runPipelineCommand(context.Background(), client, "p", &out)
			if calls != 1 {
				t.Fatalf("the runtime was called %d times, want 1: the attempt must be recorded", calls)
			}
			wantHint := "hint: resolve pipeline: missing resource"
			if detail == nil {
				wantHint = `hint: pipeline "p" not found in config`
			}
			if err == nil || !strings.Contains(err.Error(), "runtime refused") || !strings.Contains(err.Error(), wantHint) || out.Len() != 0 {
				t.Fatalf("output = %q, error = %v", out.String(), err)
			}
		})
	}
}

func TestPipelineListAndShowOutput(t *testing.T) {
	var out bytes.Buffer
	if err := printPipelineList(&out, []cerbapi.PipelineInfo{{ID: "test", Name: "Test", Stages: 2, Description: "Checks"}}); err != nil {
		t.Fatal(err)
	}
	want := "ID    NAME  STAGES  DESCRIPTION\n--    ----  ------  -----------\ntest  Test  2       Checks\n"
	if out.String() != want {
		t.Fatalf("list = %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := printPipelineList(&out, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "No pipelines defined. Add pipelines to your config under the 'pipelines:' key.\n" {
		t.Fatalf("empty list = %q", out.String())
	}
	out.Reset()
	detail := &cerbapi.PipelineDetail{Definition: config.PipelineDef{ID: "test", Name: "Test"}, ValidationError: "no stages"}
	if err := printPipelineDetail(&out, detail); err != nil {
		t.Fatal(err)
	}
	want = "{\n  \"Description\": \"\",\n  \"ID\": \"test\",\n  \"Name\": \"Test\",\n  \"Stages\": null\n}\n"
	if out.String() != want {
		t.Fatalf("show = %q, want %q", out.String(), want)
	}
}

// pipeline run takes --approval; pipeline plan plans only.
func TestPipelineRunTakesAnApproval(t *testing.T) {
	run, _, err := rootCmd.Find([]string{"pipeline", "run"})
	if err != nil || run.Flags().Lookup("approval") == nil {
		t.Fatalf("pipeline run has no --approval: %v", err)
	}
	plan, _, err := rootCmd.Find([]string{"pipeline", "plan"})
	if err != nil || plan.Name() != "plan" || plan.Flags().Lookup("approval") != nil {
		t.Fatalf("pipeline plan: %v", err)
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
)

type fixturePipelineClient struct {
	detail *cerbapi.PipelineDetail
	raw    []byte
	runErr error
}

func (f fixturePipelineClient) ListPipelines(context.Context) ([]cerbapi.PipelineInfo, error) {
	return nil, nil
}
func (f fixturePipelineClient) GetPipeline(context.Context, string) (*cerbapi.PipelineDetail, error) {
	return f.detail, nil
}
func (f fixturePipelineClient) RunPipeline(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.PipelineRunResult, error) {
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

func TestPipelineRunRejectsInvalidDefinitionBeforeExecution(t *testing.T) {
	client := fixturePipelineClient{
		detail: &cerbapi.PipelineDetail{ValidationError: "resolve pipeline: missing resource"},
		runErr: errors.New("execution should not be reached"),
	}
	var out bytes.Buffer
	err := runPipelineCommand(context.Background(), client, "invalid", &out)
	if err == nil || err.Error() != client.detail.ValidationError || out.String() != "" {
		t.Fatalf("output = %q, error = %v", out.String(), err)
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

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
// The resource printers write to os.Stdout directly, not to the command's
// output writer.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	previous := os.Stdout
	os.Stdout = write

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(read)
		done <- string(data)
	}()

	runErr := fn()
	os.Stdout = previous
	_ = write.Close()
	out := <-done
	_ = read.Close()
	return out, runErr
}

func writeUnsupervisedConfig(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `version: 2
project:
  id: demo
  name: Demo
resources:
  - id: mtbf-monitor
    name: MTBF Monitor
    type: container
    connector: docker
    config:
      compose_file: /tmp/docker-compose.yml
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	previous := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = previous })
}

func TestResourceStatusExplainsAnUnsupervisedResource(t *testing.T) {
	writeUnsupervisedConfig(t)
	previousOutput := resourceStatusOutput
	resourceStatusOutput = outputFormatJSON
	t.Cleanup(func() { resourceStatusOutput = previousOutput })

	resourceStatusCmd.SetContext(context.Background())
	out, err := captureStdout(t, func() error {
		return resourceStatusCmd.RunE(resourceStatusCmd, []string{"mtbf-monitor"})
	})
	// The whole point: asking a reasonable question about a working resource
	// used to come back as an error.
	if err != nil {
		t.Fatalf("status errored on an unsupervised resource: %v", err)
	}

	var st struct {
		Status              string `json:"status"`
		RecommendedNextStep string `json:"recommended_next_step"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &st); jsonErr != nil {
		t.Fatalf("status did not emit JSON: %v\n%s", jsonErr, out)
	}
	if st.Status != "unsupervised" {
		t.Errorf("status = %q, want unsupervised", st.Status)
	}
	if !strings.Contains(st.RecommendedNextStep, "cerberus docker") {
		t.Errorf("next step does not name the docker commands: %q", st.RecommendedNextStep)
	}
}

func TestResourceDeployStillRefusesAnUnsupervisedResourceButNamesTheAlternative(t *testing.T) {
	writeUnsupervisedConfig(t)

	resourceDeployCmd.SetContext(context.Background())
	err := resourceDeployCmd.RunE(resourceDeployCmd, []string{"mtbf-monitor"})
	if err == nil {
		t.Fatal("deploy must still refuse a resource the supervision lane does not own")
	}
	if !strings.Contains(err.Error(), "cerberus docker") {
		t.Errorf("error does not name what does work: %v", err)
	}
}

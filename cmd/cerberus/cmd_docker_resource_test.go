package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const dockerResourceConfigYAML = `version: 2
project:
  id: demo
  name: Demo
resources:
  - id: mtbf-monitor
    name: MTBF Monitor
    type: container
    connector: docker
    config:
      compose_file: /Users/cburks/Projects/mtbf-monitor/docker-compose.yml
  - id: single
    type: container
    connector: docker
    config:
      container: nginx-router
  - id: muctlvaig
    type: server
    connector: ssh
    config:
      host: muctlvaig.corp.adtran.com
      user: cburks
`

// dockerTestCommand builds a command carrying the flags the real docker
// subcommands carry, against a config fixture.
func dockerTestCommand(t *testing.T, withFileFlag bool) *cobra.Command {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(dockerResourceConfigYAML), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	previous := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = previous })

	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	cmd.Flags().StringP("host", "H", "", "")
	cmd.Flags().String("context", "", "")
	if withFileFlag {
		cmd.Flags().StringP("file", "f", "", "")
	}
	return cmd
}

func TestDockerOperationConfigResolvesADeclaredComposeResource(t *testing.T) {
	cmd := dockerTestCommand(t, true)

	cfg, err := dockerOperationConfig(cmd, "mtbf-monitor")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// This is the point of declaring the resource: up by id, no -f.
	if cfg["compose_file"] != "/Users/cburks/Projects/mtbf-monitor/docker-compose.yml" {
		t.Fatalf("compose_file = %#v, want the declared path", cfg["compose_file"])
	}
	if cfg["id"] != "mtbf-monitor" || cfg["name"] != "MTBF Monitor" {
		t.Fatalf("identity not carried over: %#v", cfg)
	}
	// A stack has no single container, and inventing one would send `start`
	// down the container path instead of compose.
	if _, ok := cfg["container"]; ok {
		t.Fatalf("compose resource gained a container name: %#v", cfg["container"])
	}
}

func TestDockerOperationConfigResolvesADeclaredContainerResource(t *testing.T) {
	cmd := dockerTestCommand(t, true)

	cfg, err := dockerOperationConfig(cmd, "single")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg["container"] != "nginx-router" {
		t.Fatalf("container = %#v, want the declared name", cfg["container"])
	}
	if _, ok := cfg["compose_file"]; ok {
		t.Fatalf("container resource gained a compose file: %#v", cfg)
	}
}

func TestDockerOperationConfigFallsBackToALiteralContainerName(t *testing.T) {
	cmd := dockerTestCommand(t, false)

	// Most containers are never declared. `cerberus docker logs <container>`
	// has to keep working for them.
	cfg, err := dockerOperationConfig(cmd, "mtbf-monitor-app-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg["container"] != "mtbf-monitor-app-1" {
		t.Fatalf("container = %#v, want the literal argument", cfg["container"])
	}
}

func TestDockerOperationConfigRefusesAResourceFromAnotherConnector(t *testing.T) {
	cmd := dockerTestCommand(t, true)

	_, err := dockerOperationConfig(cmd, "muctlvaig")
	if err == nil {
		t.Fatal("expected a server/ssh resource to be refused by a docker command")
	}
	// Silently retrying it as a container named "muctlvaig" would fail later
	// and say nothing useful, so the error names what does operate it.
	if !strings.Contains(err.Error(), "cerberus ssh") {
		t.Errorf("error does not name the ssh commands: %v", err)
	}
}

func TestDockerOperationConfigLetsFlagsBeatTheDeclaration(t *testing.T) {
	cmd := dockerTestCommand(t, true)
	if err := cmd.Flags().Set("file", "/tmp/override-compose.yml"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("host", "ssh://cburks@muctlvaig"); err != nil {
		t.Fatal(err)
	}

	cfg, err := dockerOperationConfig(cmd, "mtbf-monitor")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg["compose_file"] != "/tmp/override-compose.yml" {
		t.Fatalf("compose_file = %#v, want the -f override", cfg["compose_file"])
	}
	if cfg["host"] != "ssh://cburks@muctlvaig" {
		t.Fatalf("host = %#v, want the --host override", cfg["host"])
	}
}

func TestDockerOperationConfigSurvivesAnUnreadableConfig(t *testing.T) {
	broken := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(broken, []byte("version: 2\nresources: [oh no\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := cfgPath
	cfgPath = broken
	t.Cleanup(func() { cfgPath = previous })

	cmd := &cobra.Command{}
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	cmd.Flags().StringP("host", "H", "", "")
	cmd.Flags().String("context", "", "")

	cfg, err := dockerOperationConfig(cmd, "some-container")
	if err != nil {
		t.Fatalf("an unreadable config must not break an undeclared container: %v", err)
	}
	if cfg["container"] != "some-container" {
		t.Fatalf("container = %#v, want the literal argument", cfg["container"])
	}
	if !strings.Contains(stderr.String(), "could not read config") {
		t.Errorf("the config problem was hidden rather than reported: %q", stderr.String())
	}
}

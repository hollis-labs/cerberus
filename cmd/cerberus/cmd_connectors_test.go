package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func TestPrintConnectorDefinitions(t *testing.T) {
	var out bytes.Buffer
	printConnectorDefinitions(&out, []contract.Definition{
		{ID: "docker", ResourceTypes: []string{"container"}, Operations: []contract.Operation{{Name: "start"}}},
		{ID: "github", ResourceTypes: []string{"repository"}},
	}, map[string]bool{"docker": true})

	got := out.String()
	for _, want := range []string{"ID", "docker", "container", "yes", "github", "repository", "no"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestConnectorsPrototypeCommandWritesDockerPrototype(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	connectorsPrototypeCmd.SetOut(&out)
	connectorsPrototypeCmd.SetErr(&out)
	connectorsPrototypeBuildBinary = false

	if err := connectorsPrototypeCmd.RunE(connectorsPrototypeCmd, []string{"docker", dir}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, pluginhost.PluginYAMLFilename))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "id: docker") {
		t.Fatalf("plugin.yaml = %s", string(data))
	}
}

func TestConnectorsPrototypeCommandWritesDockerPrototypeWithBinary(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	connectorsPrototypeCmd.SetOut(&out)
	connectorsPrototypeCmd.SetErr(&out)
	connectorsPrototypeBuildBinary = true
	defer func() { connectorsPrototypeBuildBinary = false }()

	if err := connectorsPrototypeCmd.RunE(connectorsPrototypeCmd, []string{"docker", dir}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "bin", "cerberus-docker-plugin"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("binary mode = %v, want executable", info.Mode())
	}
}

func TestConnectorsCommandUsesDaemonManagedPluginInventory(t *testing.T) {
	t.Setenv("GO_WANT_CONNECTORS_PLUGIN_HELPER", "1")
	pluginDir := helperPluginDir(t)
	startManagedPluginSocketServer(t)

	connectorsPluginDev = false
	connectorsPluginManagedInstallCmd.SetContext(context.Background())
	if err := connectorsPluginManagedInstallCmd.RunE(connectorsPluginManagedInstallCmd, []string{pluginDir}); err != nil {
		t.Fatalf("install RunE: %v", err)
	}
	connectorsPluginManagedLoadCmd.SetContext(context.Background())
	if err := connectorsPluginManagedLoadCmd.RunE(connectorsPluginManagedLoadCmd, []string{"docker"}); err != nil {
		t.Fatalf("load RunE: %v", err)
	}

	var out bytes.Buffer
	connectorsCmd.SetOut(&out)
	connectorsCmd.SetErr(&out)
	connectorsCmd.SetContext(context.Background())
	if err := connectorsCmd.RunE(connectorsCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	got := out.String()
	for _, want := range []string{"docker", "yes", "3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestConnectorDescriptionByID(t *testing.T) {
	desc, err := connectorDescriptionByID([]contract.Definition{
		{
			ID:            "cloudflare",
			ResourceTypes: []string{"domain"},
			Operations:    []contract.Operation{{Name: "create_dns_record", Destructive: true, SupportsDry: true}},
		},
	}, map[string]bool{"cloudflare": true}, "cloudflare")
	if err != nil {
		t.Fatalf("connectorDescriptionByID: %v", err)
	}
	if desc.ID != "cloudflare" || !desc.Live || len(desc.Operations) != 1 || !desc.Operations[0].SupportsDry {
		t.Fatalf("desc = %+v", desc)
	}
}

func TestConnectorsDescribeCommandUsesDaemonInventory(t *testing.T) {
	t.Setenv("GO_WANT_CONNECTORS_PLUGIN_HELPER", "1")
	pluginDir := helperPluginDir(t)
	startManagedPluginSocketServer(t)

	connectorsPluginDev = false
	connectorsPluginManagedInstallCmd.SetContext(context.Background())
	if err := connectorsPluginManagedInstallCmd.RunE(connectorsPluginManagedInstallCmd, []string{pluginDir}); err != nil {
		t.Fatalf("install RunE: %v", err)
	}
	connectorsPluginManagedLoadCmd.SetContext(context.Background())
	if err := connectorsPluginManagedLoadCmd.RunE(connectorsPluginManagedLoadCmd, []string{"docker"}); err != nil {
		t.Fatalf("load RunE: %v", err)
	}

	var out bytes.Buffer
	connectorsDescribeCmd.SetOut(&out)
	connectorsDescribeCmd.SetErr(&out)
	connectorsDescribeCmd.SetContext(context.Background())
	if err := connectorsDescribeCmd.RunE(connectorsDescribeCmd, []string{"docker"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	got := out.String()
	for _, want := range []string{`"id": "docker"`, `"live": true`, `"operations"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/spf13/cobra"
)

type failingCommandClient struct{ cerbapi.Client }

func (failingCommandClient) ExecuteConnectorOperation(context.Context, cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	return cerbapi.ExternalConnectorOperationResult{}, errors.New("daemon deliberately refused operation")
}

func TestConnectorCommandKeepsDaemonFailure(t *testing.T) {
	startCommandSocket(t, failingCommandClient{})
	client, closeFn, err := newExternalConnectorService(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	_, err = client.Execute(context.Background(), cerbapi.ExternalConnectorOperationArgs{Connector: "test-only", Operation: "mutate"})
	if err == nil || !strings.Contains(err.Error(), "daemon deliberately refused operation") {
		t.Fatalf("daemon failure was lost or retried locally: %v", err)
	}
}

func TestPipelineCommandExplicitConfigUsesSharedLocalService(t *testing.T) {
	startCommandSocket(t, failingCommandClient{})
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("version: 2\npipelines:\n  - id: standalone\n    name: Standalone\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := cfgPath
	cfgPath = configPath
	t.Cleanup(func() { cfgPath = previous })
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().String("config", "", "")
	if err := cmd.Flags().Set("config", configPath); err != nil {
		t.Fatal(err)
	}
	client, err := newPipelineClient(cmd)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := client.GetPipeline(context.Background(), "standalone")
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || detail.Definition.Name != "Standalone" {
		t.Fatalf("explicit config ignored: %#v", detail)
	}
}

func startCommandSocket(t *testing.T, client cerbapi.Client) {
	t.Helper()
	homeDir, err := os.MkdirTemp("/tmp", "cerbcmd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(homeDir) })
	t.Setenv("HOME", homeDir)
	socketPath, err := cerbapi.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cerbapi.NewSocketServer(client, socketPath).Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := cerbapi.NewSocketClient(socketPath).Ping(context.Background()); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test socket did not become ready")
}

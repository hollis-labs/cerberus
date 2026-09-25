package cerbapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/connector"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// sshSocket serves an in-process client over a real socket, with the SSH
// connector on a recording backend and server-1 configured.
func sshSocket(t *testing.T) (*SocketClient, *fakeSSHBackend) {
	t.Helper()
	backend := &fakeSSHBackend{}
	registry := connector.NewRegistry()
	registry.Register(sshconn.NewWithBackendFactory(nil, func() sshconn.Backend { return backend }))
	svc := NewExternalConnectorService(registry)
	svc.SetResourceLookup(sshTestLookup())
	return startConnectorSocket(t, NewInProcessClient(WithExternalConnectorService(svc))), backend
}

// Every ssh verb works over the socket by id alone, and reaches the host the
// resource configures.
func TestSSHVerbsOverSocketByID(t *testing.T) {
	for _, tc := range []struct {
		operation string
		config    map[string]any
		check     func(*fakeSSHBackend) bool
	}{
		{"exec", map[string]any{"command": "uptime"}, func(b *fakeSSHBackend) bool { return b.command == "uptime" }},
		{"status", nil, func(b *fakeSSHBackend) bool { return b.connects > 0 }},
		{"stop", nil, func(b *fakeSSHBackend) bool { return b.command != "" }},
		{"put", map[string]any{"local_path": "/tmp/a", "remote_path": "/srv/a"}, func(b *fakeSSHBackend) bool { return b.putCalls == 1 }},
		{"get", map[string]any{"remote_path": "/srv/a", "local_path": "/tmp/a"}, func(b *fakeSSHBackend) bool { return b.getCalls == 1 }},
		{"put_dir", map[string]any{"local_path": "/tmp/d", "remote_path": "/srv/d"}, func(b *fakeSSHBackend) bool { return b.putDirCalls == 1 }},
		{"get_dir", map[string]any{"remote_path": "/srv/d", "local_path": "/tmp/d"}, func(b *fakeSSHBackend) bool { return b.getDirCalls == 1 }},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			client, backend := sshSocket(t)
			cfg := map[string]any{"id": "server-1"}
			for k, v := range tc.config {
				cfg[k] = v
			}
			_, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
				Connector: "ssh", Operation: tc.operation, Config: cfg, Acknowledged: true,
			})
			if err != nil {
				t.Fatalf("%s by id over the socket: %v", tc.operation, err)
			}
			if !tc.check(backend) {
				t.Fatalf("%s did not reach the backend: %+v", tc.operation, backend)
			}
			if backend.connectHost != "127.0.0.1" || backend.connectKey != "/tmp/fake-key" {
				t.Fatalf("connected to %q with key %q, want the configured 127.0.0.1 and /tmp/fake-key", backend.connectHost, backend.connectKey)
			}
		})
	}
}

// A dry-run preview names the target the resource configures.
func TestSSHDryRunPreviewShowsConfiguredTarget(t *testing.T) {
	client, backend := sshSocket(t)
	result, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "ssh", Operation: "exec", DryRun: true,
		Config: map[string]any{"id": "server-1", "command": "uptime"},
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	data, _ := redact.Marshal(result.Data)
	if !strings.Contains(string(data), "127.0.0.1") {
		t.Fatalf("preview %s does not name the configured host", data)
	}
	if backend.connects != 0 {
		t.Fatal("dry run connected")
	}
}

// Connection settings are refused over the socket, by name, and never reach
// the backend. Each field is refused on its own, not only in company.
func TestSSHOverridesRefusedOverSocket(t *testing.T) {
	for _, field := range []struct {
		key   string
		value any
	}{
		{"host", "evil.example"},
		{"port", 2222},
		{"user", "root"},
		{"key_file", "/Users/me/.ssh/id_ed25519"},
		{"known_hosts_file", "/dev/null"},
		{"allow_insecure_host_key", true},
		{"name", "anything"},
	} {
		t.Run(field.key, func(t *testing.T) {
			client, backend := sshSocket(t)
			_, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
				Connector: "ssh", Operation: "exec", Acknowledged: true,
				Config: map[string]any{"id": "server-1", "command": "uptime", field.key: field.value},
			})
			if err == nil || !strings.Contains(err.Error(), "refusing fields ("+field.key+")") || !strings.Contains(err.Error(), "configured resource id") {
				t.Fatalf("err = %v, want a refusal naming %s", err, field.key)
			}
			if backend.connects != 0 || backend.command != "" {
				t.Fatalf("refused request reached the backend: %+v", backend)
			}
		})
	}
}

func TestSSHRequiresConfiguredSSHResource(t *testing.T) {
	client, _ := sshSocket(t)
	for _, tc := range []struct {
		cfg  map[string]any
		want string
	}{
		{map[string]any{"command": "uptime"}, "missing required fields (id)"},
		{map[string]any{"id": "nope", "command": "uptime"}, `resource "nope" not found`},
	} {
		_, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
			Connector: "ssh", Operation: "exec", Acknowledged: true, Config: tc.cfg,
		})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("cfg %v: err = %v, want %q", tc.cfg, err, tc.want)
		}
	}
}

func TestSSHRefusalsSurviveRedaction(t *testing.T) {
	svc := NewExternalConnectorService(connector.NewRegistry())
	svc.SetResourceLookup(sshTestLookup())
	for _, cfg := range []map[string]any{
		{"id": "server-1", "host": "h", "key_file": "/Users/me/.ssh/id_ed25519", "known_hosts_file": "/Users/me/.ssh/known_hosts", "allow_insecure_host_key": true},
		{},
		{"id": "nope"},
	} {
		args := ExternalConnectorOperationArgs{Connector: "ssh", Operation: "exec", Config: cfg}
		err := checkFromSurface(SurfaceInProcess, args)
		if err == nil {
			_, err = svc.resolveSSHTarget(args)
		}
		var connErr *ExternalConnectorError
		if !errors.As(err, &connErr) {
			t.Fatalf("not a connector error: %v", err)
		}
		raw := connErr.Connector + " " + connErr.Operation + ": " + string(connErr.Code) + ": " + connErr.Err.Error()
		if got := err.Error(); got != raw {
			t.Errorf("redaction changed the refusal:\n got %q\nwant %q", got, raw)
		}
	}

	// The configured key and known_hosts paths travel through the redactor
	// in previews and results; they are paths, not credentials, and must
	// survive intact.
	data, err := redact.Marshal(map[string]any{"key_file": "/Users/me/.ssh/id_ed25519", "known_hosts_file": "/Users/me/.ssh/known_hosts"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/Users/me/.ssh/id_ed25519", "/Users/me/.ssh/known_hosts"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("redact.Marshal ate %s: %s", want, data)
		}
	}
}

package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/connector"
	cfconn "github.com/chrispian/cerberus/internal/connector/cloudflare"
	dockerconn "github.com/chrispian/cerberus/internal/connector/docker"
	forgeconn "github.com/chrispian/cerberus/internal/connector/forge"
	ghconn "github.com/chrispian/cerberus/internal/connector/github"
	ncconn "github.com/chrispian/cerberus/internal/connector/namecheap"
	sshconn "github.com/chrispian/cerberus/internal/connector/ssh"
)

type fakeDockerBackend struct {
	started  string
	logName  string
	logLines int
}

func (b *fakeDockerBackend) ListContainers(_ context.Context) ([]dockerconn.Container, error) {
	return []dockerconn.Container{{ID: "abc", Name: "web", State: "running"}}, nil
}

func (b *fakeDockerBackend) ContainerStatus(_ context.Context, _ string) (*dockerconn.Container, error) {
	return &dockerconn.Container{State: "running"}, nil
}

func (b *fakeDockerBackend) StartContainer(_ context.Context, nameOrID string) error {
	b.started = nameOrID
	return nil
}

func (b *fakeDockerBackend) StopContainer(_ context.Context, _ string) error {
	return nil
}

func (b *fakeDockerBackend) RemoveContainer(_ context.Context, _ string) error {
	return nil
}

func (b *fakeDockerBackend) ContainerLogs(_ context.Context, nameOrID string, lines int) (string, error) {
	b.logName = nameOrID
	b.logLines = lines
	return "logs", nil
}

func (b *fakeDockerBackend) ComposeUp(_ context.Context, _ string) error {
	return nil
}

func (b *fakeDockerBackend) ComposeDown(_ context.Context, _ string) error {
	return nil
}

func (b *fakeDockerBackend) ComposePS(_ context.Context, _ string) (*dockerconn.ComposeStack, error) {
	return nil, nil
}

type fakeGitHubBackend struct {
	owner string
	repo  string
	limit int
}

type fakeCloudflareBackend struct {
	zoneID string
}

func (b *fakeCloudflareBackend) ListZones(_ context.Context) ([]cfconn.Zone, error) {
	return []cfconn.Zone{{ID: "zone-1", Name: "example.com", Status: "active"}}, nil
}

func (b *fakeCloudflareBackend) ListDNSRecords(_ context.Context, zoneID string) ([]cfconn.DNSRecord, error) {
	b.zoneID = zoneID
	return []cfconn.DNSRecord{{ID: "dns-1", Type: "A", Name: "www", Content: "1.2.3.4"}}, nil
}

func (b *fakeCloudflareBackend) CreateDNSRecord(_ context.Context, _ string, rec cfconn.DNSRecord) (*cfconn.DNSRecord, error) {
	return &rec, nil
}

func (b *fakeCloudflareBackend) DeleteDNSRecord(_ context.Context, _, _ string) error {
	return nil
}

func (b *fakeCloudflareBackend) ListTunnels(_ context.Context, _ string) ([]cfconn.Tunnel, error) {
	return nil, nil
}

type fakeNamecheapBackend struct {
	domain string
}

func (b *fakeNamecheapBackend) ListDomains(_ context.Context) ([]ncconn.Domain, error) {
	return []ncconn.Domain{{Name: "example.com"}}, nil
}

func (b *fakeNamecheapBackend) GetDomainStatus(_ context.Context, domain string) (*ncconn.DomainStatus, error) {
	b.domain = domain
	return &ncconn.DomainStatus{Domain: domain, Registered: true}, nil
}

func (b *fakeNamecheapBackend) ListDNSRecords(_ context.Context, sld, tld string) ([]ncconn.DNSRecord, error) {
	b.domain = sld + "." + tld
	return []ncconn.DNSRecord{{ID: 1, Type: "A", Host: "@", Value: "1.2.3.4"}}, nil
}

func (b *fakeNamecheapBackend) SetDNSRecords(_ context.Context, sld, tld string, _ []ncconn.DNSRecord) error {
	b.domain = sld + "." + tld
	return nil
}

type fakeForgeBackend struct {
	serverID int
	siteID   int
	command  string
}

func (b *fakeForgeBackend) ListServers(_ context.Context) ([]forgeconn.Server, error) {
	return []forgeconn.Server{{ID: 1, Name: "prod"}}, nil
}

func (b *fakeForgeBackend) GetServer(_ context.Context, serverID int) (*forgeconn.Server, error) {
	b.serverID = serverID
	return &forgeconn.Server{ID: serverID, Name: "prod", IsReady: true}, nil
}

func (b *fakeForgeBackend) ListSites(_ context.Context, serverID int) ([]forgeconn.Site, error) {
	b.serverID = serverID
	return []forgeconn.Site{{ID: 2, ServerID: serverID, Name: "app"}}, nil
}

func (b *fakeForgeBackend) GetDeploymentScript(_ context.Context, serverID, siteID int) (string, error) {
	b.serverID = serverID
	b.siteID = siteID
	return "deploy.sh", nil
}

func (b *fakeForgeBackend) UpdateDeploymentScript(_ context.Context, serverID, siteID int, _ string, _ bool) error {
	b.serverID = serverID
	b.siteID = siteID
	return nil
}

func (b *fakeForgeBackend) DeploySite(_ context.Context, serverID, siteID int) error {
	b.serverID = serverID
	b.siteID = siteID
	return nil
}

func (b *fakeForgeBackend) ExecuteSiteCommand(_ context.Context, serverID, siteID int, command string) (*forgeconn.SiteCommand, error) {
	b.serverID = serverID
	b.siteID = siteID
	b.command = command
	return &forgeconn.SiteCommand{ID: 7, ServerID: serverID, SiteID: siteID, Command: command, Status: "running"}, nil
}

type fakeSSHBackend struct {
	command string
}

func (b *fakeSSHBackend) Connect(_ context.Context, _ string, _ int, _ string, _ string) error {
	return nil
}

func (b *fakeSSHBackend) Exec(_ context.Context, command string) (*sshconn.ExecResult, error) {
	b.command = command
	return &sshconn.ExecResult{Stdout: "ok", ExitCode: 0}, nil
}

func (b *fakeSSHBackend) Ping(_ context.Context) error {
	return nil
}

func (b *fakeSSHBackend) Close() error {
	return nil
}

func (b *fakeGitHubBackend) RepoStatus(_ context.Context, owner, repo string) (*ghconn.RepoStatus, error) {
	b.owner = owner
	b.repo = repo
	return &ghconn.RepoStatus{Owner: owner, Repo: repo}, nil
}

func (b *fakeGitHubBackend) ListReleases(_ context.Context, owner, repo string, limit int) ([]ghconn.Release, error) {
	b.owner = owner
	b.repo = repo
	b.limit = limit
	return []ghconn.Release{{TagName: "v1"}}, nil
}

func (b *fakeGitHubBackend) ListWorkflowRuns(_ context.Context, _ string, _ string, _ int) ([]ghconn.WorkflowRun, error) {
	return nil, nil
}

func TestExternalConnectorServiceExecutesDockerOperation(t *testing.T) {
	backend := &fakeDockerBackend{}
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker",
		Operation: "logs",
		Config:    map[string]any{"container": "web", "lines": 12},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Data != "logs" {
		t.Fatalf("Data = %#v, want logs", result.Data)
	}
	if backend.logName != "web" || backend.logLines != 12 {
		t.Fatalf("logs called with %q/%d", backend.logName, backend.logLines)
	}
}

func TestExternalConnectorServiceExecutesGitHubOperation(t *testing.T) {
	backend := &fakeGitHubBackend{}
	registry := connector.NewRegistry()
	registry.Register(ghconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "github",
		Operation: "list_releases",
		Config:    map[string]any{"owner": "hollis-labs", "repo": "cerberus", "limit": 3},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	releases, ok := result.Data.([]ghconn.Release)
	if !ok || len(releases) != 1 || releases[0].TagName != "v1" {
		t.Fatalf("Data = %#v, want release slice", result.Data)
	}
	if backend.owner != "hollis-labs" || backend.repo != "cerberus" || backend.limit != 3 {
		t.Fatalf("backend called with %q/%q/%d", backend.owner, backend.repo, backend.limit)
	}
}

func TestExternalConnectorServiceExecutesCloudflareOperation(t *testing.T) {
	backend := &fakeCloudflareBackend{}
	registry := connector.NewRegistry()
	registry.Register(cfconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "cloudflare",
		Operation: "list_dns_records",
		Config:    map[string]any{"zone_id": "zone-1"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	records, ok := result.Data.([]cfconn.DNSRecord)
	if !ok || len(records) != 1 || records[0].ID != "dns-1" {
		t.Fatalf("Data = %#v, want cloudflare record slice", result.Data)
	}
	if backend.zoneID != "zone-1" {
		t.Fatalf("zoneID = %q, want zone-1", backend.zoneID)
	}
}

func TestExternalConnectorServiceRequiresAcknowledgmentForDestructiveOperation(t *testing.T) {
	backend := &fakeCloudflareBackend{}
	registry := connector.NewRegistry()
	registry.Register(cfconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(registry)

	_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "cloudflare",
		Operation: "create_dns_record",
		Config: map[string]any{
			"zone_id": "zone-1",
			"type":    "A",
			"name":    "www",
			"content": "1.2.3.4",
		},
	})
	var connErr *ExternalConnectorError
	if !errors.As(err, &connErr) {
		t.Fatalf("err = %T, want ExternalConnectorError", err)
	}
	if connErr.Code != ExternalConnectorAckRequired {
		t.Fatalf("Code = %q, want %q", connErr.Code, ExternalConnectorAckRequired)
	}
}

func TestExternalConnectorServiceDryRunPreviewBypassesAcknowledgment(t *testing.T) {
	registry := connector.NewRegistry()
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "cloudflare",
		Operation: "create_dns_record",
		DryRun:    true,
		Config: map[string]any{
			"zone_id": "zone-1",
			"type":    "A",
			"name":    "www",
			"content": "1.2.3.4",
			"ttl":     300,
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	preview, ok := result.Data.(ExternalConnectorDryRunPreview)
	if !ok {
		t.Fatalf("Data = %T, want ExternalConnectorDryRunPreview", result.Data)
	}
	if !preview.DryRun || preview.Target["zone_id"] != "zone-1" || preview.Input["ttl"] != 300 {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestExternalConnectorServiceNamecheapDryRunWarnsAboutSetHostsRewrite(t *testing.T) {
	registry := connector.NewRegistry()
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "namecheap",
		Operation: "delete_dns_record",
		DryRun:    true,
		Config: map[string]any{
			"domain":    "example.com",
			"record_id": 42,
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	preview, ok := result.Data.(ExternalConnectorDryRunPreview)
	if !ok {
		t.Fatalf("Data = %T, want ExternalConnectorDryRunPreview", result.Data)
	}
	if len(preview.Warnings) == 0 || preview.Warnings[0] == "" {
		t.Fatalf("preview warnings = %#v, want rewrite warning", preview.Warnings)
	}
}

func TestExternalConnectorServiceExecutesNamecheapOperation(t *testing.T) {
	backend := &fakeNamecheapBackend{}
	registry := connector.NewRegistry()
	registry.Register(ncconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "namecheap",
		Operation: "get_domain_status",
		Config:    map[string]any{"domain": "example.com"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	status, ok := result.Data.(*ncconn.DomainStatus)
	if !ok || status.Domain != "example.com" || !status.Registered {
		t.Fatalf("Data = %#v, want namecheap domain status", result.Data)
	}
	if backend.domain != "example.com" {
		t.Fatalf("domain = %q, want example.com", backend.domain)
	}
}

func TestExternalConnectorServiceExecutesForgeOperation(t *testing.T) {
	backend := &fakeForgeBackend{}
	registry := connector.NewRegistry()
	registry.Register(forgeconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector:    "forge",
		Operation:    "exec_site_command",
		Acknowledged: true,
		Config: map[string]any{
			"server_id": 1,
			"site_id":   2,
			"command":   "php artisan migrate",
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	command, ok := result.Data.(*forgeconn.SiteCommand)
	if !ok || command.Command != "php artisan migrate" {
		t.Fatalf("Data = %#v, want forge command", result.Data)
	}
	if backend.serverID != 1 || backend.siteID != 2 || backend.command != "php artisan migrate" {
		t.Fatalf("backend = %+v", backend)
	}
}

func TestExternalConnectorServiceExecutesSSHOperation(t *testing.T) {
	backend := &fakeSSHBackend{}
	registry := connector.NewRegistry()
	registry.Register(sshconn.NewWithBackendFactory(nil, func() sshconn.Backend { return backend }))
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector:    "ssh",
		Operation:    "exec",
		Acknowledged: true,
		Config: map[string]any{
			"id":       "server-1",
			"host":     "127.0.0.1",
			"user":     "root",
			"key_file": "/tmp/fake-key",
			"command":  "uptime",
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	exec, ok := result.Data.(*sshconn.ExecResult)
	if !ok || exec.Stdout != "ok" {
		t.Fatalf("Data = %#v, want ssh exec result", result.Data)
	}
	if backend.command != "uptime" {
		t.Fatalf("command = %q, want uptime", backend.command)
	}
}

func TestExternalConnectorServiceUnavailableConnectorReturnsStructuredError(t *testing.T) {
	registry := connector.NewRegistry()
	registry.RegisterUnavailable("github", errors.New("missing token"))
	svc := NewExternalConnectorService(registry)

	_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{Connector: "github", Operation: "status"})
	var connErr *ExternalConnectorError
	if !errors.As(err, &connErr) {
		t.Fatalf("err = %T, want ExternalConnectorError", err)
	}
	if connErr.Code != ExternalConnectorCredentialMissing {
		t.Fatalf("Code = %q, want %q", connErr.Code, ExternalConnectorCredentialMissing)
	}
}

func TestInProcessClientListsExternalConnectorDefinitions(t *testing.T) {
	registry := connector.NewRegistry()
	registry.RegisterDefinition(dockerconn.Definition())
	client := NewInProcessClient(WithExternalConnectorService(NewExternalConnectorService(registry)))

	defs, err := client.ListConnectors(context.Background())
	if err != nil {
		t.Fatalf("ListConnectors: %v", err)
	}
	if len(defs) != 1 || defs[0].ID != "docker" {
		t.Fatalf("defs = %#v, want docker definition", defs)
	}
}

func TestSocketClientExecutesConnectorOperation(t *testing.T) {
	backend := &fakeDockerBackend{}
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(backend))
	client := NewInProcessClient(WithExternalConnectorService(NewExternalConnectorService(registry)))

	socketClient := startConnectorSocket(t, client)
	result, err := socketClient.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker",
		Operation: "logs",
		Config:    map[string]any{"container": "web", "lines": 7},
	})
	if err != nil {
		t.Fatalf("ExecuteConnectorOperation: %v", err)
	}
	if result.Data != "logs" {
		t.Fatalf("Data = %#v, want logs", result.Data)
	}
	if backend.logName != "web" || backend.logLines != 7 {
		t.Fatalf("logs called with %q/%d", backend.logName, backend.logLines)
	}
}

func TestExternalConnectorServiceIncludesInstalledManagedPluginDefinitions(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	managed := mustManagedPluginService(t, "")
	pluginDir := helperPluginDir(t)

	if _, err := managed.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: pluginDir,
		Trust: PluginConnectorTrustOptions{
			CatalogSigned: true,
			ArchiveSigned: true,
			ArchiveSHA256: "abc",
		},
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	svc := NewExternalConnectorService(connector.NewRegistry(), managed)
	defs := svc.Definitions()
	if len(defs) != 1 || defs[0].ID != "docker" {
		t.Fatalf("Definitions = %#v", defs)
	}
	if live := svc.LiveDefinitions(); len(live) != 0 {
		t.Fatalf("LiveDefinitions before load = %#v, want none", live)
	}

	if _, err := managed.Load(context.Background(), "docker"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	live := svc.LiveDefinitions()
	if len(live) != 1 || live[0].ID != "docker" {
		t.Fatalf("LiveDefinitions after load = %#v", live)
	}
}

func TestSocketClientExecutesManagedPluginThroughConnectorAPI(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	managed := mustManagedPluginService(t, "")
	pluginDir := helperPluginDir(t)

	if _, err := managed.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: pluginDir,
		Trust: PluginConnectorTrustOptions{
			CatalogSigned: true,
			ArchiveSigned: true,
			ArchiveSHA256: "abc",
		},
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := managed.Load(context.Background(), "docker"); err != nil {
		t.Fatalf("Load: %v", err)
	}

	client := NewInProcessClient(WithExternalConnectorService(NewExternalConnectorService(connector.NewRegistry(), managed)))
	socketClient := startConnectorSocket(t, client)

	result, err := socketClient.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker",
		Operation: "logs",
		Config:    map[string]any{"container": "web"},
	})
	if err != nil {
		t.Fatalf("ExecuteConnectorOperation: %v", err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["tool"] != "cerberus_docker_logs" {
		t.Fatalf("result = %#v", result)
	}
}

type pingBypassClient struct {
	Client
}

func (c pingBypassClient) Health(context.Context, string) (*DaemonHealth, error) {
	return nil, errors.New("health should not be called by ping")
}

func TestSocketClientPingBypassesHealthWork(t *testing.T) {
	socketClient := startConnectorSocket(t, pingBypassClient{
		Client: NewInProcessClient(WithExternalConnectorService(NewExternalConnectorService(connector.NewRegistry()))),
	})

	if err := socketClient.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func startConnectorSocket(t *testing.T, client Client) *SocketClient {
	t.Helper()
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("cerb-connector-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socketPath) })

	server := NewSocketServer(client, socketPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("socket server: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("socket %s never appeared", socketPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return NewSocketClient(socketPath)
}

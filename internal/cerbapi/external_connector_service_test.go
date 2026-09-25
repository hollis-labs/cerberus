package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/connector"
	cfconn "github.com/hollis-labs/cerberus/internal/connector/cloudflare"
	doconn "github.com/hollis-labs/cerberus/internal/connector/digitalocean"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	forgeconn "github.com/hollis-labs/cerberus/internal/connector/forge"
	ghconn "github.com/hollis-labs/cerberus/internal/connector/github"
	ncconn "github.com/hollis-labs/cerberus/internal/connector/namecheap"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"gopkg.in/yaml.v3"
)

type fakeDockerBackend struct {
	started  string
	logName  string
	logLines int
	target   dockerconn.Target
	upFile   string
	stopFile string
	lists    int
}

func (b *fakeDockerBackend) WithTarget(target dockerconn.Target) dockerconn.Backend {
	b.target = target
	return b
}

func (b *fakeDockerBackend) ListContainers(_ context.Context) ([]dockerconn.Container, error) {
	b.lists++
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

func (b *fakeDockerBackend) ComposeUp(_ context.Context, composeFile string) error {
	b.upFile = composeFile
	return nil
}

func (b *fakeDockerBackend) ComposeStop(_ context.Context, composeFile string) error {
	b.stopFile = composeFile
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

type fakeDigitalOceanBackend struct {
	dropletID int
	created   *godo.Droplet
}

func (b *fakeDigitalOceanBackend) CreateDroplet(_ context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, error) {
	b.created = &godo.Droplet{
		ID:     42,
		Name:   req.Name,
		Status: "new",
		Region: &godo.Region{Slug: req.Region},
		Size:   &godo.Size{Slug: req.Size},
		Image:  &godo.Image{Slug: req.Image.Slug},
	}
	return b.created, nil
}

func (b *fakeDigitalOceanBackend) PowerOnDroplet(_ context.Context, id int) error {
	b.dropletID = id
	return nil
}

func (b *fakeDigitalOceanBackend) PowerOffDroplet(_ context.Context, id int) error {
	b.dropletID = id
	return nil
}

func (b *fakeDigitalOceanBackend) DeleteDroplet(_ context.Context, id int) error {
	b.dropletID = id
	return nil
}

func (b *fakeDigitalOceanBackend) GetDroplet(_ context.Context, id int) (*godo.Droplet, error) {
	b.dropletID = id
	return &godo.Droplet{ID: id, Name: "web", Status: "active"}, nil
}

func (b *fakeDigitalOceanBackend) ListDroplets(_ context.Context) ([]godo.Droplet, error) {
	return []godo.Droplet{{ID: 7, Name: "api", Status: "active"}}, nil
}

type fakeCloudflareBackend struct {
	zoneID    string
	accountID string
	zoneName  string
	zoneType  string
}

func (b *fakeCloudflareBackend) ListZones(_ context.Context) ([]cfconn.Zone, error) {
	return []cfconn.Zone{{ID: "zone-1", Name: "example.com", Status: "active"}}, nil
}

func (b *fakeCloudflareBackend) CreateZone(_ context.Context, accountID, name, zoneType string) (*cfconn.Zone, error) {
	b.accountID = accountID
	b.zoneName = name
	b.zoneType = zoneType
	return &cfconn.Zone{ID: "zone-2", Name: name, Status: "pending", NameServers: []string{"ns1.cloudflare.com", "ns2.cloudflare.com"}}, nil
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
	recordSet   *ncconn.DNSRecordSet
	domain      string
	nameservers []string
}

func (b *fakeNamecheapBackend) ListDomains(_ context.Context) ([]ncconn.Domain, error) {
	return []ncconn.Domain{{Name: "example.com"}}, nil
}

func (b *fakeNamecheapBackend) GetDomainStatus(_ context.Context, domain string) (*ncconn.DomainStatus, error) {
	b.domain = domain
	return &ncconn.DomainStatus{Domain: domain, Registered: true}, nil
}

func (b *fakeNamecheapBackend) GetDNSRecordSet(_ context.Context, sld, tld string) (*ncconn.DNSRecordSet, error) {
	b.domain = sld + "." + tld
	return &ncconn.DNSRecordSet{EmailType: "MX", Records: []ncconn.DNSRecord{{ID: 1, Type: "A", Host: "@", Value: "1.2.3.4"}}}, nil
}

func (b *fakeNamecheapBackend) SetDNSRecordSet(_ context.Context, sld, tld string, set ncconn.DNSRecordSet) error {
	b.recordSet = &set
	b.domain = sld + "." + tld
	return nil
}

func (b *fakeNamecheapBackend) SetCustomNameservers(_ context.Context, domain string, nameservers []string) (*ncconn.DomainNameserverUpdate, error) {
	b.domain = domain
	b.nameservers = append([]string(nil), nameservers...)
	return &ncconn.DomainNameserverUpdate{Domain: domain, Updated: true, NameServers: nameservers}, nil
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
	command     string
	localPath   string
	remotePath  string
	putCalls    int
	getCalls    int
	putDirCalls int
	getDirCalls int
	connects    int
	connectHost string
	connectKey  string
}

func (b *fakeSSHBackend) Connect(_ context.Context, host string, _ int, _ string, keyFile string, _ sshconn.HostKeyConfig) error {
	b.connects++
	b.connectHost = host
	b.connectKey = keyFile
	return nil
}

func (b *fakeSSHBackend) Exec(_ context.Context, command string) (*sshconn.ExecResult, error) {
	b.command = command
	return &sshconn.ExecResult{Stdout: "ok", ExitCode: 0}, nil
}

func (b *fakeSSHBackend) Put(_ context.Context, localPath, remotePath string) (int64, error) {
	b.putCalls++
	b.localPath, b.remotePath = localPath, remotePath
	return 42, nil
}

func (b *fakeSSHBackend) Get(_ context.Context, remotePath, localPath string) (int64, error) {
	b.getCalls++
	b.localPath, b.remotePath = localPath, remotePath
	return 7, nil
}

func (b *fakeSSHBackend) PutDir(_ context.Context, localDir, remoteDir string) (*sshconn.DirTransferResult, error) {
	b.putDirCalls++
	b.localPath, b.remotePath = localDir, remoteDir
	return &sshconn.DirTransferResult{Direction: "upload", LocalPath: localDir, RemotePath: remoteDir, Files: 2, Dirs: 1, Bytes: 42}, nil
}

func (b *fakeSSHBackend) GetDir(_ context.Context, remoteDir, localDir string) (*sshconn.DirTransferResult, error) {
	b.getDirCalls++
	b.localPath, b.remotePath = localDir, remoteDir
	return &sshconn.DirTransferResult{Direction: "download", LocalPath: localDir, RemotePath: remoteDir, Files: 3, Dirs: 2, Bytes: 7}, nil
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

func TestExternalConnectorServiceRoutesDockerOperationsToTheRequestedHost(t *testing.T) {
	backend := &fakeDockerBackend{}
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(registry)

	if _, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker",
		Operation: "list_containers",
		Config:    map[string]any{"host": "ssh://cburks@muctlvaig"},
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if backend.target.Host != "ssh://cburks@muctlvaig" {
		t.Fatalf("backend target = %#v, want the requested host", backend.target)
	}

	// Resolution is per call: the next operation must not inherit the last
	// one's host just because the connector instance is shared.
	backend.target = dockerconn.Target{}
	if _, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker",
		Operation: "list_containers",
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !backend.target.IsZero() {
		t.Fatalf("a later call inherited %#v", backend.target)
	}
}

func TestExternalConnectorServiceRejectsDockerHostAndContextTogether(t *testing.T) {
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(&fakeDockerBackend{}))
	svc := NewExternalConnectorService(registry)

	_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker",
		Operation: "list_containers",
		Config:    map[string]any{"host": "ssh://muctlvaig", "context": "azure-dev"},
	})
	if err == nil {
		t.Fatal("expected host and context together to be rejected")
	}
	var opErr *ExternalConnectorError
	if !errors.As(err, &opErr) || opErr.Code != ExternalConnectorInvalidArgs {
		t.Fatalf("err = %v, want %s", err, ExternalConnectorInvalidArgs)
	}
}

func TestExternalConnectorServiceExecutesDigitalOceanOperation(t *testing.T) {
	backend := &fakeDigitalOceanBackend{}
	registry := connector.NewRegistry()
	registry.Register(doconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "digitalocean",
		Operation: "get_droplet",
		Config:    map[string]any{"droplet_id": 42},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	droplet, ok := result.Data.(*doconn.DropletStatus)
	if !ok {
		t.Fatalf("Data type = %T, want *digitalocean.DropletStatus", result.Data)
	}
	if droplet.ID != 42 || backend.dropletID != 42 {
		t.Fatalf("droplet = %#v backend=%d", droplet, backend.dropletID)
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

func TestExternalConnectorServiceExecutesCloudflareZoneCreate(t *testing.T) {
	backend := &fakeCloudflareBackend{}
	registry := connector.NewRegistry()
	registry.Register(cfconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector:    "cloudflare",
		Operation:    "create_zone",
		Acknowledged: true,
		Config: map[string]any{
			"account_id": "acct-1",
			"name":       "chrispian.dev",
			"type":       cfconn.ZoneTypeFull,
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	zone, ok := result.Data.(*cfconn.Zone)
	if !ok || zone.ID != "zone-2" || zone.Name != "chrispian.dev" {
		t.Fatalf("Data = %#v, want created cloudflare zone", result.Data)
	}
	if backend.accountID != "acct-1" || backend.zoneName != "chrispian.dev" || backend.zoneType != cfconn.ZoneTypeFull {
		t.Fatalf("backend = %+v", backend)
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

func TestExternalConnectorServiceCloudflareZoneCreateDryRun(t *testing.T) {
	registry := connector.NewRegistry()
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "cloudflare",
		Operation: "create_zone",
		DryRun:    true,
		Config: map[string]any{
			"account_id": "acct-1",
			"name":       "chrispian.dev",
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	preview, ok := result.Data.(ExternalConnectorDryRunPreview)
	if !ok {
		t.Fatalf("Data = %T, want ExternalConnectorDryRunPreview", result.Data)
	}
	if !preview.DryRun || preview.Target["account_id"] != "acct-1" || preview.Target["name"] != "chrispian.dev" {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.Input["type"] != cfconn.ZoneTypeFull {
		t.Fatalf("preview input = %#v, want full zone type", preview.Input)
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

func TestNamecheapPerRecordWritesRefusedWithoutCredentialsIncludingDryRun(t *testing.T) {
	svc := NewExternalConnectorService(nil)
	for _, op := range []string{"create_dns_record", "delete_dns_record"} {
		for _, dryRun := range []bool{true, false} {
			_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{Connector: "namecheap", Operation: op, DryRun: dryRun, Acknowledged: true, Config: map[string]any{"domain": "example.com"}})
			if !errors.Is(err, ncconn.ErrUnsafePerRecordWrite) {
				t.Fatalf("expected disabled operation before credential lookup: %v", err)
			}
		}
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

func TestExternalConnectorServiceExecutesNamecheapNameserverChange(t *testing.T) {
	backend := &fakeNamecheapBackend{}
	registry := connector.NewRegistry()
	registry.Register(ncconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(registry)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector:    "namecheap",
		Operation:    "set_custom_nameservers",
		Acknowledged: true,
		Config: map[string]any{
			"domain":      "chrispian.dev",
			"nameservers": []string{"aldo.ns.cloudflare.com", "betty.ns.cloudflare.com"},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	update, ok := result.Data.(*ncconn.DomainNameserverUpdate)
	if !ok || !update.Updated || update.Domain != "chrispian.dev" {
		t.Fatalf("Data = %#v, want nameserver update", result.Data)
	}
	if backend.domain != "chrispian.dev" || len(backend.nameservers) != 2 {
		t.Fatalf("backend = %+v", backend)
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
	svc.SetResourceLookup(sshTestLookup())

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector:    "ssh",
		Operation:    "exec",
		Acknowledged: true,
		Config:       map[string]any{"id": "server-1", "command": "uptime"},
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

func newSSHTestService(backend *fakeSSHBackend) *ExternalConnectorService {
	registry := connector.NewRegistry()
	registry.Register(sshconn.NewWithBackendFactory(nil, func() sshconn.Backend { return backend }))
	svc := NewExternalConnectorService(registry)
	svc.SetResourceLookup(sshTestLookup())
	return svc
}

// sshTestLookup is the configured resource every SSH test targets by id.
func sshTestLookup() ResourceLookup {
	return ConfigResourceLookup(&config.ConfigV2{Version: 2, Resources: []config.ResourceDef{{
		ID:        "server-1",
		Name:      "Server 1",
		Type:      "server",
		Connector: "ssh",
		Config: map[string]any{
			"host":     "127.0.0.1",
			"user":     "root",
			"key_file": "/tmp/fake-key",
		},
	}}})
}

func sshTransferConfig(extra map[string]any) map[string]any {
	cfg := map[string]any{"id": "server-1"}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestExternalConnectorServiceExecutesSSHPut(t *testing.T) {
	backend := &fakeSSHBackend{}
	svc := newSSHTestService(backend)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector:    "ssh",
		Operation:    "put",
		Acknowledged: true,
		Config: sshTransferConfig(map[string]any{
			"local_path":  "./docker-compose.yml",
			"remote_path": "/opt/app/docker-compose.yml",
		}),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	transfer, ok := result.Data.(*sshconn.TransferResult)
	if !ok {
		t.Fatalf("Data = %#v, want ssh transfer result", result.Data)
	}
	if transfer.Bytes != 42 {
		t.Fatalf("Bytes = %d, want 42", transfer.Bytes)
	}
	if backend.putCalls != 1 || backend.getCalls != 0 {
		t.Fatalf("put=%d get=%d, want put=1 get=0", backend.putCalls, backend.getCalls)
	}
	if backend.localPath != "./docker-compose.yml" || backend.remotePath != "/opt/app/docker-compose.yml" {
		t.Fatalf("backend paths = %q -> %q", backend.localPath, backend.remotePath)
	}
}

// put overwrites a file on a real host, so it must be gated the same way exec
// is. Without the ack an agent could replace a config with no confirmation.
func TestExternalConnectorServiceSSHPutRequiresAcknowledgment(t *testing.T) {
	backend := &fakeSSHBackend{}
	svc := newSSHTestService(backend)

	_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "ssh",
		Operation: "put",
		Config: sshTransferConfig(map[string]any{
			"local_path":  "./a",
			"remote_path": "/opt/a",
		}),
	})
	var connErr *ExternalConnectorError
	if !errors.As(err, &connErr) {
		t.Fatalf("err = %T, want ExternalConnectorError", err)
	}
	if connErr.Code != ExternalConnectorAckRequired {
		t.Fatalf("Code = %q, want %q", connErr.Code, ExternalConnectorAckRequired)
	}
	if backend.putCalls != 0 {
		t.Fatalf("put ran %d times despite a missing acknowledgment", backend.putCalls)
	}
}

// get only reads, so it must NOT demand an ack — otherwise every read becomes
// a confirmation prompt and the gate stops meaning anything.
func TestExternalConnectorServiceSSHGetNeedsNoAcknowledgment(t *testing.T) {
	backend := &fakeSSHBackend{}
	svc := newSSHTestService(backend)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "ssh",
		Operation: "get",
		Config: sshTransferConfig(map[string]any{
			"remote_path": "/etc/nginx/nginx.conf",
			"local_path":  "./nginx.conf",
		}),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	transfer, ok := result.Data.(*sshconn.TransferResult)
	if !ok || transfer.Bytes != 7 {
		t.Fatalf("Data = %#v, want ssh transfer result of 7 bytes", result.Data)
	}
	if backend.getCalls != 1 {
		t.Fatalf("getCalls = %d, want 1", backend.getCalls)
	}
}

func TestExternalConnectorServiceExecutesSSHPutDir(t *testing.T) {
	backend := &fakeSSHBackend{}
	svc := newSSHTestService(backend)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector:    "ssh",
		Operation:    "put_dir",
		Acknowledged: true,
		Config: sshTransferConfig(map[string]any{
			"local_path":  "./deploy",
			"remote_path": "/opt/app/deploy",
		}),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	transfer, ok := result.Data.(*sshconn.DirTransferResult)
	if !ok {
		t.Fatalf("Data = %#v, want ssh directory transfer result", result.Data)
	}
	if transfer.Files != 2 || transfer.Bytes != 42 {
		t.Fatalf("transfer = %+v, want 2 files and 42 bytes", transfer)
	}
	if backend.putDirCalls != 1 || backend.getDirCalls != 0 {
		t.Fatalf("put_dir=%d get_dir=%d, want 1 and 0", backend.putDirCalls, backend.getDirCalls)
	}
	if backend.localPath != "./deploy" || backend.remotePath != "/opt/app/deploy" {
		t.Fatalf("backend paths = %q -> %q", backend.localPath, backend.remotePath)
	}
}

// put_dir replaces a whole tree on a real host, so it is gated like put.
func TestExternalConnectorServiceSSHPutDirRequiresAcknowledgment(t *testing.T) {
	backend := &fakeSSHBackend{}
	svc := newSSHTestService(backend)

	_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "ssh",
		Operation: "put_dir",
		Config: sshTransferConfig(map[string]any{
			"local_path":  "./deploy",
			"remote_path": "/opt/app/deploy",
		}),
	})
	var connErr *ExternalConnectorError
	if !errors.As(err, &connErr) {
		t.Fatalf("err = %T, want ExternalConnectorError", err)
	}
	if connErr.Code != ExternalConnectorAckRequired {
		t.Fatalf("Code = %q, want %q", connErr.Code, ExternalConnectorAckRequired)
	}
	if backend.putDirCalls != 0 {
		t.Fatalf("put_dir ran %d times despite a missing acknowledgment", backend.putDirCalls)
	}
}

// The trap this guards: without a dryRunPreview case, --dry-run falls through
// to the acknowledgment gate and a preview demands --ack, which is the one
// thing a preview exists to avoid.
func TestExternalConnectorServiceSSHPutDirDryRunNeedsNoAcknowledgment(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, "conf"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	backend := &fakeSSHBackend{}
	svc := newSSHTestService(backend)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "ssh",
		Operation: "put_dir",
		DryRun:    true,
		Config: sshTransferConfig(map[string]any{
			"local_path":  src,
			"remote_path": "/opt/app/deploy",
		}),
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	preview, ok := result.Data.(ExternalConnectorDryRunPreview)
	if !ok {
		t.Fatalf("Data = %T, want ExternalConnectorDryRunPreview", result.Data)
	}
	// File count and total bytes are what an operator decides on.
	if preview.Input["files"] != 1 || preview.Input["dirs"] != 2 || preview.Input["bytes"] != int64(13) {
		t.Fatalf("Input = %#v, want 1 file, 2 dirs, 13 bytes", preview.Input)
	}
	if preview.Target["remote_path"] != "/opt/app/deploy" {
		t.Fatalf("Target = %#v", preview.Target)
	}
	if backend.putDirCalls != 0 {
		t.Fatalf("a dry run transferred %d times", backend.putDirCalls)
	}
}

// get_dir writes only to the local machine, under a path the operator named —
// the same reason get needs no ack. Making a read prompt empties the gate.
func TestExternalConnectorServiceSSHGetDirNeedsNoAcknowledgment(t *testing.T) {
	backend := &fakeSSHBackend{}
	svc := newSSHTestService(backend)

	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "ssh",
		Operation: "get_dir",
		Config: sshTransferConfig(map[string]any{
			"remote_path": "/opt/app/conf",
			"local_path":  "./conf",
		}),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	transfer, ok := result.Data.(*sshconn.DirTransferResult)
	if !ok || transfer.Files != 3 {
		t.Fatalf("Data = %#v, want a directory transfer of 3 files", result.Data)
	}
	if backend.getDirCalls != 1 {
		t.Fatalf("getDirCalls = %d, want 1", backend.getDirCalls)
	}
}

func TestExternalConnectorServiceSSHTransferRequiresPaths(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation string
		config    map[string]any
	}{
		{"put without remote_path", "put", map[string]any{"local_path": "./a"}},
		{"put without local_path", "put", map[string]any{"remote_path": "/opt/a"}},
		{"get without local_path", "get", map[string]any{"remote_path": "/opt/a"}},
		{"get without remote_path", "get", map[string]any{"local_path": "./a"}},
		{"put_dir without remote_path", "put_dir", map[string]any{"local_path": "./a"}},
		{"put_dir without local_path", "put_dir", map[string]any{"remote_path": "/opt/a"}},
		{"get_dir without local_path", "get_dir", map[string]any{"remote_path": "/opt/a"}},
		{"get_dir without remote_path", "get_dir", map[string]any{"local_path": "./a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &fakeSSHBackend{}
			svc := newSSHTestService(backend)

			_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
				Connector:    "ssh",
				Operation:    tc.operation,
				Acknowledged: true,
				Config:       sshTransferConfig(tc.config),
			})
			var connErr *ExternalConnectorError
			if !errors.As(err, &connErr) {
				t.Fatalf("err = %T, want ExternalConnectorError", err)
			}
			if connErr.Code != ExternalConnectorInvalidArgs {
				t.Fatalf("Code = %q, want %q", connErr.Code, ExternalConnectorInvalidArgs)
			}
		})
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

func TestNamecheapWholeZoneReplacementIsExplicitAndAcknowledged(t *testing.T) {
	backend := &fakeNamecheapBackend{}
	reg := connector.NewRegistry()
	reg.Register(ncconn.NewWithBackend(backend))
	service := NewExternalConnectorService(reg)
	args := ExternalConnectorOperationArgs{Connector: "namecheap", Operation: "set_dns_record_set", Config: map[string]any{
		"domain": "example.com", "email_type": "MX", "records": []any{map[string]any{"type": "TXT", "host": "resend._domainkey", "value": "p=AA/BB"}},
	}}
	if _, err := service.Execute(context.Background(), args); err == nil {
		t.Fatal("replacement accepted without acknowledgment")
	}
	if backend.recordSet != nil {
		t.Fatal("unacknowledged write reached backend")
	}
	args.DryRun = true
	if _, err := service.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if backend.recordSet != nil {
		t.Fatal("dry run wrote")
	}
	args.DryRun = false
	args.Acknowledged = true
	if _, err := service.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if backend.recordSet == nil || backend.recordSet.EmailType != "MX" || backend.recordSet.Records[0].Value != "p=AA/BB" {
		t.Fatal("explicit mode/records lost")
	}
	delete(args.Config, "records")
	if _, err := service.Execute(context.Background(), args); err == nil {
		t.Fatal("missing authoritative records accepted")
	}
}

// writeTestPluginDir creates a minimal installable plugin directory for the
// given connector id. It is never loaded, so the entrypoint only has to exist
// and be executable.
func writeTestPluginDir(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Owner-only, but still executable: the launcher rejects a non-executable
	// entrypoint, and these tests install without ever loading.
	if err := os.WriteFile(filepath.Join(dir, "bin", "plugin"), []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // an entrypoint must carry an execute bit; 0o700 is owner-only
		t.Fatalf("write entrypoint: %v", err)
	}

	manifest := contract.ManifestFromDefinition(sshconn.Definition())
	manifest.ID = id
	spec := pluginhost.PluginYAMLFromManifest(manifest, pluginhost.Entrypoint{Command: "bin/plugin"})
	spec.ID = id
	data, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal plugin.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, pluginhost.PluginYAMLFilename), data, 0o600); err != nil {
		t.Fatalf("write plugin.yaml: %v", err)
	}
	return dir
}

// A plugin that shadows a built-in must not disable it when unloaded.
// Previously `cerberus docker ps` stayed permanently broken after an unload,
// with no uninstall command and hand-editing the state file as the only way out.
func TestExternalConnectorServiceFallsBackToBuiltInWhenPluginNotLoaded(t *testing.T) {
	backend := &fakeSSHBackend{}
	registry := connector.NewRegistry()
	registry.Register(sshconn.NewWithBackendFactory(nil, func() sshconn.Backend { return backend }))

	managed, err := NewManagedPluginConnectorService("test", io.Discard, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("managed plugin service: %v", err)
	}
	if _, installErr := managed.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: writeTestPluginDir(t, "ssh"),
	}); installErr != nil {
		t.Fatalf("install: %v", installErr)
	}
	if !managed.Installed("ssh") {
		t.Fatal("plugin should be installed")
	}
	if managed.Loaded("ssh") {
		t.Fatal("plugin should not be loaded")
	}

	svc := NewExternalConnectorService(registry, managed)
	svc.SetResourceLookup(sshTestLookup())
	result, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "ssh",
		Operation: "status",
		Config:    sshTransferConfig(nil),
	})
	if err != nil {
		t.Fatalf("execute fell through to an error instead of the built-in: %v", err)
	}
	if _, ok := result.Data.(string); !ok {
		t.Fatalf("Data = %#v, want the built-in ssh status payload", result.Data)
	}
}

// With no built-in to fall back to, an installed-but-unloaded plugin is still
// an error — and the message should say how to recover.
func TestExternalConnectorServiceErrorsWhenPluginNotLoadedAndNoBuiltIn(t *testing.T) {
	managed, err := NewManagedPluginConnectorService("test", io.Discard, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("managed plugin service: %v", err)
	}
	if _, installErr := managed.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: writeTestPluginDir(t, "ssh"),
	}); installErr != nil {
		t.Fatalf("install: %v", installErr)
	}

	svc := NewExternalConnectorService(connector.NewRegistry(), managed)
	svc.SetResourceLookup(sshTestLookup())
	_, err = svc.Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "ssh",
		Operation: "status",
		Config:    sshTransferConfig(nil),
	})
	if err == nil {
		t.Fatal("expected an error with no built-in connector available")
	}
	if !strings.Contains(err.Error(), "uninstall") {
		t.Fatalf("error %q should point at the recovery commands", err.Error())
	}
}

// A local install needs no options, records origin "installed", and the host
// fingerprints the entrypoint itself.
func TestManagedPluginLocalInstallRecordsOriginAndFingerprint(t *testing.T) {
	managed, err := NewManagedPluginConnectorService("test", io.Discard, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("managed plugin service: %v", err)
	}
	state, err := managed.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: writeTestPluginDir(t, "ssh"),
	})
	if err != nil {
		t.Fatalf("local install failed: %v", err)
	}
	if state.Origin != string(pluginhost.OriginInstalled) {
		t.Fatalf("Origin = %q, want %q", state.Origin, pluginhost.OriginInstalled)
	}
	installed, ok := managed.manager.Installed("ssh")
	if !ok {
		t.Fatal("plugin not registered")
	}
	if installed.EntrypointSHA256 == "" {
		t.Fatal("host did not fingerprint the entrypoint")
	}
}

func TestManagedPluginUninstallRemovesEntry(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	managed, err := NewManagedPluginConnectorService("test", io.Discard, statePath)
	if err != nil {
		t.Fatalf("managed plugin service: %v", err)
	}
	if _, err := managed.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: writeTestPluginDir(t, "ssh"),
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := managed.Uninstall(context.Background(), "ssh"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if managed.Installed("ssh") {
		t.Fatal("plugin still installed after uninstall")
	}
	if _, err := managed.Uninstall(context.Background(), "ssh"); err == nil {
		t.Fatal("uninstalling an absent plugin should error")
	}
}

// Deleting a plugin directory must not take the daemon down. Returning an error
// from restore made the launchd job fail to start and KeepAlive crash-loop, so
// `make clean` in a plugin repo — or a fresh clone with a gitignored dist/ —
// bricked Cerberus entirely.
func TestManagedPluginRestoreSkipsMissingDirectoryInsteadOfFailing(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	pluginDir := writeTestPluginDir(t, "ssh")

	managed, err := NewManagedPluginConnectorService("test", io.Discard, statePath)
	if err != nil {
		t.Fatalf("managed plugin service: %v", err)
	}
	if _, installErr := managed.Install(context.Background(), PluginConnectorHealthArgs{
		PluginDir: pluginDir,
	}); installErr != nil {
		t.Fatalf("install: %v", installErr)
	}

	// The plugin directory goes away, as `make clean` would do.
	if rmErr := os.RemoveAll(pluginDir); rmErr != nil {
		t.Fatalf("remove plugin dir: %v", rmErr)
	}

	var warnings strings.Builder
	restored, err := NewManagedPluginConnectorService("test", &warnings, statePath)
	if err != nil {
		t.Fatalf("restore returned an error for a missing plugin directory: %v", err)
	}
	if restored.Installed("ssh") {
		t.Fatal("a plugin whose directory is gone should not be registered")
	}
	if !strings.Contains(warnings.String(), "skipping plugin") {
		t.Fatalf("missing plugin was skipped silently; warnings = %q", warnings.String())
	}
}

// A registration is kept when its directory is temporarily absent, so a
// rebuilt plugin returns on the next restart instead of needing reinstalling.
func TestManagedPluginRestorePreservesUnrestorableEntries(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	pluginDir := writeTestPluginDir(t, "ssh")

	managed, err := NewManagedPluginConnectorService("test", io.Discard, statePath)
	if err != nil {
		t.Fatalf("managed plugin service: %v", err)
	}
	if _, installErr := managed.Install(context.Background(), PluginConnectorHealthArgs{PluginDir: pluginDir}); installErr != nil {
		t.Fatalf("install: %v", installErr)
	}
	if rmErr := os.RemoveAll(pluginDir); rmErr != nil {
		t.Fatalf("remove plugin dir: %v", rmErr)
	}

	restored, err := NewManagedPluginConnectorService("test", io.Discard, statePath)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	// Any persist must not drop the entry whose directory is missing.
	if persistErr := restored.persist(); persistErr != nil {
		t.Fatalf("persist: %v", persistErr)
	}
	state, err := readPluginConnectorState(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if len(state.Entries) != 1 || state.Entries[0].PluginDir != pluginDir {
		t.Fatalf("registration was dropped; entries = %+v", state.Entries)
	}
}

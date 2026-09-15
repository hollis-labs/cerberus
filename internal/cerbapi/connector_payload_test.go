package cerbapi

import (
	"context"
	"reflect"
	"testing"
	"time"

	cf "github.com/hollis-labs/cerberus/internal/connector/cloudflare"
	do "github.com/hollis-labs/cerberus/internal/connector/digitalocean"
	docker "github.com/hollis-labs/cerberus/internal/connector/docker"
	forge "github.com/hollis-labs/cerberus/internal/connector/forge"
	gh "github.com/hollis-labs/cerberus/internal/connector/github"
	nc "github.com/hollis-labs/cerberus/internal/connector/namecheap"
	ssh "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/redact"
)

type connectorPayloadClient struct {
	Client
	payload any
}

func (c connectorPayloadClient) ExecuteConnectorOperation(_ context.Context, args ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	return ExternalConnectorOperationResult{Connector: args.Connector, Operation: args.Operation, Data: c.payload}, nil
}

func TestTypedConnectorTransportPreservesCLIValuesAndJSON(t *testing.T) {
	timestamp := time.Date(2026, 9, 12, 12, 30, 0, 123, time.UTC)
	for _, tt := range []struct {
		connector, operation string
		payload              any
	}{
		{"cloudflare", "list_zones", []cf.Zone{{ID: "zone", Name: "example.com"}}},
		{"cloudflare", "create_zone", &cf.Zone{ID: "zone", Name: "example.com"}},
		{"cloudflare", "list_dns_records", []cf.DNSRecord{{ID: "record", Type: "TXT", Content: "public value"}}},
		{"cloudflare", "create_dns_record", &cf.DNSRecord{ID: "record", Type: "TXT", Content: "public value"}},
		{"digitalocean", "list_droplets", []do.DropletStatus{{ID: 123, Name: "web", CreatedAt: timestamp}}},
		{"digitalocean", "get_droplet", &do.DropletStatus{ID: 123, Name: "web", CreatedAt: timestamp}},
		{"digitalocean", "create_droplet", &do.DropletStatus{ID: 123, Name: "web", CreatedAt: timestamp}},
		{"docker", "list_containers", []docker.Container{{ID: "abc", Name: "web", State: "running"}}},
		{"docker", "logs", "line one\nline two\n"},
		{"forge", "list_servers", []forge.Server{{ID: 123, Name: "web"}}},
		{"forge", "get_server", &forge.Server{ID: 123, Name: "web"}},
		{"forge", "list_sites", []forge.Site{{ID: 456, Name: "example.com"}}},
		{"forge", "get_deployment_script", "#!/bin/sh\ntrue\n"},
		{"forge", "exec_site_command", &forge.SiteCommand{}},
		{"github", "status", &gh.RepoStatus{Owner: "org", Repo: "repo", UpdatedAt: timestamp}},
		{"github", "list_releases", []gh.Release{{TagName: "v1", PublishedAt: timestamp}}},
		{"github", "list_workflow_runs", []gh.WorkflowRun{{ID: 9007199254740993, Name: "Build", CreatedAt: timestamp}}},
		{"namecheap", "list_domains", []nc.Domain{{Name: "example.com", AutoRenew: true}}},
		{"namecheap", "get_domain_status", &nc.DomainStatus{Domain: "example.com", Registered: true, NameServers: []string{"ns1.example.com"}}},
		{"namecheap", "list_dns_records", []nc.DNSRecord{{ID: 123, Type: "MX", Host: "@", Value: "mail.example.com", MXPref: 10}}},
		{"namecheap", "get_dns_record_set", &nc.DNSRecordSet{EmailType: "MX", Records: []nc.DNSRecord{}}},
		{"namecheap", "set_dns_record_set", nc.DNSRecordSet{EmailType: "NONE", Records: []nc.DNSRecord{}}},
		{"namecheap", "set_custom_nameservers", &nc.DomainNameserverUpdate{Domain: "example.com", Updated: true}},
		{"ssh", "exec", &ssh.ExecResult{Stdout: "hello\n", ExitCode: 7}},
		{"ssh", "status", `{"state":"running"}`},
		{"docker", "list_containers", []docker.Container(nil)},
	} {
		t.Run(tt.connector+"/"+tt.operation, func(t *testing.T) {
			client := startConnectorSocket(t, connectorPayloadClient{payload: tt.payload})
			result, err := client.ExecuteTypedConnectorOperation(context.Background(), ExternalConnectorOperationArgs{Connector: tt.connector, Operation: tt.operation})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Data, tt.payload) {
				t.Fatalf("typed payload = %#v (%T), want %#v (%T)", result.Data, result.Data, tt.payload, tt.payload)
			}
			before, err := redact.MarshalIndent(tt.payload, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			after, err := redact.MarshalIndent(result.Data, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("CLI JSON changed: %s != %s", after, before)
			}
		})
	}
}

// A foreground command can wait beyond a configured short client timeout, but
// its cancellation must still reach the daemon operation.
type cancellableConnectorClient struct {
	Client
	started, canceled chan struct{}
}

func (c cancellableConnectorClient) ExecuteConnectorOperation(ctx context.Context, _ ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	return ExternalConnectorOperationResult{}, ctx.Err()
}
func TestForegroundConnectorTransportHonorsCancellation(t *testing.T) {
	backend := cancellableConnectorClient{started: make(chan struct{}), canceled: make(chan struct{})}
	socket := startConnectorSocket(t, backend)
	client := NewSocketClient(socket.DialPath(), WithClientTimeout(time.Nanosecond), WithClientTimeout(0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.ExecuteTypedConnectorOperation(ctx, ExternalConnectorOperationArgs{Connector: "test-only", Operation: "wait"})
		result <- err
	}()
	select {
	case <-backend.started:
	case err := <-result:
		t.Fatalf("foreground request ended before execution: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case <-backend.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not reach operation")
	}
	if err := <-result; err == nil {
		t.Fatal("canceled request succeeded")
	}
}

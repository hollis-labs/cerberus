package cerbapi

import (
	"context"
	"errors"
	"github.com/hollis-labs/cerberus/internal/audit"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/connector"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// dockerTestResources are the declared docker resources the socket tests
// operate by id: the `type: container` + compose_file pattern AGENTS.md
// documents, one on a remote daemon, and a plain container.
func dockerTestResources() ResourceLookup {
	return ConfigResourceLookup(&config.ConfigV2{Version: 2, Resources: []config.ResourceDef{
		{ID: "mtbf-monitor", Name: "MTBF Monitor", Type: "container", Connector: "docker",
			Config: map[string]any{"compose_file": "/srv/mtbf/docker-compose.yml"}},
		{ID: "remote-stack", Type: "container", Connector: "docker",
			Config: map[string]any{"compose_file": "/srv/app/compose.yml", "host": "ssh://ops@docker-host"}},
		{ID: "single", Type: "container", Connector: "docker",
			Config: map[string]any{"container": "nginx-router"}},
		{ID: "muctlvaig", Type: "server", Connector: "ssh",
			Config: map[string]any{"host": "muctlvaig.example"}},
	}})
}

func dockerSocket(t *testing.T) (*SocketClient, *fakeDockerBackend) {
	t.Helper()
	backend := &fakeDockerBackend{}
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(audit.NewMemory(), registry)
	svc.SetResourceLookup(dockerTestResources())
	return startConnectorSocket(t, NewInProcessClient(WithExternalConnectorService(svc))), backend
}

// `cerberus docker up <id>` on a configured `type: container` resource works
// over the socket: the id is all that is sent, and the daemon resolves the
// compose file from the declaration.
func TestDockerUpByResourceIDOverSocket(t *testing.T) {
	client, backend := dockerSocket(t)
	if _, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "start", Acknowledged: true, Config: map[string]any{"resource": "mtbf-monitor"},
	}); err != nil {
		t.Fatalf("up by id: %v", err)
	}
	if backend.upFile != "/srv/mtbf/docker-compose.yml" || !backend.target.IsZero() {
		t.Fatalf("compose up ran %q on %+v, want the declared file on the local daemon", backend.upFile, backend.target)
	}

	if _, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "stop", Acknowledged: true, Config: map[string]any{"resource": "mtbf-monitor"},
	}); err != nil {
		t.Fatalf("down by id: %v", err)
	}
	if backend.stopFile != "/srv/mtbf/docker-compose.yml" {
		t.Fatalf("compose stop ran %q, want the declared file", backend.stopFile)
	}
}

// A declared remote target is configuration, not an ad-hoc target: its host
// comes from the resource and reaches the backend.
func TestDockerResourceHostComesFromTheDeclaration(t *testing.T) {
	client, backend := dockerSocket(t)
	if _, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "start", Acknowledged: true, Config: map[string]any{"resource": "remote-stack"},
	}); err != nil {
		t.Fatalf("up by id: %v", err)
	}
	if backend.target.Host != "ssh://ops@docker-host" || backend.upFile != "/srv/app/compose.yml" {
		t.Fatalf("target %+v file %q, want the declared host and file", backend.target, backend.upFile)
	}
}

func TestDockerContainerResourceAndLiteralsOverSocket(t *testing.T) {
	client, backend := dockerSocket(t)
	if _, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "start", Acknowledged: true, Config: map[string]any{"resource": "single"},
	}); err != nil {
		t.Fatalf("container resource: %v", err)
	}
	if backend.started != "nginx-router" {
		t.Fatalf("started %q, want the declared container", backend.started)
	}
	// An undeclared container on the local daemon still works by name.
	if _, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "start", Acknowledged: true, Config: map[string]any{"container": "web", "id": "web", "name": "web"},
	}); err != nil {
		t.Fatalf("literal container: %v", err)
	}
	if backend.started != "web" {
		t.Fatalf("started %q, want web", backend.started)
	}
	// `docker ps` with no resource and no flags goes to the local daemon.
	if _, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "list_containers",
	}); err != nil {
		t.Fatalf("ps: %v", err)
	}
	if backend.lists != 1 || !backend.target.IsZero() {
		t.Fatalf("ps: lists=%d target=%+v, want one local list", backend.lists, backend.target)
	}
}

func TestDockerResourceMustBeADockerResource(t *testing.T) {
	client, _ := dockerSocket(t)
	for id, want := range map[string]string{"muctlvaig": "not a docker resource", "nope": `resource "nope" not found`} {
		_, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
			Connector: "docker", Operation: "start", Acknowledged: true, Config: map[string]any{"resource": id},
		})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %q", id, err, want)
		}
	}
}

// Every ad-hoc target field is refused over the socket, by name, and nothing
// reaches the backend.
func TestDockerAdHocTargetsRefusedOverSocket(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
	}{
		{"host", "ssh://evil@elsewhere"},
		{"context", "prod"},
		{"docker_host", "tcp://10.0.0.9:2376"},
		{"docker_context", "prod"},
		{"compose_file", "/tmp/evil-compose.yml"},
		{"composeFile", "/tmp/evil-compose.yml"},
		{"file", "/tmp/evil-compose.yml"},
		{"anything_else", "x"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			client, backend := dockerSocket(t)
			for _, cfg := range []map[string]any{
				{"container": "web", tc.field: tc.value},
				{"resource": "mtbf-monitor", tc.field: tc.value},
			} {
				_, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
					Connector: "docker", Operation: "start", Acknowledged: true, Config: cfg,
				})
				if err == nil || !strings.Contains(err.Error(), "refusing fields ("+tc.field+")") || !strings.Contains(err.Error(), "resource=<id>") {
					t.Fatalf("cfg %v: err = %v, want a refusal naming %s", cfg, err, tc.field)
				}
			}
			if backend.started != "" || backend.upFile != "" {
				t.Fatalf("a refused request reached the backend: %+v", backend)
			}
		})
	}
}

// In-process — the operator's shell — keeps the ad-hoc flags.
func TestDockerAdHocTargetsWorkInProcess(t *testing.T) {
	backend := &fakeDockerBackend{}
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(backend))
	svc := NewExternalConnectorService(audit.NewMemory(), registry)
	svc.SetResourceLookup(dockerTestResources())
	if _, err := svc.Execute(WithCallerSurface(context.Background(), SurfaceInProcess), ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "start", Acknowledged: true,
		Config: map[string]any{"resource": "mtbf-monitor", "compose_file": "/tmp/override.yml", "host": "ssh://ops@other"},
	}); err != nil {
		t.Fatalf("in-process ad-hoc: %v", err)
	}
	if backend.upFile != "/tmp/override.yml" || backend.target.Host != "ssh://ops@other" {
		t.Fatalf("file %q target %+v, want the -f and --host overrides", backend.upFile, backend.target)
	}
}

func TestDockerRefusalsSurviveRedaction(t *testing.T) {
	err := RefuseAdHocDockerTarget(ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "start",
		Config: map[string]any{"host": "h", "context": "c", "docker_host": "h", "docker_context": "c", "compose_file": "/tmp/x.yml"},
	})
	svc := NewExternalConnectorService(audit.NewMemory(), connector.NewRegistry())
	svc.SetResourceLookup(dockerTestResources())
	_, notDocker := svc.resolveDockerResource(ExternalConnectorOperationArgs{Connector: "docker", Operation: "start", Config: map[string]any{"resource": "muctlvaig"}})
	_, missing := svc.resolveDockerResource(ExternalConnectorOperationArgs{Connector: "docker", Operation: "start", Config: map[string]any{"resource": "nope"}})
	for _, e := range []error{err, notDocker, missing} {
		var connErr *ExternalConnectorError
		if !errors.As(e, &connErr) {
			t.Fatalf("not a connector error: %v", e)
		}
		raw := connErr.Connector + " " + connErr.Operation + ": " + string(connErr.Code) + ": " + connErr.Err.Error()
		if got := e.Error(); got != raw || redact.Text(raw) != raw {
			t.Errorf("redaction changed the refusal:\n got %q\nwant %q", got, raw)
		}
	}
}

// Every compose-file alias the connector reads is refused from a socket
// caller, because the allow-list is built from the same table the connector
// reads. This is the bypass that motivated the allow-list: {"file": ...} used
// to pass a denylist and reach `compose -f`.
func TestEveryComposeAliasIsRefused(t *testing.T) {
	for _, key := range dockerconn.ComposeFileKeys {
		err := RefuseAdHocDockerTarget(ExternalConnectorOperationArgs{
			Connector: "docker", Operation: "start", Config: map[string]any{key: "/tmp/evil.yml"},
		})
		if err == nil || !strings.Contains(err.Error(), "refusing fields ("+key+")") {
			t.Errorf("compose alias %q: err = %v, want refused", key, err)
		}
	}
	for _, key := range dockerconn.TargetKeys() {
		if dockerCallerFields()[key] {
			t.Errorf("target key %q is on the socket/web allow-list", key)
		}
	}
}

// Every config key the admin lane's docker path reads must be classified in
// the connector's key table: an operation field (allowed from socket and web)
// or a target (refused). A key read here but absent from the table fails, so
// a new alias cannot slip past the allow-list unclassified.
func TestDockerAdminLaneReadsOnlyClassifiedKeys(t *testing.T) {
	src, err := os.ReadFile("external_connector_service.go")
	if err != nil {
		t.Fatal(err)
	}
	classified := dockerCallerFields()
	for _, key := range dockerconn.TargetKeys() {
		classified[key] = true
	}
	for _, fn := range []string{"func (s *ExternalConnectorService) executeDocker(", "func externalResource("} {
		body := functionBody(t, string(src), fn)
		for _, m := range regexp.MustCompile(`(?:requiredString|stringFromConfig|intFromConfig|boolFromConfig)\(args\.Config, "(\w+)"`).FindAllStringSubmatch(body, -1) {
			if !classified[m[1]] {
				t.Errorf("%s reads config key %q, which the docker key table does not classify", fn, m[1])
			}
		}
		if strings.Contains(body, "args.Config[") {
			t.Errorf("%s indexes args.Config directly; read through a helper so the key is checked here", fn)
		}
	}
}

func functionBody(t *testing.T, src, signature string) string {
	t.Helper()
	i := strings.Index(src, signature)
	if i < 0 {
		t.Fatalf("function %q not found", signature)
	}
	j := strings.Index(src[i:], "\n}\n")
	if j < 0 {
		t.Fatalf("end of %q not found", signature)
	}
	return src[i : i+j]
}

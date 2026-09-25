package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/connector"
	cf "github.com/hollis-labs/cerberus/internal/connector/cloudflare"
)

type priorityBackend struct {
	cf.Backend
	created chan cf.DNSRecord
}

func (b *priorityBackend) CreateDNSRecord(_ context.Context, _ string, record cf.DNSRecord) (*cf.DNSRecord, error) {
	b.created <- record
	return &record, nil
}

func TestCloudflareMXPriorityReachesBackendThroughSocket(t *testing.T) {
	backend := &priorityBackend{created: make(chan cf.DNSRecord, 1)}
	registry := connector.NewRegistry()
	registry.Register(cf.NewWithBackend(backend))
	client := startDaemonSocketWithClient(t, cerbapi.NewInProcessClient(cerbapi.WithExternalConnectorService(cerbapi.NewExternalConnectorService(registry))))
	tool := NewCerberusCloudflareDNSCreateTool(client)
	for _, priority := range []any{float64(10), float64(0), 25} {
		out, err := tool.Handler(context.Background(), map[string]any{"zone_id": "zone", "type": "MX", "name": "mail.example.com", "content": "mx.example.com", "priority": priority, "acknowledged": true})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case record := <-backend.created:
			want, _ := priority.(float64)
			if integer, ok := priority.(int); ok {
				want = float64(integer)
			}
			if record.Priority == nil || *record.Priority != int(want) {
				t.Fatalf("priority dropped: %+v", record)
			}
		default:
			t.Fatalf("create did not reach backend: %s", out)
		}
	}
	for _, invalid := range []any{-1.0, 1.5, 65536.0, "10"} {
		out, err := tool.Handler(context.Background(), map[string]any{"priority": invalid})
		if err == nil || !strings.Contains(err.Error(), "priority must be an integer") {
			t.Fatalf("bad validation: want a tool error, got %v %v", out, err)
		}
	}
}

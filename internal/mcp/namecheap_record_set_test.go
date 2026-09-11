package mcp

import (
	"context"
	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/connector"
	nc "github.com/chrispian/cerberus/internal/connector/namecheap"
	"testing"
)

type recordSetBackend struct {
	nc.Backend
	writes chan nc.DNSRecordSet
}

func (b *recordSetBackend) SetDNSRecordSet(_ context.Context, _, _ string, set nc.DNSRecordSet) error {
	b.writes <- set
	return nil
}
func TestNamecheapMCPPreservesExplicitEmailModeAndAuthoritativeRecords(t *testing.T) {
	backend := &recordSetBackend{writes: make(chan nc.DNSRecordSet, 1)}
	registry := connector.NewRegistry()
	registry.Register(nc.NewWithBackend(backend))
	client := startDaemonSocketWithClient(t, cerbapi.NewInProcessClient(cerbapi.WithExternalConnectorService(cerbapi.NewExternalConnectorService(registry))))
	for _, tool := range NewCerberusDNSRecordSetTools(client) {
		if tool.Name != "cerberus_set_dns_record_set" {
			continue
		}
		args := map[string]any{"domain": "example.com", "email_type": "MX", "records": []any{map[string]any{"type": "TXT", "host": "resend._domainkey", "value": "p=AA/BB"}}, "acknowledged": true}
		out, err := tool.Handler(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case set := <-backend.writes:
			if set.EmailType != "MX" || set.Records[0].Value != "p=AA/BB" {
				t.Fatal("intent changed")
			}
		default:
			t.Fatalf("write did not reach backend: %s", out)
		}
		args["dry_run"] = true
		if _, err = tool.Handler(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		select {
		case <-backend.writes:
			t.Fatal("dry run mutated DNS")
		default:
		}
		return
	}
	t.Fatal("replacement tool missing")
}

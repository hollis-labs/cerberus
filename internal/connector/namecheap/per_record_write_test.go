package namecheap

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type lossyDNSBackend struct {
	Backend
	records       []DNSRecord
	reads, writes int
}

func (b *lossyDNSBackend) GetDNSRecordSet(context.Context, string, string) (*DNSRecordSet, error) {
	b.reads++
	return &DNSRecordSet{EmailType: "MX", Records: []DNSRecord{{ID: 1, Type: "A", Host: "@", Value: "192.0.2.1"}}}, nil
}
func (b *lossyDNSBackend) SetDNSRecordSet(_ context.Context, _, _ string, set DNSRecordSet) error {
	b.writes++
	b.records = set.Records
	return nil
}

func TestPerRecordWritesCannotDestroyRecordsOmittedByGetHosts(t *testing.T) {
	original := []DNSRecord{{ID: 1, Type: "A", Host: "@", Value: "192.0.2.1"}, {ID: 2, Type: "TXT", Host: "resend._domainkey", Value: "p=AA/BB"}}
	backend := &lossyDNSBackend{records: append([]DNSRecord(nil), original...)}
	connector := NewWithBackend(backend)
	_, err := connector.CreateDNSRecord(context.Background(), "example.com", DNSRecord{Type: "A", Host: "www", Value: "192.0.2.2"})
	if !errors.Is(err, ErrUnsafePerRecordWrite) {
		t.Fatalf("create was not refused: %v", err)
	}
	if err = connector.DeleteDNSRecord(context.Background(), "example.com", 1); !errors.Is(err, ErrUnsafePerRecordWrite) {
		t.Fatalf("delete was not refused: %v", err)
	}
	if backend.reads != 0 || backend.writes != 0 {
		t.Fatal("disabled operation accessed DNS")
	}
	if !reflect.DeepEqual(backend.records, original) {
		t.Fatal("hidden record was changed or removed")
	}
}

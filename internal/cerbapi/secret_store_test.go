package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
)

type memoryStore struct{ values map[string]string }

func (m *memoryStore) Get(_ context.Context, service, key string) (string, error) {
	return m.values[service+"/"+key], nil
}
func (m *memoryStore) Set(_ context.Context, service, key, value string) error {
	m.values[service+"/"+key] = value
	return nil
}
func (m *memoryStore) Delete(_ context.Context, service, key string) error {
	delete(m.values, service+"/"+key)
	return nil
}

type failingSink struct{ audit.Sink }

func (failingSink) Write(audit.Record) (audit.Record, error) {
	return audit.Record{}, errors.New("disk full")
}

func TestSetStoredSecretRecordsTheNameNeverTheValue(t *testing.T) {
	sink := audit.NewMemory()
	store := &memoryStore{values: map[string]string{}}
	const value = "ops_eyJzaWduSW5BZGRyZXNzIjoibXkuMXBhc3N3b3JkLmNvbSJ9" //nolint:gosec // a test sentinel
	if err := SetStoredSecret(context.Background(), sink, store, "onepassword", "service_account_token", value); err != nil {
		t.Fatal(err)
	}
	if store.values["onepassword/service_account_token"] != value {
		t.Fatal("the value was not stored")
	}
	records := sink.Records()
	if len(records) != 2 || records[1].Kind != audit.KindOutcome || records[1].Operation != "set" || records[1].Target.Fields["name"] != "onepassword/service_account_token" {
		t.Fatalf("records = %+v", records)
	}
	data, _ := json.Marshal(records)
	if strings.Contains(string(data), value) || strings.Contains(string(data), "eyJzaWdu") {
		t.Fatal("the value reached the audit log")
	}
}

func TestSetStoredSecretRefuses(t *testing.T) {
	store := &memoryStore{values: map[string]string{}}
	for name, call := range map[string]func() error{
		"no key": func() error {
			return SetStoredSecret(context.Background(), audit.NewMemory(), store, "keeper", "", "v")
		},
		"a path": func() error { return SetStoredSecret(context.Background(), audit.NewMemory(), store, "../x", "k", "v") },
		"empty": func() error {
			return SetStoredSecret(context.Background(), audit.NewMemory(), store, "keeper", "ksm_config", "")
		},
		"no audit": func() error {
			return SetStoredSecret(context.Background(), failingSink{audit.NewMemory()}, store, "keeper", "ksm_config", "v")
		},
		"upper case": func() error {
			return SetStoredSecret(context.Background(), audit.NewMemory(), store, "Keeper", "ksm_config", "v")
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: stored", name)
		}
	}
	if len(store.values) != 0 {
		t.Fatalf("something was stored: %v", store.values)
	}
}

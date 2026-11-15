package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

type acceptAll struct{}

func (acceptAll) Verify(approval.Approval, approval.Decision) error { return nil }

// `cerberus audit verify` passes a chain with a sealed owner-session decision, lists a
// pre-cutover decision as legacy, and fails once a forged owner-session decision is appended.
func TestAuditVerifyChecksTheChainAndTheOwnerSessionSeals(t *testing.T) {
	secrets.MockStoreForTests()
	data := t.TempDir()
	sink, err := auditchain.Open(filepath.Join(data, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sink.Write(audit.Record{Kind: audit.KindIntent, Connector: "local", Operation: "apply"}); err != nil {
		t.Fatal(err)
	}
	store, err := approval.Open(filepath.Join(data, "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	request := func(digest string) approval.Approval {
		t.Helper()
		a, reqErr := store.Request(approval.Approval{Connector: "local", Operation: "apply", ArgsDigest: digest, Channel: approval.ChannelOutOfBand}, time.Hour)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		return a
	}
	web := audit.Principal{Kind: "human", Via: gate.ViaWeb}
	marker := "owner-session:" + strings.Repeat("a", 16)
	v := gate.OwnerSessionVerifier{Key: sink}

	genuine := request("hmac-sha256:01")
	if _, err = store.Decide(genuine.ID, approval.Decision{Approve: true, By: web, KeyFingerprint: marker, Assertion: v.Seal(genuine, marker, time.Now())}, v); err != nil {
		t.Fatal(err)
	}
	legacy := request("hmac-sha256:02")
	if _, err = store.Decide(legacy.ID, approval.Decision{Approve: true, By: web, KeyFingerprint: "sha256:passkey", Assertion: []byte(`{"v":2}`)}, acceptAll{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = auditVerify(data, &out); err != nil {
		t.Fatalf("a clean store: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "audit chain intact") || !strings.Contains(out.String(), "1 owner-session decision(s) verified, 1 legacy") || !strings.Contains(out.String(), "legacy: "+legacy.ID) {
		t.Fatalf("report:\n%s", out.String())
	}

	forged := request("hmac-sha256:03")
	fake, _ := json.Marshal(map[string]any{"v": 1, "t": time.Now().UnixNano(), "mac": "hmac-sha256:" + strings.Repeat("0", 64)})
	if _, err = store.Decide(forged.ID, approval.Decision{Approve: true, By: web, KeyFingerprint: marker, Assertion: fake}, acceptAll{}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err = auditVerify(data, &out)
	if err == nil || !strings.Contains(err.Error(), forged.ID) {
		t.Fatalf("a forged decision passed: %v\n%s", err, out.String())
	}
}

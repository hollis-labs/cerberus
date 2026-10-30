package cerbapi

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/oauth"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The oauth host write set must name every write operation of the host
// connector except the ones that stay under operate, so adding a write
// operation cannot silently fall outside (or inside) the narrow scope.
func TestHostWriteScopeCoversExactlyTheHostWrites(t *testing.T) {
	underOperate := map[string]bool{OpHostRemove: true}
	for _, op := range HostDefinition().Operations {
		if op.Effect == contract.EffectRead || op.Effect == contract.EffectReadSensitive {
			continue
		}
		want := !underOperate[op.Name]
		if oauth.HostWriteOperations[op.Name] != want {
			t.Errorf("host op %s (%s): in HostWriteOperations = %v, want %v", op.Name, op.Effect, oauth.HostWriteOperations[op.Name], want)
		}
	}
	for name := range oauth.HostWriteOperations {
		if _, ok := HostDefinition().Operation(name); !ok {
			t.Errorf("HostWriteOperations names %s, which is not a host operation", name)
		}
	}
}

type tokenHolder struct {
	mu  sync.Mutex
	tok string
}

func (h *tokenHolder) set(t string) { h.mu.Lock(); h.tok = t; h.mu.Unlock() }
func (h *tokenHolder) get(context.Context) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tok
}

func TestHostScopesEnforcedByTheDaemonForBearerCallers(t *testing.T) {
	sink := audit.NewMemory()
	auth := withAuth(t, sink)
	f := newWriteFixture(t, nil)
	sockClient := serveOnSocketWithBearer(t, NewInProcessClient(WithHostService(f.svc)))
	ctx := context.Background()

	readTok, _ := mint(t, auth, "tachyon-ro", oauth.ScopeHostsRead)
	writeTok, _ := mint(t, auth, "tachyon-rw", oauth.ScopeHostsWrite)
	opTok, _ := mint(t, auth, "operator", oauth.ScopeOperate)

	as := func(tok string) *SocketClient { sockClient.holder.set(tok); return sockClient.SocketClient }

	// hosts.read: reads yes, writes no, and the refusal names the narrow scope.
	c := as(readTok)
	if _, err := c.GetHost(ctx, "worker-1"); err != nil {
		t.Fatalf("hosts.read must read: %v", err)
	}
	rev, err := c.HostsRevision(ctx)
	if err != nil {
		t.Fatalf("revision is a read: %v", err)
	}
	wa := WriteArgs{Revision: rev, Acknowledged: true}
	_, err = c.UpdateHost(ctx, HostUpdateArgs{WriteArgs: wa, Name: "worker-5", Patch: map[string]any{"role": "service"}})
	if err == nil || !strings.Contains(err.Error(), oauth.ScopeHostsWrite) {
		t.Fatalf("a read-only token writing: %v", err)
	}
	// hosts.write: the registry writes yes, host_remove no.
	c = as(writeTok)
	if _, err = c.UpdateHost(ctx, HostUpdateArgs{WriteArgs: wa, Name: "worker-5", Patch: map[string]any{"role": "service"}}); err != nil {
		t.Fatalf("hosts.write must update a host: %v", err)
	}
	rev, _ = c.HostsRevision(ctx)
	_, err = c.RemoveHost(ctx, HostRemoveArgs{WriteArgs: WriteArgs{Revision: rev, Acknowledged: true}, Name: "worker-5"})
	if err == nil || !strings.Contains(err.Error(), oauth.ScopeOperate) {
		t.Fatalf("hosts.write removing a host must need operate: %v", err)
	}
	// operate does it.
	c = as(opTok)
	if _, err = c.RemoveHost(ctx, HostRemoveArgs{WriteArgs: WriteArgs{Revision: rev, Acknowledged: true}, Name: "worker-5"}); err != nil {
		t.Fatalf("operate must remove a host: %v", err)
	}
	// The token and subject are in the audit records.
	var seen bool
	for _, r := range f.sink.Records() {
		if r.Operation == OpHostUpdate && r.Principal.Client == "tachyon-rw" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the audit record of a bearer write must name the token's client")
	}
	// A token that does not verify is refused, never read as a lesser claim.
	c = as("not-a-token")
	if _, err = c.GetHost(ctx, "worker-1"); err == nil {
		t.Fatal("a bad token read a host")
	}
}

// socketWithBearer is a socket client whose bearer token a test can swap.
type socketWithBearer struct {
	*SocketClient
	holder *tokenHolder
}

func serveOnSocketWithBearer(t *testing.T, client Client) socketWithBearer {
	t.Helper()
	plain := serveOnSocket(t, client)
	h := &tokenHolder{}
	return socketWithBearer{SocketClient: NewSocketClient(plain.DialPath(), WithBearer(h.get)), holder: h}
}

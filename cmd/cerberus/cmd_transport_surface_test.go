package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// The in-process lane is the operator's own shell, and it is the only lane
// that marks itself so. Driven through the real transport helper with no
// daemon running, a local-only docker input (--host) is accepted; the same
// call on an unmarked service is refused, because unmarked means remote.
func TestInProcessTransportIsTheOnlyLocalSurface(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "cerbhome-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)

	// A socket path that does not exist, so docker fails locally, fast, and
	// after the key table has had its say.
	args := cerbapi.ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "list_containers",
		Config: map[string]any{"host": "unix://" + home + "/no-docker.sock"},
	}
	refusedHost := func(err error) bool {
		var connErr *cerbapi.ExternalConnectorError
		return errors.As(err, &connErr) && connErr.Code == cerbapi.ExternalConnectorInvalidArgs && strings.Contains(err.Error(), "refusing fields (host)")
	}

	svc, closeFn, err := newExternalConnectorService(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, ok := svc.(localConnectorExecutor); !ok {
		t.Fatalf("with no daemon, executor = %T, want the in-process lane", svc)
	}
	if _, err := svc.Execute(context.Background(), args); refusedHost(err) {
		t.Fatalf("in-process lane refused a local-only input: %v", err)
	}

	if _, err := app.NewExternalConnectorService(cfgPath).Execute(context.Background(), args); !refusedHost(err) {
		t.Fatalf("unmarked service: err = %v, want host refused", err)
	}
}

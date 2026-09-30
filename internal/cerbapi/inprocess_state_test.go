package cerbapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
)

// With HOME pointed away from the account's home, an in-process mutation is
// refused before it runs or is recorded anywhere; reads, dry runs and calls
// through the daemon are not affected (M11).
func TestAnInProcessMutationNeedsTheRealState(t *testing.T) {
	SetInProcessStateCheck(func() error { return errors.New(`HOME is "/tmp/x", but this account's home is "/Users/op"`) })
	t.Cleanup(func() { SetInProcessStateCheck(nil) })
	sink := audit.NewMemory()
	svc, backend := dockerLane(t, sink)
	inProcess := BeginRequest(WithPrincipal(context.Background(), Principal{Kind: PrincipalHuman, Via: ViaCLI}), SurfaceInProcess)

	_, err := svc.Execute(inProcess, devStop())
	if connectorErrorCode(err) != ExternalConnectorAuditUnavailable || !strings.Contains(err.Error(), `"/tmp/x"`) || backend.stopped != "" {
		t.Fatalf("an in-process stop: %v (stopped %q)", err, backend.stopped)
	}
	if len(sink.Records()) != 0 {
		t.Fatalf("the refused call was recorded in the wrong log: %d records", len(sink.Records()))
	}
	if _, err := svc.Execute(inProcess, ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers"}); err != nil {
		t.Fatalf("an in-process read: %v", err)
	}
	dry := devStop()
	dry.DryRun = true
	if _, err := svc.Execute(inProcess, dry); connectorErrorCode(err) == ExternalConnectorAuditUnavailable {
		t.Fatalf("an in-process dry run was refused: %v", err)
	}
	daemon := BeginRequest(WithPrincipal(context.Background(), Principal{Kind: PrincipalHuman, Via: ViaCLI}), SurfaceSocket)
	if _, err := svc.Execute(daemon, devStop()); err != nil || backend.stopped == "" {
		t.Fatalf("a stop through the daemon: %v", err)
	}
}

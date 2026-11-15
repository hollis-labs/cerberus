package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/standby"
)

func TestStandbyGuardSinkRefusesOutsideTheDaemon(t *testing.T) {
	root := t.TempDir()
	main := audit.NewMemory()
	paths := standby.PathsIn(root)
	g := standbyGuardSink{next: main, paths: paths}
	rec := audit.Record{Connector: "secrets", Operation: "set"}

	if _, err := g.Write(rec); err != nil {
		t.Fatalf("without the flag the write goes through: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, standby.FlagName), []byte("flagged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := g.Write(rec)
	if !errors.Is(err, audit.ErrUnavailable) {
		t.Fatalf("in standby the write is refused as unavailable, got %v", err)
	}
	var guidance redact.Renderer
	if !errors.As(err, &guidance) {
		t.Fatalf("the refusal carries guidance for the operator, got %T %v", err, err)
	}
	if n := len(main.Records()); n != 1 {
		t.Fatalf("the main sink got %d records, want only the one written before the flag", n)
	}

	daemonProcess.Store(true)
	t.Cleanup(func() { daemonProcess.Store(false) })
	if _, err := g.Write(rec); err != nil {
		t.Fatalf("the daemon's own sink is not guarded (its fence diverts it): %v", err)
	}
	daemonProcess.Store(false)

	if err := os.Remove(filepath.Join(root, standby.FlagName)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Write(rec); err != nil {
		t.Fatalf("after the flag is removed the write goes through again: %v", err)
	}
	if n := len(main.Records()); n != 3 {
		t.Fatalf("the main sink got %d records, want 3", n)
	}
}

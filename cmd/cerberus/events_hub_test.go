package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/eventexport"
	"github.com/hollis-labs/libs/mesh/eventbus"
)

// Without HOLLIS_EVENTS_URL there is no hub: the relay does not start, and a
// daemon with the switch on still runs exactly as it did.
func TestStartHubRelayDoesNothingWithoutAURL(t *testing.T) {
	t.Setenv(eventbus.EnvURL, "")
	root := t.TempDir()
	exporter, err := eventexport.New(eventexport.Config{
		AuditDir: filepath.Join(root, "audit"), ApprovalsDir: filepath.Join(root, "approvals"),
		OutboxDir: filepath.Join(root, "events"), Grace: time.Minute,
	})
	if err != nil {
		t.Skipf("exporter needs real stores in this environment: %v", err)
	}
	var wg sync.WaitGroup
	startHubRelay(t.Context(), &wg, slog.New(slog.NewTextHandler(io.Discard, nil)), exporter, root)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a goroutine was started although there is no hub URL")
	}
}

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/hollis-labs/cerberus/internal/userpresence"
)

// The daemon's user-presence check fails closed: with no cerberus-presence
// next to the binary (a test binary has none), it refuses as unavailable,
// naming the recovery, and never asks anyone or falls back to a claim.
func TestTheDaemonsPresenceCheckFailsClosed(t *testing.T) {
	err := newUserPresence(slog.New(slog.NewTextHandler(io.Discard, nil))).Verify(context.Background(), "allow a new passkey")
	if !errors.Is(err, userpresence.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

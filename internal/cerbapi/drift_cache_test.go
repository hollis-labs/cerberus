package cerbapi

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func newTestDriftCache() *DriftCache {
	return NewDriftCache(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestDriftCacheLookupMiss(t *testing.T) {
	d := newTestDriftCache()
	if _, ok := d.Lookup("missing"); ok {
		t.Fatal("Lookup on an empty cache should miss")
	}
}

func TestDriftCacheLookupFresh(t *testing.T) {
	d := newTestDriftCache()
	d.entries["api"] = driftEntry{
		installed:   true,
		stale:       true,
		staleReason: "repo_head_changed",
		updatedAt:   time.Now(),
	}
	art, ok := d.Lookup("api")
	if !ok {
		t.Fatal("a fresh entry should hit")
	}
	if !art.Installed || !art.Stale || art.StaleReason != "repo_head_changed" {
		t.Fatalf("unexpected artifact status: %+v", art)
	}
}

func TestDriftCacheLookupExpired(t *testing.T) {
	d := newTestDriftCache()
	d.entries["api"] = driftEntry{
		installed: true,
		stale:     true,
		updatedAt: time.Now().Add(-d.ttl - time.Second),
	}
	if _, ok := d.Lookup("api"); ok {
		t.Fatal("an entry older than the TTL should miss so callers fall back to the basic probe")
	}
}

func TestRuntimeLookupDriftWithoutCache(t *testing.T) {
	s := NewResourceRuntimeService()
	if _, ok := s.lookupDrift("anything"); ok {
		t.Fatal("lookupDrift should miss when no drift cache is attached")
	}
}

func TestRuntimeLookupDriftWithCache(t *testing.T) {
	s := NewResourceRuntimeService()
	d := newTestDriftCache()
	d.entries["api"] = driftEntry{installed: true, stale: true, staleReason: "source_changed", updatedAt: time.Now()}
	s.AttachDriftCache(d)

	art, ok := s.lookupDrift("api")
	if !ok {
		t.Fatal("lookupDrift should hit once a cache with a fresh entry is attached")
	}
	if !art.Stale || art.StaleReason != "source_changed" {
		t.Fatalf("unexpected artifact status: %+v", art)
	}
}

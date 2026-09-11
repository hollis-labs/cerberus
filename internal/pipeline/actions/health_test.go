package actions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthWaitProbesImmediately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	action := NewHealthWait("app", server.URL, time.Second)
	action.interval = time.Hour
	if err := action.Execute(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestHealthWaitRetriesUntilHealthy(t *testing.T) {
	var healthy atomic.Bool
	var once sync.Once
	unhealthyProbe := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		once.Do(func() { close(unhealthyProbe) })
	}))
	defer server.Close()
	action := NewHealthWait("app", server.URL, time.Second)
	action.interval = time.Millisecond
	result := make(chan error, 1)
	go func() { result <- action.Execute(context.Background(), nil) }()
	select {
	case <-unhealthyProbe:
	case err := <-result:
		t.Fatalf("returned before an unhealthy probe: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("health endpoint was never probed")
	}
	healthy.Store(true)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestHealthWaitTimeoutDuringPollInterval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	action := NewHealthWait("app", server.URL, 20*time.Millisecond)
	action.interval = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := action.Execute(ctx, nil); err == nil || !strings.Contains(err.Error(), "health check timed out") {
		t.Fatalf("expected health timeout, got %v", err)
	}
}

func TestHealthWaitCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- NewHealthWait("app", server.URL, time.Minute).Execute(ctx, nil) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("health endpoint was never probed")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("health wait ignored cancellation")
	}
}

package outpost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewTransportBoundaries(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic-review-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	var destinationCalls int
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls++; w.WriteHeader(200) }))
	defer destination.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, status) }))
		_, err := (Client{URL: server.URL, TokenFile: token}).Execute(context.Background(), Request{Verb: "units"})
		server.Close()
		if err == nil || destinationCalls != 0 {
			t.Fatalf("redirect followed or accepted: %v", err)
		}
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(Result{Output: "ok"})
	}))
	defer server.Close()
	if err := os.Chmod(token, 0644); err != nil { // #nosec G302 -- deliberately public fixture tests credential refusal.
		t.Fatal(err)
	}
	if _, err := (Client{URL: server.URL, TokenFile: token}).Execute(context.Background(), Request{Verb: "units"}); err == nil || calls != 0 {
		t.Fatal("public token reached server")
	}
	if err := os.Chmod(token, 0600); err != nil {
		t.Fatal(err)
	}
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer tls.Close()
	if _, err := (Client{URL: tls.URL, TokenFile: token}).Execute(context.Background(), Request{Verb: "units"}); err == nil || calls != 0 {
		t.Fatal("untrusted TLS accepted")
	}
	oversize := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1))) }))
	defer oversize.Close()
	if _, err := (Client{URL: oversize.URL, TokenFile: token}).Execute(context.Background(), Request{Verb: "units"}); err == nil {
		t.Fatal("oversized response accepted")
	}
	for _, endpoint := range []string{"http://example.com", "http://192.168.1.1", "https://user:pass@example.com", "https://example.com/path", "https://example.com?q=1"} {
		if err := ValidateEndpoint(endpoint); err == nil {
			t.Errorf("invalid endpoint accepted: %s", endpoint)
		}
	}
}

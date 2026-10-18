package outpost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/libs/security/secrets/tokenfile"
)

func TestCredentialFileRefusedBeforeTransport(t *testing.T) {
	for _, kind := range []string{"symlink", "public", "read-only", "oversized", "empty", "directory", "missing"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credential")
			if kind != "missing" {
				if err := os.WriteFile(path, []byte("synthetic-credential"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var setupErr error
			switch kind {
			case "symlink":
				setupErr = os.Symlink(path, path+"-link")
				path += "-link"
			case "public":
				setupErr = os.Chmod(path, 0644) // #nosec G302 -- deliberately insecure fixture.
			case "read-only":
				setupErr = os.Chmod(path, 0400)
			case "oversized":
				setupErr = os.WriteFile(path, []byte(strings.Repeat("x", tokenfile.DefaultMaxSize+1)), 0600)
			case "empty":
				setupErr = os.WriteFile(path, []byte(" \n"), 0600)
			case "directory":
				path = t.TempDir()
			}
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(Result{Output: "ok"})
			}))
			defer server.Close()
			registered := false
			client := Client{URL: server.URL, TokenFile: path, Credential: func(string) { registered = true }}
			_, err := client.Execute(context.Background(), Request{Verb: "probe"})
			if err == nil || registered || calls.Load() != 0 {
				t.Fatalf("invalid credential reached callback or network: err=%v registered=%v calls=%d", err, registered, calls.Load())
			}
			message := redact.ErrorText(err)
			if !strings.Contains(message, "mode 0600") || !strings.Contains(message, "no symlink") || strings.Contains(message, path) {
				t.Fatalf("recovery guidance lost or path disclosed: %s", message)
			}
		})
	}
}

func TestCredentialRotationIsReadOnEveryCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	seen := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(Result{Output: "ok"})
	}))
	defer server.Close()
	var registered string
	client := Client{URL: server.URL, TokenFile: path, Credential: func(value string) { registered = value }}
	for _, value := range []string{"synthetic-first-credential", "synthetic-rotated-credential"} {
		if err := tokenfile.Write(path, value); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Execute(context.Background(), Request{Verb: "probe"}); err != nil {
			t.Fatal(err)
		}
		if got := <-seen; got != "Bearer "+value || registered != value {
			t.Fatalf("rotation not observed by transport and redaction callback: transport=%q registered=%q", got, registered)
		}
	}
}

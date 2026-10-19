package secretref

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateFileCredentialThroughRuntimeResolver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	const canary = "fixture-secret-not-for-output-7275"
	if err := os.WriteFile(path, []byte(canary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(nil)
	env, err := resolver.ResolveEnv(context.Background(), map[string]string{"DB_PASSWORD": "file://" + path})
	if err != nil || env["DB_PASSWORD"] != canary {
		t.Fatal("runtime could not resolve private file reference")
	}
	if err = os.Chmod(path, 0o644); err != nil { //nolint:gosec // deliberately unsafe fixture must be refused
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), "file://"+path)
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatal("unsafe file accepted or value leaked")
	}
	if _, err = Parse("file://relative/runtime"); err == nil {
		t.Fatal("ambiguous managed file reference accepted")
	}
}

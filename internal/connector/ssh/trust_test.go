package ssh

import (
	"crypto/rand"
	"crypto/rsa"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestHostKeyConfigFromResourceConfigDefaults(t *testing.T) {
	cfg := hostKeyConfigFromResourceConfig(map[string]any{})
	if cfg.AllowInsecureHostKey {
		t.Fatal("allow_insecure_host_key defaulted to true")
	}
	if cfg.KnownHostsFile == "" {
		t.Fatal("expected default known_hosts file")
	}
}

func TestHostKeyCallbackMissingFile(t *testing.T) {
	_, err := (HostKeyConfig{KnownHostsFile: filepath.Join(t.TempDir(), "missing")}).callback("example.com", 22)
	if err == nil {
		t.Fatal("expected missing known_hosts error")
	}
	if !strings.Contains(err.Error(), "known_hosts file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHostKeyCallbackAllowsExplicitInsecure(t *testing.T) {
	cb, err := (HostKeyConfig{AllowInsecureHostKey: true}).callback("example.com", 22)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if cb == nil {
		t.Fatal("expected callback")
	}
}

func TestHostKeyCallbackVerifiesKnownHost(t *testing.T) {
	dir := t.TempDir()
	knownHostsPath := filepath.Join(dir, "known_hosts")

	signer, err := testSigner()
	if err != nil {
		t.Fatalf("build test signer: %v", err)
	}
	line := knownhosts.Line([]string{"example.com"}, signer.PublicKey())
	if err := os.WriteFile(knownHostsPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	cb, err := (HostKeyConfig{KnownHostsFile: knownHostsPath}).callback("example.com", 22)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}

	if err := cb("example.com:22", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}, signer.PublicKey()); err != nil {
		t.Fatalf("expected matching host key to verify: %v", err)
	}
}

func TestHostKeyCallbackRejectsWrongKey(t *testing.T) {
	dir := t.TempDir()
	knownHostsPath := filepath.Join(dir, "known_hosts")

	signerA, err := testSigner()
	if err != nil {
		t.Fatalf("build test signer A: %v", err)
	}
	signerB, err := testSigner()
	if err != nil {
		t.Fatalf("build test signer B: %v", err)
	}
	line := knownhosts.Line([]string{"example.com"}, signerA.PublicKey())
	if err := os.WriteFile(knownHostsPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	cb, err := (HostKeyConfig{KnownHostsFile: knownHostsPath}).callback("example.com", 22)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}

	if err := cb("example.com:22", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}, signerB.PublicKey()); err == nil {
		t.Fatal("expected mismatched host key to fail")
	}
}

func testSigner() (gossh.Signer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return gossh.NewSignerFromKey(key)
}

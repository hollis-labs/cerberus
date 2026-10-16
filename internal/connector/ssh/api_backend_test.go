package ssh

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestAPIBackendVerifiedHandshakeAndExec(t *testing.T) {
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := cryptossh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := cryptossh.NewSignerFromKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	key, err := cryptossh.MarshalPrivateKey(clientPrivate, "test")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath, knownPath := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(key), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Errorf("close listener: %v", closeErr)
		}
	}()
	if err = os.WriteFile(knownPath, []byte(knownhosts.Line([]string{listener.Addr().String()}, host.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	serverConfig := &cryptossh.ServerConfig{PublicKeyCallback: func(_ cryptossh.ConnMetadata, key cryptossh.PublicKey) (*cryptossh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
			return nil, fmt.Errorf("unexpected client key")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(host)
	served := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			served <- acceptErr
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		sc, channels, requests, handshakeErr := cryptossh.NewServerConn(conn, serverConfig)
		if handshakeErr != nil {
			served <- handshakeErr
			return
		}
		defer sc.Close()
		go cryptossh.DiscardRequests(requests)
		incoming := <-channels
		if incoming == nil {
			served <- fmt.Errorf("client closed before session")
			return
		}
		ch, reqs, channelErr := incoming.Accept()
		if channelErr != nil {
			served <- channelErr
			return
		}
		defer ch.Close()
		req := <-reqs
		if req == nil {
			served <- fmt.Errorf("client closed before exec")
			return
		}
		var payload struct{ Command string }
		if decodeErr := cryptossh.Unmarshal(req.Payload, &payload); decodeErr != nil || req.Type != "exec" || payload.Command != "echo verified" {
			served <- fmt.Errorf("unexpected exec request: %+v (decode: %s)", req, fmt.Sprint(decodeErr))
			return
		}
		_ = req.Reply(true, nil)
		_, _ = ch.Write([]byte("verified\n"))
		_, _ = ch.SendRequest("exit-status", false, cryptossh.Marshal(struct{ Status uint32 }{0}))
		served <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backend := NewAPIBackend()
	defer func() {
		if closeErr := backend.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Errorf("close backend: %v", closeErr)
		}
	}()
	if err = backend.Connect(ctx, "127.0.0.1", listener.Addr().(*net.TCPAddr).Port, "test", keyPath, HostKeyConfig{KnownHostsFile: knownPath}); err != nil {
		t.Fatal(err)
	}
	result, err := backend.Exec(ctx, "echo verified")
	if err != nil || result.Stdout != "verified" || result.ExitCode != 0 {
		t.Fatalf("exec failed: %+v %v", result, err)
	}
	if err = <-served; err != nil {
		t.Fatal(err)
	}
}

// handshakeServer accepts connections, completes the SSH handshake with the
// given host keys and closes. It stops when the listener closes.
func handshakeServer(t *testing.T, listener net.Listener, clientPub cryptossh.PublicKey, hostKeys ...cryptossh.Signer) {
	t.Helper()
	cfg := &cryptossh.ServerConfig{PublicKeyCallback: func(_ cryptossh.ConnMetadata, key cryptossh.PublicKey) (*cryptossh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), clientPub.Marshal()) {
			return nil, fmt.Errorf("unexpected client key")
		}
		return nil, nil
	}}
	for _, k := range hostKeys {
		cfg.AddHostKey(k)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if sc, _, reqs, err := cryptossh.NewServerConn(conn, cfg); err == nil {
					go cryptossh.DiscardRequests(reqs)
					_ = sc.Wait()
				}
			}()
		}
	}()
}

func connectWithKnownKey(t *testing.T, serverKeys []cryptossh.Signer, trusted cryptossh.PublicKey) error {
	t.Helper()
	_, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := cryptossh.NewSignerFromKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	pemKey, err := cryptossh.MarshalPrivateKey(clientPrivate, "test")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath, knownPath := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(pemKey), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close() //nolint:errcheck
	line := knownhosts.Line([]string{listener.Addr().String()}, trusted)
	if err = os.WriteFile(knownPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handshakeServer(t, listener, clientSigner.PublicKey(), serverKeys...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backend := NewAPIBackend()
	defer backend.Close() //nolint:errcheck
	return backend.Connect(ctx, "127.0.0.1", listener.Addr().(*net.TCPAddr).Port, "test", keyPath, HostKeyConfig{KnownHostsFile: knownPath})
}

func hostSigners(t *testing.T) (ecdsaKey, ed25519Key cryptossh.Signer) {
	t.Helper()
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if ecdsaKey, err = cryptossh.NewSignerFromKey(ec); err != nil {
		t.Fatal(err)
	}
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if ed25519Key, err = cryptossh.NewSignerFromKey(ed); err != nil {
		t.Fatal(err)
	}
	return ecdsaKey, ed25519Key
}

// known_hosts holds only the ed25519 key; the server also offers ecdsa. The
// connection must succeed on the ed25519 key rather than fail on a key type
// known_hosts never held.
func TestConnectUsesTheKeyTypeKnownHostsHolds(t *testing.T) {
	ec, ed := hostSigners(t)
	if err := connectWithKnownKey(t, []cryptossh.Signer{ec, ed}, ed.PublicKey()); err != nil {
		t.Fatalf("expected to verify against the ed25519 key known_hosts holds: %v", err)
	}
}

// The retry must not weaken verification: a changed key still fails.
func TestConnectStillRejectsAChangedKey(t *testing.T) {
	ec, ed := hostSigners(t)
	_, otherEd := hostSigners(t)
	if err := connectWithKnownKey(t, []cryptossh.Signer{ec, ed}, otherEd.PublicKey()); err == nil {
		t.Fatal("expected a changed host key to be rejected")
	}
}

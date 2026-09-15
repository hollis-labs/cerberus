package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
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
	defer listener.Close()
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
	defer backend.Close()
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

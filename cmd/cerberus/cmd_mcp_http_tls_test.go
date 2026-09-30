package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCert(t *testing.T, dir, cn string, dns []string) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: dns, IPAddresses: []net.IP{net.ParseIP("10.0.0.5")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// The certificate's names join the Host allow-list, and a renewed
// certificate is served without a restart; a half-written one keeps the
// old serving.
func TestMCPHTTPTLSCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeCert(t, dir, "one", []string{"cerberus.example"})
	hosts, err := certHosts(certFile)
	if err != nil || strings.Join(hosts, ",") != "cerberus.example,10.0.0.5" {
		t.Fatalf("hosts: %v %v", hosts, err)
	}
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := r.GetCertificate(nil)
	time.Sleep(20 * time.Millisecond)
	writeCert(t, dir, "two", []string{"cerberus.example"})
	second, _ := r.GetCertificate(nil)
	if first == second {
		t.Fatal("a renewed certificate was not picked up")
	}
	if err = os.WriteFile(certFile, []byte("half-written"), 0o600); err != nil {
		t.Fatal(err)
	}
	if third, gerr := r.GetCertificate(nil); gerr != nil || third != second {
		t.Fatalf("a half-written renewal: %v", gerr)
	}
	if _, err = newCertReloader(filepath.Join(dir, "none.pem"), keyFile); err == nil {
		t.Fatal("a missing certificate loaded")
	}
}

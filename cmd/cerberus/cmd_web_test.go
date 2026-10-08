package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/webui"
	"github.com/spf13/cobra"
)

func TestWebOpenUsesRunningConsolePublicURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	savedListen, savedBrowser := webListenAddr, webOpenBrowser
	t.Cleanup(func() { webListenAddr, webOpenBrowser = savedListen, savedBrowser })
	webListenAddr, webOpenBrowser = "127.0.0.1:4783", false
	srv, err := webui.New(nil, audit.NewMemory(), "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	remove, err := srv.WriteLoginKey(webui.LoginKeyPath(home, webListenAddr), "https://cerberus.example")
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if checkErr := webOpenCmd.RunE(cmd, nil); checkErr != nil {
		t.Fatal(checkErr)
	}
	if !strings.Contains(out.String(), "https://cerberus.example/login?token=") || strings.Contains(out.String(), "localhost") {
		t.Fatalf("web open did not use public URL: %s", out.String())
	}
}

// A bad public URL is refused before startup attempts to contact a daemon.
func TestWebRefusesInvalidPublicURLBeforeStartup(t *testing.T) {
	savedConfig, savedListen := cfgPath, webListenAddr
	t.Cleanup(func() { cfgPath, webListenAddr = savedConfig, savedListen })
	cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	webListenAddr = "127.0.0.1:4783"
	if err := os.WriteFile(cfgPath, []byte("version: 2\nweb:\n  public_url: http://cerberus.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := webCmd.RunE(&cobra.Command{}, nil); err == nil || !strings.Contains(err.Error(), "web.public_url") {
		t.Fatalf("invalid origin reached daemon startup: %v", err)
	}
}

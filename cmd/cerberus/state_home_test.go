package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealStateCheck(t *testing.T) {
	saved := accountHome
	t.Cleanup(func() { accountHome = saved })
	accountDir := t.TempDir()
	accountHome = func() (string, error) { return accountDir, nil }

	t.Setenv("HOME", accountDir)
	if err := realStateCheck(); err != nil {
		t.Fatalf("the real home: %v", err)
	}
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(accountDir, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", link)
	if err := realStateCheck(); err != nil {
		t.Fatalf("a symlink to the real home: %v", err)
	}
	scratch := t.TempDir()
	t.Setenv("HOME", scratch)
	err := realStateCheck()
	if err == nil || !strings.Contains(err.Error(), scratch) || !strings.Contains(err.Error(), accountDir) || !strings.Contains(err.Error(), "without --config") {
		t.Fatalf("a scratch home: %v", err)
	}
	accountHome = func() (string, error) { return "", errors.New("no such uid") }
	t.Setenv("HOME", accountDir)
	if err := realStateCheck(); err == nil || !strings.Contains(err.Error(), "could not be looked up") {
		t.Fatalf("an unknown account: %v", err)
	}
}

// On this machine, the account's home by uid is a real directory.
func TestAccountHomeByUID(t *testing.T) {
	home, err := accountHome()
	if err != nil {
		t.Skipf("no account lookup here: %v", err)
	}
	if info, statErr := os.Stat(home); statErr != nil || !info.IsDir() {
		t.Fatalf("account home %q: %v", home, statErr)
	}
}

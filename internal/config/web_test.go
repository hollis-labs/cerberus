package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeWebPublicURL(t *testing.T) {
	for raw, want := range map[string]string{"": "", "https://cerberus.example/": "https://cerberus.example", "https://CERBERUS.example:443": "https://cerberus.example", "https://cerberus.example:8443/": "https://cerberus.example:8443"} {
		got, err := NormalizeWebPublicURL(raw)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"http://cerberus.example", "https://localhost", "https://127.0.0.1", "https://[::1]", "https://user:password@cerberus.example", "https://cerberus.example/ui", "https://cerberus.example/?token=x", "https://cerberus.example?", "https://cerberus.example/#fragment", "https://cerberus.example/#", "https://cerberus.example:0", "https://cerberus.example:65536", "https://cerberus.example:", "https://bad_host", "https://-bad.example", "//cerberus.example", "https://cerberus.example/%2f", " https://cerberus.example"} {
		if _, err := NormalizeWebPublicURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestLoadWebPublicURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	for _, valid := range []bool{true, false} {
		base := "https://cerberus.example/"
		if !valid {
			base = "http://cerberus.example"
		}
		if err := os.WriteFile(path, []byte("version: 2\nweb:\n  public_url: "+base+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadUnified(path)
		if !valid {
			if err == nil {
				t.Fatal("accepted insecure public URL")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Web == nil || cfg.Web.PublicURL != "https://cerberus.example" {
			t.Fatalf("web: %+v", cfg.Web)
		}
	}
}

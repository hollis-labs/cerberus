package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadConfigRejectsTrailingData(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{StateDir: dir, CredentialsFile: filepath.Join(dir, "pairings.json"), Plugins: []Plugin{{ID: "example", Command: filepath.Join(dir, "plugin"), Dir: dir}}}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", " \n\t", " {}", " null", " junk"} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "inventory.json")
			if err := os.WriteFile(path, append(body[:len(body):len(body)], suffix...), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadConfig(path)
			if suffix == "" || suffix == " \n\t" {
				if err != nil {
					t.Fatal(err)
				}
				if got.Address != "127.0.0.1:8977" {
					t.Fatalf("default address = %q", got.Address)
				}
			} else if err == nil {
				t.Fatal("accepted trailing configuration data")
			}
		})
	}
}

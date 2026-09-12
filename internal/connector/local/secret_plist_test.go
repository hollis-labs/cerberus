package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretReferencePlistDoctorDetectsStaleGenerator(t *testing.T) {
	spec := ProcessSpec{Env: map[string]string{"API_KEY": "keychain://provider/item"}}
	path := filepath.Join(t.TempDir(), "service.plist")
	for _, tc := range []struct {
		name      string
		args      []string
		env       string
		wantError string
	}{
		{"current", []string{"/managed/cerberus", "run-secrets", "--", "/managed/app"}, "keychain://provider/item", ""},
		{"old generator", []string{"/managed/app"}, "keychain://provider/item", "run-secrets"},
		{"misleading argument", []string{"/managed/app", "--message", "run-secrets"}, "keychain://provider/item", "run-secrets"},
		{"literal on disk", []string{"/managed/cerberus", "run-secrets", "--", "/managed/app"}, "synthetic-private-value", "API_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := renderLaunchdPlist(plistTemplateData{ProgramArguments: tc.args, Environment: map[string]string{"API_KEY": tc.env}, EnvironmentEntries: []plistEnvEntry{{Key: "API_KEY", Value: tc.env}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			err = CheckSecretReferencePlist(path, spec)
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected actionable diagnosis: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-private-value") {
				t.Fatal("doctor exposed credential")
			}
		})
	}
}

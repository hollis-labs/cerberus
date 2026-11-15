package supervise_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// Supervise mode's digest key against the applied digests: a fresh store records the key it
// creates; migrated digests need the migrated key, by its recorded fingerprint, and a missing
// or different key refuses the start without creating one.
func TestAppliedDigestKeyMustBeTheMigratedOne(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	type setup struct {
		digests  bool
		recorded string // "migrated", "other" or ""
		key      string // "migrated", "other" or "" (absent)
	}
	migratedDir, otherDir := t.TempDir(), t.TempDir()
	migrated, err := audit.OpenDigester(migratedDir)
	if err != nil {
		t.Fatal(err)
	}
	other, err := audit.OpenDigester(otherDir)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := map[string]string{"migrated": migratedDir, "other": otherDir}
	fingerprint := map[string]string{"migrated": migrated.Fingerprint(), "other": other.Fingerprint()}
	for _, c := range []struct {
		name string
		s    setup
		want string // "" for a start
	}{
		{"fresh store, no key", setup{}, ""},
		{"migrated digests and key", setup{digests: true, recorded: "migrated", key: "migrated"}, ""},
		{"migrated digests, key not copied", setup{digests: true, recorded: "migrated"}, "missing"},
		{"migrated digests, another key", setup{digests: true, recorded: "migrated", key: "other"}, "not the key"},
		{"digests, no fingerprint recorded", setup{digests: true, key: "migrated"}, "not the key"},
		{"no digests, recorded key replaced", setup{recorded: "migrated", key: "other"}, "not the key"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := openStore(t, filepath.Join(t.TempDir(), "cerberus.db"))
			auditDir := filepath.Join(t.TempDir(), "audit")
			if c.s.key != "" {
				if err := os.MkdirAll(auditDir, 0o700); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(keyFile[c.s.key], ".digest_key"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(auditDir, ".digest_key"), raw, 0o600); err != nil { //nolint:gosec // the test's own temp dir
					t.Fatal(err)
				}
			}
			if c.s.digests {
				if err := storage.SetApplied(ctx, st, "web", migrated.Digest("web"), now); err != nil {
					t.Fatal(err)
				}
			}
			if c.s.recorded != "" {
				if err := storage.SetAppliedKey(ctx, st, fingerprint[c.s.recorded], now); err != nil {
					t.Fatal(err)
				}
			}
			d, err := supervise.OpenAppliedKey(ctx, st, auditDir, now)
			if c.want != "" {
				if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "supervise refuses to start") {
					t.Fatalf("err = %v, want a refusal naming %q", err, c.want)
				}
				if c.s.key == "" {
					if _, statErr := os.Stat(filepath.Join(auditDir, ".digest_key")); !os.IsNotExist(statErr) {
						t.Fatal("a refused start created a digest key")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			fp, ok, err := storage.AppliedKey(ctx, st)
			if err != nil || !ok || fp != d.Fingerprint() {
				t.Fatalf("recorded %q %v %v, want %q", fp, ok, err, d.Fingerprint())
			}
			if _, err := supervise.OpenAppliedKey(ctx, st, auditDir, now); err != nil {
				t.Fatalf("a second start: %v", err)
			}
		})
	}
}

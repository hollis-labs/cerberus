package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	auth "github.com/hollis-labs/libs/security/auth"
)

// FileVerifier reloads the operator-owned pairing records on every request so
// removing a pairing revokes access without restarting any plugin.
type FileVerifier struct{ Path string }

func (v FileVerifier) Verify(ctx context.Context, token string) (auth.Principal, error) {
	keys, err := readKeys(v.Path)
	if err != nil {
		return auth.Principal{}, auth.ErrUnavailable
	}
	return auth.NewAPIKeyVerifier(keys...).Verify(ctx, token)
}

func readKeys(path string) ([]auth.APIKey, error) {
	b, err := os.ReadFile(path) //nolint:gosec // The local operator selects the pairing store; no request controls this path.
	if err != nil {
		return nil, err
	}
	var keys []auth.APIKey
	if err := json.Unmarshal(b, &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

// Pair is a local operator operation. Only a hash is kept at Outpost; the
// credential goes into a new private file for the named controller to receive
// through the operator's authenticated deployment channel.
func Pair(cfg Config, id, plugin, output string) error {
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	storePath, err := filepath.Abs(cfg.CredentialsFile)
	if err != nil {
		return err
	}
	if outputPath == storePath {
		return fmt.Errorf("credential output must differ from the pairing store")
	}
	unlock, err := pairingLock(cfg.CredentialsFile)
	if err != nil {
		return err
	}
	defer unlock()
	if !identifier.MatchString(id) {
		return fmt.Errorf("invalid pairing id")
	}
	found := false
	for _, p := range cfg.Plugins {
		if p.ID == plugin {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("plugin %q is not configured", plugin)
	}
	keys, err := readKeys(cfg.CredentialsFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, k := range keys {
		if k.ID == id {
			return fmt.Errorf("pairing %q already exists; revoke it before replacing", id)
		}
	}
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(secret[:])
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) //nolint:gosec // Operator-selected output is created exclusively with private permissions.
	if err != nil {
		return err
	}
	_, err = f.WriteString(token + "\n")
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(output)
		return fmt.Errorf("write pairing credential: %w", errors.Join(err, closeErr))
	}
	keys = append(keys, auth.APIKey{Hash: auth.HashKey(token), ID: id, Scopes: []string{"outpost." + plugin}})
	if err := writeKeys(cfg.CredentialsFile, keys); err != nil {
		_ = os.Remove(output)
		return err
	}
	return nil
}

func Revoke(cfg Config, id string) error {
	unlock, err := pairingLock(cfg.CredentialsFile)
	if err != nil {
		return err
	}
	defer unlock()
	keys, err := readKeys(cfg.CredentialsFile)
	if err != nil {
		return err
	}
	kept := keys[:0]
	found := false
	for _, k := range keys {
		if k.ID == id {
			found = true
		} else {
			kept = append(kept, k)
		}
	}
	if !found {
		return fmt.Errorf("pairing %q not found", id)
	}
	return writeKeys(cfg.CredentialsFile, kept)
}

// Local administrative writers serialize the entire read/modify/write. A
// concurrent pairing must fail visibly rather than silently lose a grant.
func pairingLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := path + ".lock"
	if err := os.Mkdir(lock, 0700); err != nil {
		return nil, fmt.Errorf("pairing store is locked; finish the other local pairing operation before retrying: %w", err)
	}
	return func() { _ = os.Remove(lock) }, nil
}

func writeKeys(path string, keys []auth.APIKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pairing-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func pluginID(path string) string {
	rest, ok := strings.CutPrefix(path, "/api/plugins/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}

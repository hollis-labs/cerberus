package secrets

import (
	"sync/atomic"

	"github.com/zalando/go-keyring"
)

// The OS credential store calls, as variables so a test can replace the store
// with one that never answers. Everything goes through KeyringCall, which
// bounds the wait.
var (
	keyringGet    = keyring.Get
	keyringSet    = keyring.Set
	keyringDelete = keyring.Delete
)

// KeyringGet reads an entry, bounded by KeyringTimeout.
func KeyringGet(service, user string) (string, error) {
	return KeyringCall("read "+user, func() (string, error) { return keyringGet(service, user) })
}

// KeyringSet writes an entry, bounded by KeyringTimeout.
func KeyringSet(service, user, value string) error {
	_, err := KeyringCall("write "+user, func() (struct{}, error) { return struct{}{}, keyringSet(service, user, value) })
	return err
}

// KeyringDelete removes an entry, bounded by KeyringTimeout.
func KeyringDelete(service, user string) error {
	_, err := KeyringCall("delete "+user, func() (struct{}, error) { return struct{}{}, keyringDelete(service, user) })
	return err
}

// BlockCredentialStoreForTests replaces the OS credential store with one whose
// every call blocks until release is closed, the way a locked Secret Service
// does when nobody can answer its unlock prompt. calls counts the calls made.
// For tests of the daemon's startup; it also marks the store as mocked.
func BlockCredentialStoreForTests(release <-chan struct{}, calls *atomic.Int64) {
	block := func() {
		if calls != nil {
			calls.Add(1)
		}
		<-release
	}
	keyringGet = func(string, string) (string, error) { block(); return "", keyring.ErrNotFound }
	keyringSet = func(string, string, string) error { block(); return nil }
	keyringDelete = func(string, string) error { block(); return nil }
	storeMocked.Store(true)
}

package secrets

import (
	"context"
	"os"
)

// Presence is whether a secret has a value, as the credential editor shows
// it, found without resolving anything.
type Presence string

const (
	// PresenceMissing is a secret with no value anywhere in the chain.
	PresenceMissing Presence = "missing"
	// PresenceStored is a value held in the environment or the keychain.
	PresenceStored Presence = "stored"
	// PresenceReference is a reference (op://, keeper://, keyring://, …)
	// that would be resolved on use. It is reported as a reference and never
	// followed: a vault is not called to draw a page.
	PresenceReference Presence = "reference"
)

// PresenceReader reports a secret's presence without resolving it.
type PresenceReader interface {
	Presence(ctx context.Context, service, key string) (Presence, error)
}

// Presence walks the chain Get walks — a binding, the environment, the
// keychain — and stops at the first value, reporting a reference as one
// rather than resolving it.
func (p *ReferenceProvider) Presence(ctx context.Context, service, key string) (Presence, error) {
	file, err := p.Bindings()
	if err != nil {
		return PresenceMissing, err
	}
	// The same precedence as Get: a labeled binding decides; otherwise the
	// mapping's reference, overridden by the environment, then the keychain.
	b := file[service].Resolve(service, key, nil)
	value := b.Ref
	if b.Label == "" {
		if env := os.Getenv(envVarName(service, key)); env != "" {
			value = env
		}
		if value == "" && p.base != nil {
			if value, err = p.base.Get(ctx, service, key); err != nil {
				return PresenceMissing, err
			}
		}
	}
	switch {
	case value == "":
		return PresenceMissing, nil
	case p.resolver.IsRef(value):
		return PresenceReference, nil
	}
	return PresenceStored, nil
}

// Presence forwards to the wrapped reader, registering nothing: no value is
// resolved, so there is none to register.
func (p registeringProvider) Presence(ctx context.Context, service, key string) (Presence, error) {
	if r, ok := p.Reader.(PresenceReader); ok {
		return r.Presence(ctx, service, key)
	}
	return presenceByReading(ctx, p.Reader, service, key)
}

// presenceByReading is presence for a reader that has no chain of its own
// (an in-memory store, a keychain): reading it resolves nothing.
func presenceByReading(ctx context.Context, r interface {
	Get(ctx context.Context, service, key string) (string, error)
}, service, key string) (Presence, error) {
	value, err := r.Get(ctx, service, key)
	if err != nil || value == "" {
		return PresenceMissing, err
	}
	return PresenceStored, nil
}

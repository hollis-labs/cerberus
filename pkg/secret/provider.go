package secret

import "context"

// Reader resolves connector secrets such as API keys and tokens. Resolution —
// a connector's credential, a plugin's declared secret — depends on Reader
// alone: a backend that can only be read, such as a vault Cerberus is granted
// read access to, satisfies it without pretending to be writable.
type Reader interface {
	Get(ctx context.Context, service, key string) (string, error)
}

// ReadWriter is a Reader that can also store and remove entries. Only entry
// management — the console's credential form — needs it; everything that
// resolves takes a Reader.
type ReadWriter interface {
	Reader
	Set(ctx context.Context, service, key, value string) error
	Delete(ctx context.Context, service, key string) error
}

// Provider is the pre-split name of ReadWriter.
//
// Deprecated: take a Reader to resolve, or a ReadWriter to manage entries.
type Provider = ReadWriter

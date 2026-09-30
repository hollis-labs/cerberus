package domain

import "github.com/hollis-labs/cerberus/pkg/secret"

// SecretProvider resolves secrets (API keys, tokens, etc.). It is read-only:
// the default implementation reads the process environment, the connector
// reference mapping and the OS keychain, and writing an entry is the console's
// business alone (secret.ReadWriter).
type SecretProvider = secret.Reader

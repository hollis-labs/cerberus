package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// SecretBackend declares that a plugin resolves secret references of one
// scheme for the host: op:// for a 1Password backend, keeper:// for Keeper.
//
// A backend is an ordinary installed, reviewed, hash-checked plugin. What
// the declaration adds is that the host routes <scheme>:// references to it,
// so the install review says plainly that it will see every secret resolved
// through that scheme. Its own credential still arrives over the declared
// secret channel, resolved by the host through the core chain alone (the
// environment, the reference mapping and the OS credential store), never
// through another backend.
type SecretBackend struct {
	// Scheme is the reference scheme the plugin resolves, without "://".
	Scheme string `json:"scheme" yaml:"scheme"`
	// Reference shows the reference shape, for the review and for errors,
	// for example "op://<vault>/<item>/<field>".
	Reference string `json:"reference,omitempty" yaml:"reference,omitempty"`
}

// ResolveCommand is the plugin-sdk command/execute name the host sends a
// secret backend to resolve one reference. The argument is the JSON object
// ResolveArgs; a success is Action "message" with the value as Content, and a
// failure is Action "error" with a coded error payload (ErrorResult's
// content) as Content.
//
// It is a command, not a connector operation, so it is never a CLI command,
// an API operation or an MCP tool: nothing but the host's own secret
// resolution can reach it.
const ResolveCommand = "cerberus.secret/resolve"

// ResolveArgs is the argument of ResolveCommand.
type ResolveArgs struct {
	Ref string `json:"ref"`
}

// Resolve command result actions.
const (
	ResolveActionValue = "message"
	ResolveActionError = "error"
)

// ReservedSchemes are the schemes no plugin may claim: the host's own
// (keychain and its platform-neutral alias keyring, helper, env) and schemes
// that name something other than a secret store.
var ReservedSchemes = []string{"keychain", "keyring", "helper", "env", "file", "http", "https"}

var schemePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,15}$`)

// Validate reports what is wrong with the declaration.
func (b SecretBackend) Validate() []string {
	var problems []string
	switch {
	case !schemePattern.MatchString(b.Scheme):
		problems = append(problems, fmt.Sprintf("secret_backend scheme %q must be 2 to 16 lowercase letters, digits or hyphens, starting with a letter", b.Scheme))
	case IsReservedScheme(b.Scheme):
		problems = append(problems, fmt.Sprintf("secret_backend scheme %q is reserved (%s)", b.Scheme, strings.Join(ReservedSchemes, ", ")))
	}
	if b.Reference != "" && !strings.HasPrefix(b.Reference, b.Scheme+"://") {
		problems = append(problems, fmt.Sprintf("secret_backend reference %q must begin %s://", b.Reference, b.Scheme))
	}
	return problems
}

// IsReservedScheme reports whether scheme is one no plugin may claim.
func IsReservedScheme(scheme string) bool {
	for _, reserved := range ReservedSchemes {
		if scheme == reserved {
			return true
		}
	}
	return false
}

// ResolveFailure reads a coded failure from a resolve command's error
// content. ok is false when the content is not a coded payload.
func ResolveFailure(content string) (code ErrorCode, message string, ok bool) {
	var payload errorPayload
	if err := json.Unmarshal([]byte(content), &payload); err != nil || payload.Error.Code == "" {
		return "", "", false
	}
	return payload.Error.Code, payload.Error.Message, true
}

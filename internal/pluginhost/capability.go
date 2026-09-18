package pluginhost

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// CapabilityRequest is the SDK's declaration type. Aliased rather than
// redefined: the shape is the shared contract's, and a second copy would
// drift.
type CapabilityRequest = subprocess.CapabilityRequest

// Capability names Cerberus understands.
//
// The SDK carries the declaration mechanism and defines no names, deliberately:
// a capability name is host vocabulary and one host's names mean nothing to
// another. These are ours.
//
// Each name maps to the environment variables a plugin needs in order to use
// it. Nothing else is granted by naming a capability, and a plugin that
// declares none receives none.
const (
	// CapabilitySSHAgent forwards the operator's SSH agent socket. This is a
	// live credential handle, not a credential-shaped string: a plugin holding
	// it authenticates as the operator to every host that trusts their key,
	// without ever seeing the key. Grant it only to a plugin that genuinely
	// reaches remote hosts on the operator's behalf.
	CapabilitySSHAgent = "ssh_agent"

	// CapabilityDockerSocket forwards the Docker client's target and
	// configuration. Socket access to a Docker daemon is root-equivalent on
	// most hosts.
	CapabilityDockerSocket = "docker_socket"
)

// capabilityEnv maps each capability to the environment variables it unlocks.
// A variable appears here only because some capability unlocks it; a variable
// every plugin may have belongs in the base launch environment instead.
var capabilityEnv = map[string][]string{
	CapabilitySSHAgent:     {"SSH_AUTH_SOCK"},
	CapabilityDockerSocket: {"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG"},
}

// KnownCapabilities returns the capability names this host recognizes, sorted.
// Exposed so an error can tell an author what is available rather than only
// that their name was not.
func KnownCapabilities() []string {
	out := make([]string, 0, len(capabilityEnv))
	for name := range capabilityEnv {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// GrantCapabilities decides what a plugin actually gets.
//
// Today the rule is simple: a capability the plugin declared and this host
// recognizes is granted. That is a deliberate first step rather than the
// finished design — it removes the ambient grant, where every plugin received
// the SSH agent whether or not it asked, and it makes what each plugin holds
// visible in `plugin managed list`. It does not yet require an operator to
// approve the grant, so a plugin that declares a capability receives it on the
// strength of its own manifest.
//
// What that buys is real but bounded: the manifest is reviewable before the
// plugin runs, and installing a plugin is already an explicit operator act.
// What it does not buy is protection from a plugin that declares a capability
// it has no business having. Operator-held approval is the next step.
func GrantCapabilities(requests []CapabilityRequest) []string {
	if len(requests) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(requests))
	granted := make([]string, 0, len(requests))
	for _, req := range requests {
		name := strings.TrimSpace(req.Name)
		if name == "" || seen[name] {
			continue
		}
		if _, ok := capabilityEnv[name]; !ok {
			// Unknown names are refused at install by ValidateCapabilities, so
			// reaching here means the vocabulary shrank under an installed
			// plugin. Declining is the safe reading.
			continue
		}
		seen[name] = true
		granted = append(granted, name)
	}
	sort.Strings(granted)
	return granted
}

// CapabilityEnv returns the environment entries the granted capabilities
// unlock, read from the host's own environment.
//
// A variable that is unset in the host is simply absent: granting a capability
// cannot invent a value, and passing an empty one would be worse than passing
// nothing, since a plugin cannot tell an empty socket path from a missing one.
func CapabilityEnv(granted []string) []string {
	var out []string
	for _, name := range granted {
		for _, key := range capabilityEnv[name] {
			if value, ok := os.LookupEnv(key); ok && value != "" {
				out = append(out, key+"="+value)
			}
		}
	}
	return out
}

// ValidateCapabilities rejects a manifest whose capability declarations this
// host cannot honor. Refusing at install is the point: a plugin that asks for
// something misspelled should be told so while an operator is watching, not
// silently run without it and fail later in a way that looks like a bug.
func ValidateCapabilities(requests []CapabilityRequest) error {
	seen := make(map[string]bool, len(requests))
	for _, req := range requests {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			return fmt.Errorf("capability declaration has an empty name")
		}
		if seen[name] {
			return fmt.Errorf("capability %q is declared more than once", name)
		}
		seen[name] = true
		if _, ok := capabilityEnv[name]; !ok {
			return fmt.Errorf("unknown capability %q: this host understands %s",
				name, strings.Join(KnownCapabilities(), ", "))
		}
	}
	return nil
}

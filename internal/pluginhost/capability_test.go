package pluginhost

import (
	"os"
	"strings"
	"testing"
)

func TestGrantCapabilitiesGrantsOnlyDeclaredAndKnown(t *testing.T) {
	cases := []struct {
		name     string
		requests []CapabilityRequest
		want     string
	}{
		{"nothing declared", nil, ""},
		{"empty slice", []CapabilityRequest{}, ""},
		{"one known", []CapabilityRequest{{Name: CapabilitySSHAgent}}, CapabilitySSHAgent},
		// Sorted, not declaration-ordered: the grant is recorded in state and
		// reported to the operator, so it should not change shape because an
		// author reordered their manifest.
		{"both known, sorted", []CapabilityRequest{{Name: CapabilitySSHAgent}, {Name: CapabilityDockerSocket}},
			CapabilityDockerSocket + "," + CapabilitySSHAgent},
		// Unknown names are refused at install; if one reaches here the
		// vocabulary shrank under an installed plugin, and declining is safe.
		{"unknown declines", []CapabilityRequest{{Name: "nonexistent"}}, ""},
		{"duplicate collapses", []CapabilityRequest{{Name: CapabilitySSHAgent}, {Name: CapabilitySSHAgent}}, CapabilitySSHAgent},
		{"empty name ignored", []CapabilityRequest{{Name: ""}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(GrantCapabilities(tc.requests), ",")
			if got != tc.want {
				t.Errorf("GrantCapabilities = %q, want %q", got, tc.want)
			}
		})
	}
}

// The point of the whole change: a plugin that declared nothing must not be
// handed a credential handle. This is the regression test CERB-GAP-334 asked
// for, in the form that actually catches the defect — CERB-GAP-837 showed that
// asserting no allow-list entry "looks like a credential" passes while
// SSH_AUTH_SOCK is present, because an agent socket is not credential-shaped.
func TestUndeclaredPluginReceivesNoCredentialHandle(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	t.Setenv("DOCKER_CONFIG", "/tmp/docker")

	env := CapabilityEnv(GrantCapabilities(nil))
	if len(env) != 0 {
		t.Fatalf("a plugin declaring nothing received %v", env)
	}
}

func TestDeclaredCapabilityUnlocksExactlyItsVariables(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")

	env := CapabilityEnv(GrantCapabilities([]CapabilityRequest{{Name: CapabilitySSHAgent}}))
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, "SSH_AUTH_SOCK=/tmp/agent.sock") {
		t.Errorf("ssh_agent did not unlock SSH_AUTH_SOCK: %v", env)
	}
	// Granting one capability must not leak another's variables.
	if strings.Contains(joined, "DOCKER_HOST") {
		t.Errorf("ssh_agent leaked a docker variable: %v", env)
	}
}

// Granting cannot invent a value. An empty variable is worse than an absent
// one, because a plugin cannot tell an empty socket path from a missing one.
func TestCapabilityEnvSkipsVariablesTheHostDoesNotHave(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	if env := CapabilityEnv([]string{CapabilitySSHAgent}); len(env) != 0 {
		t.Errorf("empty host variable was forwarded: %v", env)
	}
	os.Unsetenv("SSH_AUTH_SOCK")
	if env := CapabilityEnv([]string{CapabilitySSHAgent}); len(env) != 0 {
		t.Errorf("unset host variable was forwarded: %v", env)
	}
}

func TestValidateCapabilitiesRefusesWhatTheHostCannotHonour(t *testing.T) {
	cases := []struct {
		name     string
		requests []CapabilityRequest
		wantErr  string
	}{
		{"none", nil, ""},
		{"known", []CapabilityRequest{{Name: CapabilitySSHAgent}}, ""},
		{"unknown", []CapabilityRequest{{Name: "ssh-agent"}}, "unknown capability"},
		{"empty name", []CapabilityRequest{{Name: ""}}, "empty name"},
		{"duplicate", []CapabilityRequest{{Name: CapabilitySSHAgent}, {Name: CapabilitySSHAgent}}, "more than once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCapabilities(tc.requests)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}

	// A misspelling should be told what is available, not just that it was wrong.
	err := ValidateCapabilities([]CapabilityRequest{{Name: "ssh-agent"}})
	if err == nil || !strings.Contains(err.Error(), CapabilitySSHAgent) {
		t.Errorf("error does not name the available capabilities: %v", err)
	}
}

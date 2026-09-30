package main

import (
	"errors"
	"testing"
)

// An agent's shell has no terminal: the command refuses before it reads
// anything or touches the store.
func TestSecretsSetRefusesWithoutATerminal(t *testing.T) {
	saved := policyIsTerminal
	t.Cleanup(func() { policyIsTerminal = saved })
	policyIsTerminal = func() bool { return false }
	err := secretsSetCmd.RunE(secretsSetCmd, []string{"keeper/ksm_config"})
	if !errors.Is(err, errSecretsNotInteractive) {
		t.Fatalf("err = %v", err)
	}
}

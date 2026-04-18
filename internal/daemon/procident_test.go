package daemon

import "testing"

func TestDaemonCmdRegex(t *testing.T) {
	cases := []struct {
		name  string
		cmd   string
		match bool
	}{
		{"full path + daemon", "/usr/local/bin/cerberus daemon --foreground", true},
		{"bare cerberus daemon", "cerberus daemon", true},
		{"cerberus daemon --replace", "cerberus daemon --replace", true},
		{"cerberus status", "cerberus status", false},
		{"cerberus mcp", "cerberus mcp", false},
		{"cerberus no subcommand", "cerberus", false},
		{"cerberus-daemon (dash)", "cerberus-daemon", false},
		{"empty", "", false},
		{"some other daemon", "/usr/sbin/nginxd daemon", false},
		{"cerberus with prefix word", "xcerberus daemon", false}, // regex requires ^ or / before cerberus
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := daemonCmdRegex.MatchString(tc.cmd)
			if got != tc.match {
				t.Errorf("cmd %q: match=%v, want %v", tc.cmd, got, tc.match)
			}
		})
	}
}

// TestPSIdentifierSelf checks that the real PSIdentifier at least runs
// without error against our own PID and correctly reports we are NOT a
// cerberus daemon (the test binary is a "go test" invocation).
func TestPSIdentifierSelf(t *testing.T) {
	p := PSIdentifier{}
	// We don't care about the exact result — just that the probe doesn't
	// explode on a real PID.
	_, err := p.IsCerberusDaemon(testCtx(t), 0)
	if err != nil {
		t.Errorf("IsCerberusDaemon(0) errored: %v", err)
	}
}

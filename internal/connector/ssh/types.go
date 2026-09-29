package ssh

import "time"

// ExecResult holds the output of a remote command execution.
type ExecResult struct {
	Stdout   string `json:"stdout" cerb:"untrusted"`
	Stderr   string `json:"stderr" cerb:"untrusted"`
	ExitCode int    `json:"exit_code"`
}

// HostStatus holds connectivity and OS information about a remote host.
type HostStatus struct {
	Reachable bool          `json:"reachable"`
	Latency   time.Duration `json:"latency"`
	OS        string        `json:"os" cerb:"untrusted"`
}

package ssh

import "time"

// ExecResult holds the output of a remote command execution.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// HostStatus holds connectivity and OS information about a remote host.
type HostStatus struct {
	Reachable bool          `json:"reachable"`
	Latency   time.Duration `json:"latency"`
	OS        string        `json:"os"`
}

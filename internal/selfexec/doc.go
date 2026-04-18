// Package selfexec watches the running binary's inode + mtime. If the binary
// is replaced on disk, the current process self-terminates with exit code 0
// so its parent (Claude Code, Nanite, Carrier, any MCP host) can respawn it
// cleanly against the new binary on the next tool call.
//
// # Why this exists
//
// MCP subprocesses are long-lived. When their binary is replaced on disk —
// by `cerberus rebuild`, `go install`, a package manager, or a manual `cp`
// — the running process keeps serving stale behavior until something kills
// it. CERB-3's cascade-kill handles `cerberus rebuild`, but binary updates
// from anywhere else still leave stale subprocesses behind.
//
// Selfexec adds a small, subprocess-local watcher that handles this case
// without coordination, IPC, or new infrastructure. Each subprocess
// fingerprints its own binary at startup (inode + mtime) and re-checks on
// a fixed interval. On any change, it logs a structured event and exits 0.
//
// # Usage
//
// Add ONE goroutine at startup, after any preconditions are wired:
//
//	go selfexec.WatchAndExit(ctx, selfexec.DefaultOptions())
//
// That's it. Default interval is 30s; clean exit on detected change.
//
// # Test-time opt-out
//
// DefaultOptions() detects test context (os.Args[0] contains ".test" or
// the GO_TESTING=1 env var is set) and returns options with a one-hour
// interval, effectively disabling the watcher under `go test`. Tests that
// exercise the watcher itself construct Options directly with a short
// interval and an injected ExitFn.
//
// # Cross-platform
//
// Path resolution uses os.Executable(), which is implemented via
// proc_pidpath on macOS and /proc/self/exe on Linux. Inode + mtime come
// from a single os.Stat call on that path. No platform-specific build
// tags are required.
//
// # Rollout
//
// Cerberus's `cerberus mcp` subprocess wires this in cmd/cerberus/cmd_mcp.go.
// Other portfolio repos (Clockwork, Nanite, future MCP services) can adopt
// the same three-line pattern by importing this package.
package selfexec

---
id: "CERB-CAP-201"
class: "capability"
name: "SSH connector"
summary: "Reaches a remote host in-process over x/crypto/ssh to probe it, run a command, move a file or a directory tree either way, or shut it down, always addressing a configured resource."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.9
confidence_label: "status verified live against a VPN-only host; exec and transfer proven by tests only, per audit scope; target resolution re-read on main after P0 (#48 to #54)"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/ssh/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "host-key"
  - "remote"
  - "sftp"
  - "ssh"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "One of the two connectors the control plane is built on"
  - type: "relates_to"
    target: "CERB-CAP-202"
    note: "Remote Docker rides this transport via ssh:// targets"
  - type: "blocks"
    target: "CERB-GAP-275"
    note: "ssh exec has no privilege elevation"
  - type: "blocks"
    target: "CERB-GAP-276"
    note: "put_dir and get_dir landed in commit 8589c58 (WP-9); closed"
  - type: "relates_to"
    target: "CERB-DEC-813"
    note: "every call names a configured resource; connection settings are refused"
  - type: "blocks"
    target: "CERB-GAP-850"
    note: "no path policy for the local side of a transfer"
---

# SSH connector

> Reaches a remote host in-process over x/crypto/ssh to probe it, run a command, move a file or a directory tree either way, or shut it down, always addressing a configured resource.

`ssh` is a primitive rather than an integration, and the shape of it follows
from that: it is entirely in-process over `golang.org/x/crypto/ssh` and
`pkg/sftp`, and it shells out to nothing. That is the property WP-9 protected
when it rejected rsync for recursive transfer — shelling out would introduce a
second SSH transport with different auth, different config resolution and
different failure modes from the one the connector already uses.

Seven operations: `status`, `exec`, `put`, `get`, `put_dir`, `get_dir`, `stop`.
Four are destructive and dry-runnable (`exec`, `put`, `put_dir`, `stop`). `get`
and `get_dir` take no `--ack`, and there is a test asserting that for `get`.
Both overwrite a local path, so PR #49 hinted their MCP tools destructive while
leaving the connector operations non-destructive. `put` writes to a
temporary name in the destination directory and renames over the target, so a
transfer that dies midway leaves the previous file intact rather than a
truncated one — which is the difference between a failed deploy and a broken
compose file.

Host key verification is real and defaults to strict: `~/.ssh/known_hosts`
unless `known_hosts_file` names another, with `allow_insecure_host_key` as an
explicit opt-out. A missing known_hosts file is an error that names all three
recoveries, and that message survives `redact.Text` intact.

**The target is always a configured resource.** Since PR #50 an operation's
config may carry `id` plus the fields in `sshconn.OperationFields` (`command`,
`local_path`, `remote_path`) and nothing else. `resolveSSHTarget` runs at the
top of the admin lane's `Execute`, on the socket, the web API, MCP and
in-process alike, and refuses any other key by name:
`refusing fields host, key_file: an ssh operation takes a configured resource
id, not connection settings; pass id=<resource-id> …`. So `known_hosts_file`
and `allow_insecure_host_key` are properties of a declaration, never of a call
(CERB-DEC-813). The CLI and the MCP tools send only the id and operation
fields, and PR #54 made the discovery schemas advertise exactly that. The CLI
makes local paths absolute before sending. Which local paths a transfer may
read or overwrite is not constrained (CERB-GAP-850).

Two things this connector does not do, both known and both deliberate to leave
open. `exec` runs as the operator's own account with no elevation, which is
WP-8 — and the constraints are already decided, not open: elevation is opt-in
per operation, needs a PTY because sudo on the work host requires a tty, must quote
each argument individually (a real bug in the shell predecessor mangled a
display name containing a space), and must never prompt for a password because
a Cerberus-started ssh has no terminal to prompt on. Recursive transfer, WP-9,
is done: `put_dir` and `get_dir` landed in commit 8589c58 on top of
`hollis-labs/go-sftpsync`, which keeps mode, refuses symlink escapes, renames
each file into place and honours cancellation between files.

The connector is also the one place in the lane where `live` carries no
information. Unlike the credentialed connectors, `ssh` is registered as an eager
instance rather than a factory, so `Probe` always succeeds and
`cerberus connectors list` reports `live=yes` unconditionally — whether or not
the VPN is up or any host is reachable. Verified separately: `cerberus ssh
status <id>` against a VPN-only host returned `reachable: true, os: Linux`.

## Owns

- SSH dial, auth and host key verification against an OpenSSH known_hosts file
- Reachability probing with latency and remote OS
- Single-file and recursive SFTP upload and download over the already-open connection
- Resolving the target from a configured resource id, and refusing connection settings in a call
- Temp-and-rename on upload, and mode preservation
- Remote host shutdown

## Does not own

- Privilege elevation. Every command runs as the operator's own account; there is no sudo path
- Which local paths a transfer may read or overwrite. There is no path policy yet
- Interactive sessions. There is no TTY, so nothing can answer a password or MFA prompt
- ~/.ssh/config resolution — the connector is in-process and shells out to nothing
- Tunnel supervision. A tunnel to the work host is a local process resource, not this connector

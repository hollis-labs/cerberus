---
id: "CERB-CAP-201"
class: "capability"
name: "SSH connector"
summary: "Reaches a remote host in-process over x/crypto/ssh to probe it, run a command, move a single file either way, or shut it down."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.9
confidence_label: "status verified live against muctlvaig; exec and transfer proven by tests only, per audit scope"
last_reviewed: "2026-09-17"
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
    note: "put and get move one file, not a tree"
---

# SSH connector

> Reaches a remote host in-process over x/crypto/ssh to probe it, run a command, move a single file either way, or shut it down.

`ssh` is a primitive rather than an integration, and the shape of it follows
from that: it is entirely in-process over `golang.org/x/crypto/ssh` and
`pkg/sftp`, and it shells out to nothing. That is the property WP-9 protected
when it rejected rsync for recursive transfer — shelling out would introduce a
second SSH transport with different auth, different config resolution and
different failure modes from the one the connector already uses.

Five operations: `status`, `exec`, `put`, `get`, `stop`. Three are destructive
and dry-runnable (`exec`, `put`, `stop`); `get` is the only transfer verb that
takes no `--ack`, and there is a test asserting exactly that. `put` writes to a
temporary name in the destination directory and renames over the target, so a
transfer that dies midway leaves the previous file intact rather than a
truncated one — which is the difference between a failed deploy and a broken
compose file.

Host key verification is real and defaults to strict: `~/.ssh/known_hosts`
unless `known_hosts_file` names another, with `allow_insecure_host_key` as an
explicit opt-out. A missing known_hosts file is an error that names all three
recoveries, and that message survives `redact.Text` intact.

Two things this connector does not do, both known and both deliberate to leave
open. `exec` runs as the operator's own account with no elevation, which is
WP-8 — and the constraints are already decided, not open: elevation is opt-in
per operation, needs a PTY because sudo on muctlvaig requires a tty, must quote
each argument individually (a real bug in the shell predecessor mangled a
display name containing a space), and must never prompt for a password because
a Cerberus-started ssh has no terminal to prompt on. And `put`/`get` move one
file, which is WP-9.

The connector is also the one place in the lane where `live` carries no
information. Unlike the credentialed connectors, `ssh` is registered as an eager
instance rather than a factory, so `Probe` always succeeds and
`cerberus connectors list` reports `live=yes` unconditionally — whether or not
the VPN is up or any host is reachable. Verified separately: `cerberus ssh
status muctlvaig` returned `reachable: true, os: Linux`.

## Owns

- SSH dial, auth and host key verification against an OpenSSH known_hosts file
- Reachability probing with latency and remote OS
- Single-file SFTP upload and download over the already-open connection
- Temp-and-rename on upload, and mode preservation
- Remote host shutdown

## Does not own

- Privilege elevation. Every command runs as the operator's own account; there is no sudo path
- Recursive directory transfer
- Interactive sessions. There is no TTY, so nothing can answer a password or MFA prompt
- ~/.ssh/config resolution — the connector is in-process and shells out to nothing
- Tunnel supervision. tunnel-muctlvaig is a local process resource, not this connector

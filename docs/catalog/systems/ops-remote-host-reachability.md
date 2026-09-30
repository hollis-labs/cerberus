---
id: "CERB-CAP-606"
class: "capability"
name: "Reaching a remote host through Cerberus"
summary: "Against a remote host on a private network, `ssh status` and the ContextForge plugin's open health probe both work; the Docker connector reaches the host and is refused by its socket when the account is not in the docker group."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "all three paths run live on 2026-09-17 with the network up; anything needing --ack was not run"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/ssh/api_backend.go"
tags:
  - "cerberus"
  - "class:capability"
  - "operational-reality"
  - "remote-host"
  - "ssh"
  - "docker"
  - "contextforge"
  - "locus:core"
relationships:
  - type: "depends_on"
    target: "CERB-CAP-600"
    note: "the remote host is the server/ssh handle in that installation"
  - type: "relates_to"
    target: "CERB-GAP-640"
    note: "no PTY, so no sudo, so no docker-group workaround"
  - type: "relates_to"
    target: "CERB-GAP-639"
    note: "every remote exec needs --ack, including read-only probes"
  - type: "relates_to"
    target: "CERB-DEC-673"
    note: "work infrastructure is read-only by decision"
  - type: "relates_to"
    target: "CERB-CAP-201"
  - type: "relates_to"
    target: "CERB-CAP-202"
---

# Reaching a remote host through Cerberus

The host in this audit was reachable only on a private network, through a
supervised tunnel resource forwarding two local ports. Everything below was run
live with that network up. Without it, all of it is unverifiable, and the failure
presents as DNS rather than as a missing network.

**`cerberus ssh status <id>` works.** It returns `reachable: true`, `os:
"Linux"` and a latency. Worth knowing how: the connector uses
`golang.org/x/crypto/ssh` with `ssh.PublicKeys(signer)` as its only auth method,
reading exactly the `key_file` named in the resource config. It does not read
`~/.ssh/config`, so a `Host` block's ControlMaster and ControlPersist are
irrelevant to it; it does not offer password or keyboard-interactive auth; it
makes exactly one publickey attempt. That is why this probe is safe to run
against a corporate auth endpoint: there is no prompt to hang on and no retry
loop to lock an account with. It also means a script that uses password auth
interactively describes a different path from the Cerberus one.

**ContextForge's open health probe works.** `cerberus connectors plugin managed
exec contextforge get_health` returns `{address: "http://127.0.0.1:<port>", ok:
true, status: "healthy"}` through the tunnel, and curling `/health` directly
returns the gateway's full payload. This is the fast discriminator the design
intends: it is open, so it keeps working while `list_gateways` 401s for want of
a JWT, and that is how you tell a down tunnel from a down gateway. The Cerberus
DTO flattens the gateway's response. The live payload carries `mcp_runtime`
detail, including `pod_id` and `effective_mode`, that the DTO does not surface,
which is the correct call under ADR 0003 but does mean the runtime mode is
invisible through Cerberus.

**Docker over `ssh://` reaches the host and is refused by its socket.** The
Docker connector does support a remote target: `TargetFromConfig` accepts a
`DOCKER_HOST` value, and `ssh://` shells out to `ssh -- <host> docker system
dial-stdio`. Pointed at a host where the account is not in the `docker` group,
`cerberus docker ps -H ssh://user@host-a.example.com` exits 1 with an unusually
good error: "cannot connect to the Docker daemon: failed to open the raw stream
connection: dial unix /var/run/docker.sock: connect: permission denied — the
account can reach the host but not its Docker socket; add the account to the
docker group there (usermod -aG docker <user>, then reconnect) or run docker
under sudo". Both recoveries it names can be outside Cerberus's reach: the group
change belongs to whoever owns the host, and `dial-stdio` has no sudo escape
hatch the connector could use. A script-based workaround (`sudo -n docker
inspect ...` over ssh) is out of reach too, because the ssh connector never
requests a PTY and such a host's sudo can refuse without one. So container
introspection on such a host goes over HTTP (the ContextForge probe above), not
through the Docker connector, and that is a property of the host rather than a
bug in the connector.

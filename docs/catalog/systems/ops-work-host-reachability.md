---
id: "CERB-CAP-606"
class: "capability"
name: "Reaching the work host through Cerberus"
summary: "Over VPN, `ssh status` and the ContextForge plugin's open health probe both work against muctlvaig; the Docker connector reaches the host and is refused by its socket, because the operator is not in the docker group."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "all three paths run live on 2026-09-17 with VPN up; anything needing --ack was not run"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/ssh/api_backend.go"
tags:
  - "cerberus"
  - "class:capability"
  - "operational-reality"
  - "work-host"
  - "ssh"
  - "docker"
  - "contextforge"
  - "vpn"
  - "locus:core"
relationships:
  - type: "depends_on"
    target: "CERB-CAP-600"
    note: "muctlvaig is the server/ssh handle in that estate"
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

# Reaching the work host through Cerberus

VPN was up during this audit: `muctlvaig.corp.adtran.com` resolves to
172.20.5.118, port 22 accepts, and the supervised `tunnel-muctlvaig` resource
has both forwards bound on 18095 and 14444. Everything below was run live under
those conditions; on a machine without VPN all of it is unverifiable, and the
failure presents as DNS rather than as a missing VPN.

**`cerberus ssh status muctlvaig` works.** It returns `reachable: true`, `os:
"Linux"` and a latency. Worth knowing how: the connector uses
`golang.org/x/crypto/ssh` with `ssh.PublicKeys(signer)` as its only auth method,
reading exactly the `key_file` named in the resource config. It does not read
`~/.ssh/config`, so the `Host muctlvaig` block's ControlMaster and
ControlPersist are irrelevant to it; it does not offer password or
keyboard-interactive auth; it makes exactly one publickey attempt. That is why
this probe is safe to run against a corporate auth endpoint — there is no prompt
to hang on and no retry loop to lock an account with. It also means the
`~/Projects/tools` README's premise of password auth as `cburks` describes the
interactive path, not the Cerberus one; key auth with `~/.ssh/id_rsa` is
configured and working.

**ContextForge's open health probe works.** `cerberus connectors plugin managed
exec contextforge get_health` returns `{address:
"http://127.0.0.1:14444", ok: true, status: "healthy"}`, and curling
`http://127.0.0.1:14444/health` directly through the tunnel returns the
gateway's full payload. This is the fast discriminator the design intends: it is
open, so it keeps working while `list_gateways` 401s for want of a JWT, and that
is how you tell a down tunnel from a down gateway. The Cerberus DTO flattens the
gateway's response — the live payload carries `mcp_runtime` detail including
`pod_id` and `effective_mode` that the DTO does not surface — which is the
correct call under ADR 0003 but does mean the runtime mode is invisible through
Cerberus.

**Docker over `ssh://` reaches the host and is refused by its socket.** The
Docker connector does support a remote target: `TargetFromConfig` accepts a
`DOCKER_HOST` value and `ssh://` shells out to `ssh -- <host> docker system
dial-stdio`. Pointing it at the work host the obvious way,
`cerberus docker ps -H ssh://cburks@muctlvaig.corp.adtran.com`, exits 1 with an
unusually good error: "cannot connect to the Docker daemon: failed to open the
raw stream connection: dial unix /var/run/docker.sock: connect: permission
denied — the account can reach the host but not its Docker socket; add the
account to the docker group there (usermod -aG docker <user>, then reconnect) or
run docker under sudo". Both recoveries it names are outside Cerberus's reach:
the group change belongs to the team that owns the host, and `dial-stdio` has no
sudo escape hatch the connector could use. The proven workaround in
`~/Projects/tools/lib/common.sh` is `rsh "sudo -n docker inspect ..."`, and
Cerberus cannot do that either, because its ssh connector never requests a PTY
and that host's sudo refuses without one. So container introspection on the work
host goes over HTTP — the ContextForge probe above — and not through the Docker
connector, and that is a property of the estate rather than a bug in the
connector.

Local Docker, for contrast, works fine and lists two `mtbf-monitor-*`
containers that no Cerberus resource models.

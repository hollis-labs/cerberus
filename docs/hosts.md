# Hosts

A host is a named machine Cerberus keeps a record of (ADR 0004). It is an
admin-lane target, not a supervised resource: Cerberus does not start, stop or
restart a host, and a resource may not be declared with `type: host`.

Cerberus reports facts about hosts. It does not rank them, reserve them or
choose where work goes, and no behavior differs by a host's `role` or labels.

## hosts.yaml

Declared data lives in `~/.cerberus/hosts.yaml`, outside every repo. A missing
file is an empty registry. Unknown keys are an error.

```yaml
version: 1

# Extend the known vocabularies. Without these, role must be one of
# hub, operator, worker, builder, service and provider must be homelab.
extra_roles: [gpu-farm]
extra_providers: [example-cloud]

hosts:
  - name: box-a                       # unique; lowercase letters, digits . _ -
    hostname: box-a.example.test
    tailnet_name: box-a.tailnet.example.test
    tailnet_ip: 100.64.0.10           # must be in 100.64.0.0/10 (or fd7a:115c:a1e0::/48)
    lan_ip: 192.0.2.10
    role: worker
    provider: homelab
    labels: {agents: "true", zone: a} # consumers filter on these; Cerberus does not act on them

    reach:
      ssh: box-a-ssh                  # id of an existing server/ssh resource

    declared:                         # what you say the machine is
      os: linux
      arch: amd64
      cpu: 8
      memory: 16GiB
      disk: 512GB
      gpu: none

    created_at: 2026-10-11T00:00:00Z
    owner: ops
    ttl: 72h                          # disposable hosts
    cost: {monthly: 12.5, currency: USD}

    tether_environment_id: env-0000   # opaque pointer, set by a hand-off
    backup_refs: [backup-policy-a]
```

`reach.ssh` names a `server`/`ssh` resource. The key, port, user and host-key
trust stay on that resource; a host never carries a credential.

## Probed data

Measurements are derived data kept by the daemon (`~/.cerberus/state/hosts.json`),
rebuilt by probing and safe to delete. They sit beside the declared values and
are never merged into them, so a disagreement between the two stays visible.

A probe is a read over ssh. It uses the `sysmon` plugin's `host_report` when that
plugin is loaded and falls back to plain POSIX commands otherwise. Every tool is
resolved per call. Results are reduced to an allow-list of fields.

Every host view reports `state` (`online`, `offline` or `unknown`), `probed_at`
(when the facts were measured), `checked_at` (the last attempt) and `stale`
(true when the data is older than five minutes, or there is none). A host that
does not answer is `offline`, with the last good measurement kept and its age
shown; that is a state, not an error.

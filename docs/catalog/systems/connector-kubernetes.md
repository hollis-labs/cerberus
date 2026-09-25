---
id: "CERB-CAP-211"
class: "capability"
name: "Kubernetes connector (plugin)"
summary: "Reads and administers a Kubernetes cluster from a runtime-loaded plugin: fourteen reads and five writes, every write behind --ack with an API-server dry run."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.8
confidence_label: "Every operation verified live against a kind cluster through the host's in-process exec path; not yet run against a managed cluster or through the installed daemon"
last_reviewed: "2026-09-25"
created_at: "2026-09-25"
namespace: "cerberus"
locus: "plugin"
pointer_locator: "hollis-labs/cerberus-plugins: kubernetes/"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "dry-run"
  - "kubernetes"
  - "plugin"
  - "locus:plugin"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "Its operations dispatch through the admin lane"
  - type: "depends_on"
    target: "CERB-CAP-301"
    note: "Loaded at runtime; the host resolves its optional token secret"
  - type: "relates_to"
    target: "CERB-DEC-290"
    note: "Ships as a plugin because client-go would grow the host binary by roughly 40%"
  - type: "relates_to"
    target: "CERB-DEC-292"
    note: "The first connector to build writes under the amended decision"
  - type: "relates_to"
    target: "CERB-GAP-652"
    note: "Its dry runs need --ack under the host"
---

# Kubernetes connector (plugin)

> Reads and administers a Kubernetes cluster from a runtime-loaded plugin: fourteen reads and five writes, every write behind --ack with an API-server dry run.

Kubernetes is a plugin rather than a built-in because of client-go. The typed
clientsets alone produce a binary of about 37 MB, which compiled into the host
would grow it by roughly 40%; as a subprocess it costs the host nothing. It
takes client-go v0.37 with apimachinery and the metrics client in lockstep, and
deliberately not controller-runtime: a reconcile loop against a remote cluster
is the supervision lane, which does not reach remote systems.

Fourteen reads: kubeconfig contexts, an access preflight, health, namespaces,
nodes, pods, workloads, events, logs, a workload describe, services, ingresses,
the API resource types the server serves, and current usage from
metrics-server. Two of them, `list_contexts` and `check_access`, never contact
the cluster, because they are what an operator needs when authentication is the
thing that is broken. Every list is bounded and says when it truncated.

Five writes: scale, rolling restart, cordon, uncordon, and pod delete. Each is
`Destructive` and `SupportsDry`. The dry run is the API server's own
(`dryRun=All`), so admission, validation and RBAC all run and nothing persists.
An identity that may not make a change fails the preview exactly as it would
fail the write. Each write is recorded under its own field manager, reports the
fields it changed rather than the object, and sends nothing for a no-op.

The DTOs are an allow-list and the sharpest in the repo: env values, container
command and args, probe exec commands, secret contents and the
last-applied-configuration annotation are never emitted, only names. Three bugs
were found only by running against a real API server through the host, and each
now has a test. Host redaction blanked a DTO key containing "secret", and would
have blanked the whole credential-helper diagnosis under a key containing
"credential". The kubeconfig context's namespace was ignored, which with writes
means changing a same-named workload in `default`. And a helper set to
interactive mode Always passed preflight, then failed every call.

The exec credential-helper path is verified with a stand-in helper found
outside the daemon's minimal PATH, not yet with a managed cluster's own helper.

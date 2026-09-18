# Kubernetes Connector Plugin

Work packages for a `kubernetes` connector serving the cluster we are being
given. Read `docs/plans/infra-admin-control-plane.md` for the direction and
`docs/plans/connector-work-packages.md` for the house rules — this file does not
repeat them, it assumes them.

Written 2026-09-18, before the cluster exists. That is deliberate: WP-K1 is
buildable today against a fake backend, and everything cluster-shaped is gated
behind one question (WP-K0) that nobody can answer yet.

## The lane decision

**Plugin, not a built-in.** AGENTS.md: "If you are about to add a vendor SDK to
`go.mod` for a connector, that is the signal you are in the wrong lane." A third
directory in `hollis-labs/cerberus-plugins`, beside `contextforge/` and
`azure/`, own Go module, subprocess entrypoint, generated `plugin.yaml`.

This is also the lane that makes the size question go away. Measured on this
machine, 2026-09-18, Go 1.26.3, darwin/arm64:

| Probe | Binary |
|---|---|
| hello-world baseline | 2.5 MB |
| `client-go` dynamic/unstructured only | 15.1 MB |
| `client-go` typed clientsets (`kubernetes.NewForConfig`) | 36.9 MB |
| `cerberus` today | 85.6 MB |
| `cerberus-azure-plugin` | 11.8 MB |
| `cerberus-contextforge-plugin` | 9.5 MB |

A compiled-in Kubernetes connector would grow the shipped binary by ~40%. A
plugin subprocess costs the host nothing, which is the whole argument for the
lane and the reason the minimal-client alternatives below lose on their only
selling point.

## Libraries

**Take `k8s.io/client-go` v0.37.0** with `k8s.io/apimachinery` v0.37.0. Versions
track the server release in lockstep — v0.37.x is Kubernetes 1.37, released
2026-08-26. Pin both explicitly and bump them together; a split between
`client-go` and `apimachinery` produces type errors that read like unrelated
breakage.

Sub-packages this connector will actually use:

- `tools/clientcmd` — kubeconfig parsing, context selection, `exec` credential plugins
- `kubernetes` — typed clientsets, for the core workload reads
- `dynamic` + `discovery` — CRDs and "what does this cluster even serve"
- `k8s.io/metrics` — `top nodes` / `top pods`, only if the metrics-server is installed
- `tools/remotecommand`, `tools/portforward` — deferred to WP-K5

Rejected, with reasons, so nobody re-opens these:

- **`sigs.k8s.io/controller-runtime`** — a watch-and-reconcile loop with an
  in-memory informer cache. That is the supervision lane, for a remote system,
  which `infra-admin-control-plane.md` explicitly forbids. The admin lane is
  stateless and resolved per call. Do not import it, and do not build informers
  out of client-go by hand either.
- **`k8s.io/cli-runtime` / `k8s.io/kubectl`** — kubectl's internals. Large, and
  a weaker API-stability promise than client-go's.
- **`helm.sh/helm/v4`** — Helm 4.0 shipped 2025-11-12 and its SDK is now
  formally separated from the CLI with an API-stability commitment, so it is a
  real option later. Not in v1; see WP-K7.
- **`kubetail-org/kubeslim`, `castai/k8s-client-go`** — minimal clients that
  exist to dodge client-go's ~20MB+ binary tax. We are a subprocess, so we do
  not care about the tax, and neither implements `exec` credential plugins,
  which is the one thing a managed cluster's kubeconfig is most likely to need.
- **Shelling out to `kubectl -o json`** — has repo precedent (the Docker
  connector, `internal/connector/docker/cli_backend.go`) and costs 0 MB. It is a
  legitimate fallback if WP-K0 turns up an auth mode client-go cannot do, but it
  buys less here than it does for Docker: same PATH problem, no schema, and
  output shape that drifts between kubectl versions.

## WP-K0 — Establish how the target cluster authenticates — BLOCKING, operator action

**Nothing downstream of WP-K1 can be finished without this answer**, and no
amount of code guesses it. Four questions go to whoever provisions the cluster:
its kubeconfig shape, whether a non-interactive identity exists, what RBAC the
connector will get, and whether the API server is reachable without a VPN.

Those questions are tracked outside this repo, with the reasoning for each, in
the follow-up tracker. The short version is below, because the *consequences*
belong in the plan even when the asks do not.

### Why this is the gate, and not a detail

Two of this repo's documented outages are waiting at the end of the `exec:`
branch:

- **client-go handles `exec` credential plugins by shelling out.** Under
  launchd, the daemon's `PATH` is `/usr/bin:/bin:/usr/sbin:/sbin` — see "The
  daemon's environment is not your shell's" in AGENTS.md. `kubelogin` and `az`
  live in neither. This is the Docker connector's silent outage verbatim:
  resolve once, fail, cache the failure for the daemon's lifetime while
  `connectors list` reports healthy. Resolve the credential binary **explicitly
  and per call**, with fallback search locations, and surface "credential plugin
  `kubelogin` not found on the daemon's PATH" as the error rather than a
  401.
- **A credential plugin that wants a browser or an MFA prompt cannot run here.**
  A Cerberus-launched process has no TTY. This is why the SSH tunnel resources
  are deliberately `auto_start: false` and `auto_restart: false`, and why an
  auto-retrying auth loop against an SSO endpoint is a good way to get an
  account locked out. Where no non-interactive identity exists, the honest
  outcome is that the operator refreshes credentials out of band and the
  connector reports `credential_missing` with the helper named as the recovery —
  not that we build a retry.

If the kubeconfig turns out to carry a static ServiceAccount token, all of the
above evaporates and even the minimal clients become viable. Record the answer
in this file when it arrives.

---

## WP-K1 — Plugin skeleton, backend seam, DTOs — DONE 2026-09-18

**Why now:** none of it needs a cluster, and it is the shape every later package
fills in. This is the package to hand to a parallel agent.

**Do:** `kubernetes/` in `hollis-labs/cerberus-plugins`, copying the
`contextforge/` shape exactly:

```
kubernetes/
  go.mod                        module …/cerberus-plugins/kubernetes
  Makefile                      BINARY := cerberus-kubernetes-plugin, DIST := ../dist/kubernetes
  cmd/cerberus-kubernetes-plugin/main.go     subprocess.Serve + write-dist
  internal/k8splugin/
    connector.go                Definition(), Manifest(), ConnectorID = "kubernetes"
    plugin.go                   Init/Load/Unload/Health/MCPCallTool
    backend.go                  the Backend interface; client-go lives behind it
    dto.go                      the allow-list types
    fake_backend.go             test seam
```

Add `kubernetes` to `PLUGINS` in the root `Makefile`. That is the only
registration step — a plugin declares its operations in its manifest and the
host derives CLI, API and MCP surfaces from it, so unlike a built-in it touches
none of the five files in `connector-work-packages.md`.

**The `Backend` interface is not optional.** `digitalocean`, `docker` and
`contextforge` all put the vendor SDK behind one, and it is what makes the
connector testable with no cluster — which, today, is the only way to test it at
all. Every operation in WP-K3 gets a `Backend` method and a fake.

### DTOs: this is a sharper boundary than ContextForge's

ADR 0003 applies, and Kubernetes is the worst case for it. Returning
`corev1.Pod` or `corev1.Secret` emits, into CLI stdout, daemon logs, MCP tool
results and an agent's context window at once:

- `corev1.Secret.Data` — the credential values themselves, base64 is not redaction
- `corev1.Pod.Spec.Containers[].Env` — literal env values, which is where
  application credentials usually are in practice
- the `kubectl.kubernetes.io/last-applied-configuration` annotation — routinely
  a verbatim copy of the original manifest, env values included, on objects
  whose live spec looks clean
- `ServiceAccount` token references, `imagePullSecrets`, ingress TLS material

So the DTO is an allow-list, exactly as `contextforge/internal/cfplugin/dto.go`
is, and for the same reason. `PodDTO` is name, namespace, phase, ready count,
restarts, node, age, owner — not a hundred fields of cluster internals. Follow
the convention the rest of this repo's probe tooling follows: **names, never
values.**
`EnvNames []string`, never `Env`. For a Secret, emit name, namespace, type and
`keys []string` — never `data`, not even truncated.

`dto_test.go` asserts it, the way `contextforge`'s does: construct a vendor
object with every credential field populated, marshal the DTO, assert no
sentinel value appears in the JSON. A vendor adding a field in a minor release
must not be able to widen our output.

**Acceptance:**
- `make test` and `make lint` clean at the plugins repo root.
- `make dist` produces `dist/kubernetes/` with a generated `plugin.yaml`.
- `cerberus connectors plugin managed install "$PWD/dist/kubernetes"` then
  `load` then `connectors list` shows it, with the declared secrets reported
  under `missing_secrets` rather than failing the load.
- Every operation runs green against the fake backend.
- A DTO round-trip test proves no credential-shaped field escapes.

**Do not:** import `k8s.io/client-go` into any file outside `clientgo.go`. Do
not touch the Cerberus host repo at all — if something is missing from the
authoring contract, raise it rather than reaching into `internal/`.

### What shipped

`kubernetes/` in `hollis-labs/cerberus-plugins`, on branch
`feat/kubernetes-plugin`, added to the root `Makefile`'s `PLUGINS`. Nine
operations, all read-only, all served. `make test`, `go vet` and `gofmt -l` are
clean across all three plugins; the built plugin binary is 37.6MB, matching the
measured typed-client-go cost and costing the 85.6MB host nothing.

**More than a skeleton: two operations are complete and cluster-independent.**
`list_contexts` and `check_access` answer from the kubeconfig and the local
filesystem, never touching `Backend` — asserted by
`TestKubeconfigOperationsDoNotTouchTheBackend`. That is what makes them usable
when authentication is the thing that is broken.

`check_access` is WP-K0's question answered mechanically. Driven over the real
JSON-RPC protocol against a kubeconfig in the shape a managed cluster most
likely hands us:

```
$ check_access --context corp-prod
  auth_mode: exec   ready: false
  credential_plugin: {command: kubelogin, resolved: false,
                      interactive_mode: Always, env_names: [AAD_CLIENT_SECRET]}
  problems:
    credential_missing: credential plugin kubelogin was not found on PATH or in
      11 fallback locations; the daemon runs under a minimal PATH, so install it
      somewhere standard or add its directory to CERBERUS_KUBE_CREDENTIAL_PATH
    credential plugin kubelogin requires an interactive terminal, which a
      Cerberus-launched process does not have; refresh the credential in a
      terminal first, or move this connector to a non-interactive identity

$ get_health --context local-dev
  reachable: false
  message: cannot resolve the API server host in https://k8s.corp.example.com:6443;
    if the cluster is only reachable on the VPN, check the VPN before the cluster
```

Both documented outages are now *detected and named* rather than waited for.
`RestConfig` resolves an exec helper per call across eleven fallback locations
plus `CERBERUS_KUBE_CREDENTIAL_PATH`, and rewrites the absolute path into an
in-memory copy of the kubeconfig before client-go execs it — the operator's file
on disk is never modified.

**Auth is an option set, not a guess.** WP-K0 is still open, so `auth.go` names
every plausible answer — `host-secret`, `in-cluster`, `exec`, `token`,
`token-file`, `client-certificate`, `basic`, `auth-provider`, `anonymous` — and
classifies whichever one it is handed. When WP-K0 returns, the work is picking a
mode, not rewriting the connector.

**The DTO boundary is closed and asserted.** `dto_test.go` populates
`Pod.Spec.Containers[].Env`, the `last-applied-configuration` annotation, and an
`ExecConfig`'s `Args` and `Env` with one sentinel and asserts it appears nowhere
in the marshalled output. A full protocol run greps clean for the sentinel
across both stdout and stderr. Env is reported as `env_names`; exec `Args` are
not emitted at all.

`TestEveryOperationIsReadOnly` is the tripwire for WP-K6: the first write
operation added will fail it, forcing a deliberate decision rather than letting
a write arrive unannounced.

### Two things worth knowing before picking this up

- **The redaction guard cannot import `internal/redact`**, so
  `redaction_test.go` mirrors the host's three rules and asserts this package's
  operator-facing strings stay clear of the shapes that get rewritten. It is a
  floor, not a proof, and it does not follow the host automatically.
  `TestRedactionHazardsDetectKnownBadStrings` guards the guard — a mirror that
  matches nothing would pass silently forever.
- **The SDK dispatches every request in its own goroutine.** A probe that pipes
  init, load and a tool call in at once races the handshake and gets an empty
  answer from a plugin that is fine. The host sends sequentially; a test driver
  must wait for each response before sending the next.

---

## WP-K2 — Kubeconfig and credential resolution

**Blocked on WP-K0.**

**Do:** resolve a cluster connection per call, from (in order) an explicit
`context` operation argument, a `kubeconfig` config field, `$KUBECONFIG`, then
`~/.kube/config`. Selecting a context per operation, not per process, is the
same call `docker` made in WP-3 — one daemon, several clusters.

**Design notes:**
- **Per-call resolution, never boot-time.** AGENTS.md names boot-time resolution
  as the cause of a silent outage. The credential binary, the kubeconfig and the
  API server all get resolved when an operation runs.
- Kubeconfig is a *path*, not a secret, so it is a config field. Anything
  token-shaped is a declared secret the host resolves and hands over in
  `InitParams.Config` — read it with
  `subprocess.NewConfigReader(params.Config).Secret(name)`, which also registers
  it with the logger's redaction tracker. Never resolve a credential yourself.
- A missing credential is not fatal. The plugin loads; the operation reports
  `credential_missing` and names the recovery.
- **Name what actually failed.** A refused dial on a VPN-only API server means
  the VPN is down, not the cluster. An expired `exec` credential is not an RBAC
  denial. A 403 should print the RBAC verb and resource the API server named,
  because that string is the whole diagnosis.

**Acceptance:**
- Context selection proven: two contexts, two different answers, and a test that
  the next call does not inherit the last one's context
  (`TestExternalConnectorServiceRoutesDockerOperationsToTheRequestedHost` is the
  model).
- A test that the connector's recovery instructions survive `redact.Text`
  intact. AGENTS.md requires this for any error carrying a recovery instruction,
  and there have been seven patches to those regexes: a safety net that eats the
  instruction is worse than no instruction. Watch specifically for text
  containing `Bearer`, an `X-`-prefixed header name, and anything that parses as
  an assignment after an error code.

---

## WP-K3 — Read operations

**Blocked on WP-K2.** Everything here is read-only and therefore
`Destructive: false`, `SupportsDry: false`. Making a read prompt for `--ack`
empties the gate of meaning.

Proposed operation set, smallest thing that is genuinely useful:

| Operation | Notes |
|---|---|
| `get_health` | API server reachable + version. Must work with no credential where the endpoint allows it, for the same reason ContextForge's does: it is how you tell a down tunnel from a down cluster. |
| `list_contexts` | From the kubeconfig. No cluster contact — works offline and is the first thing to build. |
| `list_namespaces` | |
| `list_nodes` | name, status, roles, version, pressure conditions |
| `list_workloads` | pods/deployments/statefulsets/daemonsets, namespace-scoped |
| `describe_workload` | the useful subset — not `kubectl describe`'s full dump |
| `list_events` | namespace or object scoped, sorted by last-seen. The highest-value read for "why is this broken". |
| `top` | nodes/pods, **only if** metrics-server is installed; degrade with a named reason, do not error |

**Do not** add `get_secret` in any form, not even keys-only, until someone asks
for it with a concrete need. It is the one read where a DTO slip is
unrecoverable.

---

## WP-K4 — Logs

Pod logs, with `container`, `tail`, `since` and `previous`. Snapshot only —
**not** streaming. The admin lane returns a result; a long-lived stream over the
daemon socket is a different transport problem, and `cerberus resource logs`
already establishes the snapshot shape.

Worth knowing before building: logs are the operation most likely to return a
credential, because applications log their own config at startup. This is not
something the connector can fix, but the operator should be told what they are
about to paste into an agent context. Say so in the operation description.

---

## WP-K5 — `exec` and `port-forward` — deferred, and here is the shape of it

Both are real capabilities with real demand, and both are SPDY/WebSocket
streaming sessions rather than request/response — the same mismatch as WP-K4's
streaming, but worse, because they are interactive.

`port-forward` has a second problem: it is long-lived, which makes it look like
a supervision-lane resource. It is not one, and widening the supervision lane to
reach a remote cluster is exactly what `infra-admin-control-plane.md` forbids.
If it lands, it lands as an admin-lane verb that starts a forwarder and hands
back a handle, or it does not land.

`exec` is a write to a running workload by any reasonable reading, so it also
sits behind WP-K6.

---

## WP-K6 — Write operations — LOCKED

"Work infrastructure is read-only" is a scope decision, not a permissions
workaround. `apply`, `delete`, `scale`, `rollout restart`, `cordon`, `drain`,
`exec` are documented as locked rather than built speculatively.

**Unlock conditions, so this is not a dead end:**

- The cluster is ours to operate, with an owner who has agreed in writing that
  Cerberus writes to it — as opposed to an infrastructure team owning it and
  Cerberus being a tool that helps operate it.
- A dedicated ServiceAccount with a scoped Role, so a write is attributable to
  Cerberus and bounded by RBAC rather than by our own restraint.
- Every write operation carries `Destructive: true` and a real `SupportsDry`
  preview. Kubernetes gives us server-side dry-run (`dryRun=All`) for free,
  which is a better preview than anything we would compose — and skipping the
  `dryRunPreview` case is what makes `--dry-run` demand `--ack`.

A disposable local or development cluster is the natural first write target and
needs none of the above. A cluster someone else owns needs all of it.

---

## WP-K7 — Helm — deferred

If we deploy with Helm, shell out to the `helm` binary first and learn which
verbs we actually want. `helm.sh/helm/v4`'s `pkg/action` is the embeddable
client and the SDK now carries an API-stability commitment, so pulling it in
later is a supported move — but it drags client-go plus a large tail, and we
would be taking that weight before knowing whether three verbs or thirty are
wanted.

---

## Sequencing

```
WP-K0 auth discovery   operator action, BLOCKING ──┐
                                                    ├─► WP-K2 kubeconfig/creds ──► WP-K3 reads ──► WP-K4 logs
WP-K1 skeleton + DTOs  DONE 2026-09-18           ──┘   (largely landed in K1;      (written, unproven   └─► WP-K5 exec/forward (deferred)
                                                        what remains is proving     against a cluster)
                                                        it against a real cluster)
                                                                                              └─► WP-K6 writes (locked)
                                                                                              └─► WP-K7 helm (deferred)
```

WP-K1 was the whole parallelizable surface and it has landed, including the
DTO boundary — the part that is expensive to get wrong and was cheap to get
right while there was no cluster to be careless with.

**WP-K2, WP-K3 and WP-K4 are now verified against a real API server** — see
"Verified against a real cluster" below. What remains genuinely blocked on the
target cluster is narrow: the `exec` credential path (kind uses client
certificates, so the launchd-PATH rewrite is still only unit-tested), RBAC
behaviour under a restricted role, and whatever the mapping gets wrong on
objects we have not thought to create.

## Verified against a real cluster — 2026-09-18

`kind` v0.33.0, cluster `cerberus-probe` on Kubernetes **v1.37.0**, matching
client-go v0.37.0 exactly. Installed with `go install sigs.k8s.io/kind@latest`;
no Homebrew involved. Recreate with:

```bash
kind create cluster --name cerberus-probe
kubectl apply -f <the probe manifest: two namespaces, a Deployment with a
  literal credential in its env, a Secret, a StatefulSet, a DaemonSet, a pod in
  a second namespace, and a deliberately crash-looping Deployment>
kind delete cluster --name cerberus-probe   # when finished
```

All nine operations ran green against it, both directly over the plugin's
JSON-RPC protocol and through the installed daemon. What the cluster settled
that a fake could not:

- **Pagination is real.** `list_namespaces --arg limit=2` returns
  `count: 2, truncated: true` plus the API server's own continuation token. The
  fake clientset ignores `ListOptions.Limit`, so this was previously unverified
  by construction.
- **`all_namespaces` is real**, returning `kube-system`, `local-path-storage`,
  `probe-apps` and `probe-other`.
- **A third auth mode got exercised for free.** kind writes a
  client-certificate kubeconfig, so `check_access` classified
  `client-certificate` and the daemon — with its minimal `PATH` — authenticated
  without any external helper. That is a useful negative result: the PATH
  problem only bites the `exec` modes.
- **The crash-loop path maps correctly.** A busybox container exiting 1 came
  back as `ready: "0/1"`, `restarts: 3`, `state: terminated`, `reason: Error`,
  with `phase: Running` — which is genuine Kubernetes behaviour and exactly the
  combination a hand-built fixture gets wrong.
- **The DTO boundary holds against live objects.** A real `Secret` and a pod
  carrying a literal credential in `env` were read by eleven operations, over
  both surfaces, with debug logging on. The canary appears in **none** of them,
  on stdout or stderr, while `env_names: [DB_PASSWORD, API_TOKEN]` and
  `env_from_names: [secret/web-secrets, configmap/web-config]` all survive — so
  the allow-list is safe *and* still useful.
- **`get_logs` is the one deliberate exception, now demonstrated rather than
  theorised.** The crash-looping container logs its own config at startup, and
  the canary comes straight back through the log snapshot. That is by design —
  without log content the operation has no value — and the operation description
  says so. Worth knowing before piping logs into an agent context.

### Two bugs the live path found that no test had

Both were silent wrong answers, and both are fixed with tests over both typings.

1. **Six of seven list calls were unbounded.** Only `list_events` had a limit.
   Since a connector operation *is* an MCP tool, an unbounded namespace listing
   drops every pod into an agent's context in one result. Every list is now
   bounded and returns a `List[T]` envelope with `count` and `truncated`,
   because silent truncation is worse than none — an agent cannot tell "3 pods"
   from "the first 3 of 900".

2. **`--arg` types everything as a string, and the parser only read JSON types.**
   This is the one to remember. Over MCP, `limit` arrives as a float64 and
   `all_namespaces` as a bool. Over the CLI, `--arg limit=2` makes both strings.
   The parser silently dropped them: `limit=2` fell back to the default, and
   `all_namespaces=true` read as false, so an operator asking for a cluster-wide
   read got a single namespace with no indication the flag was ignored.

   A malformed value now **fails loudly** rather than defaulting —
   `limit=abc` returns `invalid arguments: limit must be a whole number, got
   "abc"` — and every problem is reported at once. Any plugin reading arguments
   on both surfaces has this bug waiting for it; the fake-backend tests could
   not see it because they supply JSON types, which is what the MCP path sends.

### Still not verified

- The **`exec` credential path** end to end. kind does not use one, so the
  PATH-rewrite and the interactive-mode refusal remain unit-tested only. This is
  the piece most likely to matter on a managed cluster, and the reason WP-K0
  still blocks.
- **RBAC.** kind grants cluster-admin, so every `describeError` branch for a
  forbidden read is unit-tested against a synthesised error rather than a real
  restricted role.
- **`top`.** kind ships no metrics-server, so the operation is still unbuilt.

## Working alongside other sessions

**Take a worktree.** Two sessions in one checkout share an index, and a bare
`git commit` or `git add -A` sweeps up whatever the other session staged — this
has already produced a commit that did not compile. `git worktree add
../cerberus-<wp> -b <branch>`, commit with explicit pathspecs, check
`git diff --cached --name-only` first.

The plugins repo is a separate checkout at `~/Projects-apps/cerberus-plugins`
and the same rule applies there.

`--new-from-rev` does not check formatting; run `gofmt -l .` separately.

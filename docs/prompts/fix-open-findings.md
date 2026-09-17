# Task: fix the open findings

Five defects, all confirmed against the running system. Read `AGENTS.md` first.
Take a worktree: `git worktree add ../cerberus-fixes -b fix/open-findings`.

**Ordering note.** PR #33 (the capability catalog) is open and touches
`AGENTS.md`. Either merge it before starting, or branch from its head — several
of these fixes want an `AGENTS.md` line and you will otherwise conflict.

Each item below names the evidence. Reproduce it before fixing it: a fix for a
defect you have not seen is a guess.

---

## 1. An error after the request is sent must never trigger an in-process retry

**Severity: highest. Do this one first, and alone if you do nothing else.**

`SocketClient` returns `DaemonUnreachableError` for *any* error from
`http.Client.Do` — `socket_client.go:490` and `:545`, and the log key says
`dial_failed`. The comment claims to distinguish "daemon not running" from other
errors; the code does not.

`cmd_resource.go` then falls back to in-process execution on that error at **six
mutation sites**: `ReloadResource` (224), `StopResource` (255), `ApplyResource`
(287, 325, 374), `SyncResource` (528). The other five fallback sites are reads
and are harmless.

So a daemon that restarts mid-operation — which is exactly what deploying the
daemon does — closes the connection, the EOF is classified as *the daemon is not
running*, and the CLI silently re-runs the mutation in-process **with the
serving runtime's self-mutation guard bypassed**, because the retry never
reaches the serving runtime.

`cmd/cerberus/cmd_transport.go:25` already states the correct invariant, and the
connector lane honours it:

> Choose a transport before executing anything. Once an operation is sent, an
> error must never trigger an in-process retry of a possible mutation.

**The predicate that matters is not "does this look like the daemon is down."
It is "was this request definitely never delivered."** A dial failure is safe to
retry because nothing was sent. An EOF, a connection reset or a timeout is not,
because the daemon may have executed the mutation before the connection died.

Fix both halves:

- Narrow the classification so `DaemonUnreachableError` means *the connection
  was never established*. A `*net.OpError` with `Op == "dial"`, or
  `ECONNREFUSED` / `ENOENT` on the socket path, qualifies; `io.EOF`,
  `io.ErrUnexpectedEOF`, `ECONNRESET` and context deadlines do not.
- Make the mutation paths choose a transport before sending, as
  `commandSocket` does, so the retry decision is made before anything is at
  stake rather than after.

**Acceptance:** a test where the transport fails *after* the request is
delivered proves the mutation is not retried in-process. A test where dial
fails proves the fallback still works. Reads keep their current behaviour.

---

## 2. Every `cerberus install` produces a daemon that cannot find `go`

`launchdPlistTemplate` in `cmd/cerberus/cmd_install.go` declares no
`EnvironmentVariables` key at all, so the daemon runs with launchd's minimal
`PATH=/usr/bin:/bin:/usr/sbin:/sbin`. That is what killed the Docker connector,
and `DetectDocker`'s fallback paths only fixed the Docker symptom — anything
else the daemon shells out to, `go` for a build being the obvious one, still
fails.

Cerberus already knows how to do this: `internal/connector/local/launchd.go`
emits `EnvironmentVariables` for *managed resources*. The daemon's own template
is the one that does not.

Give the installed daemon a usable `PATH`. Prefer composing it from the
installing user's environment over hardcoding Homebrew paths — an installer that
assumes `/opt/homebrew` is wrong on Intel Macs and wrong under MacPorts.

**Acceptance:** a freshly installed daemon resolves `go` and `docker`. State in
the PR how you verified it without reinstalling over the running daemon.

---

## 3. A fresh checkout does not compile

```
$ git clone … && go build ./...
internal/webui/server.go:27:12: pattern all:dist: no matching files found
```

`internal/webui/dist` is gitignored and `//go:embed all:dist` is a compile-time
error when the pattern matches nothing. So `go build`, `make test` and
therefore lefthook's pre-push hook all fail on a clone, and a new contributor
cannot push anything until they work out that `make all` must run first.

Options, in rough order of preference — choose one and record why:

- Commit a placeholder into `dist/` so the embed always matches, with the real
  bundle overwriting it. Smallest change; keeps the bundle out of git.
- A build tag separating the embedded console from the rest.
- Generate a stub in the `make` target that `go build` depends on.

Do **not** commit the built bundle.

**Acceptance:** `git clone` then `go build ./...` succeeds with no prior `make`.
Test it by actually cloning, not by reasoning about it.

---

## 4. Two live redaction defects

`redact.Text` still eats its own guidance in two places, neither of them the
`Bearer` case that was fixed:

- The `assignment` rule eats the word after the error code
  `credential_missing:` — including the verb `reload` in a recovery
  instruction.
- The `flag` rule eats the word after `X-API-Key`, because that internal hyphen
  satisfies its `--?` prefix.

The rule in `AGENTS.md` stands: **do not run redaction over a value that is a
name by construction**, and an error carrying a recovery instruction needs a
test that it survives intact.

Fix both, and add the guidance messages to the redaction test table so the
next one is caught by CI rather than by an operator reading a mangled message.

Note this is the sixth and seventh time this has happened. If the structural
answer is redacting at the value boundary rather than over rendered output, say
so — a seventh patch to the same regexes is worth questioning.

---

## 5. `make all` rewrites `web/package-lock.json` on every run

npm 11.6 normalising a lockfile written by an older npm: 28 `libc: [glibc]`
entries stripped, deterministic, reproducible. It has been dirtying the working
tree for the whole of this work and will confuse every future diff.

Simplest fix is to commit the regenerated lockfile once — it is npm normalising
its own format, not a dependency change; verify that before committing. If the
project needs a pinned npm, say so instead.

**Acceptance:** `make all` leaves the working tree clean.

---

## Not in scope

`list_gateways` has still never run against a real ContextForge gateway. That
needs a JWT from the host, not code. See WP-10.

## Gates

`make test`, `go vet`, `gofmt -l .` and
`golangci-lint run --new-from-rev=main ./...` all clean. Remember `--new-from-rev`
does **not** check formatting — run `gofmt` separately.

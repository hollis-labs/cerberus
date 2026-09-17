# Task: build `hollis-labs/go-sftpsync`

Build a small, opinionated Go library for **recursive directory transfer over an
existing SSH connection**. New repository, standalone, consumed by Cerberus once
it lands — do not vendor it into Cerberus or develop it inside that repo.

The name is a proposal. If a better one fits the `hollis-labs/go-*` space, use it
and say why.

## Why this exists

Cerberus needs `ssh put_dir` / `get_dir` (work package WP-9) and there is no good
Go library for it. That was checked rather than assumed:

| Candidate | Why it does not serve |
|---|---|
| `github.com/pkg/sftp` | The right foundation — `Walk`, `ReadDir`, `MkdirAll`, `Chtimes` — but recursion, safety and options are left to the caller |
| `github.com/bramvdbogaerde/go-scp` v1.6.1 | Tagged and maintained, but speaks the **SCP protocol**, which OpenSSH has deprecated and now implements over SFTP anyway |
| `github.com/povsister/scp` | Supports recursion; **no tagged release** (v0.0.0 pseudo-version), and also SCP |
| `github.com/melbahja/goph` | A convenience wrapper over `x/crypto/ssh` — adds a layer rather than solving recursion |

So the gap is real. The library is worth existing because of its **opinions**,
not because it wraps SFTP.

## The opinions — this is the actual specification

A general-purpose "copy a directory" helper usually lacks these. They are what
makes it safe to point at a real host.

1. **Preserve mode.** An uploaded script that arrives non-executable is a bug
   that surfaces an hour later. Carry the permission bits.
2. **Refuse symlink escapes.** A symlink inside the tree pointing at
   `/etc/shadow` must not exfiltrate it. Resolve and reject links whose target
   leaves the sync root. Default deny; if following is ever allowed, it is an
   explicit option with a name that sounds like a decision.
3. **Temp-and-rename per file.** Write to a sibling temp name, then rename into
   place, so an interrupted sync never leaves a half-written file where a good
   one was. Fall back to remove-then-rename where the server lacks POSIX rename.
4. **Honour cancellation between files as well as within one.** `io.Copy` ignores
   context. A sync over a dropped VPN must stop promptly, not when the TCP stack
   notices.
5. **Report what happened.** Return a result — files transferred, skipped,
   bytes, duration — not just `error`. Callers surface this to operators.
6. **Plan before doing.** A dry-run mode that walks and reports without writing.
   Cerberus gates destructive operations behind an operator preview; a library
   that cannot describe its intent forces the caller to reimplement the walk.

## Design constraints

**Take a connection, do not make one.** The API accepts an existing
`*ssh.Client` (or an `*sftp.Client`). It must not take credentials, read
`~/.ssh/config`, or dial. Cerberus already owns authentication, host-key policy
and connection lifetime; a library that dials again would be a second transport
with different failure modes from the one the caller already has. This is the
single most important API decision — get it wrong and the library is unusable in
its first consumer.

**No delta transfer, and say so.** This copies every byte every time. That is
correct for config files, env files, compose stacks and agent definitions, and
wrong for shipping a 600MB image. State the limit in the README rather than
letting someone discover it. If a tar-over-exec strategy is added later it is a
second strategy, not a replacement.

**Symmetry.** Upload and download are the same walk in opposite directions.
Resist letting them drift into two implementations with different safety
properties — the download path is where "it is only reading" quietly becomes
"it wrote outside the destination".

**Errors name the path.** A failure must say which file and which side. "dial
unix: permission denied" with no path is the failure mode this whole effort
exists to avoid.

## Prior art and credit

Read `povsister/scp` and `bramvdbogaerde/go-scp` before starting. Take what they
got right — their handling of directory creation order and mode bits is worth
studying — and note where they stop.

**Where an approach is borrowed, say so in a comment naming the source.** Credit
is not optional and costs nothing.

The README carries an **Alternatives** section listing all four candidates above
with one honest line each on when to prefer them. A reader deciding between
libraries is better served by that than by a claim this one is best; if
`pkg/sftp` alone suffices for someone, the README should tell them.

## Repository conventions

Match the `hollis-labs/go-*` space — `go-strutil`, `go-sqlite` and `go-queue` are
the closest models. Read one before starting.

- `go.mod` on the same Go version as its siblings (currently `1.26.x`), module
  `github.com/hollis-labs/go-sftpsync`
- `doc.go` at the root carrying the package overview for pkg.go.dev
- `README.md`: one-paragraph purpose, a pre-1.0 status banner, the pkg.go.dev
  link, installation, a runnable snippet, an API table, and **Alternatives**
- `CHANGELOG.md` from the first release, breaking changes called out loudly
- `LICENSE` matching the siblings
- `examples/` with runnable demonstrations, as `go-strutil` does
- `AGENTS.md` if the repo will be worked by agents — `go-queue` has one
- Minimal dependencies: `pkg/sftp` and `golang.org/x/crypto`. Nothing else
  without a reason in the commit message.

## Go idioms worth being explicit about

- Options via functional options (`WithPreserveMode(false)`), not a struct
  literal callers must keep in sync.
- `context.Context` first parameter on anything that does I/O.
- Errors wrapped with `%w`, and sentinel errors for conditions a caller acts on
  — a symlink-escape refusal is one a caller may want to detect.
- Exported surface as small as it can be. Every exported symbol is a promise.
- Table-driven tests. Test against a **real SFTP server in-process** (`pkg/sftp`
  ships a server implementation) rather than a mock, so the tests exercise the
  protocol rather than your idea of it.

## Acceptance

- A directory with nested subdirectories, an executable script, an empty
  directory and a symlink round-trips upload → download and is byte-identical,
  modes intact.
- A symlink pointing outside the sync root is **refused**, with a test asserting
  it, and the refusal is a detectable sentinel error.
- An interrupted transfer leaves the previous file intact, with a test.
- Cancelling the context mid-sync returns promptly, with a test that would fail
  if cancellation were only checked once at the start.
- Dry-run reports the same file set the real run transfers.
- Tests run with no network and no external SSH server.
- `go vet`, `gofmt` and the linter the sibling repos use are clean.

## Out of scope

Delta transfer, compression, resume, bandwidth limiting, and anything that
requires a binary on the remote host. If any turn out to be needed, they are a
later minor version with a decision recorded — not scope creep into v0.1.0.

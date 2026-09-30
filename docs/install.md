# Install Cerberus

Cerberus ships as a single binary during beta. The four supported install
paths are Homebrew, GitHub release tarball, source build, and `go install`.
All four are equivalent — `cerberus install` (the launchd bootstrap) resolves
the running binary's path at runtime, so a Homebrew install, a `~/.local/bin`
install, and a `$GOPATH/bin` install all produce the right plist.

## Prerequisites

- macOS (primary platform; daemon install requires launchd)
- Linux binaries are published for CLI use; the launchd `install` subcommand is macOS-only
- If building from source: Go `1.26.6+` and `make`
- No separate database; Cerberus uses local SQLite under `~/.cerberus/`

## Option 1: Homebrew

```sh
brew install hollis-labs/tap/cerberus
```

Installs `cerberus` into Homebrew's bin (`/opt/homebrew/bin/cerberus` on Apple
Silicon, `/usr/local/bin/cerberus` on Intel). Verify with:

```sh
cerberus --version
```

## Option 2: Release Tarballs

Tagged beta releases publish tarballs for:

- macOS `arm64`
- macOS `amd64`
- Linux `arm64`
- Linux `amd64`

Each archive contains:

- `cerberus`
- `README.md`
- `LICENSE`

Example:

```sh
curl -L -o cerberus.tar.gz \
  https://github.com/hollis-labs/cerberus/releases/download/v0.4.0-beta.1/cerberus_0.4.0-beta.1_darwin_arm64.tar.gz
tar -xzf cerberus.tar.gz
install -d "$HOME/.local/bin"
install -m 0755 cerberus cerberus-presence "$HOME/.local/bin/"
export PATH="$HOME/.local/bin:$PATH"
```

Releases also include a per-archive `<artifact>.tar.gz.sha256` and a combined
`checksums.txt`.

## Option 3: Source Installs

### Build in place

Use this if you want repo-local binaries:

```sh
git clone git@github.com:hollis-labs/cerberus.git
cd cerberus
make build
export PATH="$PWD/bin:$PATH"
```

### Install into a prefix

Use this if you want shell-visible binaries from a source checkout:

```sh
git clone git@github.com:hollis-labs/cerberus.git
cd cerberus
make homebrew-install PREFIX="$HOME/.local"
export PATH="$HOME/.local/bin:$PATH"
```

Defaults:

- `PREFIX=/usr/local`
- `BINDIR=$(PREFIX)/bin`

Override either:

```sh
make homebrew-install BINDIR="$HOME/bin"
```

Uninstall mirrors the path you installed to:

```sh
make uninstall PREFIX="$HOME/.local"
```

### Web UI assets

`make build` skips the web console. To produce the full local stack (binary
plus the embedded React bundle):

```sh
make all
```

## Option 4: `go install`

```sh
go install github.com/hollis-labs/cerberus/cmd/cerberus@latest
```

This installs into one of:

- `$GOBIN`
- `$GOPATH/bin`
- `$HOME/go/bin`

Install the presence helper the same way, so it sits next to `cerberus`:

```sh
go install github.com/hollis-labs/cerberus/cmd/cerberus-presence@latest
```

## The presence helper

`cerberus-presence` is how the daemon asks the person at the Mac, through Touch
ID or the account password, before it allows a passkey enrollment. The daemon
looks for it next to its own `cerberus` binary. It is built with cgo on macOS:

- `make build`, `make go-install` and `make homebrew-install` build and install
  it beside `cerberus`.
- The release tarballs carry it beside `cerberus`, and the Homebrew formula
  installs both. On macOS it is built natively with cgo for arm64 and amd64,
  which is why a release is cut on a Mac. The Linux tarballs carry the stub,
  which refuses.
- `cerberus status` shows it on its `presence` line. It shows as installed, or
  MISSING with the path it looked at and how to put it there.

Without the helper, or on another OS, passkey enrollment is refused, and says
so. Everything else works as before. See docs/policy.md, "Out-of-band
approval, with a passkey".

## First-Time Setup

1. Seed a starter config:

   ```sh
   cerberus init
   ```

   Writes `~/.cerberus/config.yaml` with a blank `projects:` list.

2. (macOS only) Bootstrap the launchd agent so the daemon survives reboots:

   ```sh
   cerberus install
   ```

   This writes `~/Library/LaunchAgents/com.hollis-labs.cerberus.plist`
   with the path of the currently invoked `cerberus` binary baked in. Run it
   from whichever install path you want the plist to track — Homebrew,
   `~/.local/bin`, `$GOPATH/bin`, etc.

   The launch agent was named `com.fragments-engine.cerberus` before the rename. On
   an install that still has it, `cerberus install` boots that job out and removes
   its plist before it loads `com.hollis-labs.cerberus`, so two daemons never run
   at once. Resources whose launchd label Cerberus derives move to the new prefix
   the next time they are applied.

3. Confirm the daemon is healthy:

   ```sh
   cerberus resource list
   ```

By default Cerberus stores state under `~/.cerberus/`:

- config: `~/.cerberus/config.yaml`
- registry + main DB: resolved via `go-apppaths` (XDG-compliant)
- installed app artifacts: `~/.cerberus/apps/<project>/<resource>/`
- logs: `~/.cerberus/logs/`

`CERBERUS_DB_PATH` and `CERBERUS_WORKSPACE` override the default DB resolution.

## Daemon, Web Console, and MCP Modes

Cerberus is a single binary with three long-running modes:

- `cerberus daemon` — local HTTP/socket server used by the CLI, MCP, and web console
- `cerberus web` — compact local web console (default `http://127.0.0.1:4783`)
- `cerberus mcp` — stdio MCP server for agent clients
- `cerberus mcp-http` — HTTP MCP endpoint (default `http://127.0.0.1:4785/mcp`)

They share `~/.cerberus/` and the same SQLite store.

`cerberus web` needs a sign-in. On start it prints and opens a one-time
sign-in URL, good for two minutes and for one use. Visiting it gives the browser
two things:

- an `HttpOnly`, `SameSite=Strict` session cookie named for the console's port
  (`cerberus_session_4783`);
- a session key, handed to the page in the address fragment, which no server
  sees.

The page keeps the key in `localStorage` and sends it with every request. A
browser sends every `localhost` cookie to every port, so the cookie alone reaches
any other local server; the key is scoped to the console's own origin, port
included, and without it the cookie is not a session.

The session ends after 30 minutes without use (`--session-idle`), after twelve
hours in any case, on **Sign out**, and whenever `cerberus web` exits.
`cerberus web open` prints and opens another link for the running console. It
mints the link from a key that console keeps under `~/.cerberus/web/`, readable
only by you. Without a session, every API route answers 401.

The console listens on **both** `127.0.0.1` and `[::1]`, on one port. Every
link it hands out says `localhost`, which a browser may try on `::1` first. If
another process already holds `[::1]` on that port, `cerberus web` refuses to
start and says how to find it, rather than let it receive your sign-in and
approval links. A machine with no IPv6 loopback is served on `127.0.0.1` alone.

`~/.cerberus` is made `0700` and the config file `0600` on every start. If
either was readable by other accounts, the command says so once, as it fixes it.

`cerberus web` and, until you configure auth, `cerberus mcp-http` are
loopback-only. For both, `--listen` must name `localhost` or a literal loopback
IP (`127.0.0.1`, `[::1]`), on any port, and either command refuses to start
otherwise. Other hostnames are refused even when they resolve to
loopback. Both also refuse a request whose `Host` header is not a
loopback name, which defeats DNS rebinding. An SSH local forward
(`ssh -L 9000:127.0.0.1:4785 host`) works; a tunnel or reverse proxy that
forwards a public hostname does not. For browser-based MCP clients,
`mcp-http --allow-origin` adds exact origins to the loopback set.

**For a local MCP client, use `cerberus mcp` (stdio).** `mcp-http` needs
auth, because an HTTP listener can't tell which local account is calling. It is
an OAuth 2.1 resource server: with `~/.cerberus/mcp-http.yaml` configured, every
call needs a bearer token bound to its resource URL, and off loopback it needs
TLS as well. Without that file it refuses to start. On a machine with no other
human accounts, `--no-auth` runs it on loopback without auth, checked at every
start, with a banner and an audit record. Tokens come from
Cerberus's own minimal issuer (`cerberus mcp-http token issue`) or from an
external authorization server. See [docs/mcp-http.md](mcp-http.md).

Under the permissive posture only, `mcp-http --insecure-listen` accepts a
non-loopback `--listen`, and `--allow-host` names the host names and
addresses that clients reach it by. It has no authentication, so anyone who can
reach the address can call every tool. It warns at start and is recorded in the
audit log before it listens. The web console stays loopback-only in every
posture until it serves TLS. `cerberus posture show` lists everything the
posture changes.

## Uninstall

```sh
cerberus uninstall            # remove the launchd agent (macOS)
brew uninstall cerberus       # if installed via Homebrew
# or
make uninstall PREFIX="$HOME/.local"
rm -rf ~/.cerberus            # remove all local state (destructive!)
```

## Notes

- Cerberus is MIT licensed.
- The CLI and daemon are beta software; expect continued UX and docs iteration.
- Homebrew is the cleanest non-source install path on macOS today.
- Source installs remain the best fit when you are editing Cerberus itself.
- The launchd `install` subcommand always uses the path of the running binary;
  re-run it after switching install paths to refresh the plist.

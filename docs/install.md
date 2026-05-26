# Install Cerberus

Cerberus ships as a single binary during beta. The four supported install
paths are Homebrew, GitHub release tarball, source build, and `go install`.
All four are equivalent — `cerberus install` (the launchd bootstrap) resolves
the running binary's path at runtime, so a Homebrew install, a `~/.local/bin`
install, and a `$GOPATH/bin` install all produce the right plist.

## Prerequisites

- macOS (primary platform; daemon install requires launchd)
- Linux binaries are published for CLI use; the launchd `install` subcommand is macOS-only
- If building from source: Go `1.26.3+` and `make`
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
install -m 0755 cerberus "$HOME/.local/bin/"
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
go install github.com/chrispian/cerberus/cmd/cerberus@latest
```

This installs into one of:

- `$GOBIN`
- `$GOPATH/bin`
- `$HOME/go/bin`

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

   This writes `~/Library/LaunchAgents/com.fragments-engine.cerberus.plist`
   with the path of the currently invoked `cerberus` binary baked in. Run it
   from whichever install path you want the plist to track — Homebrew,
   `~/.local/bin`, `$GOPATH/bin`, etc.

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

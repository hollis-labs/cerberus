#!/usr/bin/env bash
# Verify, with the OLD build's `cerberus audit verify`, an audit chain the new build continued
# (docs/substrate-port-plan.md, section 4.2: hash-chained formats unchanged, so old can verify
# across them). Everything runs under a scratch HOME and a private D-Bus session with its own
# keyring daemon: the operator's keyring and ~/.cerberus are never touched.
#
#   scripts/verify-continued-chain.sh [scratch-dir]
#
# Needs git, go, dbus-run-session, gnome-keyring-daemon and secret-tool.
set -euo pipefail

repo=$(git rev-parse --show-toplevel)
scratch=${1:-$(mktemp -d /tmp/cerb-chain-XXXXXX)}
home="$scratch/home"
audit_dir="$home/.cerberus/audit"
mkdir -p "$home/.cerberus"
export GOWORK=off

echo "== build the old binary from origin/main"
git -C "$repo" worktree add -q --detach "$scratch/main" origin/main
trap 'git -C "$repo" worktree remove --force "$scratch/main" >/dev/null 2>&1 || true' EXIT
git -C "$scratch/main" log --oneline -1
(cd "$scratch/main" && go build -o "$scratch/cerberus-main" ./cmd/cerberus)

echo "== continue the old fixture chain with this branch"
(cd "$repo" && HOME="$scratch/testhome" CERBERUS_CONTINUED_CHAIN_OUT="$audit_dir" \
	go test -count=1 -run '^TestNewContinuesTheOldChain$' ./internal/auditchain)
mv "$home/.cerberus/anchor.json" "$scratch/anchor.json"
wc -l "$audit_dir"/*.jsonl

echo "== old binary: audit verify (private session bus and keyring)"
mkdir -p "$scratch/run" && chmod 700 "$scratch/run"
HOME="$home" XDG_DATA_HOME="$home/.local/share" XDG_CONFIG_HOME="$home/.config" \
	XDG_RUNTIME_DIR="$scratch/run" ANCHOR_FILE="$scratch/anchor.json" AUDIT_DIR="$audit_dir" BIN="$scratch/cerberus-main" \
	dbus-run-session -- bash -c '
		set -euo pipefail
		# A throwaway login keyring in the scratch HOME, unlocked with a throwaway password.
		eval "$(printf "scratch-keyring" | gnome-keyring-daemon --unlock --components=secrets)" >/dev/null
		secret-tool store --label="cerberus audit anchor (scratch)" service cerberus-audit-anchor username "$AUDIT_DIR" < "$ANCHOR_FILE"
		"$BIN" audit verify
	'
echo "== verified: the old binary accepts the chain the new build continued"

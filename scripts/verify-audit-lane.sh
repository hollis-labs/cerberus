#!/usr/bin/env bash
# Verify, with the OLD build's `cerberus audit verify`, the audit chains the new build's audit
# lane leaves (slice 7d: `cerberus audit prune` and `audit reanchor`, served by the serving
# Cerberus on its own sink). Everything runs under a scratch HOME and a private D-Bus session with
# its own keyring daemon: the operator's keyring and ~/.cerberus are never touched.
#
#   scripts/verify-audit-lane.sh [scratch-dir]
#
# Three chains, written through the lane's routes by TestAuditLaneChainsForTheOldVerify:
#   prune           two month files pruned, then continued     old verify passes
#   reanchor-cut    a cut log the owner reanchored             old verify passes (the anchor moved)
#   reanchor-break  a rewritten record the owner reanchored    old verify reports the break as
#                   acknowledged by 1 reanchor, as old always did: a reanchor rewrites nothing
#
# Needs git, go, tar, dbus-run-session, gnome-keyring-daemon and secret-tool.
set -euo pipefail

repo=$(git rev-parse --show-toplevel)
scratch=${1:-$(mktemp -d /tmp/cerb-lane-XXXXXX)}
export GOWORK=off

echo "== build the old binary from git archive origin/main"
mkdir -p "$scratch/main"
git -C "$repo" archive origin/main | tar -x -C "$scratch/main"
git -C "$repo" log --oneline -1 origin/main
(cd "$scratch/main" && go build -o "$scratch/cerberus-main" ./cmd/cerberus)

echo "== write the chains through the audit lane on this branch"
gc=$(go env GOCACHE)
gm=$(go env GOMODCACHE)
(cd "$repo" && HOME="$scratch/testhome" GOCACHE="$gc" GOMODCACHE="$gm" CERBERUS_AUDIT_LANE_OUT="$scratch/out" \
	go test -count=1 -run '^TestAuditLaneChainsForTheOldVerify$' ./internal/api)

verify() { # scenario, then whether old verify must pass
	local scenario=$1 want=$2 home="$scratch/home-$1"
	mkdir -p "$home/.cerberus" "$scratch/run-$1" && chmod 700 "$scratch/run-$1"
	cp -r "$scratch/out/$scenario/audit" "$home/.cerberus/audit"
	echo "== $scenario: $(cat "$home"/.cerberus/audit/*.jsonl | wc -l) records in $(ls "$home"/.cerberus/audit/*.jsonl | wc -l) month file(s)"
	set +e
	out=$(HOME="$home" XDG_DATA_HOME="$home/.local/share" XDG_CONFIG_HOME="$home/.config" \
		XDG_RUNTIME_DIR="$scratch/run-$1" ANCHOR_FILE="$scratch/out/$scenario/anchor.json" AUDIT_DIR="$home/.cerberus/audit" BIN="$scratch/cerberus-main" \
		dbus-run-session -- bash -c '
			set -euo pipefail
			# A throwaway login keyring in the scratch HOME, unlocked with a throwaway password.
			eval "$(printf "scratch-keyring" | gnome-keyring-daemon --unlock --components=secrets)" >/dev/null
			secret-tool store --label="cerberus audit anchor (scratch)" service cerberus-audit-anchor username "$AUDIT_DIR" < "$ANCHOR_FILE"
			"$BIN" audit verify
		' 2>&1)
	code=$?
	set -e
	echo "$out" | sed 's/^/   old: /'
	case $want in
	pass)
		[ "$code" -eq 0 ] || { echo "FAIL: old audit verify refused the $scenario chain"; exit 1; } ;;
	acknowledged)
		[ "$code" -ne 0 ] && echo "$out" | grep -q 'acknowledged by 1 reanchor(s): what follows the last one is trusted' ||
			{ echo "FAIL: old audit verify did not report the $scenario break as acknowledged"; exit 1; } ;;
	esac
	echo "   ok ($want)"
}

verify prune pass
verify reanchor-cut pass
verify reanchor-break acknowledged
echo "== verified: the old binary accepts the chains the audit lane left"

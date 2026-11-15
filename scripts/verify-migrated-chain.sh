#!/usr/bin/env bash
# Verify, with the OLD build's `cerberus audit verify`, the audit chain `cerberus migrate-from`
# carried into a new data directory (docs/substrate-port-plan.md, section 5: the chain is copied
# byte for byte and its anchor written under the new directory's path, so old still verifies it).
#
# It materializes the old-tree fixture (internal/legacyimport/testdata, written on main), runs
# this branch's migrate-from on it, then places the migrated <data>/audit as ~/.cerberus/audit of a
# second scratch HOME, seeds that account with the anchor migrate-from wrote, and runs the old
# binary's audit verify there. Everything runs under scratch HOMEs and a private D-Bus session
# with its own keyring daemon: the operator's keyring, ~/.cerberus and XDG directories are never
# touched.
#
#   scripts/verify-migrated-chain.sh [scratch-dir]
#
# Needs git, go, dbus-run-session, gnome-keyring-daemon and secret-tool.
set -euo pipefail
umask 077

repo=$(git rev-parse --show-toplevel)
scratch=${1:-$(mktemp -d /tmp/cerb-migrated-XXXXXX)}
fixture="$repo/internal/legacyimport/testdata"
home="$scratch/u"
snap="$scratch/snap/.cerberus"
backup="$scratch/backup/main.db"
data="$scratch/data"
check_home="$scratch/oldcheck/u"
mkdir -p "$home" "$scratch/snap" "$scratch/backup" "$check_home/.cerberus"
export GOWORK=off

echo "== build the old binary from origin/main"
git -C "$repo" worktree add -q --detach "$scratch/main" origin/main
trap 'git -C "$repo" worktree remove --force "$scratch/main" >/dev/null 2>&1 || true' EXIT
git -C "$scratch/main" log --oneline -1
(cd "$scratch/main" && go build -o "$scratch/cerberus-main" ./cmd/cerberus)

echo "== build this branch"
git -C "$repo" log --oneline -1
(cd "$repo" && go build -o "$scratch/cerberus-new" ./cmd/cerberus)

echo "== materialize the old-tree fixture"
cp -a "$fixture/oldtree" "$snap"
while read -r d; do [ -n "$d" ] && mkdir -p "$snap/$d"; done < "$fixture/oldtree.emptydirs"
mkdir -p "$scratch/apps/shop"
cp "$fixture/descriptors/shop.cerberus.yaml" "$scratch/apps/shop/"
sed -i "s#/tmp/oldtree-gen/apps#$scratch/apps#" "$snap/registry.yaml"
cp "$fixture/oldmain.db" "$backup"
chmod -R go-rwx "$scratch/snap" "$scratch/backup"

mkdir -p "$scratch/run" && chmod 700 "$scratch/run"
HOME="$home" XDG_DATA_HOME="$home/.local/share" XDG_CONFIG_HOME="$home/.config" XDG_STATE_HOME="$home/.local/state" \
	XDG_CACHE_HOME="$home/.cache" XDG_RUNTIME_DIR="$scratch/run" CERBERUS_DATA_DIR="$data" \
	SNAP="$snap" BACKUP="$backup" DATA="$data" OLD_ACCOUNT="$home/.cerberus/audit" ANCHOR_FILE="$fixture/oldanchor.json" \
	CHECK_HOME="$check_home" NEW="$scratch/cerberus-new" OLD="$scratch/cerberus-main" \
	dbus-run-session -- bash -c '
		set -euo pipefail
		# A throwaway login keyring, unlocked with a throwaway password.
		eval "$(printf "scratch-keyring" | gnome-keyring-daemon --unlock --components=secrets)" >/dev/null
		printf %s "$(cat "$ANCHOR_FILE")" | secret-tool store --label="cerberus audit anchor (old, scratch)" service cerberus-audit-anchor username "$OLD_ACCOUNT"

		echo "== new binary: migrate-from (private session bus and keyring)"
		"$NEW" migrate-from --old-home "$SNAP" --old-db "$BACKUP" --old-anchor-account "$OLD_ACCOUNT"

		echo "== the anchor migrate-from wrote under the new audit dir"
		new_anchor=$(secret-tool lookup service cerberus-audit-anchor username "$DATA/audit")
		echo "$new_anchor"
		[ "$new_anchor" = "$(cat "$ANCHOR_FILE")" ] || { echo "the new anchor is not the old head"; exit 1; }

		echo "== old binary: audit verify on the migrated chain, placed as ~/.cerberus/audit"
		cp -a "$DATA/audit" "$CHECK_HOME/.cerberus/audit"
		printf %s "$new_anchor" | secret-tool store --label="cerberus audit anchor (migrated, scratch)" service cerberus-audit-anchor username "$CHECK_HOME/.cerberus/audit"
		HOME="$CHECK_HOME" XDG_DATA_HOME="$CHECK_HOME/.local/share" XDG_CONFIG_HOME="$CHECK_HOME/.config" "$OLD" audit verify

		echo "== old binary: audit verify after the chain is cut (it must fail)"
		cut="$(dirname "$CHECK_HOME")/cut"
		mkdir -p "$cut/u/.cerberus"
		cp -a "$DATA/audit" "$cut/u/.cerberus/audit"
		# Cut the log at the anchored record: the records after it go, and so does it.
		file=$(printf %s "$new_anchor" | sed -E "s/.*\"file\":\"([^\"]+)\".*/\1/")
		off=$(printf %s "$new_anchor" | sed -E "s/.*\"offset\":([0-9]+).*/\1/")
		f="$cut/u/.cerberus/audit/$file"
		head -c "$off" "$f" > "$f.tmp" && mv "$f.tmp" "$f"
		printf %s "$new_anchor" | secret-tool store --label="cerberus audit anchor (cut, scratch)" service cerberus-audit-anchor username "$cut/u/.cerberus/audit"
		if HOME="$cut/u" XDG_DATA_HOME="$cut/u/.local/share" XDG_CONFIG_HOME="$cut/u/.config" "$OLD" audit verify; then
			echo "the old binary accepted a cut chain: the check above proves nothing"; exit 1
		fi
		echo "(refused, as it must be)"
	'
echo "== verified: the old binary accepts the chain migrate-from carried, under the anchor it wrote"

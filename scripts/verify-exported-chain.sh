#!/usr/bin/env bash
# Verify, with the OLD build's `cerberus audit verify`, the chain `cerberus export-legacy` wrote
# back into an old tree (docs/substrate-port-plan.md, section 5, the reverse path; section 6 of the
# slice 10b spec): the rollback rehearsal.
#
# It builds the old binary from `git archive origin/main` in a temp dir (never a working tree),
# materializes the old-tree fixture (internal/legacyimport/testdata, written on main) twice, takes
# the D3 manifest of copy1 and runs this branch's migrate-from on it, drives a window of new
# activity in the data directory (the Go driver TestDriveActivityForTheRehearsal: the gate, an
# owner-session approval, a brake, pause, applied digests, host rows, a receipt, a month
# rollover), then runs export-legacy onto copy2, runs it again (a no-op), and ends with the old
# binary's `audit verify` on copy2 (it must pass) and on a cut copy of it (it must fail).
#
# Everything runs under scratch HOMEs and a private D-Bus session with its own keyring daemon: the
# operator's keyring, ~/.cerberus and XDG directories are never touched.
#
#   scripts/verify-exported-chain.sh [scratch-dir]
#
# Needs git, go, tar, dbus-run-session, gnome-keyring-daemon and secret-tool.
set -euo pipefail
umask 077

repo=$(git rev-parse --show-toplevel)
scratch=${1:-$(mktemp -d /tmp/cerb-exported-XXXXXX)}
fixture="$repo/internal/legacyimport/testdata"
home="$scratch/u"
data="$scratch/data"
copy1="$scratch/copy1/.cerberus"
db1="$scratch/copy1db/main.db"
copy2home="$scratch/copy2"
copy2="$copy2home/.cerberus"
db2="$scratch/copy2db/main.db"
mkdir -p "$home" "$scratch/copy1" "$copy2home" "$scratch/copy1db" "$scratch/copy2db" "$scratch/apps/shop"
export GOWORK=off
# The Go caches, read before any HOME below is a scratch one.
GOCACHE=$(go env GOCACHE)
GOMODCACHE=$(go env GOMODCACHE)
export GOCACHE GOMODCACHE

echo "== build the old binary from origin/main (git archive into a temp dir)"
mkdir -p "$scratch/main"
git -C "$repo" archive origin/main | tar -x -C "$scratch/main"
git -C "$repo" log --oneline -1 origin/main
(cd "$scratch/main" && go build -o "$scratch/cerberus-main" ./cmd/cerberus)

echo "== build this branch"
git -C "$repo" log --oneline -1
(cd "$repo" && go build -o "$scratch/cerberus-new" ./cmd/cerberus)

echo "== materialize the old-tree fixture twice"
cp "$fixture/descriptors/shop.cerberus.yaml" "$scratch/apps/shop/"
materialize() {
	cp -a "$fixture/oldtree" "$1"
	while read -r d; do [ -n "$d" ] && mkdir -p "$1/$d"; done < "$fixture/oldtree.emptydirs"
	sed -i "s#/tmp/oldtree-gen/apps#$scratch/apps#" "$1/registry.yaml"
	cp "$fixture/oldmain.db" "$2"
	chmod -R go-rwx "$1" "$(dirname "$2")"
}
materialize "$copy1" "$db1"
materialize "$copy2" "$db2"

mkdir -p "$scratch/run" && chmod 700 "$scratch/run"
HOME="$home" XDG_DATA_HOME="$home/.local/share" XDG_CONFIG_HOME="$home/.config" XDG_STATE_HOME="$home/.local/state" \
	XDG_CACHE_HOME="$home/.cache" XDG_RUNTIME_DIR="$scratch/run" CERBERUS_DATA_DIR="$data" \
	SCRATCH="$scratch" REPO="$repo" DATA="$data" COPY1="$copy1" DB1="$db1" COPY2HOME="$copy2home" COPY2="$copy2" DB2="$db2" \
	ANCHOR_FILE="$fixture/oldanchor.json" NEW="$scratch/cerberus-new" OLD="$scratch/cerberus-main" \
	dbus-run-session -- bash -c '
		set -euo pipefail
		# A throwaway login keyring, unlocked with a throwaway password.
		eval "$(printf "scratch-keyring" | gnome-keyring-daemon --unlock --components=secrets)" >/dev/null
		anchor=$(cat "$ANCHOR_FILE")
		for acct in "$COPY1/audit" "$COPY2/audit"; do
			printf %s "$anchor" | secret-tool store --label="cerberus audit anchor (old, scratch)" service cerberus-audit-anchor username "$acct"
		done
		scratch_env() { env HOME="$1" XDG_DATA_HOME="$1/.local/share" XDG_CONFIG_HOME="$1/.config" XDG_STATE_HOME="$1/.local/state" XDG_CACHE_HOME="$1/.cache" "${@:2}"; }

		echo "== new binary: the D3 manifest of copy1, then migrate-from with it"
		"$NEW" manifest write --old-home "$COPY1" --old-db "$DB1" --anchor-account "$COPY1/audit" --out "$SCRATCH/d3-manifest.json"
		"$NEW" migrate-from --old-home "$COPY1" --old-db "$DB1" --old-anchor-account "$COPY1/audit" --manifest "$SCRATCH/d3-manifest.json" >"$SCRATCH/migrate-from.txt"
		head -1 "$SCRATCH/migrate-from.txt"
		handoff=$(sed -nE "s/.*\"body_sha256\": \"(sha256:[0-9a-f]{64})\".*/\1/p" "$SCRATCH/d3-manifest.json")
		echo "handoff $handoff"

		echo "== a window of new activity in the data directory (the Go driver)"
		(cd "$REPO" && CERBERUS_EXPORT_DRIVE_DATA="$DATA" CERBERUS_EXPORT_DRIVE_DB="$DATA/cerberus.db" \
			go test ./internal/legacyexport/ -run "^TestDriveActivityForTheRehearsal$" -count=1 -v | grep -E "drove:|^(ok|FAIL|---)")
		ls "$DATA/audit"

		echo "== new binary: runbook step 0, plugin-connectors.json against the D3 copy"
		mkdir -p "$SCRATCH/d3copy" && cp -a "$COPY2" "$SCRATCH/d3copy/cerberus-home"
		"$NEW" export-legacy --check-legacy-files --old-home "$COPY2" --d3-copy "$SCRATCH/d3copy"

		echo "== new binary: export-legacy onto copy2"
		"$NEW" export-legacy --since "$handoff" --old-home "$COPY2" --old-db "$DB2" --old-anchor-account "$COPY2/audit" >"$SCRATCH/export-1.txt" || { cat "$SCRATCH/export-1.txt"; exit 1; }
		cat "$SCRATCH/export-1.txt"
		ls "$COPY2/audit"

		echo "== new binary: export-legacy again (a no-op)"
		snap() { (cd "$COPY2" && find . -type f -print0 | sort -z | xargs -0 sha256sum; sha256sum "$DB2"; secret-tool lookup service cerberus-audit-anchor username "$COPY2/audit") | sha256sum; }
		before=$(snap)
		"$NEW" export-legacy --since "$handoff" --old-home "$COPY2" --old-db "$DB2" --old-anchor-account "$COPY2/audit" >"$SCRATCH/export-2.txt" || { cat "$SCRATCH/export-2.txt"; exit 1; }
		head -1 "$SCRATCH/export-2.txt"
		[ "$before" = "$(snap)" ] || { echo "the second run changed copy2"; exit 1; }
		echo "(copy2, its main.db and its anchor unchanged)"

		echo "== the moved anchor is new'\''s last record"
		moved=$(secret-tool lookup service cerberus-audit-anchor username "$COPY2/audit")
		echo "$moved"
		[ "$moved" != "$anchor" ] || { echo "the anchor did not move"; exit 1; }

		echo "== approvals and brakes on copy2 (old has no verify verb for them)"
		(cd "$REPO" && CERBERUS_EXPORT_CHECK_OLD_HOME="$COPY2" CERBERUS_EXPORT_DRIVE_DATA="$DATA" \
			go test ./internal/legacyexport/ -run "^TestCheckTheExportedCopyForTheRehearsal$" -count=1 -v | grep -E "approvals:|^(ok|FAIL|---)")
		mkdir -p "$SCRATCH/apcheck" && cp -a "$COPY2" "$SCRATCH/apcheck/.cerberus"
		scratch_env "$SCRATCH/apcheck" "$OLD" approvals list >"$SCRATCH/approvals-list.txt" 2>&1 || { cat "$SCRATCH/approvals-list.txt"; exit 1; }
		cat "$SCRATCH/approvals-list.txt"
		! grep -q "Warning" "$SCRATCH/approvals-list.txt" || { echo "the old binary warns about the approvals store"; exit 1; }
		grep -q "consumed" "$SCRATCH/approvals-list.txt" || { echo "the old binary does not list the consumed approval"; exit 1; }

		echo "== old binary: audit verify on copy2 (HOME=copy2)"
		scratch_env "$COPY2HOME" "$OLD" audit verify

		echo "== old binary: audit verify after the chain is cut (it must fail)"
		cut="$SCRATCH/cut"
		mkdir -p "$cut/.cerberus"
		cp -a "$COPY2/audit" "$cut/.cerberus/audit"
		# Cut the log at the anchored record: the records after it go, and so does it.
		file=$(printf %s "$moved" | sed -E "s/.*\"file\":\"([^\"]+)\".*/\1/")
		off=$(printf %s "$moved" | sed -E "s/.*\"offset\":([0-9]+).*/\1/")
		f="$cut/.cerberus/audit/$file"
		head -c "$off" "$f" > "$f.tmp" && mv "$f.tmp" "$f"
		printf %s "$moved" | secret-tool store --label="cerberus audit anchor (cut, scratch)" service cerberus-audit-anchor username "$cut/.cerberus/audit"
		if scratch_env "$cut" "$OLD" audit verify; then
			echo "the old binary accepted a cut chain: the check above proves nothing"; exit 1
		fi
		echo "(refused, as it must be)"
	'
echo "== verified: the old binary accepts the chain export-legacy wrote back, under the anchor it moved"

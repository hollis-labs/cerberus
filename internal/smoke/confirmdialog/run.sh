#!/bin/sh
# Browser smoke of the console's approval UI: the confirm dialog for a
# resource stop (shown its plan, refused on a wrong target and on a stale
# plan, run on the right target), and break glass on a protected target (a
# passkey enrolled on a virtual authenticator, the BREAK GLASS approval, the
# retry, the follow-up), a credential save on the Credentials page and a
# prod stop approved with the passkey in place on the console, a lockdown
# engaged, refusing
# and lifted with the passkey in place, the circuit breaker, and an approval link's sign-in
# scoped to one approval. Needs the web bundle built
# (`make all`), Node 22+ and Google Chrome. Everything lives in a scratch
# HOME under /tmp; nothing touches ~/.cerberus or a running daemon.
set -eu
root=$(cd "$(dirname "$0")/../../.." && pwd)
home=$(mktemp -d /tmp/cerb-confirm-smoke.XXXXXX)
bin="$home/confirmdialog"
trap 'kill "$pid" 2>/dev/null || true; rm -rf "$home"' EXIT INT TERM
(cd "$root" && go build -o "$bin" ./internal/smoke/confirmdialog)
HOME="$home" "$bin" >"$home/server.log" 2>&1 &
pid=$!
i=0
until grep -q READY "$home/server.log" 2>/dev/null; do
	i=$((i + 1))
	[ "$i" -gt 50 ] && { cat "$home/server.log"; exit 1; }
	sleep 0.2
done
SMOKE_HOME="$home" node "$root/internal/smoke/confirmdialog/smoke.mjs"

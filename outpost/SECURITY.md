# Security

Outpost is pre-release software. Security fixes target the current main branch;
there are no supported release lines yet.

Report suspected vulnerabilities through [GitHub private vulnerability
reporting](https://github.com/hollis-labs/tools/security/advisories/new).
Include Outpost's revision and a minimal reproduction using synthetic data.
Do not publish bearer credentials, pairing stores or machine inventories.

## Trust boundary

The local operator controls the inventory, pairing store, plugin binaries and
plugin configuration. Keep these files and their parent directories private to
the service account. Plugins run as that account and can access anything it
can access; per-plugin HTTP scopes do not sandbox a plugin's filesystem or
process access. Install only trusted plugins.

Pairings use random bearer credentials and store only their hashes. Use a
separate pairing for each controller and plugin. A pairing grants access to all
routes that plugin exposes; the plugin/controller owns any finer policy,
approval, audit and output redaction. Revocation affects subsequent requests,
not operations already admitted.

Only Content-Type is forwarded as a request header to plugins. Avoid putting
credentials in query strings or request bodies: those are forwarded as payload.
The unauthenticated /healthz endpoint indicates process liveness. The public
/api/readyz endpoint reports aggregate plugin readiness and the host PID, without
plugin identities, configuration, or failure details. Optional plugin failures
do not relax authentication: their routes remain protected and return 503 only
to a controller authorized for that plugin. Deployment acceptance requires all
plugins to be healthy, even optional ones.

Plain HTTP accepts literal loopback addresses and the Tailscale IPv4/IPv6
ranges. An address-range check cannot verify that an interface is actually
managed by Tailscale or that traffic is encrypted. The operator must ensure
that transport is trusted and encrypted, or use an HTTPS reverse proxy bound
to a loopback listener. Never expose a plaintext bearer-token endpoint to an
untrusted network.

Pair/revoke serialize local writes using a sibling .lock directory. If a
process is interrupted, confirm that no pairing operation is running before
removing a stale lock. Keep recovery access independent of Outpost.

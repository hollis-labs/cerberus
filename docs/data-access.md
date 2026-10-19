# Session-authorized data projections

The serving daemon owns `POST /data/v1/query`. Enable it by placing an
operator-owned `data-access.json` beside its registry config. An absent file
leaves the endpoint unavailable. No database credential or grant is created
by enabling the endpoint.

```json
{
  "issuer": "https://issuer.example.test",
  "jwks_url": "https://issuer.example.test/.well-known/jwks.json",
  "denylist_url": "https://issuer.example.test/denylist",
  "grants_file": "/private/cerberus/data-grants.json"
}
```

Use the actual Tether issuer and its published authorization endpoints. HTTPS
is mandatory; JWKS and deny-list URLs must use the issuer's origin, without
redirects, userinfo, query credentials or fragments. The deny-list endpoint
must return a full shared-auth snapshot with `jti`, `sub` and `sid` arrays.
A first 304 cannot establish state. Successful conditional refreshes may keep
a verified snapshot fresh; failures never clear it. The maximum snapshot age
is ten seconds, polling every five seconds with a two-second fetch deadline.

A token is a verified EdDSA Tether **agent session** JWT, audience
`cerberus-data`, carrying exact scope `cerberus-data.read`, `sub`, `sid`, `par`,
`jti`, `iat` and `exp`. Its lifetime must be at most five minutes. Wildcard
scope alone, an operator/admin identity, missing session provenance, future
issuance and expiry within the shared verifier's leeway are refused here.

The grant file is a strict JSON array of host-owned grants, readable by the
serving daemon and not writable by group/others. Publish edits atomically.
Removing or changing a grant cancels an in-flight call on its next authorization
check (50ms polling); the result is checked again before delivery.

```json
[{
  "id": "example-read",
  "issuer": "https://issuer.example.test",
  "subject": "msg://agent/example",
  "session": "SESSION_FROM_VERIFIED_ISSUER",
  "token_id": "JTI_FROM_VERIFIED_ISSUER",
  "parent": "PARENT_FROM_VERIFIED_ISSUER",
  "connector": "postgres-data",
  "target": "example",
  "projection": "documents",
  "version": "SHA256_OF_PUBLISHED_PROJECTION",
  "expires_at": "2026-01-01T00:00:00Z",
  "max_rows": 100,
  "max_bytes": 1048576
}]
```

That example is deliberately expired. Grants bind an exact token generation;
renewing a JWT does not extend a grant. Grant authoring is an operator file
workflow in this slice, not an agent-accessible issuance endpoint. Publish only
projections the operator has approved for that session. The ordinary Cerberus
policy decision must also explicitly allow the `postgres.data` target and
`read_sensitive` effect; a grant never widens that decision.

## Scoped policy activation

The secure built-in baseline requires approval for an agent's sensitive read,
so the baseline alone intentionally refuses these requests even with a grant.
Register the named `postgres-data` target with explicit environment/ownership
labels. For an approved target, the existing policy model can relax its baseline
using a posture rule scoped to the exact connector, target id and target kind:

```yaml
posture_rules:
  - match:
      connector: postgres-data
      id: example
      kind: postgres.data
    posture: permissive
```

Review/apply that rule through the normal policy workflow. Do not replace the
global sensitive-read baseline or global posture. Other matching operator deny,
approval and rate rules remain effective. The data adapter still enforces JWT,
exact grants, revocation freshness and audit regardless of policy posture; the
plugin capability gate still refuses every generic entry path. Scoped posture
rules only apply to registered targets with explicit environment/owner labels.
The integration test first proves baseline denial, then activates this exact
scope to prove a real authorized query.

## Calling the route

```sh
cerberus data query --token-file /private/session.jwt < request.json
```

```json
{
  "grant_id": "example-read",
  "target": "example",
  "projection": "documents",
  "version": "SHA256_OF_PUBLISHED_PROJECTION",
  "parameters": {"tenant": "example"}
}
```

The CLI reads a bounded private token file; it has no token argument or
environment-token fallback. The web console also forwards this same versioned
path to the daemon, accepting only the original bearer header and a typed
request. It does not exchange browser sessions for data authority; requests
with a browser Origin are refused. Publish that path through the existing TLS
reverse proxy. The daemon verifies again. Older daemons refuse the new route;
clients never fall back to the generic connector or in-process executor.
Agents using MCP may invoke the CLI through their private session environment,
or use the HTTPS endpoint. Generic generated plugin tools cannot issue data
queries: they have no internal authorization proof.

## Executor boundary

`data_query_v1` is a plugin capability. Old hosts reject it at installation.
The host launches a fresh read-credential instance for each authorized query,
placing a query digest, expiry and output limits only in private Init config.
It strips a same-named operator setting and refuses generic calls without the
internal proof. Unload, cancellation and completion terminate that instance.

The separate `postgres-data` distribution implements PostgreSQL reads. It holds
one dedicated non-owner SELECT credential, never the administration plugin's
provisioner credential. The host contains no PostgreSQL driver. Parameters
remain values; target, projection definition and database connection are
operator settings. Definition hashes bind grants to the exact column/filter/
vector contract. Changing a definition invalidates old grants.

Every query has a deadline of the earliest of two seconds, token expiry and
grant expiry; PostgreSQL adds statement and lock timeouts. Limits are 100 rows
or vector top-k, 1MiB encoded results, two active queries per issuer/session and
eight globally. Results are buffered. Admission and completion audit persistence
are mandatory; audit failure releases no data. Audit stores identity, target,
grant/version, keyed parameter digest, duration and row/byte counts, never raw
query parameters, rows, database passwords or JWTs.

These checks are not an OS sandbox against the operator UID or root. Deploy
with distinct remote trust/OS boundaries where that threat is relevant.

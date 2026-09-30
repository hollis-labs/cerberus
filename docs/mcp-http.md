# mcp-http: authentication and TLS

**For a local client, use `cerberus mcp` (stdio).** It runs as you, over the
daemon's socket, and the daemon checks that the caller is your account.

`cerberus mcp-http` serves the same tools over HTTP, for clients that can't
spawn a stdio server and for reaching Cerberus from elsewhere. An HTTP listener
can't tell which local account is calling (a TCP connection on macOS carries no
uid), so it needs auth:

- **An OAuth 2.1 resource server.** This is the normal mode. Once you configure
  an issuer, every call needs a bearer token bound to this endpoint, on loopback
  too, because loopback isn't a trust boundary between processes or accounts.
  Off loopback, it also needs TLS. Without an issuer configured, mcp-http
  refuses to start.
- **`--no-auth`, on loopback, on a single-account machine.** It listens on
  `127.0.0.1:4785` and trusts any process that can reach the port. That
  includes another account's processes, so it is allowed only when the machine
  has no other human accounts (on macOS, a local account with a uid of 501 or
  more and a login shell; on Linux, `/etc/passwd` from uid 1000). The account
  list is read at every start, and the refusal names the accounts it found. The
  start is recorded in the audit log, and a banner says what it means.

**The easy way to reach it from another machine is to leave it on loopback**,
and use an SSH local forward (`ssh -L 9000:127.0.0.1:4785 host`) or Tailscale.
They bring their own transport security and identity. Use the rest of this
page when you want mcp-http itself to authenticate its callers.

## Configure

Create `~/.cerberus/mcp-http.yaml`:

```yaml
resource: https://cerberus.example.com:4785/mcp   # the URL clients reach /mcp by
builtin: true                                    # Cerberus's own operator-issued tokens
tls:
  cert: /etc/ssl/cerberus/fullchain.pem
  key: /etc/ssl/cerberus/privkey.pem
```

- `resource` is the audience every token must carry
  ([RFC 8707](https://www.rfc-editor.org/rfc/rfc8707)). A token minted for any
  other resource is refused, even one from the same issuer.
- `resource` may be plain `http` only on loopback, as in
  `http://127.0.0.1:4785/mcp`.

The file is strict: an unknown key refuses it. Restart the daemon after you
edit it. The daemon verifies every token itself, and mcp-http won't start
requiring auth until the daemon confirms it has loaded the same resource.

## Tokens from the built-in issuer

The built-in issuer authenticates nobody, and Cerberus isn't an identity
provider. You, on your terminal, mint a token for one named client:

```bash
cerberus mcp-http token issue --client claude-code --scope cerberus:read,cerberus:operate --ttl 7d
cerberus mcp-http token list
cerberus mcp-http token revoke tok_…
```

Minting and revoking each ask for a typed confirmation, and each is recorded in
the audit log. An agent can't do either. A token is shown once, and Cerberus
keeps only its id. Give it to the client as a header in its HTTP MCP
configuration:

```json
"headers": { "Authorization": "Bearer <token>" }
```

Built-in tokens are EdDSA-signed JWTs. The signing key is kept in your
Keychain. A token lives 7 days by default and 90 days at most (`max_ttl` in the
file lowers that). The daemon checks revocation on every call.

## Tokens from an external authorization server

```yaml
issuers:
  - issuer: https://idp.example.com
    jwks_uri: https://idp.example.com/.well-known/jwks.json   # optional; discovered from the issuer's metadata
    client_claim: azp        # default: client_id, then azp
    scopes_claim: scp        # default: scope, then scp
```

The issuer must put this resource in the token's `aud`. Clients are
identified by their Client ID Metadata Document URL. Dynamic Client
Registration isn't supported. Only EdDSA, ES256 and RS256 signatures are
accepted, never `none` or HMAC. Keys are cached for 10 minutes, and an unknown
key id triggers a refetch at most once a minute.

## Scopes

| Scope | Allows |
|---|---|
| `cerberus:read` | plain reads |
| `cerberus:read_sensitive` | adds reads of text Cerberus didn't compose: logs, command output. It doesn't cover a read that writes local files, such as an SFTP download into a local path, which needs `operate` |
| `cerberus:operate` | any effect, each still decided by policy |

Scopes narrow what a caller may ask for. They never widen what policy allows.

- **A token's caller is always an agent.** It still goes through every deny,
  approval, brake, rate limit and the circuit breaker, and it can't approve
  anything.
- **A call its scopes don't cover** is refused as `insufficient_scope`, and
  nothing runs. The refusal says which scope is needed.

**A verified caller is recorded by who its token names:**

- its subject, issuer, client and token id. The client is the token's own
  client claim, or none. It is never the name the MCP client gives itself,
  which it could change on every call;
- rate limits and the circuit breaker count it by issuer and subject alone,
  so a suspension survives the client reconnecting, and a changed client name
  doesn't reset its counters;
- an approval or a grant belongs to the subject that asked for it. One token
  holder's window grant doesn't cover another's calls, and a once approval
  can't be used by another token holder who learns its id. A refreshed token
  for the same subject is the same caller;
- a policy rule can name `subject:` or `issuer:` under `principals:`. Such a
  rule matches only a verified caller, never a claim.

## Where it listens

| Listener | No auth configured | Auth configured |
|---|---|---|
| Loopback | Open to local processes | Token required |
| Off loopback, with TLS | Refused (unless `--insecure-listen`, below) | Token required |
| Off loopback, no TLS | Refused | Refused: a bearer token must not cross a network in plaintext |

Pass the certificate with `--tls-cert` and `--tls-key`, or put it under `tls:`
in the file.

- Use the full chain. mcp-http reloads the certificate when the files
  change, so a renewal needs no restart.
- The certificate's names and addresses, and the resource's host, join the
  `Host` allow-list. `--allow-host` adds more.

`--insecure-listen`, which runs off loopback with no auth, still works only
under the permissive posture, warns at start and is audited. It gets the same
single-account check as `--no-auth`, and it can't be combined with auth. The web console stays loopback-only in every posture.

## What it serves

- `/.well-known/oauth-protected-resource`, and the same path followed by the
  resource path: Protected Resource Metadata
  ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)).
- With the built-in issuer, `/.well-known/oauth-authorization-server` and
  `/.well-known/jwks.json`. They say where its keys are. It has no
  authorization or token endpoint, because tokens are minted on your
  terminal.
- `/mcp`, which answers `401` with a `WWW-Authenticate` header naming the
  metadata when there's no valid token.
- `/health`, which stays open.

## Limits

- **The same-uid limit stands.** A process running as your user can read the
  built-in issuer's key from your Keychain, and can edit
  `~/.cerberus/mcp-http.yaml`. Auth separates callers that reach mcp-http over
  the network or as other users. It doesn't separate processes of your own
  user.
- **Revocation is checked by the daemon**, not by mcp-http. mcp-http only
  checks the signature, audience, issuer and expiry.
- **There is no sign-in flow.** A client that only speaks interactive OAuth,
  with no static header, can't get a built-in token by itself. An external
  authorization server can provide that flow.

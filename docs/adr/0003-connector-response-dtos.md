# ADR 0003: Connectors Return Our Own DTOs, Never Vendor SDK Types

## Status

Accepted. Amended 2026-09-17 — see "How to write the mapping", added after the
question came up of whether a Go DTO library should be adopted. The decision is
unchanged; the amendment records how to implement it, and which tools not to
reach for.

## Date

2026-09-16 (amended 2026-09-17)

## Context

Cerberus connectors wrap vendor SDKs. The shortest path from an SDK call to a
connector operation is to return the SDK's own struct: it is already typed, it
already has JSON tags, and it needs no mapping code. Several existing connectors
do exactly this — `digitalocean` returns `godo.Droplet`, `github` returns
`go-github` types.

That shortcut is safe only as long as the vendor type contains nothing we must
not emit. While preparing the ContextForge connector (WP-4) we found a type
where that does not hold.

`go-contextforge`'s `Gateway` type — the upstream MCP server registration — is
declared with these fields, among others:

```go
AuthToken       *string             `json:"authToken,omitempty"`
AuthPassword    *string             `json:"authPassword,omitempty"`
AuthHeaderValue *string             `json:"authHeaderValue,omitempty"`
AuthValue       *string             `json:"authValue,omitempty"`
AuthUsername    *string             `json:"authUsername,omitempty"`
AuthHeaders     []map[string]string `json:"authHeaders,omitempty"`
OAuthConfig     map[string]any      `json:"oauthConfig,omitempty"`
```

`types.go` carries 32 secret-bearing field references in total.

A `list_gateways` operation that marshalled that struct would emit **live
upstream credentials** into four places at once:

- CLI stdout, and from there a terminal scrollback and any shell history or log
- the daemon socket response, and `~/.cerberus/` logs
- MCP tool results, which land directly in an agent's context window and are
  then sent to a model provider
- anything downstream that captures those, such as the `out/<date>/` run logs
  the `tools/` scripts keep

This is not a hypothetical exposure. ContextForge gateways are, by our own
operational notes, **the only place upstream auth can be set** — so that one
payload carries the credential for every MCP server behind the gateway. And the
most likely caller is an agent, because agent-facing tool discovery is the point
of the admin lane.

The severity is a property of the vendor type, not of our code. It is invisible
at the call site: `return gateways, nil` looks identical whether the struct
holds a name or a bearer token. Nothing in the compiler, the tests or the linter
objects. The existing house rule — "never log or return a secret value" — is
easy to satisfy in spirit while breaking in practice, because the author never
sees a secret in the code they wrote.

## Decision

**A connector operation returns a DTO defined in Cerberus. It does not return a
vendor SDK type.**

The DTO is an allow-list: it names the fields we intend to expose. A field that
is not named is not emitted, so a vendor adding a new credential field in a
minor release cannot silently widen our output.

For credentials specifically: expose the *shape* of the configuration, never the
value. A gateway response carries `auth_type` — that some bearer auth is
configured — and never `auth_token`. This matches the `probe-*` convention
already in use in `~/Projects/tools`: environment variable names, never values,
so output is safe to paste into a document.

Each connector that returns credential-adjacent data carries a test asserting
that a populated secret field does not appear in the serialized response.

## How to write the mapping

*Added 2026-09-17.*

There is no standard Go DTO library, and that absence is not a gap to fill. The
idiom is a plain struct with json tags plus an explicit mapping function — which
is what the ContextForge and Azure connectors already do:

```go
type Gateway struct { ... }                        // the DTO: an allow-list
func GatewayFromSDK(g *cf.Gateway) Gateway { ... }  // the mapping: the boundary
```

`encoding/json` covers serialization. The transformation half is hand-written,
on purpose.

### Do not use a reflection-based mapper for a boundary DTO

`jinzhu/copier`, `dranikpg/dto-mapper` and similar map fields by name at
runtime. The objection is specific rather than stylistic.

This ADR's whole claim is that **the mapping function is the security boundary
and should read like one.** A reflection mapper deletes that artifact. Exposure
stops being something a reviewer sees in a diff and becomes a property of
whether two field names happened to match. The allow-list may still hold — a DTO
with only safe fields copies only safe fields — but the reviewable moment is
gone, and this boundary exists precisely so that a vendor adding a credential
field in a minor release cannot widen our output without someone noticing.

**The boilerplate is the feature.** A mapping function is tedious to write once
and cheap to review forever.

`jmattheis/goverter` is the closest fit of the generators: it emits explicit
mapping code at build time, so compile-time checking and a readable artifact
both survive. But it is built for "map everything, report what is unmapped," and
an allow-list DTO deliberately ignores most of the vendor type. Using it here
means fighting the tool with ignore directives.

`go-viper/mapstructure` is a different job entirely — `map[string]any` to
struct, for decoding configuration. It is not a DTO mapper and should not be
reached for as one.

### Where a mapper is fine

Not every DTO is a boundary. Internal reshaping with no credentials in scope —
view models, response shaping, test fixtures — is where a generator earns its
keep, and hand-writing fifty field assignments there is waste.

**The rule: explicit mapping when the DTO exists to *exclude* something;
codegen when it exists to *reshape* something.**

### Why a vendor struct cannot be fixed in place

An obvious-seeming alternative is to tag the offending fields `json:"-"` and
return the vendor type. It does not work: the tags belong to the vendor, in
their module, and change on their release schedule. That is the mechanical
reason a separate type is required rather than a matter of taste.

### For other projects

The portable form of this, worth carrying wherever these DTOs are encouraged:

> A type that crosses a trust boundary gets its own struct and its own mapping
> function. Reflection-based mappers are fine for reshaping, never for
> excluding.

## Implications

### For new connectors

Define response types in the connector package. Map explicitly. The mapping
function is the security boundary and should read like one.

Where a vendor type is large and mostly harmless, it is still an allow-list —
the cost of listing thirty safe fields once is much lower than the cost of
shipping the one unsafe field.

### For existing connectors

`digitalocean`, `github`, `cloudflare`, `forge` and `namecheap` return vendor
types today. They are **not** retroactively in violation — this ADR does not
call for an immediate rewrite. They should be audited for secret-bearing fields
in the order they are touched, and migrated to DTOs as part of the plugin
migration those connectors are already scheduled for
(`docs/plans/connector-work-packages.md`).

The audit question for each is narrow: does any type this connector returns
carry a token, password, key, header value or OAuth blob? If yes, it is a DTO
now, not later.

### For `decodeConnectorPayload`

Cerberus-owned DTOs are the types the socket client decodes into, so this ADR
makes the payload decoding surface smaller and more stable, not larger — the
DTO is ours and cannot change under us on a vendor's release schedule.

## Consequences

### Positive

- A vendor adding a credential field cannot widen our output.
- The exposure decision is written down in one reviewable place per connector
  rather than implied by an SDK's struct definition.
- DTOs decouple the CLI, API and MCP surfaces from vendor churn — the same
  property that makes the `Backend` interface worth having.

### Negative

- Mapping code per operation, which is real work and will feel redundant in the
  cases where the vendor type is entirely benign.
- A field the vendor adds that we *do* want is not picked up for free.

That second cost is the point. Opting in is the behaviour we want at this
boundary.

## References

- `docs/plans/connector-work-packages.md` — WP-4 carries the ContextForge
  instance of this, including the specific fields to drop.
- `docs/secrets.md` — how a resource names a credential without carrying one.
  This ADR is the read path; that document is the write path.
- Worked examples of the mapping as a boundary:
  `contextforge/internal/cfplugin/dto.go` and `azure/internal/azplugin/dto.go`
  in `hollis-labs/cerberus-plugins`, each with a test asserting that a fully
  populated credential does not serialize.

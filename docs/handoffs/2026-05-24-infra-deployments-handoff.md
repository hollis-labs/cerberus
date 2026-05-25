# Infra Deployments Handoff

Date: 2026-05-24

Git commit at handoff: `a54daa8`

## Resume Update

Verified after handoff:

- `chrispian.dev` is now delegated to Cloudflare publicly:
  - `aldo.ns.cloudflare.com`
  - `betty.ns.cloudflare.com`
- Cloudflare now answers authoritatively for `chrispian.dev`
- Cerberus Cloudflare auth had drifted to an older keychain token and was
  repaired to use the latest 1Password secret:
  - `op://Keys/Cloudflare API Key - Cerberus/api key`
- `cerberus cloudflare zones` now succeeds again and confirms:
  - zone `chrispian.dev`
  - zone ID `d5de28a9e30fa53237cbd173d04f950f`
  - status `active`
- Public DNS is still incomplete for serving the site:
  - no apex record is published yet
  - no `www` record is published yet

This means the Cloudflare activation question is resolved. The next work for
`chrispian.dev` is now a deployment/domain-attachment problem, not a registrar
delegation problem.

## Current Direction

Cerberus has started its first real infra/operator lane beyond local process
management:

- provider settings for deployment-related services now belong in the GUI
- deployment profiles are now persisted as Cerberus-owned state
- the first dogfood target is `chrispian.dev`
- Vercel is the initial deployment provider
- Namecheap and Cloudflare are the initial DNS/domain providers
- GitHub/Git metadata is part of the deployment model

The goal of the next session is not more scaffolding. It is to make one real
site deployment path work reliably from the GUI.

## What Is Already Done

### Roadmap

- the GUI roadmap now includes a dedicated infra/deployments phase in
  [../plans/gui-roadmap.md](../plans/gui-roadmap.md)

### Cerberus-Owned Infra State

- new infra state model exists in
  [../../internal/infra/state.go](../../internal/infra/state.go)
- state is stored next to the active config as `infra.yaml`
- state currently holds:
  - provider-level non-secret values
  - deployment profiles
  - suggested deployment profiles

### Registry Alignment

- Cerberus-side `registry_urn` compatibility is landed:
  - parser/schema support in
    [../../internal/registry/projectconfig.go](../../internal/registry/projectconfig.go)
  - parser coverage in
    [../../internal/registry/registry_test.go](../../internal/registry/registry_test.go)
  - out-of-repo registry bootstrap/resolve coverage with `registry_urn` in
    [../../internal/registry/resolve_test.go](../../internal/registry/resolve_test.go)
- registry/web surfacing is stronger now:
  - registry entries expose shared-vs-local identity
  - registry audit view now surfaces config/index paths, registration provenance,
    resolve warnings, and skipped entries
  - the stale roadmap language describing `registry_urn` support as future work
    was corrected in [../plans/gui-roadmap.md](../plans/gui-roadmap.md)

### Deployment Runner

- initial deployment runner exists in
  [../../internal/infra/deploy.go](../../internal/infra/deploy.go)
- current supported provider is `vercel`
- the runner currently performs:
  - repo existence check
  - git branch / commit / dirty inspection
  - optional preflight command
  - optional build command
  - optional `vercel link --yes --project ...`
  - `vercel --prod --yes` style deployment
  - deployment output capture
  - deployment URL extraction from the final non-empty line

### Web Backend

- the local web UI now gets direct access to the keychain-backed secret provider
  in [../../cmd/cerberus/cmd_web.go](../../cmd/cerberus/cmd_web.go)
- new infra/deployment web handlers exist in
  [../../internal/webui/infra.go](../../internal/webui/infra.go)
- new routes were added in
  [../../internal/webui/server.go](../../internal/webui/server.go):
  - `GET /api/infra`
  - `POST /api/infra/providers/{id}`
  - `GET /api/deployments`
  - `POST /api/deployments`
  - `POST /api/deployments/{id}/delete`
  - `POST /api/deployments/{id}/run`

### Frontend

- provider settings and deployment profiles now have a dedicated page in
  [../../web/src/pages/deployments.tsx](../../web/src/pages/deployments.tsx)
- the page is wired into the app shell in
  [../../web/src/App.tsx](../../web/src/App.tsx)
- client support exists in
  [../../web/src/api/client.ts](../../web/src/api/client.ts)

The page currently supports:

- saving provider settings for:
  - Vercel
  - GitHub
  - Cloudflare
  - Namecheap
  - Git
- saving secrets through the local keychain-backed provider
- adopting suggested deployment profiles
- editing/saving deployment profiles
- deleting deployment profiles
- running a saved deployment profile
- viewing git status and per-step output from the last run

## Important Real-World Findings

### `chrispian.dev`

- repo exists at `/Users/chrispian/dev/sites/chrispian.dev`
- it is an Astro site
- `vercel` is installed locally and available
- there is currently no checked-in `.vercel/project.json`
- there is currently no checked-in `vercel.json`
- `package.json` already exposes the expected commands:
  - `pnpm check`
  - `pnpm build`

### `hollislabs.com`

- `/Users/chrispian/dev/sites/hollislabs.com` does not currently exist on disk
- do not assume that second site is ready to wire until the actual repo path is
  available

### Providers

- Cerberus already had connector metadata and operations for:
  - GitHub
  - Cloudflare
  - Namecheap
  - Forge
  - SSH
- Cerberus did **not** already have a Vercel connector
- the current Vercel path is therefore a bounded deployment runner, not a full
  connector contract
- during resume work, Cerberus gained additional real infra control paths:
  - Cloudflare zone creation via `cerberus cloudflare zones create`
  - Namecheap custom nameserver switching via
    `cerberus domain nameservers set`

### Real Infra Actions Proven During Resume

- Cloudflare zone creation now works through Cerberus when a user-owned
  Cloudflare token with `Zone` / `DNS` permissions is used
- `chrispian.dev` was created in Cloudflare and returned:
  - nameservers `aldo.ns.cloudflare.com`
  - nameservers `betty.ns.cloudflare.com`
- Namecheap API access was verified against the correct account
- Cerberus successfully switched `chrispian.dev` to the Cloudflare
  nameservers via:

```bash
cerberus domain nameservers set chrispian.dev aldo.ns.cloudflare.com betty.ns.cloudflare.com --ack
```

That distinction matters. The next session should either:

1. continue with the current bounded Vercel deployment runner, or
2. promote Vercel into a first-class connector intentionally

Do not accidentally blur those two models.

## Verified Before Pause

These passed at the end of the session:

```bash
go test ./internal/infra ./internal/webui ./cmd/cerberus
npm run typecheck
npm run build
```

Additional verification completed during resume:

```bash
go test ./internal/registry
go test ./internal/connector/namecheap ./internal/cerbapi ./cmd/cerberus
go test ./internal/webui ./cmd/cerberus ./internal/registry
```

## What Has Not Been Proven Yet

No real production deployment was run in this session.

That means the following are still unverified in live use:

- Vercel token/scope handling in the GUI
- non-interactive `vercel link` against the intended project
- non-interactive production deploy for `chrispian.dev`
- the exact deployment URL parsing behavior with real Vercel output
- how much project metadata must be stored in the profile vs inferred
- the exact GUI shape for registrar nameserver switching and Cloudflare zone
  creation during deployment onboarding
- Cloudflare zone activation timing after registrar delegation
- the final Vercel DNS record set/domain-attachment flow for `chrispian.dev`

## Recommended Next Slice

Use the new Deployments page to make `chrispian.dev` actually deploy.

Suggested order:

1. Open `cerberus web`
2. Go to `Deployments`
3. Save provider settings/secrets for:
   - Vercel token
   - optional Vercel scope
   - GitHub token
   - Namecheap credentials if DNS work is expected next
   - Cloudflare token only if the zone will be managed there
4. Adopt the suggested `chrispian.dev` profile
5. Fill in any missing profile fields:
   - `vercel_project`
   - `vercel_scope` if needed
   - `git_owner`
   - `git_repo`
   - `cloudflare_zone_id` or `namecheap_domain` depending on DNS owner
6. Run the deployment profile
7. Fix any real-world issues in the runner based on actual Vercel output

## Likely Follow-On Work After First Real Deploy

- make Vercel output parsing more explicit if needed
- decide whether to persist last deployment records/history
- add DNS verification/preview helpers before write actions
- add domain assignment / DNS record automation for the chosen provider
- expose Cloudflare zone creation and Namecheap nameserver switching in the GUI
- decide whether to model deployments as first-class records in SQLite
- decide whether Vercel should remain a deployment-runner concern or become a
  true connector

## Recommended Next Session Start

1. Keep using the repaired Cloudflare secret source:
   - `op://Keys/Cloudflare API Key - Cerberus/api key`
   - Cerberus keychain secret: `cloudflare/api_token`
2. Confirm current zone/DNS state through Cerberus:
   - `cerberus cloudflare zones`
   - `cerberus cloudflare dns list d5de28a9e30fa53237cbd173d04f950f`
3. Open `cerberus web` and resume the `chrispian.dev` deployment profile
4. Complete or verify the Vercel profile fields:
   - `vercel_project`
   - optional `vercel_scope`
   - `git_owner`
   - `git_repo`
   - `cloudflare_zone_id=d5de28a9e30fa53237cbd173d04f950f`
5. Run the first real production deployment for `chrispian.dev`
6. Use the resulting Vercel domain instructions to add the required Cloudflare
   DNS records for apex and/or `www`
7. Re-check public DNS after the write and verify the final serving hostname(s)
8. Only after that, tighten the GUI/domain automation based on the real flow

## Useful Context For Resume

- the user explicitly wants Cerberus to become a credible infra story, not just
  a local process manager
- the user wants to start with small-site publishing as the first capability
  unlock
- `chrispian.dev` is the immediate dogfood target
- `hollislabs.com` should follow the same pattern later, but the repo path is
  not present yet
- the user expects Vercel, Namecheap, Git, and Cloudflare settings/config to be
  manageable from the GUI

## Current Session Endpoint

Cerberus now has the first coherent infra/deployments surface:

- provider settings exist
- deployment profiles exist
- `chrispian.dev` is suggested automatically
- deployment execution exists

The next session should move from scaffolding to first successful real deploy.

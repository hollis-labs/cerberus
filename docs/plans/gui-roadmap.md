# Cerberus GUI Roadmap

## Status

Draft

## Date

2026-05-24

## Goal

Turn the current Cerberus web UI from a resource-only demo into a strong,
usable operator surface for local runtime management, registry visibility, and
project-level lifecycle work.

## Related Dependency

Cross-substrate registry migration with Tether is still in progress. Cerberus
should plan its GUI/backend work assuming Tether will eventually ship
`LookupBy(kind, external_id, substrate?)`, Cerberus bootstrap import, and URN
write-back from Tether's v060-02 sprint.

Cerberus-side `registry_urn` compatibility is now landed:

- `internal/registry/projectconfig.go` explicitly accepts
  `registry_urn` as optional metadata in `*.cerberus.yaml`
- `internal/registry/registry_test.go` covers parser acceptance for
  `registry_urn`
- registry/API DTOs can already surface `registry_urn` in the web backend

The remaining registry-alignment work is integration confidence and operator
visibility, not schema compatibility.

## Current Audit

### 1. Frontend

Current state:

- The app shell exposes nav items for Resources, Projects, Pipelines,
  Connectors, and Plugins in `web/src/App.tsx`.
- Only `ResourcesPage` exists. Every other route renders a placeholder.
- The frontend API client only wraps:
  - `/api/session`
  - `/api/resources`
  - `/api/resources/{id}`
  - `/api/resources/{id}/logs`
  - resource actions `apply|deploy|reload|stop`

Implication:

- The GUI advertises five domains but only one is usable.
- Even inside Resources, the UI omits backend-supported actions like `sync` and
  `remove`.

### 2. Cerberus Web Backend

Current state:

- `internal/webui/server.go` already exposes:
  - `GET /api/health`
  - `GET /api/projects`
  - `GET /api/pipelines`
  - `POST /api/pipelines/{id}/run`
  - `GET /api/connectors`
  - `POST /api/connectors/{id}/operations/{operation}`
  - managed plugin list/health/install/load/unload/operation routes
  - resource inspect/doctor/logs/sync/remove in addition to the main lifecycle
    routes
- The backend does not currently expose Cerberus registry management over the
  web surface.
- The backend does not currently expose an operator settings/overview document
  comparable to Tether's `/api/settings` or `/api/overview`.

Implication:

- The GUI gap is partly frontend-only: several usable APIs already exist and
  simply are not consumed.
- The bigger backend gaps are in operator settings, registry visibility, and
  system-level reporting rather than raw resource lifecycle.

### 3. Cerberus Runtime/Registry Management Gaps

The current GUI does not expose:

- daemon health and socket reachability as a first-class page
- resolved config/registry health
- project registry entries and registration health
- config migration state and validation helpers
- project/pipeline browsing beyond raw list calls
- any guided distinction between:
  - repo config truth
  - installed artifact truth
  - live runtime truth
- connector/plugin capability summaries and safe operation forms

### 4. Tether Reference Patterns Worth Reusing

Tether's Sysop backend is a good reference for how to build a strong operator
surface without collapsing everything into one page.

Useful patterns from `apps/sysop/cmd/tether_sysop/main.go`:

- `/api/settings` as one aggregated operator snapshot
- `/api/overview` for top-level system summaries
- dedicated save/delete endpoints for config-backed CRUD
- registry browse/save/deregister/sync/bootstrap flows
- explicit session lifecycle controls beyond plain list/detail
- activity/event/tool-call visibility pages
- roadmap/status data emitted by the backend so the UI can explain what is live
  vs what is next

Cerberus should copy the shape, not the product domain. Cerberus is a resource
control plane, so its equivalent should be oriented around resources, registry,
daemon health, connectors, plugins, and local-runtime diagnostics.

## What Cerberus Needs Once Tether Finishes v060-02

1. Validate that Tether's Cerberus bootstrap importer works against real
   `~/.cerberus/registry.yaml` entries and out-of-repo `*.cerberus.yaml` paths.
2. Decide whether Cerberus wants to surface the shared registry URN in:
   - `resource inspect`
   - project/registry views
   - future MCP/UI drill-downs
3. Add a Cerberus-side audit/doctor flow that can explain:
   - local owner key
   - file path
   - registry health
   - shared URN, if present
4. Keep Cerberus as the owner of runtime config while treating Tether's
   registry as discovery/identity only.

## Roadmap

### Phase 0: Registry Compatibility

Goal:
- make Cerberus safe to participate in Tether's shared registry migration

Tasks:

- validate Tether's Cerberus bootstrap importer against real local registry
  entries and out-of-repo `*.cerberus.yaml` paths
- surface shared URN metadata consistently in registry-facing views and APIs
- document the Cerberus/Tether ownership split clearly
- add or retain tests for write-back/import compatibility

Done when:

- Tether can write `registry_urn` into Cerberus project configs without
  breaking Cerberus parsing or validation, and Cerberus can explain the shared
  identity cleanly in operator-facing surfaces

### Phase 1: Operator Visibility Baseline

Goal:
- expose the system state operators need before adding more edit surfaces

Tasks:

- add a top-level Overview page backed by a new aggregated backend endpoint
- add a System/Health page showing:
  - daemon reachability
  - socket path
  - config path
  - registry path
  - last known load/reload status
- add a Projects page backed by `/api/projects`
- add a Pipelines page backed by `/api/pipelines`
- add resource summary slices by project, mode, connector, and health bucket

Done when:

- the GUI can answer "what exists, what is healthy, and what needs attention"
  without dropping to the CLI

### Phase 2: Complete Current Resource Lifecycle

Goal:
- make the resource page fully representative of the backend that already
  exists

Tasks:

- expose `sync` and `remove` in the UI alongside `apply`, `deploy`, `reload`,
  and `stop`
- surface resource inspect and doctor output more directly instead of burying
  it behind the modal
- make artifact/install/runtime distinctions explicit in the UI
- add better action result rendering for build/install output
- add project/tag/connector filters that match the backend list surface

Done when:

- operators can perform the full supported resource lifecycle from the GUI
  without losing key diagnostic detail

### Phase 3: Registry And Project Management

Goal:
- give Cerberus an operator surface for the local project registry and config
  migration work

Tasks:

- add backend routes for:
  - registry list
  - registry health
  - register/deregister
  - config validate
  - config resolve preview
- add a Registry page showing:
  - owner
  - namespace
  - source path
  - health
  - registered-at
  - shared `registry_urn`, when present
- add guided actions for register/deregister and validation
- show migration help for monolithic `config.yaml` to app-owned project configs

Done when:

- project-registry operations no longer require raw CLI usage for common cases

### Phase 4: Connectors, Plugins, And Pipelines

Goal:
- expose the non-resource operational domains already present in the backend

Tasks:

- build Connectors page on top of `/api/connectors`
- build Plugins page on top of managed plugin routes
- add pipeline run/history page
- show connector definitions, required config, and availability state
- show plugin health, install/load/unload state, and operation results

Done when:

- connectors, plugins, and pipelines are first-class operator surfaces rather
  than placeholder tabs

### Phase 5: Configuration And Runtime Editing

Goal:
- add safe CRUD where Cerberus should own edits directly

Tasks:

- decide which edits belong in Cerberus vs app-owned YAMLs
- add guarded config-edit surfaces only where the ownership boundary is clear
- prefer generated forms for Cerberus-owned settings, not arbitrary YAML text
- add backups/diff previews before mutating persisted config
- expose backup management and restore/audit visibility in the GUI once the
  underlying backup workflows are formalized

Done when:

- the GUI can perform safe, scoped edits without blurring config ownership

### Phase 6: Infra Providers And Deployments

Goal:
- make Cerberus a credible operator surface for small-site deployment and DNS
  workflows, starting with Vercel-backed Astro sites

Tasks:

- add GUI/provider settings for:
  - Vercel token and scope
  - GitHub token and repo defaults
  - Cloudflare API token and default zone metadata
  - Namecheap credentials and client IP
  - Git deployment defaults such as remote and production branch
- persist deployment profiles for app/site repos under Cerberus-owned state
- add a Deployments page that can:
  - show repo/git status
  - show provider/domain metadata
  - run preflight/build/deploy steps
  - capture deployment output and URLs
- add a first dogfood profile for `chrispian.dev`
- add the missing Vercel capability intentionally:
  - either a dedicated connector or a clearly-bounded deployment runner built
    on the Vercel CLI
- follow with DNS/domain automation for Namecheap and Cloudflare once the
  deployment profile path is stable
- expose the new registrar/domain automation already present in the CLI/MCP:
  - create Cloudflare zones from provider/account metadata
  - switch a Namecheap domain to Cloudflare nameservers
  - show current registrar nameservers and Cloudflare zone activation state
  - add guarded DNS record writes for Vercel onboarding after delegation

Done when:

- Cerberus can reliably deploy `chrispian.dev` end-to-end from the GUI and is
  structurally ready for `hollislabs.com` to follow the same path

## Recommended Build Order

1. Phase 0: registry compatibility
2. Phase 1: overview and health
3. Phase 2: finish resource lifecycle
4. Phase 3: registry/project management
5. Phase 4: connectors/plugins/pipelines
6. Phase 5: scoped editing and polish
7. Phase 6: infra providers and deployments

## Immediate Next Slice

The highest-leverage near-term slice is:

1. Validate Tether bootstrap/import behavior against real Cerberus registry entries.
2. Add an Overview/System backend endpoint.
3. Implement Projects and Pipelines pages from existing backend routes.
4. Expand Resources to include `sync` and `remove`.
5. Add a Registry page and the backend routes it requires.

import { Stub } from "./common"

const ROUTES = [
  "GET /api/plugins/connectors",
  "GET /api/plugins/connectors/{id}/health",
  "POST /api/plugins/connectors/{id}/{load|unload}[/plan|/confirm]",
] as const

export function PluginsPage() {
  return <Stub summary="Managed connector plugins: health, load and unload." routes={ROUTES} />
}

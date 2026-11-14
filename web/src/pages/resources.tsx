import { Stub } from "./common"

const ROUTES = [
  "GET /api/resources",
  "GET /api/resources/{id}",
  "GET /api/resources/{id}/logs",
  "POST /api/resources/{id}/{action}[/plan|/confirm]",
] as const

export function ResourcesPage() {
  return (
    <Stub
      summary="Resources with status, logs and actions (apply, deploy, reload, stop, sync, remove)."
      routes={ROUTES}
    />
  )
}

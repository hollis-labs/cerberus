import { Stub } from "./common"

const ROUTES = ["GET /api/overview", "GET /api/system"] as const

export function OverviewPage() {
  return (
    <Stub
      summary="Counts, health and 24-hour trends across resources, projects, pipelines, connectors and plugins."
      routes={ROUTES}
    />
  )
}

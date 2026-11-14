import { Stub } from "./common"

const ROUTES = ["GET /api/pipelines", "POST /api/pipelines/{id}/run[/plan|/confirm]"] as const

export function PipelinesPage() {
  return <Stub summary="Pipelines and running them." routes={ROUTES} />
}

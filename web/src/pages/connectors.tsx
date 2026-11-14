import { Stub } from "./common"

const ROUTES = [
  "GET /api/connectors",
  "GET /api/resources",
  "POST /api/connectors/{id}/operations/{op}[/plan|/confirm]",
] as const

export function ConnectorsPage() {
  return <Stub summary="Connector definitions and running an operation." routes={ROUTES} />
}

import { Stub } from "./common"

const ROUTES = ["GET /api/credentials", "POST /api/credentials/{id}[/plan|/confirm]"] as const

export function CredentialsPage() {
  return (
    <Stub
      summary="Connector secrets: which are stored, set or clear them. Values are never shown."
      routes={ROUTES}
    />
  )
}

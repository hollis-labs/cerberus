import { Stub } from "./common"

const ROUTES = [
  "GET /api/registry",
  "GET /api/registry/health",
  "GET /api/config/validate",
  "GET /api/config/resolve",
  "POST /api/registry/register[/plan|/confirm]",
  "POST /api/registry/deregister[/plan|/confirm]",
] as const

export function RegistryPage() {
  return (
    <Stub
      summary="Registered descriptors, their health, validation and resolution; register and deregister."
      routes={ROUTES}
    />
  )
}

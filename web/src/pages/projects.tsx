import { Stub } from "./common"

const ROUTES = ["GET /api/projects"] as const

export function ProjectsPage() {
  return <Stub summary="Projects from the registered descriptors." routes={ROUTES} />
}

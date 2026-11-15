import { Stub } from "./common"

const ROUTES = [
  "GET /api/settings",
  "GET /api/config/backups",
  "POST /api/config/backups/restore[/plan|/confirm]",
] as const

export function SettingsPage() {
  return (
    <Stub summary="Configuration summary and restoring a configuration backup." routes={ROUTES} />
  )
}

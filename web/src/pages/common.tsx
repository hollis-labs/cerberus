import { EmptyState, Pill } from "@hollis-labs/design-components"
import { type ReactNode, useEffect, useState } from "react"
import { cerberus } from "../api/cerberus"
import type { ConsoleStatus } from "../api/types"

export const messageOf = (err: unknown) => (err instanceof Error ? err.message : String(err))

const STATUS_POLL_MS = 15_000

/**
 * The header badges and the brakes banner. app-shell has no header region, so every page renders
 * this above its body (see docs/substrate-port/console-api-contract.md, gaps). Unlike the old
 * console, it polls, so a brake engaged elsewhere shows without a reload.
 */
export function ConsoleHeader() {
  const [status, setStatus] = useState<ConsoleStatus | null>(null)
  const [error, setError] = useState("")
  useEffect(() => {
    let current = true
    const load = () =>
      cerberus
        .consoleStatus()
        .then((s) => {
          if (current) {
            setStatus(s)
            setError("")
          }
        })
        .catch((e: unknown) => {
          if (current) setError(messageOf(e))
        })
    void load()
    const timer = window.setInterval(load, STATUS_POLL_MS)
    return () => {
      current = false
      window.clearInterval(timer)
    }
  }, [])
  if (error) return <p className="text-fg-muted">Status unavailable: {error}</p>
  if (!status) return null
  const lockdown = status.brakes?.lockdown
  return (
    <div className="stack">
      {lockdown ? (
        <div role="alert" className="panel">
          <strong>Lockdown engaged.</strong> {lockdown.reason ?? ""}
        </div>
      ) : null}
      <section className="flex flex-wrap gap-2" aria-label="Console status">
        {status.posture ? (
          <Pill tone={status.posture.permissive ? "warning" : "success"}>
            {status.posture.summary}
          </Pill>
        ) : null}
        {status.passkeys?.alert ? <Pill tone="warning">{status.passkeys.summary}</Pill> : null}
        {status.break_glass?.open ? <Pill tone="danger">{status.break_glass.summary}</Pill> : null}
        {status.enforcement && !status.enforcement.enforced ? (
          <Pill tone="warning">{status.enforcement.summary}</Pill>
        ) : null}
      </section>
    </div>
  )
}

/** A console page: the status header, then the page's own body. */
export function Page({ children }: { children: ReactNode }) {
  return (
    <div className="page-content stack">
      <ConsoleHeader />
      {children}
    </div>
  )
}

/**
 * A page not wired yet. `routes` are the contract routes the page will call, so the stub shows
 * what it is for; slice 11b replaces each stub with the real body.
 */
export function Stub({ summary, routes }: { summary: string; routes: readonly string[] }) {
  return (
    <Page>
      <EmptyState
        variant="empty"
        eyebrow="Not wired yet"
        title={summary}
        description="Slice 11b."
      />
      <ul className="mono text-fg-muted" aria-label="Routes this page uses">
        {routes.map((r) => (
          <li key={r}>{r}</li>
        ))}
      </ul>
    </Page>
  )
}

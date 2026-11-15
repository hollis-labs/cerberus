import { Button, EmptyState, Pill } from "@hollis-labs/design-components"
import { type ReactNode, useCallback, useEffect, useState } from "react"
import { cerberus, type GatedWrite } from "../api/cerberus"
import { ApiError, confirmableApproval, notAvailable, refusalApproval } from "../api/request"
import type { ConnectorPlan, ConsoleStatus } from "../api/types"

export const messageOf = (err: unknown) => (err instanceof Error ? err.message : String(err))

/** A read's state: loading, loaded, not available in this build yet, or failed. */
export type Load<T> =
  | { state: "loading" }
  | { state: "ok"; data: T }
  | { state: "unavailable" }
  | { state: "error"; message: string }

/**
 * Runs read once, then every pollMs (0: once), and again on reload(). A route whose backend has not
 * landed yet becomes "unavailable", never mock data.
 */
export function useLoad<T>(read: () => Promise<T>, pollMs = 0): Load<T> & { reload: () => void } {
  const [load, setLoad] = useState<Load<T>>({ state: "loading" })
  const [tick, setTick] = useState(0)
  const reload = useCallback(() => setTick((n) => n + 1), [])
  // biome-ignore lint/correctness/useExhaustiveDependencies: read is the caller's fixed function; tick is the reload signal
  useEffect(() => {
    let current = true
    const run = () =>
      read()
        .then((data) => current && setLoad({ state: "ok", data }))
        .catch((e: unknown) => {
          if (!current) return
          setLoad(
            notAvailable(e) ? { state: "unavailable" } : { state: "error", message: messageOf(e) },
          )
        })
    void run()
    const timer = pollMs > 0 ? window.setInterval(run, pollMs) : 0
    return () => {
      current = false
      if (timer) window.clearInterval(timer)
    }
  }, [tick, pollMs])
  return { ...load, reload }
}

/** The non-data states of a Load, shared by every page. */
export function Loadable<T>({
  load,
  what,
  children,
}: {
  load: Load<T>
  what: string
  children: (data: T) => ReactNode
}) {
  switch (load.state) {
    case "loading":
      return <p className="text-fg-muted">Loading {what}…</p>
    case "unavailable":
      return (
        <EmptyState
          variant="empty"
          eyebrow="Not available yet"
          title={`${what[0].toUpperCase()}${what.slice(1)} is not available in this build yet.`}
          description="This part of Cerberus arrives with a later slice of the port."
        />
      )
    case "error":
      return (
        <EmptyState variant="error" title={`Could not load ${what}`} description={load.message} />
      )
    case "ok":
      return <>{children(load.data)}</>
  }
}

/** A titled panel. */
export function Section({ title, children }: { title: string; children: ReactNode }) {
  const idRef = `section-${title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`
  return (
    <section className="panel stack" aria-labelledby={idRef}>
      <h2 id={idRef} className="text-lg font-semibold">
        {title}
      </h2>
      {children}
    </section>
  )
}

/** The text an operator types to confirm a plan: the resource, else the first field, else the kind. */
export function confirmTarget(p: ConnectorPlan["plan"]): string {
  if (p.target.resource) return p.target.resource
  const fields = p.target.fields ?? {}
  const keys = Object.keys(fields).sort()
  if (keys.length > 0) return fields[keys[0]]
  return p.target.kind || `${p.connector}.${p.operation}`
}

type GatedState =
  | { step: "idle" }
  | { step: "running" }
  | { step: "confirm"; plan: ConnectorPlan; approvalID: string; typed: string }
  | { step: "done"; message: string }
  | { step: "failed"; message: string }

/**
 * A write that policy may gate (console-api-contract.md section 1, "Gated writes"). It sends the
 * write; a tty_confirm refusal shows the plan and asks for the target to be typed before the
 * confirmed call; an out_of_band refusal points at the approvals page, where the owner decides.
 */
export function GatedButton<T>({
  label,
  write,
  danger = false,
  onDone,
}: {
  label: string
  write: () => GatedWrite<T>
  danger?: boolean
  onDone?: (result: T) => void
}) {
  const [s, setS] = useState<GatedState>({ step: "idle" })
  const finish = (result: T) => {
    setS({ step: "done", message: `${label}: done` })
    onDone?.(result)
  }
  const fail = (e: unknown) => setS({ step: "failed", message: messageOf(e) })
  const start = async () => {
    setS({ step: "running" })
    const w = write()
    try {
      finish(await w.send())
    } catch (e) {
      const confirmable = confirmableApproval(e)
      if (confirmable) {
        try {
          const plan = await w.plan()
          setS({ step: "confirm", plan, approvalID: confirmable.id, typed: "" })
        } catch (pe) {
          fail(pe)
        }
        return
      }
      const ref = refusalApproval(e)
      if (ref) {
        setS({
          step: "failed",
          message: `Needs the owner's approval (${ref.id}): decide it on the Approvals page, then run it again.`,
        })
        return
      }
      if (notAvailable(e)) {
        setS({ step: "failed", message: `${label} is not available in this build yet.` })
        return
      }
      if (e instanceof ApiError && e.code === "observe_mode") {
        setS({ step: "failed", message: e.message })
        return
      }
      fail(e)
    }
  }
  const confirm = async () => {
    if (s.step !== "confirm") return
    setS({ step: "running" })
    try {
      finish(
        await write().confirm({ approval_id: s.approvalID, confirmed_plan_hash: s.plan.plan_hash }),
      )
    } catch (e) {
      fail(e)
    }
  }
  return (
    <span className="stack" style={{ gap: "0.25rem" }}>
      <Button
        variant={danger ? "destructive" : "outline"}
        disabled={s.step === "running"}
        onClick={() => void start()}
      >
        {label}
      </Button>
      {s.step === "confirm" ? (
        <span role="dialog" aria-label={`Confirm ${label}`} className="panel stack">
          <span>
            Plan <span className="mono">{s.plan.plan_hash.slice(0, 12)}</span> for{" "}
            <span className="mono">
              {s.plan.plan.connector}.{s.plan.plan.operation}
            </span>
            . Type <span className="mono">{confirmTarget(s.plan.plan)}</span> to confirm.
          </span>
          <input
            aria-label="Target to confirm"
            className="rounded-md border border-border bg-bg px-3 py-2"
            value={s.typed}
            onChange={(e) => setS({ ...s, typed: e.target.value })}
          />
          <Button disabled={s.typed !== confirmTarget(s.plan.plan)} onClick={() => void confirm()}>
            Confirm
          </Button>
        </span>
      ) : null}
      {s.step === "done" || s.step === "failed" ? (
        <span role="status" className={s.step === "failed" ? "text-danger" : "text-fg-muted"}>
          {s.message}
        </span>
      ) : null}
    </span>
  )
}

const STATUS_POLL_MS = 15_000

/**
 * The header badges, the brakes banner and the lockdown button (GET /api/console/status). app-shell
 * has no header region, so every page renders this above its body. It polls, so a brake engaged
 * elsewhere shows without a reload.
 */
export function ConsoleHeader() {
  const load = useLoad<ConsoleStatus>(() => cerberus.consoleStatus(), STATUS_POLL_MS)
  const [engaging, setEngaging] = useState("")
  if (load.state === "unavailable") {
    return <p className="text-fg-muted">Status and brakes: not available in this build yet.</p>
  }
  if (load.state === "error")
    return <p className="text-fg-muted">Status unavailable: {load.message}</p>
  if (load.state !== "ok") return null
  const status = load.data
  const lockdown = status.brakes?.lockdown
  const engage = () => {
    setEngaging("engaging")
    cerberus
      .engageLockdown("engaged from the console")
      .then(() => {
        setEngaging("")
        load.reload()
      })
      .catch((e: unknown) => setEngaging(messageOf(e)))
  }
  return (
    <div className="stack">
      {lockdown ? (
        <div role="alert" className="panel">
          <strong>Lockdown engaged.</strong> {lockdown.reason ?? ""} Lifting it needs the owner's
          approval.
        </div>
      ) : null}
      {status.brakes?.freezes?.map((f) => (
        <div key={f.id} role="alert" className="panel">
          <strong>Freeze {f.id}</strong> on {f.scope}. {f.reason ?? ""}
        </div>
      ))}
      <section className="flex flex-wrap items-center gap-2" aria-label="Console status">
        {status.posture ? (
          <Pill tone={status.posture.permissive ? "warning" : "success"}>
            {status.posture.summary}
          </Pill>
        ) : null}
        {status.break_glass?.open ? <Pill tone="danger">{status.break_glass.summary}</Pill> : null}
        {status.enforcement && !status.enforcement.enforced ? (
          <Pill tone="warning">{status.enforcement.summary}</Pill>
        ) : null}
        {lockdown ? null : (
          <Button variant="destructive" disabled={engaging === "engaging"} onClick={engage}>
            Lockdown
          </Button>
        )}
        {engaging && engaging !== "engaging" ? <span role="status">{engaging}</span> : null}
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

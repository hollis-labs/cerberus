import { useState } from 'react'
import { FormDialog, Input } from '@hollis-labs/sysop-ui/ui'
import { apiClient, type BrakeState, type BrakeSuspension } from '../api/client'
import { ConfirmCancelled, useApproveInPlace } from './plan-confirm'

// The emergency brake on the console (§12). Engaging is one click and an
// optional reason; lifting asks the daemon, which answers with a passkey
// approval, met in place (useApproveInPlace) where a key is enrolled.

type ApproveInPlace = ReturnType<typeof useApproveInPlace>

async function lift(run: (approvalID?: string) => Promise<unknown>, approveInPlace: ApproveInPlace, setError: (e: string) => void) {
  try {
    await approveInPlace(() => run(), (approvalID) => run(approvalID))
    window.location.reload()
  } catch (err) {
    if (err instanceof ConfirmCancelled) return
    setError(err instanceof Error ? err.message : String(err))
  }
}

export function BrakesBanner({ brakes, token }: { brakes?: BrakeState | null; token: string }) {
  const [error, setError] = useState<string | null>(null)
  const approveInPlace = useApproveInPlace()
  if (!brakes || (!brakes.lockdown && !(brakes.freezes ?? []).length && !(brakes.suspensions ?? []).length)) return null
  return (
    <div data-testid="brakes-banner" className="space-y-1 border-b-2 border-red-500 bg-red-500/15 px-4 py-2 text-sm text-red-700">
      {brakes.lockdown && (
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-bold tracking-wide">LOCKDOWN</span>
          <span>
            since {new Date(brakes.lockdown.engaged_at).toLocaleString()}, by {brakes.lockdown.by.kind} over {brakes.lockdown.by.via}
            {brakes.lockdown.reason ? `: ${brakes.lockdown.reason}` : ''}. Only plain reads run.
          </span>
          <button data-testid="lift-lockdown" className="rounded border border-red-500 px-2 py-0.5 text-xs font-semibold" onClick={() => lift((approvalID) => apiClient.liftLockdown(token, approvalID), approveInPlace, setError)}>
            Lift
          </button>
        </div>
      )}
      {(brakes.freezes ?? []).map((f) => (
        <div key={f.id} className="flex flex-wrap items-center gap-2">
          <span className="font-bold tracking-wide">FREEZE</span>
          <span>
            {f.id} on {f.scope}
            {f.reason ? `: ${f.reason}` : ''}
          </span>
          <button className="rounded border border-red-500 px-2 py-0.5 text-xs font-semibold" onClick={() => lift((approvalID) => apiClient.liftFreeze(token, f.id, approvalID), approveInPlace, setError)}>
            Lift
          </button>
        </div>
      ))}
      {(brakes.suspensions ?? []).map((x) => (
        <SuspensionLine key={x.id} suspension={x} token={token} />
      ))}
      {error && <div data-testid="brakes-error">{error}</div>}
    </div>
  )
}

export function LockdownButton({ token }: { token: string }) {
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  return (
    <>
      <button
        data-testid="engage-lockdown"
        title="Lockdown: only plain reads run until a person lifts it"
        className="rounded border border-red-500 px-2 py-0.5 text-xs font-semibold text-red-600"
        onClick={() => setOpen(true)}
      >
        Lockdown
      </button>
      <FormDialog
        open={open}
        onClose={() => !busy && setOpen(false)}
        title="Put Cerberus in lockdown?"
        description="Every operation but a plain read is refused until a person lifts it. Auto-restarts keep services up; pipelines stop."
        submitLabel="Lockdown"
        submitting={busy}
        onSubmit={async () => {
          setBusy(true)
          try {
            await apiClient.engageLockdown(token, reason)
            window.location.reload()
          } finally {
            setBusy(false)
          }
        }}
      >
        <label className="block text-sm">
          Reason (optional)
          <Input data-testid="lockdown-reason" value={reason} onChange={(e) => setReason(e.target.value)} />
        </label>
      </FormDialog>
    </>
  )
}

// SuspensionLine is one session the circuit breaker suspended; resetting it
// takes the typed phrase, which the daemon checks.
function SuspensionLine({ suspension: x, token }: { suspension: BrakeSuspension; token: string }) {
  const [open, setOpen] = useState(false)
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const phrase = `reset ${x.id}`
  return (
    <div data-testid={`suspension-${x.id}`} className="flex flex-wrap items-center gap-2">
      <span className="font-bold tracking-wide">SUSPENDED</span>
      <span>
        {x.principal.kind} over {x.principal.via}
        {x.principal.client ? ` (${x.principal.client})` : ''}, after {x.denials} policy denials in {x.window}, since {new Date(x.tripped_at).toLocaleString()}
      </span>
      <button data-testid={`reset-${x.id}`} className="rounded border border-red-500 px-2 py-0.5 text-xs font-semibold" onClick={() => setOpen(true)}>
        Reset
      </button>
      <FormDialog
        open={open}
        onClose={() => !busy && setOpen(false)}
        title="Reset this suspended session?"
        description={`Its calls run again under policy. Type "${phrase}" to reset it.`}
        submitLabel="Reset"
        submitting={busy}
        onSubmit={async () => {
          setBusy(true)
          setError(null)
          try {
            await apiClient.resetSuspension(token, x.id, typed)
            window.location.reload()
          } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <label className="block text-sm">
          Confirmation
          <Input data-testid="reset-typed" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={phrase} />
        </label>
        {error && <div className="text-sm text-red-600">{error}</div>}
      </FormDialog>
    </div>
  )
}

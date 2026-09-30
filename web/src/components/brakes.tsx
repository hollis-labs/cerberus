import { useState } from 'react'
import { FormDialog, Input } from '@hollis-labs/sysop-ui/ui'
import { apiClient, refusalApproval, type BrakeState } from '../api/client'

// The emergency brake on the console (§12). Engaging is one click and an
// optional reason; lifting asks the daemon, which answers with a passkey
// approval to meet on the approvals page where a key is enrolled.

async function lift(run: () => Promise<unknown>, setError: (e: string) => void) {
  try {
    await run()
    window.location.reload()
  } catch (err) {
    const pending = refusalApproval(err)
    if (pending?.id) {
      window.location.assign(`/approvals?id=${encodeURIComponent(pending.id)}`)
      return
    }
    setError(err instanceof Error ? err.message : String(err))
  }
}

export function BrakesBanner({ brakes, token }: { brakes?: BrakeState | null; token: string }) {
  const [error, setError] = useState<string | null>(null)
  if (!brakes || (!brakes.lockdown && !(brakes.freezes ?? []).length)) return null
  return (
    <div data-testid="brakes-banner" className="space-y-1 border-b-2 border-red-500 bg-red-500/15 px-4 py-2 text-sm text-red-700">
      {brakes.lockdown && (
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-bold tracking-wide">LOCKDOWN</span>
          <span>
            since {new Date(brakes.lockdown.engaged_at).toLocaleString()}, by {brakes.lockdown.by.kind} over {brakes.lockdown.by.via}
            {brakes.lockdown.reason ? `: ${brakes.lockdown.reason}` : ''}. Only plain reads run.
          </span>
          <button data-testid="lift-lockdown" className="rounded border border-red-500 px-2 py-0.5 text-xs font-semibold" onClick={() => lift(() => apiClient.liftLockdown(token), setError)}>
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
          <button className="rounded border border-red-500 px-2 py-0.5 text-xs font-semibold" onClick={() => lift(() => apiClient.liftFreeze(token, f.id), setError)}>
            Lift
          </button>
        </div>
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

// liftFromApproval completes a lift approved with a passkey (an approval of
// connector brake).
export function liftFromApproval(token: string, operation: string, freezeID: string | undefined, approvalID: string) {
  return operation === 'lift_freeze' && freezeID ? apiClient.liftFreeze(token, freezeID, approvalID) : apiClient.liftLockdown(token, approvalID)
}

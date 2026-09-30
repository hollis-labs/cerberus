import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { Callout, FormDialog, Input } from '@hollis-labs/sysop-ui/ui'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, confirmableApproval, confirmTarget, refusalApproval, type ApprovalRef, type ConnectorPlan, type ConsoleWrite } from '../api/client'
import { ApprovalDetail } from './approval-detail'
import { PlanBody, targetLine } from './plan-body'

export { PlanBody } from './plan-body'

// Confirming on the call (P3-3b, tty_confirm in the console): when policy
// wants the operator to confirm an operation themselves, the console shows
// the plan the approval binds to, with its whole hash, and asks for the
// target to be typed. The call is then sent again on its confirm route with
// that hash and the pending approval the first attempt asked for, which the
// daemon decides and consumes in that one call.
//
// Out of band is the passkey. For a call whose step can retry under an
// approval (the console's own writes), the console shows the approval it
// asked for, approved here with the passkey (I5: the passkey is the
// boundary, so the surface that asked may approve with it), and sends the
// call again under it once approved. A step without retry leaves an
// out-of-band approval to the approvals page.

export interface ConfirmStep<T> {
  // The call's plan, as an approval binds it.
  plan: () => Promise<ConnectorPlan>
  // The call again, confirmed against the plan shown.
  confirm: (c: { approval_id?: string; confirmed_plan_hash: string }) => Promise<T>
  // The call again, under an approval met out of band.
  retry?: (approvalID: string) => Promise<T>
}

// ConfirmCancelled is what a withConfirm promise rejects with when the
// operator closes the dialog: nothing ran.
export class ConfirmCancelled extends Error {
  constructor() {
    super('Not confirmed; nothing ran.')
  }
}

type WithConfirm = <T>(send: () => Promise<T>, step: ConfirmStep<T>) => Promise<T>

const ConfirmContext = createContext<WithConfirm | null>(null)

// useConfirmOnCall is withConfirm: it sends the call and, when the refusal
// is one the operator can confirm here, shows the plan and resolves with the
// confirmed call's result. Any other refusal rejects as it came.
export function useConfirmOnCall(): WithConfirm {
  const withConfirm = useContext(ConfirmContext)
  if (!withConfirm) throw new Error('useConfirmOnCall needs a ConfirmOnCallProvider')
  return withConfirm
}

// Waiting is a call held for its out-of-band approval.
interface Waiting {
  id: string
  run: () => Promise<void>
  cancel: () => void
}

// useConsoleWrite sends a console write through its confirm step: a
// confirmation on the call, or the out-of-band approval, as policy asks.
export function useConsoleWrite(): <T>(w: ConsoleWrite<T>) => Promise<T> {
  const withConfirm = useConfirmOnCall()
  return useCallback(<T,>(w: ConsoleWrite<T>) => withConfirm(w.send, w), [withConfirm])
}

interface Open {
  shown: ConnectorPlan
  approval: ApprovalRef
  confirm: (hash: string) => Promise<void>
  cancel: () => void
}

export function ConfirmOnCallProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState<Open | null>(null)
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const openRef = useRef<Open | null>(null)
  const [waiting, setWaiting] = useState<Waiting | null>(null)
  const waitingRef = useRef<Waiting | null>(null)

  const withConfirm = useCallback(<T,>(send: () => Promise<T>, step: ConfirmStep<T>): Promise<T> => {
    return send().catch(async (err: unknown) => {
      const approval = confirmableApproval(err)
      if (!approval) {
        const pending = refusalApproval(err)
        const retry = step.retry
        if (!pending?.id || pending.channel !== 'out_of_band' || !retry) throw err
        return new Promise<T>((resolve, reject) => {
          const next: Waiting = {
            id: pending.id,
            run: async () => resolve(await retry(pending.id)),
            cancel: () => reject(new ConfirmCancelled()),
          }
          waitingRef.current = next
          setWaiting(next)
        })
      }
      const shown = await step.plan()
      return new Promise<T>((resolve, reject) => {
        const next: Open = {
          shown,
          approval,
          confirm: async (hash) => {
            resolve(await step.confirm({ approval_id: approval.id || undefined, confirmed_plan_hash: hash }))
          },
          cancel: () => reject(new ConfirmCancelled()),
        }
        openRef.current = next
        setTyped('')
        setError(null)
        setOpen(next)
      })
    })
  }, [])

  function close() {
    if (busy) return
    openRef.current?.cancel()
    openRef.current = null
    setOpen(null)
  }

  const target = open ? confirmTarget(open.shown.plan) : ''
  return (
    <ConfirmContext.Provider value={withConfirm}>
      {children}
      {waiting && (
        <OutOfBandStep
          waiting={waiting}
          onDone={() => {
            waitingRef.current = null
            setWaiting(null)
          }}
        />
      )}
      <FormDialog
        open={open !== null}
        onClose={close}
        title={open ? `Confirm ${open.shown.plan.connector} ${open.shown.plan.operation}` : ''}
        description="Policy asks you to confirm this yourself. The confirmation is bound to the plan below, by its hash."
        submitLabel="Confirm and run"
        submitDisabled={typed !== target}
        submitting={busy}
        widthClassName="max-w-2xl"
        onSubmit={async () => {
          if (!open || typed !== target) return
          setBusy(true)
          setError(null)
          try {
            await open.confirm(open.shown.plan_hash)
            openRef.current = null
            setOpen(null)
          } catch (err) {
            // A refused confirmation (plan_stale, a changed policy) stays
            // open with the reason; closing it then cancels.
            setError(err instanceof Error ? err.message : String(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        {open && (
          <div className="space-y-3 text-sm">
            <div className="space-y-1">
              <div className="font-semibold">Effect: {open.shown.plan.effect || 'unknown'}</div>
              <div className="font-semibold">Target: {targetLine(open.shown.plan.target)}</div>
              <div className="font-semibold">Computed by: {open.shown.computed_by}</div>
            </div>
            <PlanBody plan={open.shown.plan} />
            <div className="space-y-1">
              <div className="text-xs text-text-soft">Plan hash</div>
              <code data-testid="plan-hash" className="block break-all rounded border border-border-strong p-2 font-mono text-xs">
                {open.shown.plan_hash}
              </code>
            </div>
            <label className="block">
              Type the target ({target}) to confirm
              <Input data-testid="confirm-typed" autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} />
            </label>
            {error && <div className="text-danger">{error}</div>}
          </div>
        )}
      </FormDialog>
    </ConfirmContext.Provider>
  )
}

// OutOfBandStep holds a call for its out-of-band approval: it shows the
// approval as the approvals page does, to approve with the passkey, and
// sends the call again once it is approved. Closing it before then leaves
// the approval pending, and nothing ran.
function OutOfBandStep({ waiting, onDone }: { waiting: Waiting; onDone: () => void }) {
  const approvals = usePoll((signal) => apiClient.listApprovals(signal), 2000)
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const ran = useRef(false)
  const a = (approvals.data?.approvals ?? []).find((x) => x.id === waiting.id)

  useEffect(() => {
    let cancelled = false
    apiClient.getSession().then((s) => {
      if (!cancelled) setToken(s.action_token)
    })
    return () => {
      cancelled = true
    }
  }, [])

  const run = useCallback(async () => {
    setBusy(true)
    setError(null)
    try {
      await waiting.run()
      onDone()
    } catch (err) {
      // A refused retry (a changed plan, a changed policy) stays open with
      // the reason; closing it then cancels.
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }, [waiting, onDone])

  // Approved: send the call again, once. A failed run is retried by hand.
  useEffect(() => {
    if (a?.status === 'approved' && !ran.current) {
      ran.current = true
      void run()
    }
  }, [a?.status, run])

  const ended = a && ['denied', 'expired', 'revoked'].includes(a.status)
  return (
    <FormDialog
      open
      onClose={() => {
        if (busy) return
        waiting.cancel()
        onDone()
      }}
      title="Approve with your passkey"
      description="This change needs an out-of-band approval. Approve it here with a passkey enrolled for this Cerberus, and it runs under that approval."
      submitLabel="Run it"
      submitDisabled={a?.status !== 'approved' || busy}
      submitting={busy}
      widthClassName="max-w-3xl"
      onSubmit={run}
    >
      <div data-testid="out-of-band-step" className="space-y-3 text-sm">
        {!a && !approvals.error && <div>Loading approval {waiting.id}…</div>}
        {approvals.error != null && <Callout tone="danger">{approvals.error instanceof Error ? approvals.error.message : String(approvals.error)}</Callout>}
        {a && <ApprovalDetail approval={a} token={token} onChanged={approvals.refetch} />}
        {a && ended && <Callout tone="warning">This approval is {a.status}, so the change did not run. Close this and try again to ask anew.</Callout>}
        {error && <div className="text-danger">{error}</div>}
      </div>
    </FormDialog>
  )
}

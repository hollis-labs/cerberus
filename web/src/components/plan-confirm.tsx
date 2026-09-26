import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'
import { FormDialog, Input } from '@hollis-labs/sysop-ui/ui'
import { confirmableApproval, confirmTarget, type ApprovalRef, type ConnectorPlan, type PlanTarget, type PlanView } from '../api/client'

// Confirming on the call (P3-3b, tty_confirm in the console): when policy
// wants the operator to confirm an operation themselves, the console shows
// the plan the approval binds to, with its whole hash, and asks for the
// target to be typed. The call is then sent again on its confirm route with
// that hash and the pending approval the first attempt asked for, which the
// daemon decides and consumes in that one call. Out of band is not this:
// those approvals are approved on the approvals page with a passkey.

export interface ConfirmStep<T> {
  // The call's plan, as an approval binds it.
  plan: () => Promise<ConnectorPlan>
  // The call again, confirmed against the plan shown.
  confirm: (c: { approval_id?: string; confirmed_plan_hash: string }) => Promise<T>
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

  const withConfirm = useCallback(<T,>(send: () => Promise<T>, step: ConfirmStep<T>): Promise<T> => {
    return send().catch(async (err: unknown) => {
      const approval = confirmableApproval(err)
      if (!approval) throw err
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

function targetLine(t: PlanTarget): string {
  const fields = t.fields ?? {}
  const name = t.resource || fields[Object.keys(fields).sort()[0]] || ''
  const labels = [`env ${t.env || 'unknown'}`, `owner ${t.owner || 'unknown'}`, `admin ${t.admin || 'unknown'}`]
  if (t.tags && t.tags.length > 0) labels.push(`tags ${t.tags.join(',')}`)
  return `${[t.kind, name].filter(Boolean).join(' ')} (${labels.join(', ')})`
}

function PlanBody({ plan }: { plan: PlanView }) {
  const lines: string[] = []
  if (plan.state) lines.push(`State:    ${plan.state}`)
  if (plan.source) lines.push(`Source:   ${plan.source.path} at ${plan.source.head}${plan.source.dirty ? ' (uncommitted changes)' : ''}`)
  if (plan.artifact) lines.push(`Artifact: ${plan.artifact}`)
  if (plan.preview !== undefined) lines.push(`Preview (${plan.preview_kind || 'host'}): ${JSON.stringify(plan.preview)}`)
  if (plan.plugin_entrypoint_sha256) lines.push(`Plugin:   ${plan.plugin_entrypoint_sha256}`)
  ;(plan.steps ?? []).forEach((s, i) => {
    lines.push(`Step ${i + 1}:   ${s.name}: ${s.command}`)
    if (s.dir) lines.push(`          in ${s.dir}`)
    if (s.env && s.env.length > 0) lines.push(`          env: ${s.env.join(' ')}`)
  })
  if (plan.digests) lines.push(`Binds:    ${Object.keys(plan.digests).sort().join(', ')}`)
  return (
    <div className="space-y-2">
      {lines.length > 0 && (
        <pre className="max-h-48 overflow-auto rounded border border-border-strong p-2 font-mono text-xs whitespace-pre-wrap break-all">{lines.join('\n')}</pre>
      )}
      {(plan.actions ?? []).map((a, i) => (
        <div key={i} className="space-y-1 pl-3">
          <div className="font-semibold">
            {a.operation} {targetLine(a.target)}
          </div>
          <PlanBody plan={a} />
        </div>
      ))}
    </div>
  )
}

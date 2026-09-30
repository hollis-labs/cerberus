import { useState } from 'react'
import { Button, Callout, Input } from '@hollis-labs/sysop-ui/ui'
import { apiClient, type ApprovalInfo, type ApprovalPrincipal } from '../api/client'
import { assertPasskey } from '../webauthn'
import { liftFromApproval } from './brakes'
import { PlanBody } from './plan-body'

// ApprovalDetail is one approval request as its approver sees it: who
// asked, the target and its labels, the plan it binds to, and the approve
// and deny controls. The approvals page shows it, and so does the console's
// out-of-band step for a call the console itself asked for.

export function ApprovalDetail({ approval: a, token, onChanged }: { approval: ApprovalInfo; token: string; onChanged: () => void }) {
  const [typed, setTyped] = useState('')
  const [reason, setReason] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const target = targetName(a)
  const outOfBand = a.channel === 'out_of_band'

  async function act(run: () => Promise<unknown>) {
    setBusy(true)
    setError(null)
    try {
      await run()
      onChanged()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mt-4 space-y-2 rounded border border-border p-4" data-testid="approval-detail">
      {a.break_glass && (
        <div data-testid="break-glass-banner" className="rounded border-2 border-red-500 bg-red-500/15 p-3 text-red-700">
          <div className="text-lg font-bold tracking-wide">BREAK GLASS</div>
          <div className="text-sm">
            {who(a.principal)} is getting past an approval policy asks for, on a prod, shared or not-ours target. This is not an ordinary
            approval: approving it with your passkey lets the call run once, and it stays in `cerberus status` until acknowledged.
          </div>
          <div className="mt-1 text-sm">
            Reason: <span className="font-semibold">{a.break_glass.reason}</span>
          </div>
        </div>
      )}
      <h2 className="text-base font-semibold">
        {a.connector}.{a.operation} on {target} — {a.status}
      </h2>
      <dl className="grid grid-cols-[10rem_1fr] gap-1 text-sm">
        <dt className="text-text-muted">Requested by</dt>
        <dd data-testid="requester">{who(a.principal)}</dd>
        <dt className="text-text-muted">Target</dt>
        <dd>
          {target} [env {a.target.env || '-'}, owner {a.target.owner || '-'}, admin {a.target.admin || '-'}]
        </dd>
        <dt className="text-text-muted">Effect</dt>
        <dd>{a.effect || '-'}</dd>
        <dt className="text-text-muted">Why</dt>
        <dd>
          rule {a.rule || '-'}
          {a.reason ? `: ${a.reason}` : ''}
        </dd>
        <dt className="text-text-muted">How</dt>
        <dd>
          {a.channel}, scope {a.scope}
        </dd>
        <dt className="text-text-muted">Plan</dt>
        <dd className="font-mono" data-testid="plan-hash">
          {a.plan_hash || 'no plan recorded'}
        </dd>
        <dt className="text-text-muted">Arguments</dt>
        <dd className="font-mono">{a.args_digest}</dd>
      </dl>
      <ShownPlan shown={a.shown} />
      {a.decision && (
        <p className="text-sm">
          {a.decision.approve ? 'Approved' : 'Denied'} by {who(a.decision.by)} at {new Date(a.decision.at).toLocaleString()}
          {a.decision.key_fingerprint ? ` with key ${a.decision.key_fingerprint}` : ''}
          {a.decision.same_surface ? ' (passkey, same surface)' : ''}
        </p>
      )}
      {error && (
        <div data-testid="approval-error">
          <Callout tone="danger">{error}</Callout>
        </div>
      )}
      {a.status === 'pending' && (
        <div className="space-y-2">
          {outOfBand && (
            <Callout tone="warning">
              This request must be approved out of band, with a passkey enrolled for this Cerberus (Touch ID or a security key).
            </Callout>
          )}
          <label className="block text-sm">
            Type the target ({target}) to approve
            <Input data-testid="typed" value={typed} onChange={(e) => setTyped(e.target.value)} />
          </label>
          <label className="block text-sm">
            Reason (optional)
            <Input value={reason} onChange={(e) => setReason(e.target.value)} />
          </label>
          <div className="flex gap-2">
            <Button
              data-testid="approve"
              disabled={busy || !token || typed !== target}
              onClick={() =>
                act(async () => {
                  if (!outOfBand) return apiClient.decideApproval(a.id, token, true, typed, reason)
                  const c = await apiClient.approvalChallenge(a.id, token)
                  const assertion = { ceremony: c.ceremony, credential: await assertPasskey(c.options) }
                  return apiClient.decideApproval(a.id, token, true, typed, reason, assertion)
                })
              }
            >
              {outOfBand ? 'Approve with passkey' : 'Approve'}
            </Button>
            <Button
              data-testid="deny"
              variant="outline"
              disabled={busy || !token}
              onClick={() => act(() => apiClient.decideApproval(a.id, token, false, '', reason))}
            >
              Deny
            </Button>
          </div>
        </div>
      )}
      {a.status === 'approved' && a.connector === 'brake' && (
        <Button
          data-testid="lift-now"
          disabled={busy || !token}
          onClick={() =>
            act(async () => {
              await liftFromApproval(token, a.operation, a.target.fields?.id, a.id)
              window.location.assign('/')
            })
          }
        >
          Lift now
        </Button>
      )}
      {a.status === 'approved' && (
        <Button data-testid="revoke" variant="outline" disabled={busy || !token} onClick={() => act(() => apiClient.revokeApproval(a.id, token, reason))}>
          Revoke
        </Button>
      )}
    </div>
  )
}

// ShownPlan is what the call would run, as the daemon stored it with the
// approval: the plan it binds to and its arguments, redacted. Text the
// requester wrote is flagged, so the approver reads it as a claim to check,
// not as Cerberus describing the call (H3).
function ShownPlan({ shown }: { shown: ApprovalInfo['shown'] }) {
  if (!shown) {
    return (
      <div data-testid="shown-missing">
        <Callout tone="warning">
          What this call would run was not recorded with the approval (it was asked for before approvals stored their plan). Decide on the
          operation and target above, or deny it and have it asked again.
        </Callout>
      </div>
    )
  }
  const untrusted = new Set(shown.untrusted ?? [])
  const requester = (
    <span data-testid="untrusted-note" className="ml-2 rounded bg-amber-500/15 px-1 text-xs text-amber-700">
      written by the requester, not Cerberus: check it, do not take its word
    </span>
  )
  return (
    <div className="space-y-3 rounded border border-border-strong p-3" data-testid="shown-plan">
      <div className="text-sm font-semibold">What it would run</div>
      {shown.plan && (
        <div className="space-y-1 text-sm">
          <div>
            Plan it binds to
            {untrusted.has('/plan/preview') && requester}
          </div>
          <PlanBody plan={shown.plan} />
        </div>
      )}
      {shown.arguments && (
        <div className="space-y-1 text-sm">
          <div>
            Arguments
            {untrusted.has('/arguments') && requester}
          </div>
          <pre data-testid="shown-arguments" className="max-h-64 overflow-auto rounded border border-border p-2 font-mono text-xs whitespace-pre-wrap break-all">
            {JSON.stringify(shown.arguments, null, 2)}
          </pre>
        </div>
      )}
      {shown.truncated && (
        <p className="text-xs text-text-muted">Part of this was too large to store with the approval; the approval still binds the whole plan by its hash.</p>
      )}
    </div>
  )
}

export function targetName(a: ApprovalInfo): string {
  return a.target.resource || a.target.kind || '-'
}

export function who(p?: ApprovalPrincipal): string {
  if (!p) return '-'
  let s = p.kind || 'unknown'
  if (p.via) s += ` via ${p.via}`
  if (p.client) s += ` (${p.client})`
  if (p.session) s += `, session ${p.session}`
  return s
}


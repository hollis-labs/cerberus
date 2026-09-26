import { useEffect, useState } from 'react'
import { Button, Callout, EmptyState, Input } from '@hollis-labs/sysop-ui/ui'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, type ApprovalInfo, type ApprovalPrincipal } from '../api/client'

// The approvals page (P3-4): every request, who asked, and the plan it would
// run. A pending request is approved by typing its target, as on a terminal,
// or denied. One that must be approved out of band is approved here with a
// passkey; the daemon refuses it without one.
export function ApprovalsPage() {
  const approvals = usePoll((signal) => apiClient.listApprovals(signal), 5000)
  const [token, setToken] = useState('')
  const [selected, setSelected] = useState<string | null>(() => new URLSearchParams(window.location.search).get('id'))

  useEffect(() => {
    let cancelled = false
    apiClient.getSession().then((s) => {
      if (!cancelled) setToken(s.action_token)
    })
    return () => {
      cancelled = true
    }
  }, [])

  if (approvals.error) {
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus daemon"
        title="Could not load approvals"
        description={approvals.error instanceof Error ? approvals.error.message : String(approvals.error)}
        action={{ label: 'Retry', onClick: approvals.refetch }}
      />
    )
  }
  const items = approvals.data?.approvals ?? []
  const current = items.find((a) => a.id === selected)

  return (
    <div className="flex h-full min-h-0 w-full flex-col overflow-auto p-3" data-testid="approvals-page">
      {(approvals.data?.problems ?? []).map((p) => (
        <Callout key={p} tone="warning">
          {p}
        </Callout>
      ))}
      {items.length === 0 ? (
        <EmptyState variant="no-results" title="No approval requests." description="Operations that policy says need approval wait here." />
      ) : (
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="text-text-muted">
              <th className="p-2">Status</th>
              <th className="p-2">Operation</th>
              <th className="p-2">Target</th>
              <th className="p-2">Requested by</th>
              <th className="p-2">Channel</th>
              <th className="p-2">Expires</th>
            </tr>
          </thead>
          <tbody>
            {items.map((a) => (
              <tr
                key={a.id}
                data-testid={`approval-${a.id}`}
                className={`cursor-pointer border-t border-border ${a.id === selected ? 'bg-bg-soft' : ''}`}
                onClick={() => setSelected(a.id)}
              >
                <td className="p-2">{a.status}</td>
                <td className="p-2 font-mono">
                  {a.connector}.{a.operation}
                </td>
                <td className="p-2">{targetName(a)}</td>
                <td className="p-2">{who(a.principal)}</td>
                <td className="p-2">{a.channel}</td>
                <td className="p-2">{new Date(a.expires_at).toLocaleString()}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {current && <ApprovalDetail approval={current} token={token} onChanged={approvals.refetch} />}
    </div>
  )
}

function ApprovalDetail({ approval: a, token, onChanged }: { approval: ApprovalInfo; token: string; onChanged: () => void }) {
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
      {a.decision && (
        <p className="text-sm">
          {a.decision.approve ? 'Approved' : 'Denied'} by {who(a.decision.by)} at {new Date(a.decision.at).toLocaleString()}
          {a.decision.key_fingerprint ? ` with key ${a.decision.key_fingerprint}` : ''}
        </p>
      )}
      {error && <Callout tone="danger">{error}</Callout>}
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
              onClick={() => act(() => apiClient.decideApproval(a.id, token, true, typed, reason))}
            >
              Approve
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
      {a.status === 'approved' && (
        <Button data-testid="revoke" variant="outline" disabled={busy || !token} onClick={() => act(() => apiClient.revokeApproval(a.id, token, reason))}>
          Revoke
        </Button>
      )}
    </div>
  )
}

function targetName(a: ApprovalInfo): string {
  return a.target.resource || a.target.kind || '-'
}

function who(p?: ApprovalPrincipal): string {
  if (!p) return '-'
  let s = p.kind || 'unknown'
  if (p.via) s += ` via ${p.via}`
  if (p.client) s += ` (${p.client})`
  if (p.session) s += `, session ${p.session}`
  return s
}

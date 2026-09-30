import { useEffect, useState } from 'react'
import { Button, Callout, EmptyState, Input } from '@hollis-labs/sysop-ui/ui'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, type ApprovalInfo, type ApprovalPrincipal, type EnrollBegin, type PasskeyCeremony } from '../api/client'
import { assertPasskey, createPasskey } from '../webauthn'
import { liftFromApproval } from '../components/brakes'

// The approvals page (P3-4): every request, who asked, and the plan it would
// run. A pending request is approved by typing its target, as on a terminal,
// or denied. One that must be approved out of band is approved here with a
// passkey; the daemon refuses it without one. The passkeys that can do that
// are listed, enrolled and removed here too.
export function ApprovalsPage() {
  const approvals = usePoll((signal) => apiClient.listApprovals(signal), 5000)
  const [token, setToken] = useState('')
  const [params] = useState(() => new URLSearchParams(window.location.search))
  const [selected, setSelected] = useState<string | null>(() => params.get('id'))

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
  // Grants usable now (P3-5): each covers every call of its operation on its
  // target by its requester until it expires or is revoked.
  const grants = items.filter((a) => a.status === 'approved' && (a.scope === 'session' || a.scope === 'window'))

  return (
    <div className="flex h-full min-h-0 w-full flex-col overflow-auto p-3" data-testid="approvals-page">
      {(approvals.data?.problems ?? []).map((p) => (
        <Callout key={p} tone="warning">
          {p}
        </Callout>
      ))}
      {grants.length > 0 && (
        <div className="mb-3 space-y-1" data-testid="active-grants">
          <div className="text-sm font-semibold">Active grants</div>
          {grants.map((g) => (
            <div
              key={g.id}
              className="flex cursor-pointer flex-wrap items-center gap-2 rounded border border-border p-2 text-sm"
              onClick={() => setSelected(g.id)}
            >
              {protectedTarget(g) && <span className="rounded bg-status-blocked/15 px-1 font-semibold text-status-blocked">protected target</span>}
              <span className="font-mono">
                {g.connector}.{g.operation}
              </span>
              <span>on {targetName(g)}</span>
              <span className="text-text-soft">
                {g.scope} grant for {who(g.principal)}, {g.uses ?? 0} use(s), until {new Date(g.expires_at).toLocaleTimeString()}
              </span>
            </div>
          ))}
        </div>
      )}
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
              <th className="p-2">Scope</th>
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
                <td className="p-2">
                  {a.break_glass && <span className="mr-1 rounded bg-red-500/15 px-1 text-xs font-bold text-red-600">BREAK GLASS</span>}
                  {a.status}
                </td>
                <td className="p-2 font-mono">
                  {a.connector}.{a.operation}
                </td>
                <td className="p-2">{targetName(a)}</td>
                <td className="p-2">{who(a.principal)}</td>
                <td className="p-2">{a.channel}</td>
                <td className="p-2">{a.scope}</td>
                <td className="p-2">{new Date(a.expires_at).toLocaleString()}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {current && <ApprovalDetail approval={current} token={token} onChanged={approvals.refetch} />}
      <PasskeysPanel token={token} enrollToken={params.get('enroll') ?? ''} label={params.get('label') ?? ''} remove={params.get('remove') ?? ''} />
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
      {a.decision && (
        <p className="text-sm">
          {a.decision.approve ? 'Approved' : 'Denied'} by {who(a.decision.by)} at {new Date(a.decision.at).toLocaleString()}
          {a.decision.key_fingerprint ? ` with key ${a.decision.key_fingerprint}` : ''}
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

// PasskeysPanel lists the passkeys that approve out-of-band requests, and
// runs the enrollment a `cerberus approvals enroll` link opens and the
// removal `cerberus approvals keys remove` opens. After the first key, both
// need an assertion from a key already enrolled.
function PasskeysPanel({ token, enrollToken, label, remove }: { token: string; enrollToken: string; label: string; remove: string }) {
  const keys = usePoll((signal) => apiClient.getPasskeys(signal), 15000)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function act(run: () => Promise<string>) {
    setBusy(true)
    setError(null)
    setDone(null)
    try {
      setDone(await run())
      keys.refetch()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const enroll = () =>
    act(async () => {
      const begin = await apiClient.passkeyAction<EnrollBegin>('register/begin', token, { token: enrollToken, label })
      const authorize = begin.authorize ? await assertPasskey(begin.authorize) : undefined
      const credential = await createPasskey(begin.creation)
      const key = await apiClient.passkeyAction<{ fingerprint: string }>('register/finish', token, { ceremony: begin.ceremony, credential, authorize })
      return `Enrolled passkey ${key.fingerprint}.`
    })

  const removeKey = (fingerprint: string) =>
    act(async () => {
      const begin = await apiClient.passkeyAction<PasskeyCeremony>('remove/begin', token, { fingerprint })
      const credential = await assertPasskey(begin.options)
      await apiClient.passkeyAction('remove/finish', token, { ceremony: begin.ceremony, credential })
      return `Removed passkey ${fingerprint}.`
    })

  const st = keys.data
  return (
    <div className="mt-6 space-y-2 rounded border border-border p-4" data-testid="passkeys">
      <h2 className="text-base font-semibold">Passkeys for out-of-band approval</h2>
      {keys.error ? <Callout tone="warning">{keys.error instanceof Error ? keys.error.message : String(keys.error)}</Callout> : null}
      {st?.state === 'cooldown' && (
        <Callout tone="danger" data-testid="cooldown">
          The passkey registry changed outside <code>cerberus approvals enroll</code>. Out-of-band approvals are refused until{' '}
          {st.cooldown_until ? new Date(st.cooldown_until).toLocaleString() : 'the cool-down ends'}.
        </Callout>
      )}
      {st && (st.keys ?? []).length === 0 && st.state !== 'cooldown' && (
        <Callout tone="warning" data-testid="not-set-up">
          Out-of-band approval is not set up: run <code>cerberus approvals enroll</code> in a terminal.
        </Callout>
      )}
      {st && (st.keys ?? []).length > 0 && (
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="text-text-muted">
              <th className="p-2">Fingerprint</th>
              <th className="p-2">Label</th>
              <th className="p-2">Enrolled</th>
              <th className="p-2" />
            </tr>
          </thead>
          <tbody>
            {(st.keys ?? []).map((k) => (
              <tr key={k.fingerprint} className={`border-t border-border ${k.fingerprint === remove ? 'bg-bg-soft' : ''}`} data-testid={`passkey-${k.fingerprint}`}>
                <td className="p-2 font-mono">{k.fingerprint}</td>
                <td className="p-2">{k.label || '-'}</td>
                <td className="p-2">{new Date(k.enrolled_at).toLocaleString()}</td>
                <td className="p-2">
                  <Button variant="outline" disabled={busy || !token} onClick={() => removeKey(k.fingerprint)} data-testid={`remove-${k.fingerprint}`}>
                    Remove
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {enrollToken && (
        <div className="space-y-2">
          <p className="text-sm">
            Create a passkey{label ? ` (${label})` : ''} for approving out-of-band requests.
            {st && (st.keys ?? []).length > 0 ? ' A passkey already enrolled has to authorize it first.' : ' This first key is trusted on first use.'}
          </p>
          <Button data-testid="enroll" disabled={busy || !token} onClick={enroll}>
            Create passkey
          </Button>
        </div>
      )}
      {error && (
        <div data-testid="passkeys-error">
          <Callout tone="danger">{error}</Callout>
        </div>
      )}
      {done && (
        <div data-testid="passkeys-done">
          <Callout tone="success">{done}</Callout>
        </div>
      )}
    </div>
  )
}

// protectedTarget is a prod, shared, not-ours or unlabeled target, where a
// grant is the operator's loud choice (D5).
function protectedTarget(a: ApprovalInfo): boolean {
  const env = a.target.env ?? ''
  return !(env === 'dev' || env === 'lab') || a.target.owner !== 'self' || a.target.admin === 'shared'
}

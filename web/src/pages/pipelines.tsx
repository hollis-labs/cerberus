import { useEffect, useState } from 'react'
import { Route } from 'lucide-react'
import { Button, Callout, EmptyState, SummaryCards, Textarea } from '@hollis-labs/sysop-ui/ui'
import { Panel } from '@hollis-labs/sysop-ui/widgets'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient } from '../api/client'
import { ActionConfirm, type PendingConfirm } from '../components/action-confirm'
import { useConfirmOnCall } from '../components/plan-confirm'

export function PipelinesPage() {
  const pipelines = usePoll((signal) => apiClient.listPipelines(signal), 5000)
  const [sessionToken, setSessionToken] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const withConfirm = useConfirmOnCall()
  const [output, setOutput] = useState<Record<string, string>>({})
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<PendingConfirm | null>(null)

  useEffect(() => {
    let cancelled = false
    apiClient.getSession().then((session) => {
      if (!cancelled) setSessionToken(session.action_token)
    })
    return () => {
      cancelled = true
    }
  }, [])

  if (pipelines.error) {
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus daemon"
        title="Could not load pipelines"
        description={pipelines.error instanceof Error ? pipelines.error.message : String(pipelines.error)}
        action={{ label: 'Retry', onClick: pipelines.refetch }}
      />
    )
  }

  const items = (pipelines.data ?? []).slice().sort((a, b) => a.id.localeCompare(b.id))
  const cards = [
    { label: 'Pipelines', value: items.length, accentColor: 'var(--color-text)' },
    { label: 'Stages', value: items.reduce((sum, item) => sum + item.stage_count, 0), accentColor: 'var(--color-warning)' },
  ]

  // A run is exec — a stage can be a shell action — so it asks first, and the
  // request is sent, acknowledged, from the confirm step.
  function confirm(id: string, name: string) {
    if (!sessionToken || busy) return
    setPending({ verb: 'Run', subject: name, effect: 'exec', run: () => run(id) })
  }

  async function run(id: string) {
    if (!sessionToken || busy) return
    setBusy(id)
    setError(null)
    try {
      const result = await withConfirm(() => apiClient.runPipeline(id, sessionToken, true), {
        plan: () => apiClient.planPipeline(id, sessionToken),
        confirm: (c) => apiClient.confirmPipeline(id, sessionToken, c),
      })
      const raw = result.raw ? decodeRunResult(result.raw) : ''
      setOutput((current) => ({
        ...current,
        [id]: [result.error, raw].filter(Boolean).join('\n\n') || 'Pipeline completed.',
      }))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="flex h-full min-h-0 w-full flex-col overflow-auto">
      <SummaryCards cards={cards} />
      <div className="space-y-3 p-3">
        {error && <Callout tone="danger">{error}</Callout>}
        {pipelines.isLoading && items.length === 0 ? (
          <div className="border-b border-border-strong px-4 py-3 text-sm text-text-soft">Loading pipelines...</div>
        ) : items.length === 0 ? (
          <EmptyState variant="no-results" title="No pipelines declared." description="The active config does not expose any pipelines." />
        ) : (
          items.map((item) => (
            <Panel
              key={item.id}
              title={item.name || item.id}
              icon={<Route className="h-3.5 w-3.5" />}
              meta={
                <Button variant="secondary" size="sm" disabled={!sessionToken || busy !== null} onClick={() => confirm(item.id, item.name || item.id)}>
                  {busy === item.id ? 'Running...' : 'Run'}
                </Button>
              }
            >
              <div className="space-y-2 px-3 py-3">
                <div className="font-mono text-xs text-text-soft">{item.id}</div>
                <div className="text-xs text-text-soft">{item.description || `${item.stage_count} stage(s)`}</div>
                {output[item.id] && (
                  <Textarea readOnly value={output[item.id]} className="min-h-32 resize-none font-mono text-xs" />
                )}
              </div>
            </Panel>
          ))
        )}
      </div>
      <ActionConfirm pending={pending} busy={busy !== null} onClose={() => setPending(null)} />
    </div>
  )
}

// decodeRunResult turns the base64 JSON of a pipeline result into readable
// text: pretty-printed when it parses, as decoded otherwise.
function decodeRunResult(raw: string): string {
  const bytes = Uint8Array.from(atob(raw), (c) => c.charCodeAt(0))
  const text = new TextDecoder().decode(bytes)
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

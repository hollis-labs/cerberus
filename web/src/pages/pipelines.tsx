import { useEffect, useState } from 'react'
import { Route } from 'lucide-react'
import { Button, Callout, EmptyState, SummaryCards, Textarea } from '@hollis-labs/sysop-ui/ui'
import { Panel } from '@hollis-labs/sysop-ui/widgets'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient } from '../api/client'

export function PipelinesPage() {
  const pipelines = usePoll((signal) => apiClient.listPipelines(signal), 5000)
  const [sessionToken, setSessionToken] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [output, setOutput] = useState<Record<string, string>>({})
  const [error, setError] = useState<string | null>(null)

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

  async function run(id: string) {
    if (!sessionToken || busy) return
    setBusy(id)
    setError(null)
    try {
      const result = await apiClient.runPipeline(id, sessionToken)
      const raw = result.raw ? new TextDecoder().decode(Uint8Array.from(result.raw)) : ''
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
                <Button variant="secondary" size="sm" disabled={!sessionToken || busy !== null} onClick={() => void run(item.id)}>
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
    </div>
  )
}

import { useEffect, useState } from 'react'
import { Button, EmptyState, SummaryCards, Textarea, usePoll } from '@hollis-labs/sysop-ui'
import { apiClient, type ConnectorDefinition } from '../api/client'

export function ConnectorsPage() {
  const connectors = usePoll((signal) => apiClient.listConnectors(signal), 5000)
  const [sessionToken, setSessionToken] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [configByOp, setConfigByOp] = useState<Record<string, string>>({})
  const [dryRunByOp, setDryRunByOp] = useState<Record<string, boolean>>({})
  const [ackByOp, setAckByOp] = useState<Record<string, boolean>>({})
  const [resultByOp, setResultByOp] = useState<Record<string, string>>({})
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

  if (connectors.error) {
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus connectors"
        title="Could not load connector definitions"
        description={connectors.error instanceof Error ? connectors.error.message : String(connectors.error)}
        action={{ label: 'Retry', onClick: connectors.refetch }}
      />
    )
  }

  const items = (connectors.data ?? []).slice().sort((a, b) => a.id.localeCompare(b.id))
  const cards = [
    { label: 'Connectors', value: items.length, accentColor: 'var(--color-text)' },
    { label: 'Operations', value: items.reduce((sum, item) => sum + item.operations.length, 0), accentColor: 'var(--color-status-done)' },
    { label: 'Secret Requirements', value: items.reduce((sum, item) => sum + (item.config.secrets?.length ?? 0), 0), accentColor: 'var(--color-warning)' },
  ]

  async function runOperation(connector: ConnectorDefinition, operation: string) {
    const key = `${connector.id}:${operation}`
    if (!sessionToken || busy) return
    setBusy(key)
    setError(null)
    try {
      const raw = configByOp[key]?.trim()
      const config = raw ? parseJSONConfig(raw) : undefined
      const result = await apiClient.runConnectorOperation(connector.id, operation, {
        config,
        dry_run: !!dryRunByOp[key],
        acknowledged: !!ackByOp[key],
      }, sessionToken)
      setResultByOp((current) => ({
        ...current,
        [key]: JSON.stringify(result.data, null, 2),
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
      <div className="space-y-4 p-4">
        {error && <div className="border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>}
        {connectors.isLoading && items.length === 0 ? (
          <div className="text-sm text-text-soft">Loading connectors...</div>
        ) : items.length === 0 ? (
          <EmptyState variant="no-results" title="No connectors discovered." description="The daemon did not report any connector definitions." />
        ) : (
          items.map((connector) => (
            <section key={connector.id} className="border border-border bg-panel p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <div className="text-sm text-text">{connector.id}</div>
                  <div className="mt-1 text-xs text-text-soft">v{connector.version} · {connector.resource_types.join(', ') || 'no resource types'}</div>
                </div>
                <div className="flex flex-wrap gap-2 text-xs text-text-soft">
                  {capabilityLabels(connector).map((label) => (
                    <span key={label} className="rounded-full border border-border-soft bg-panel-2/40 px-2 py-1">{label}</span>
                  ))}
                </div>
              </div>

              <div className="mt-4 grid gap-4 xl:grid-cols-[.9fr_1.1fr]">
                <section className="space-y-3">
                  <div>
                    <div className="mb-2 text-xs uppercase tracking-wide text-muted">Config Fields</div>
                    {(connector.config.fields?.length ?? 0) === 0 ? (
                      <div className="text-sm text-text-soft">No explicit config fields.</div>
                    ) : (
                      <div className="space-y-2">
                        {connector.config.fields?.map((field) => (
                          <div key={field.name} className="border border-border-soft bg-panel-2/40 p-3">
                            <div className="text-sm text-text">{field.name} <span className="text-text-soft">({field.type})</span></div>
                            <div className="mt-1 text-xs text-text-soft">{field.description || 'No description.'}</div>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                  <div>
                    <div className="mb-2 text-xs uppercase tracking-wide text-muted">Secret Requirements</div>
                    {(connector.config.secrets?.length ?? 0) === 0 ? (
                      <div className="text-sm text-text-soft">No declared secrets.</div>
                    ) : (
                      <div className="space-y-2">
                        {connector.config.secrets?.map((secret) => (
                          <div key={secret.name} className="border border-border-soft bg-panel-2/40 p-3">
                            <div className="text-sm text-text">{secret.name}</div>
                            <div className="mt-1 text-xs text-text-soft">{secret.description || secret.env || 'No description.'}</div>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                </section>

                <section className="space-y-3">
                  <div className="text-xs uppercase tracking-wide text-muted">Operations</div>
                  {connector.operations.map((operation) => {
                    const key = `${connector.id}:${operation.name}`
                    return (
                      <div key={key} className="border border-border-soft bg-panel-2/40 p-3">
                        <div className="flex flex-wrap items-start justify-between gap-3">
                          <div>
                            <div className="text-sm text-text">{operation.name}</div>
                            <div className="mt-1 text-xs text-text-soft">{operation.description || 'No description.'}</div>
                          </div>
                          <Button variant="secondary" size="sm" disabled={!sessionToken || busy !== null} onClick={() => void runOperation(connector, operation.name)}>
                            {busy === key ? 'Running...' : 'Run'}
                          </Button>
                        </div>
                        <div className="mt-3 grid gap-3 lg:grid-cols-[1fr_auto_auto]">
                          <Textarea
                            value={configByOp[key] ?? ''}
                            onChange={(event) => setConfigByOp((current) => ({ ...current, [key]: event.target.value }))}
                            placeholder='{"example":"value"}'
                            className="min-h-28 resize-y font-mono text-xs"
                          />
                          <label className="flex items-center gap-2 text-xs text-text-soft">
                            <input
                              type="checkbox"
                              checked={!!dryRunByOp[key]}
                              onChange={(event) => setDryRunByOp((current) => ({ ...current, [key]: event.target.checked }))}
                              disabled={!operation.supports_dry}
                            />
                            Dry run
                          </label>
                          <label className="flex items-center gap-2 text-xs text-text-soft">
                            <input
                              type="checkbox"
                              checked={!!ackByOp[key]}
                              onChange={(event) => setAckByOp((current) => ({ ...current, [key]: event.target.checked }))}
                              disabled={!operation.destructive}
                            />
                            Acknowledge
                          </label>
                        </div>
                        {resultByOp[key] && (
                          <Textarea readOnly value={resultByOp[key]} className="mt-3 min-h-28 resize-y font-mono text-xs" />
                        )}
                      </div>
                    )
                  })}
                </section>
              </div>
            </section>
          ))
        )}
      </div>
    </div>
  )
}

function parseJSONConfig(raw: string): Record<string, unknown> {
  const parsed = JSON.parse(raw) as unknown
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error('Connector config must be a JSON object.')
  }
  return parsed as Record<string, unknown>
}

function capabilityLabels(connector: ConnectorDefinition) {
  const labels: string[] = []
  if (connector.capabilities.can_create) labels.push('create')
  if (connector.capabilities.can_destroy) labels.push('destroy')
  if (connector.capabilities.can_build) labels.push('build')
  if (connector.capabilities.can_logs) labels.push('logs')
  if (connector.capabilities.can_health) labels.push('health')
  return labels.length > 0 ? labels : ['metadata only']
}

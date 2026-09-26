import { useEffect, useState } from 'react'
import {
  Button,
  EmptyState,
  Input,
  Pill,
  SettingsField,
  SettingsGrid,
  SettingsPanel,
  SummaryCards,
  Textarea,
} from '@hollis-labs/sysop-ui/ui'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, type ConnectorDefinition, type ConnectorOperation, type ResourceInfo } from '../api/client'
import { useConfirmOnCall } from '../components/plan-confirm'

export function ConnectorsPage() {
  const connectors = usePoll((signal) => apiClient.listConnectors(signal), 5000)
  const resources = usePoll((signal) => apiClient.listResources(signal), 15000)
  const [sessionToken, setSessionToken] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const withConfirm = useConfirmOnCall()
  const [configByOp, setConfigByOp] = useState<Record<string, string>>({})
  const [fieldsByOp, setFieldsByOp] = useState<Record<string, Record<string, string>>>({})
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
    { label: 'Secret requirements', value: items.reduce((sum, item) => sum + (item.config.secrets?.length ?? 0), 0), accentColor: 'var(--color-warning)' },
  ]

  async function runOperation(connector: ConnectorDefinition, operation: string) {
    const key = `${connector.id}:${operation}`
    if (!sessionToken || busy) return
    setBusy(key)
    setError(null)
    try {
      let config: Record<string, unknown> | undefined
      if (targetKey(connector.id)) {
        config = fieldsConfig(fieldsByOp[key] ?? {})
      } else {
        const raw = configByOp[key]?.trim()
        config = raw ? parseJSONConfig(raw) : undefined
      }
      const body = { config, dry_run: !!dryRunByOp[key], acknowledged: !!ackByOp[key] }
      const result = await withConfirm(() => apiClient.runConnectorOperation(connector.id, operation, body, sessionToken), {
        plan: () => apiClient.planConnectorOperation(connector.id, operation, body, sessionToken),
        confirm: (c) => apiClient.confirmConnectorOperation(connector.id, operation, body, sessionToken, c),
      })
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
      <div className="space-y-4">
        {error ? <div className="mx-4 border border-status-blocked/30 bg-status-blocked/10 px-3 py-2 text-sm text-status-blocked">{error}</div> : null}
        {connectors.isLoading && items.length === 0 ? (
          <div className="border-b border-border-strong px-4 py-3 text-sm text-text-soft">Loading connectors...</div>
        ) : items.length === 0 ? (
          <div className="px-4 py-4">
            <EmptyState variant="no-results" title="No connectors discovered." description="The daemon did not report any connector definitions." />
          </div>
        ) : (
          items.map((connector, index) => (
            <div key={connector.id} className={index > 0 ? 'border-t border-border-strong' : undefined}>
              <SettingsPanel title={connector.id} className={index === items.length - 1 ? 'border-b-0' : undefined}>
                <div className="space-y-4 px-4 py-3">
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div>
                      <div className="text-sm text-text">{connector.id}</div>
                      <div className="mt-1 text-xs text-text-soft">
                        v{connector.version} · {connector.resource_types.join(', ') || 'no resource types'}
                      </div>
                    </div>
                    <div className="flex flex-wrap gap-2">
                      {capabilityLabels(connector).map((label) => (
                        <Pill key={label} tone="neutral">{label}</Pill>
                      ))}
                    </div>
                  </div>

                  <SettingsGrid>
                    <Field label="Operations" value={String(connector.operations.length)} />
                    <Field label="Fields" value={String(connector.config.fields?.length ?? 0)} />
                    <Field label="Secrets" value={String(connector.config.secrets?.length ?? 0)} />
                    <Field label="Resource types" value={connector.resource_types.join(', ') || 'none'} />
                  </SettingsGrid>

                  <div className="grid gap-4 xl:grid-cols-[.78fr_1.22fr]">
                    <div className="space-y-4">
                      <SettingsPanel title={targetKey(connector.id) ? 'Resource config fields (set on the resource, not per call)' : 'Config fields'}>
                        <div className="space-y-0">
                          {(connector.config.fields?.length ?? 0) === 0 ? (
                            <div className="px-4 py-3 text-sm text-text-soft">No explicit config fields.</div>
                          ) : (
                            connector.config.fields?.map((field, fieldIndex) => (
                              <div key={field.name} className={fieldIndex > 0 ? 'border-t border-border-soft px-4 py-3' : 'px-4 py-3'}>
                                <div className="text-sm text-text">
                                  {field.name} <span className="text-text-soft">({field.type})</span>
                                </div>
                                <div className="mt-1 text-xs text-text-soft">{field.description || 'No description.'}</div>
                              </div>
                            ))
                          )}
                        </div>
                      </SettingsPanel>

                      <SettingsPanel title="Secret requirements" className="border-b-0">
                        <div className="space-y-0">
                          {(connector.config.secrets?.length ?? 0) === 0 ? (
                            <div className="px-4 py-3 text-sm text-text-soft">No declared secrets.</div>
                          ) : (
                            connector.config.secrets?.map((secret, secretIndex) => (
                              <div key={secret.name} className={secretIndex > 0 ? 'border-t border-border-soft px-4 py-3' : 'px-4 py-3'}>
                                <div className="flex items-center justify-between gap-3">
                                  <div className="text-sm text-text">{secret.name}</div>
                                  <Pill tone="warning">secret</Pill>
                                </div>
                                <div className="mt-1 text-xs text-text-soft">{secret.description || secret.env || 'No description.'}</div>
                              </div>
                            ))
                          )}
                        </div>
                      </SettingsPanel>
                    </div>

                    <SettingsPanel title="Operations" className="border-b-0">
                      <div className="space-y-0">
                        {connector.operations.map((operation, operationIndex) => {
                          const key = `${connector.id}:${operation.name}`
                          return (
                            <div key={key} className={operationIndex > 0 ? 'border-t border-border-soft px-4 py-3' : 'px-4 py-3'}>
                              <div className="flex flex-wrap items-start justify-between gap-3">
                                <div>
                                  <div className="text-sm text-text">
                                    {operation.name}
                                    {operation.effect ? <span className="ml-2 text-xs text-text-soft">{operation.effect}</span> : null}
                                  </div>
                                  <div className="mt-1 max-w-2xl text-xs text-text-soft">{operation.description || 'No description.'}</div>
                                </div>
                                <Button variant="secondary" size="sm" disabled={!sessionToken || busy !== null} onClick={() => void runOperation(connector, operation.name)}>
                                  {busy === key ? 'Running...' : 'Run'}
                                </Button>
                              </div>
                              <div className="mt-3 grid gap-3 lg:grid-cols-[1fr_auto_auto]">
                                {targetKey(connector.id) ? (
                                  <ResourceOperationForm
                                    connectorID={connector.id}
                                    operation={operation}
                                    resources={resources.data ?? []}
                                    values={fieldsByOp[key] ?? {}}
                                    onChange={(field, value) =>
                                      setFieldsByOp((current) => ({ ...current, [key]: { ...(current[key] ?? {}), [field]: value } }))
                                    }
                                  />
                                ) : (
                                  <Textarea
                                    value={configByOp[key] ?? ''}
                                    onChange={(event) => setConfigByOp((current) => ({ ...current, [key]: event.target.value }))}
                                    placeholder='{"example":"value"}'
                                    className="min-h-28 resize-y font-mono text-xs"
                                  />
                                )}
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
                                    disabled={!operation.requires_ack}
                                  />
                                  Acknowledge
                                </label>
                              </div>
                              {resultByOp[key] ? (
                                <Textarea readOnly value={resultByOp[key]} className="mt-3 min-h-28 resize-y font-mono text-xs" />
                              ) : null}
                            </div>
                          )
                        })}
                      </div>
                    </SettingsPanel>
                  </div>
                </div>
              </SettingsPanel>
            </div>
          ))
        )}
      </div>
    </div>
  )
}

// SSH and docker operations take a configured resource, never a free-form
// host, key or compose file: the daemon refuses those fields, and resolves
// the target from the resource. targetKey is the config key the resource id
// travels under.
function targetKey(connectorID: string): string | null {
  if (connectorID === 'ssh') return 'id'
  if (connectorID === 'docker') return 'resource'
  return null
}

// Keys the form handles itself rather than as a free-text field: the
// resource, and docker's container name and its aliases.
const formManagedKeys = new Set(['id', 'resource', 'container', 'container_id', 'container_name', 'name'])

const inputClass =
  'w-full border border-border bg-panel-2/60 px-3 py-2 text-sm text-text outline-none transition-colors focus:border-border-strong'

function ResourceOperationForm({
  connectorID,
  operation,
  resources,
  values,
  onChange,
}: {
  connectorID: string
  operation: ConnectorOperation
  resources: ResourceInfo[]
  values: Record<string, string>
  onChange: (field: string, value: string) => void
}) {
  const resourceKey = targetKey(connectorID) ?? 'id'
  const options = resources.filter((resource) => resource.connector === connectorID)
  const properties = (operation.input_schema?.properties ?? {}) as Record<string, { description?: string }>
  const advertised = new Set(Object.keys(properties))
  const opFields = Object.keys(properties).filter((field) => !formManagedKeys.has(field)).sort()
  return (
    <div className="space-y-2">
      {advertised.has(resourceKey) ? (
        <select
          value={values[resourceKey] ?? ''}
          onChange={(event) => onChange(resourceKey, event.target.value)}
          className={inputClass}
          aria-label={`${connectorID} resource`}
        >
          <option value="">
            {options.length === 0 ? `No ${connectorID} resources configured` : `Select a ${connectorID} resource`}
            {connectorID === 'docker' ? ' (optional)' : ''}
          </option>
          {options.map((resource) => (
            <option key={resource.id} value={resource.id}>
              {resource.name && resource.name !== resource.id ? `${resource.id} — ${resource.name}` : resource.id}
            </option>
          ))}
        </select>
      ) : null}
      {connectorID === 'docker' && advertised.has('container') ? (
        <input
          value={values.container ?? ''}
          onChange={(event) => onChange('container', event.target.value)}
          placeholder="or a container name on this machine (optional)"
          className={inputClass}
        />
      ) : null}
      {opFields.map((field) => (
        <input
          key={field}
          value={values[field] ?? ''}
          onChange={(event) => onChange(field, event.target.value)}
          placeholder={properties[field]?.description ? `${field} — ${properties[field].description}` : field}
          className={inputClass}
        />
      ))}
    </div>
  )
}

// fieldsConfig turns the form's values into an operation config, dropping
// empty fields and sending lines as a number.
function fieldsConfig(values: Record<string, string>): Record<string, unknown> {
  const config: Record<string, unknown> = {}
  for (const [field, raw] of Object.entries(values)) {
    const value = raw.trim()
    if (!value) continue
    config[field] = field === 'lines' ? Number(value) : value
  }
  return config
}

function Field({ label, value }: { label: string; value: string }) {
  return <SettingsField label={label}>{value}</SettingsField>
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

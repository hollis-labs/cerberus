import { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, CheckCircle2, FileWarning, GitBranch, ShieldAlert } from 'lucide-react'
import { Button, EmptyState, SummaryCards, usePoll } from '@hollis-labs/sysop-ui'
import { apiClient, type ConfigValidationFile } from '../api/client'

export function RegistryPage() {
  const registry = usePoll((signal) => apiClient.listRegistry(signal), 5000)
  const health = usePoll((signal) => apiClient.getRegistryHealth(signal), 5000)
  const validation = usePoll((signal) => apiClient.getConfigValidation(signal), 5000)
  const resolve = usePoll((signal) => apiClient.getConfigResolve(signal), 5000)
  const [sessionToken, setSessionToken] = useState('')
  const [path, setPath] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
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

  const requestError = registry.error || health.error || validation.error || resolve.error
  if (requestError) {
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus registry"
        title="Could not load registry diagnostics"
        description={requestError instanceof Error ? requestError.message : String(requestError)}
        action={{
          label: 'Retry',
          onClick: () => {
            void registry.refetch()
            void health.refetch()
            void validation.refetch()
            void resolve.refetch()
          },
        }}
      />
    )
  }

  const entries = registry.data?.entries ?? []
  const registryAudit = registry.data
  const registryHealth = health.data
  const validationData = validation.data
  const resolveData = resolve.data
  const cards = [
    { label: 'Entries', value: registryAudit?.summary.entries ?? registryHealth?.summary.entries ?? entries.length, accentColor: 'var(--color-text)' },
    { label: 'Shared IDs', value: registryAudit?.summary.shared ?? 0, accentColor: 'var(--color-status-done)' },
    { label: 'Local Only', value: registryAudit?.summary.local_only ?? 0, accentColor: 'var(--color-warning)' },
    { label: 'Resolve Warnings', value: registryAudit?.resolve_warnings?.length ?? resolveData?.warnings?.length ?? 0, accentColor: 'var(--color-status-blocked)' },
  ]

  async function registerConfig() {
    if (!sessionToken || !path.trim() || busy) return
    setBusy('register')
    setError(null)
    try {
      await apiClient.registerConfig(path.trim(), sessionToken)
      setPath('')
      await Promise.all([registry.refetch(), health.refetch(), validation.refetch(), resolve.refetch()])
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  async function deregister(owner: string) {
    if (!sessionToken || busy) return
    setBusy(owner)
    setError(null)
    try {
      await apiClient.deregisterOwner(owner, sessionToken)
      await Promise.all([registry.refetch(), health.refetch(), validation.refetch(), resolve.refetch()])
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
        <section className="border border-border bg-panel p-4">
          <div className="mb-3 text-sm text-text">Register config</div>
          <div className="flex flex-col gap-2 md:flex-row">
            <input
              value={path}
              onChange={(event) => setPath(event.target.value)}
              placeholder="/absolute/path/to/project.cerberus.yaml"
              className="min-w-0 flex-1 border border-border bg-panel-2/60 px-3 py-2 text-sm text-text outline-none transition-colors focus:border-border-strong"
            />
            <Button variant="secondary" size="sm" disabled={!sessionToken || busy !== null || path.trim() === ''} onClick={() => void registerConfig()}>
              {busy === 'register' ? 'Registering...' : 'Register'}
            </Button>
          </div>
          <div className="mt-2 text-xs text-text-soft">Cerberus stores only the registry pointer. The app-owned file remains the source of truth.</div>
        </section>

        {error && <div className="border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>}

        <section className="border border-border bg-panel p-4">
          <div className="mb-3 text-sm text-text">Registry audit</div>
          {registryAudit ? (
            <div className="grid gap-4 xl:grid-cols-[1fr_1fr]">
              <div className="grid grid-cols-2 gap-3 text-sm">
                <Metric label="Config path" value={registryAudit.config_path || '-'} />
                <Metric label="Index path" value={registryAudit.index_path || '-'} />
                <Metric label="Config exists" value={registryAudit.config_exists ? 'yes' : 'no'} />
                <Metric label="Index exists" value={registryAudit.index_exists ? 'yes' : 'no'} />
                <Metric label="Healthy entries" value={String(registryAudit.summary.healthy)} />
                <Metric label="Unhealthy entries" value={String(registryAudit.summary.unhealthy)} />
                <Metric label="Shared identities" value={String(registryAudit.summary.shared)} />
                <Metric label="Resolve skips" value={String(registryAudit.summary.resolve_skips)} />
              </div>
              <div className="space-y-3">
                {registryAudit.resolve_warnings && registryAudit.resolve_warnings.length > 0 ? (
                  <div className="space-y-2">
                    <div className="text-xs uppercase tracking-wide text-muted">Resolve warnings</div>
                    {registryAudit.resolve_warnings.slice(0, 4).map((warning) => (
                      <div key={warning} className="border border-border-soft bg-panel-2/40 px-3 py-2 text-xs text-text-soft">{warning}</div>
                    ))}
                  </div>
                ) : (
                  <div className="text-sm text-text-soft">No resolve warnings.</div>
                )}
                {registryAudit.skipped && registryAudit.skipped.length > 0 && (
                  <div className="space-y-2">
                    <div className="text-xs uppercase tracking-wide text-muted">Resolve skips</div>
                    {registryAudit.skipped.map((report) => (
                      <div key={`${report.owner}:${report.path}`} className="border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-xs text-text-soft">
                        <div className="text-text">{report.owner}</div>
                        <div className="font-mono">{report.path}</div>
                        {report.detail && <div className="mt-1">{report.detail}</div>}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          ) : (
            <div className="text-sm text-text-soft">Loading registry audit...</div>
          )}
        </section>

        <div className="grid gap-4 xl:grid-cols-[1.2fr_.8fr]">
          <section className="border border-border bg-panel p-4">
            <div className="mb-3 flex items-center gap-2 text-sm text-text">
              <ShieldAlert className="h-4 w-4" />
              Registry Health
            </div>
            {registryHealth && registryHealth.reports.length > 0 ? (
              <div className="space-y-3">
                {registryHealth.reports.map((report) => (
                  <div key={report.owner} className="border border-border-soft bg-panel-2/40 p-3">
                    <div className="flex items-start justify-between gap-3">
                      <div>
                        <div className="text-sm text-text">{report.owner}</div>
                        <div className="font-mono text-xs text-text-soft">{report.path}</div>
                      </div>
                      <StatusPill ok={report.status === 'ok'} label={report.status} />
                    </div>
                    {report.detail && <div className="mt-2 text-xs text-text-soft">{report.detail}</div>}
                  </div>
                ))}
              </div>
            ) : (
              <div className="text-sm text-text-soft">No registry entries to check.</div>
            )}
          </section>

          <section className="space-y-4">
            <section className="border border-border bg-panel p-4">
              <div className="mb-3 flex items-center gap-2 text-sm text-text">
                <FileWarning className="h-4 w-4" />
                Validation
              </div>
              <ValidationSummary validation={validationData ?? undefined} />
            </section>

            <section className="border border-border bg-panel p-4">
              <div className="mb-3 flex items-center gap-2 text-sm text-text">
                <GitBranch className="h-4 w-4" />
                Resolve Preview
              </div>
              <ResolveSummary resolve={resolveData ?? undefined} />
            </section>
          </section>
        </div>

        {validationData && <ValidationDetails validation={validationData} />}

        {registry.isLoading && entries.length === 0 ? (
          <div className="text-sm text-text-soft">Loading registry...</div>
        ) : entries.length === 0 ? (
          <EmptyState variant="no-results" title="No registry entries." description="Register an app-owned `.cerberus.yaml` file to add it to Cerberus's local registry." />
        ) : (
          <div className="overflow-x-auto border border-border bg-panel">
            <table className="w-full min-w-full">
              <thead className="text-[10px] uppercase tracking-[.28em] text-text-subtle">
                <tr className="border-b border-border-strong">
                  <th className="px-3 py-2 text-left font-medium">Owner</th>
                  <th className="px-3 py-2 text-left font-medium">Project</th>
                  <th className="px-3 py-2 text-left font-medium">Health</th>
                  <th className="px-3 py-2 text-left font-medium">Registered</th>
                  <th className="px-3 py-2 text-left font-medium">Source</th>
                  <th className="px-3 py-2 text-left font-medium">Identity</th>
                  <th className="px-3 py-2 text-left font-medium"></th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border-soft text-sm">
                {entries.map((entry) => (
                  <tr key={entry.owner}>
                    <td className="px-3 py-2">
                      <div className="text-text">{entry.owner}</div>
                      <div className="text-xs text-text-soft">{entry.namespace}</div>
                    </td>
                    <td className="px-3 py-2">
                      <div className="text-text">{entry.project_name || entry.project_id || '-'}</div>
                      <div className="text-xs text-text-soft">{entry.resource_count} resources, {entry.pipeline_count} pipelines</div>
                    </td>
                    <td className="px-3 py-2">
                      <StatusPill ok={entry.health_status === 'ok'} label={entry.health_status || 'unknown'} />
                      <div className="mt-1 max-w-xs text-xs text-text-soft">{entry.health_detail || '-'}</div>
                    </td>
                    <td className="px-3 py-2 text-xs text-text-soft">
                      <div>{entry.registered_at ? new Date(entry.registered_at).toLocaleString() : '-'}</div>
                      <div className="mt-1">{entry.via ? 'bundle registration' : 'direct registration'}</div>
                    </td>
                    <td className="px-3 py-2 text-xs text-text-soft">
                      <div className="font-mono">{entry.path}</div>
                      {entry.via && <div className="mt-1 font-mono">via {entry.via}</div>}
                    </td>
                    <td className="px-3 py-2 text-xs text-text-soft">
                      <StatusPill ok={!!entry.shared_identity} label={entry.shared_identity ? 'shared' : 'local-only'} />
                      <div className="mt-1 font-mono">{entry.registry_urn || '-'}</div>
                    </td>
                    <td className="px-3 py-2 text-right">
                      <Button variant="outline" size="sm" disabled={!sessionToken || busy !== null} onClick={() => void deregister(entry.owner)}>
                        {busy === entry.owner ? 'Removing...' : 'Deregister'}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}

function ValidationSummary({ validation }: { validation?: { summary: { files: number; valid: number; invalid: number; warnings: number; errors: number } } }) {
  if (!validation) {
    return <div className="text-sm text-text-soft">Loading validation...</div>
  }
  return (
    <dl className="grid grid-cols-2 gap-3 text-sm">
      <Metric label="Files" value={String(validation.summary.files)} />
      <Metric label="Valid" value={String(validation.summary.valid)} />
      <Metric label="Invalid" value={String(validation.summary.invalid)} />
      <Metric label="Warnings" value={String(validation.summary.warnings)} />
      <Metric label="Errors" value={String(validation.summary.errors)} />
    </dl>
  )
}

function ResolveSummary({ resolve }: { resolve?: { projects: number; resources: number; pipelines: number; warnings?: string[]; skipped?: { owner: string }[]; error?: string } }) {
  if (!resolve) {
    return <div className="text-sm text-text-soft">Loading resolve preview...</div>
  }
  return (
    <div className="space-y-3">
      <dl className="grid grid-cols-2 gap-3 text-sm">
        <Metric label="Projects" value={String(resolve.projects)} />
        <Metric label="Resources" value={String(resolve.resources)} />
        <Metric label="Pipelines" value={String(resolve.pipelines)} />
        <Metric label="Skipped" value={String(resolve.skipped?.length ?? 0)} />
      </dl>
      {resolve.error ? (
        <div className="text-sm text-destructive">{resolve.error}</div>
      ) : resolve.warnings && resolve.warnings.length > 0 ? (
        <ul className="space-y-2 text-xs text-text-soft">
          {resolve.warnings.slice(0, 4).map((warning) => (
            <li key={warning} className="border border-border-soft bg-panel-2/40 px-3 py-2">{warning}</li>
          ))}
        </ul>
      ) : (
        <div className="text-sm text-text-soft">No resolve warnings.</div>
      )}
    </div>
  )
}

function ValidationDetails({ validation }: { validation: { global?: ConfigValidationFile; registered: ConfigValidationFile[] } }) {
  const files = useMemo(() => [validation.global, ...validation.registered].filter(Boolean) as ConfigValidationFile[], [validation.global, validation.registered])
  return (
    <section className="border border-border bg-panel p-4">
      <div className="mb-3 text-sm text-text">Validation Details</div>
      <div className="space-y-3">
        {files.map((file) => (
          <div key={`${file.kind}:${file.owner || file.path}`} className="border border-border-soft bg-panel-2/40 p-3">
            <div className="flex items-start justify-between gap-3">
              <div>
                <div className="text-sm text-text">{file.owner || file.kind}</div>
                <div className="font-mono text-xs text-text-soft">{file.path}</div>
              </div>
              <StatusPill ok={file.ok} label={file.ok ? 'valid' : 'invalid'} />
            </div>
            {file.errors && file.errors.length > 0 && (
              <div className="mt-3 space-y-2">
                {file.errors.map((issue) => (
                  <div key={issue} className="text-xs text-destructive">{issue}</div>
                ))}
              </div>
            )}
            {file.warnings && file.warnings.length > 0 && (
              <div className="mt-3 space-y-2">
                {file.warnings.map((issue) => (
                  <div key={issue} className="text-xs text-[var(--color-warning)]">{issue}</div>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </section>
  )
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-muted">{label}</dt>
      <dd className="mt-1 text-text">{value}</dd>
    </div>
  )
}

function StatusPill({ ok, label }: { ok: boolean; label: string }) {
  return (
    <span className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs ${ok ? 'border-emerald-500/30 bg-emerald-500/10 text-[var(--color-status-done)]' : 'border-amber-500/30 bg-amber-500/10 text-[var(--color-warning)]'}`}>
      {ok ? <CheckCircle2 className="h-3 w-3" /> : <AlertTriangle className="h-3 w-3" />}
      {label}
    </span>
  )
}

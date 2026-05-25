import { FileWarning, GitBranch, ShieldAlert } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import {
  Button,
  EmptyState,
  Pill,
  SettingsField,
  SettingsGrid,
  SettingsNotice,
  SettingsPanel,
  SummaryCards,
} from '@hollis-labs/sysop-ui/ui'
import { DataTable, type ColumnDef } from '@hollis-labs/sysop-ui/data'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, type ConfigValidationFile, type RegistryEntry, type RegistryHealthReport } from '../api/client'

const registryColumns: ColumnDef<RegistryEntry>[] = [
  {
    key: 'owner',
    header: 'Owner',
    width: 'fill',
    cell: (entry) => (
      <div className="min-w-0">
        <div className="truncate text-[12px] text-text">{entry.owner}</div>
        <div className="truncate text-[11px] text-text-subtle">{entry.namespace}</div>
      </div>
    ),
    sortValue: (entry) => entry.owner,
  },
  {
    key: 'project',
    header: 'Project',
    width: 'fill',
    cell: (entry) => (
      <div className="min-w-0">
        <div className="truncate text-[12px] text-text">{entry.project_name || entry.project_id || '-'}</div>
        <div className="truncate text-[11px] text-text-soft">
          {entry.resource_count} resources, {entry.pipeline_count} pipelines
        </div>
      </div>
    ),
    sortValue: (entry) => entry.project_name || entry.project_id || '',
  },
  {
    key: 'health',
    header: 'Health',
    cell: (entry) => <Pill tone={entry.health_status === 'ok' ? 'success' : 'warning'}>{entry.health_status || 'unknown'}</Pill>,
    sortValue: (entry) => entry.health_status || '',
  },
  {
    key: 'source',
    header: 'Source',
    width: 'fill',
    cell: (entry) => (
      <div className="min-w-0">
        <div className="truncate font-mono text-[11px] text-text-subtle">{entry.path}</div>
        <div className="truncate text-[11px] text-text-soft">{entry.via ? `via ${entry.via}` : 'direct registration'}</div>
      </div>
    ),
    sortValue: (entry) => entry.path,
  },
  {
    key: 'identity',
    header: 'Identity',
    cell: (entry) => (
      <Pill tone={entry.shared_identity ? 'success' : 'neutral'}>
        {entry.shared_identity ? 'shared' : 'local-only'}
      </Pill>
    ),
    sortValue: (entry) => (entry.shared_identity ? 'shared' : 'local-only'),
  },
  {
    key: 'registered',
    header: 'Registered',
    cell: (entry) => (
      <span className="text-[11px] text-text-soft">
        {entry.registered_at ? new Date(entry.registered_at).toLocaleString() : '-'}
      </span>
    ),
    sortValue: (entry) => entry.registered_at || '',
  },
]

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
    { label: 'Local only', value: registryAudit?.summary.local_only ?? 0, accentColor: 'var(--color-warning)' },
    { label: 'Resolve warnings', value: registryAudit?.resolve_warnings?.length ?? resolveData?.warnings?.length ?? 0, accentColor: 'var(--color-status-blocked)' },
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
      <div className="space-y-4">
        <div className="border-b border-border-strong bg-bg px-4 py-3">
          <div className="flex flex-col gap-2 md:flex-row">
            <input
              value={path}
              onChange={(event) => setPath(event.target.value)}
              placeholder="/absolute/path/to/project.cerberus.yaml"
              className="min-w-0 flex-1 border border-border bg-bg px-3 py-2 text-sm text-text outline-none transition-colors focus:border-border-strong"
            />
            <Button variant="secondary" size="sm" disabled={!sessionToken || busy !== null || path.trim() === ''} onClick={() => void registerConfig()}>
              {busy === 'register' ? 'Registering...' : 'Register'}
            </Button>
          </div>
          <div className="mt-2 text-xs text-text-soft">Cerberus stores only the registry pointer. The app-owned file remains the source of truth.</div>
        </div>

        {error ? <div className="px-4"><SettingsNotice tone="danger" title="Registry operation failed" description={error} /></div> : null}

        <div className="space-y-0">
          <SettingsPanel title="Registry audit" icon={<ShieldAlert className="h-4 w-4" />}>
            {registryAudit ? (
              <div className="space-y-3 px-4 py-3">
                <SettingsGrid>
                  <Field label="Config path" value={registryAudit.config_path || '-'} mono />
                  <Field label="Index path" value={registryAudit.index_path || '-'} mono />
                  <Field label="Config exists" value={registryAudit.config_exists ? 'yes' : 'no'} />
                  <Field label="Index exists" value={registryAudit.index_exists ? 'yes' : 'no'} />
                  <Field label="Healthy entries" value={String(registryAudit.summary.healthy)} />
                  <Field label="Unhealthy entries" value={String(registryAudit.summary.unhealthy)} />
                  <Field label="Shared identities" value={String(registryAudit.summary.shared)} />
                  <Field label="Resolve skips" value={String(registryAudit.summary.resolve_skips)} />
                </SettingsGrid>
                {registryAudit.resolve_warnings?.length ? (
                  <SettingsNotice
                    tone="warning"
                    title="Resolve warnings"
                    description={registryAudit.resolve_warnings.slice(0, 4).join('\n')}
                    className="whitespace-pre-wrap"
                  />
                ) : null}
                {registryAudit.skipped?.length ? <SkippedReports reports={registryAudit.skipped} /> : null}
              </div>
            ) : (
              <div className="px-4 py-3 text-sm text-text-soft">Loading registry audit...</div>
            )}
          </SettingsPanel>

          <SettingsPanel title="Registry health" icon={<ShieldAlert className="h-4 w-4" />}>
            {registryHealth?.reports.length ? (
              <div className="space-y-2 px-4 py-3">
                {registryHealth.reports.map((report) => (
                  <div key={report.owner} className="flex items-start justify-between gap-3 border border-border bg-bg px-3 py-2">
                    <div className="min-w-0">
                      <div className="truncate text-[12px] text-text">{report.owner}</div>
                      <div className="truncate font-mono text-[11px] text-text-subtle">{report.path}</div>
                      {report.detail ? <div className="mt-1 text-[11px] text-text-soft">{report.detail}</div> : null}
                    </div>
                    <Pill tone={report.status === 'ok' ? 'success' : 'warning'}>{report.status}</Pill>
                  </div>
                ))}
              </div>
            ) : (
              <div className="px-4 py-3 text-sm text-text-soft">No registry entries to check.</div>
            )}
          </SettingsPanel>

          <SettingsPanel title="Validation" icon={<FileWarning className="h-4 w-4" />}>
            <ValidationSummary validation={validationData ?? undefined} />
          </SettingsPanel>

          <SettingsPanel title="Resolve preview" icon={<GitBranch className="h-4 w-4" />}>
            <ResolveSummary resolve={resolveData ?? undefined} />
          </SettingsPanel>
        </div>

        {validationData ? <ValidationDetails validation={validationData} /> : null}

        {registry.isLoading && entries.length === 0 ? (
          <div className="text-sm text-text-soft">Loading registry...</div>
        ) : entries.length === 0 ? (
          <div className="px-4 py-4">
            <EmptyState variant="no-results" title="No registry entries." description="Register an app-owned `.cerberus.yaml` file to add it to Cerberus's local registry." />
          </div>
        ) : (
          <div className="space-y-3 px-4">
            <div className="flex flex-wrap gap-2">
              {entries.map((entry) => (
                <Button
                  key={entry.owner}
                  variant="outline"
                  size="sm"
                  disabled={!sessionToken || busy !== null}
                  onClick={() => void deregister(entry.owner)}
                >
                  {busy === entry.owner ? `Removing ${entry.owner}...` : `Deregister ${entry.owner}`}
                </Button>
              ))}
            </div>
            <DataTable items={entries} columns={registryColumns} getRowId={(entry) => entry.owner} />
          </div>
        )}
      </div>
    </div>
  )
}

function ValidationSummary({ validation }: { validation?: { summary: { files: number; valid: number; invalid: number; warnings: number; errors: number } } }) {
  if (!validation) {
    return <div className="px-4 py-3 text-sm text-text-soft">Loading validation...</div>
  }
  return (
    <SettingsGrid className="px-4 py-3">
      <Field label="Files" value={String(validation.summary.files)} />
      <Field label="Valid" value={String(validation.summary.valid)} />
      <Field label="Invalid" value={String(validation.summary.invalid)} />
      <Field label="Warnings" value={String(validation.summary.warnings)} />
      <Field label="Errors" value={String(validation.summary.errors)} />
    </SettingsGrid>
  )
}

function ResolveSummary({ resolve }: { resolve?: { projects: number; resources: number; pipelines: number; warnings?: string[]; skipped?: { owner: string }[]; error?: string } }) {
  if (!resolve) {
    return <div className="px-4 py-3 text-sm text-text-soft">Loading resolve preview...</div>
  }
  return (
    <div className="space-y-3 px-4 py-3">
      <SettingsGrid>
        <Field label="Projects" value={String(resolve.projects)} />
        <Field label="Resources" value={String(resolve.resources)} />
        <Field label="Pipelines" value={String(resolve.pipelines)} />
        <Field label="Skipped" value={String(resolve.skipped?.length ?? 0)} />
      </SettingsGrid>
      {resolve.error ? (
        <SettingsNotice tone="danger" title="Resolve failed" description={resolve.error} />
      ) : resolve.warnings?.length ? (
        <SettingsNotice tone="warning" title="Resolve warnings" description={resolve.warnings.slice(0, 4).join('\n')} className="whitespace-pre-wrap" />
      ) : (
        <div className="text-sm text-text-soft">No resolve warnings.</div>
      )}
    </div>
  )
}

function ValidationDetails({ validation }: { validation: { global?: ConfigValidationFile; registered: ConfigValidationFile[] } }) {
  const files = useMemo(() => [validation.global, ...validation.registered].filter(Boolean) as ConfigValidationFile[], [validation.global, validation.registered])
  return (
    <div className="space-y-0">
      <SettingsPanel title="Validation details" icon={<FileWarning className="h-4 w-4" />}>
        <div className="space-y-2 px-4 py-3">
          {files.map((file) => (
            <div key={`${file.kind}:${file.owner || file.path}`} className="border border-border bg-bg px-3 py-2">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-[12px] text-text">{file.owner || file.kind}</div>
                  <div className="truncate font-mono text-[11px] text-text-subtle">{file.path}</div>
                </div>
                <Pill tone={file.ok ? 'success' : 'warning'}>{file.ok ? 'valid' : 'invalid'}</Pill>
              </div>
              {file.errors?.length ? (
                <div className="mt-2 whitespace-pre-wrap text-[11px] text-status-blocked">{file.errors.join('\n')}</div>
              ) : null}
              {file.warnings?.length ? (
                <div className="mt-2 whitespace-pre-wrap text-[11px] text-status-running">{file.warnings.join('\n')}</div>
              ) : null}
            </div>
          ))}
        </div>
      </SettingsPanel>
    </div>
  )
}

function SkippedReports({ reports }: { reports: RegistryHealthReport[] }) {
  return (
    <div className="space-y-2">
      {reports.map((report) => (
        <div key={`${report.owner}:${report.path}`} className="border border-status-running/20 bg-status-running/10 px-3 py-2 text-[11px] text-text-soft">
          <div className="text-text">{report.owner}</div>
          <div className="font-mono text-text-subtle">{report.path}</div>
          {report.detail ? <div className="mt-1">{report.detail}</div> : null}
        </div>
      ))}
    </div>
  )
}

function Field({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <SettingsField
      label={label}
      valueClassName={mono ? 'font-mono text-[11px] text-text-subtle' : undefined}
    >
      {value}
    </SettingsField>
  )
}

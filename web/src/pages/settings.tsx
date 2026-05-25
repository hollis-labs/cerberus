import { useEffect, useState } from 'react'
import { Button, EmptyState, SummaryCards, usePoll } from '@hollis-labs/sysop-ui'
import { apiClient } from '../api/client'

export function SettingsPage() {
  const settings = usePoll((signal) => apiClient.getSettings(signal), 5000)
  const migratePreview = usePoll((signal) => apiClient.getConfigMigratePreview(signal), 5000)
  const backups = usePoll((signal) => apiClient.listConfigBackups(signal), 5000)
  const [sessionToken, setSessionToken] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    apiClient.getSession().then((session) => {
      if (!cancelled) setSessionToken(session.action_token)
    })
    return () => {
      cancelled = true
    }
  }, [])

  const requestError = settings.error || migratePreview.error || backups.error
  if (requestError) {
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus settings"
        title="Could not load configuration surfaces"
        description={requestError instanceof Error ? requestError.message : String(requestError)}
        action={{ label: 'Retry', onClick: () => {
          void settings.refetch()
          void migratePreview.refetch()
          void backups.refetch()
        } }}
      />
    )
  }

  const snapshot = settings.data
  const preview = migratePreview.data
  const backupList = backups.data?.backups ?? []

  const cards = [
    { label: 'Resolved resources', value: snapshot?.resolved_resources ?? 0, accentColor: 'var(--color-text)' },
    { label: 'Backups', value: snapshot?.backup_count ?? backupList.length, accentColor: 'var(--color-status-done)' },
    { label: 'Migration targets', value: preview?.project_count ?? 0, accentColor: 'var(--color-warning)' },
    { label: 'Validation blockers', value: preview?.validation_errors?.length ?? 0, accentColor: 'var(--color-status-blocked)' },
  ]

  async function runMigration() {
    if (!sessionToken || busy) return
    setBusy('migrate')
    setError(null)
    try {
      const response = await apiClient.runConfigMigrate(sessionToken)
      if (!response.success) {
        setError(response.error || 'Migration failed.')
      } else {
        setResult([
          `Wrote ${response.written_paths?.length ?? 0} project config(s).`,
          response.backup_path ? `Backup: ${response.backup_path}` : '',
          response.projects_dir ? `Projects dir: ${response.projects_dir}` : '',
        ].filter(Boolean).join('\n'))
      }
      await Promise.all([settings.refetch(), migratePreview.refetch(), backups.refetch()])
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  async function restoreBackup(path: string) {
    if (!sessionToken || busy) return
    setBusy(path)
    setError(null)
    try {
      const response = await apiClient.restoreConfigBackup(path, sessionToken)
      if (!response.success) {
        setError(response.error || 'Restore failed.')
      } else {
        setResult([
          response.restored_to ? `Restored to: ${response.restored_to}` : '',
          response.backup_path ? `Source backup: ${response.backup_path}` : '',
          response.pre_restore_path ? `Pre-restore snapshot: ${response.pre_restore_path}` : '',
        ].filter(Boolean).join('\n'))
      }
      await Promise.all([settings.refetch(), migratePreview.refetch(), backups.refetch()])
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
        {result && <div className="border border-border bg-panel-2/40 px-3 py-2 text-sm text-text-soft whitespace-pre-wrap">{result}</div>}

        <div className="grid gap-4 xl:grid-cols-[.85fr_1.15fr]">
          <section className="border border-border bg-panel p-4">
            <div className="mb-3 text-sm text-text">Current settings</div>
            {snapshot ? (
              <dl className="grid grid-cols-2 gap-3 text-sm">
                <Metric label="Config path" value={snapshot.config_path || '-'} mono />
                <Metric label="Registry path" value={snapshot.registry_path || '-'} mono />
                <Metric label="Config exists" value={snapshot.config_exists ? 'yes' : 'no'} />
                <Metric label="Registry exists" value={snapshot.registry_exists ? 'yes' : 'no'} />
                <Metric label="Version" value={String(snapshot.version || 0)} />
                <Metric label="Install after build" value={snapshot.install_after_build_default ? 'true' : 'false'} />
                <Metric label="Global build config" value={snapshot.has_global_build_config ? 'present' : 'defaulted'} />
                <Metric label="Resolved projects" value={String(snapshot.resolved_projects)} />
              </dl>
            ) : (
              <div className="text-sm text-text-soft">Loading settings...</div>
            )}
          </section>

          <section className="border border-border bg-panel p-4">
            <div className="mb-3 flex items-center justify-between gap-3">
              <div className="text-sm text-text">Migration preview</div>
              <Button
                variant="secondary"
                size="sm"
                disabled={!sessionToken || busy !== null || !preview || !!preview.error || (preview.validation_errors?.length ?? 0) > 0}
                onClick={() => void runMigration()}
              >
                {busy === 'migrate' ? 'Migrating...' : 'Run migration'}
              </Button>
            </div>
            {!preview ? (
              <div className="text-sm text-text-soft">Loading migration preview...</div>
            ) : preview.error ? (
              <div className="text-sm text-text-soft">{preview.error}</div>
            ) : (
              <div className="space-y-3">
                <dl className="grid grid-cols-2 gap-3 text-sm">
                  <Metric label="Projects dir" value={preview.projects_dir || '-'} mono />
                  <Metric label="Backup path" value={preview.backup_path || '-'} mono />
                  <Metric label="Project configs" value={String(preview.project_count)} />
                  <Metric label="Resources" value={String(preview.total_resources)} />
                </dl>
                {preview.validation_errors && preview.validation_errors.length > 0 && (
                  <div className="space-y-2">
                    {preview.validation_errors.map((issue) => (
                      <div key={`${issue.owner}:${issue.field}:${issue.message}`} className="text-xs text-destructive">
                        {issue.owner}: {issue.field} - {issue.message}
                      </div>
                    ))}
                  </div>
                )}
                {preview.warnings && preview.warnings.length > 0 && (
                  <div className="space-y-2">
                    {preview.warnings.slice(0, 6).map((warning) => (
                      <div key={warning} className="text-xs text-[var(--color-warning)]">{warning}</div>
                    ))}
                  </div>
                )}
                <div className="overflow-x-auto border border-border-soft bg-panel-2/40">
                  <table className="w-full min-w-full text-sm">
                    <thead className="text-[10px] uppercase tracking-[.28em] text-text-subtle">
                      <tr className="border-b border-border-strong">
                        <th className="px-3 py-2 text-left font-medium">Owner</th>
                        <th className="px-3 py-2 text-left font-medium">Project</th>
                        <th className="px-3 py-2 text-left font-medium">Counts</th>
                        <th className="px-3 py-2 text-left font-medium">Destination</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-border-soft">
                      {preview.entries.map((entry) => (
                        <tr key={entry.owner}>
                          <td className="px-3 py-2 text-text">{entry.owner}</td>
                          <td className="px-3 py-2 text-text-soft">{entry.project_name || entry.project_id}</td>
                          <td className="px-3 py-2 text-text-soft">{entry.resource_count} resources, {entry.pipeline_count} pipelines</td>
                          <td className="px-3 py-2 font-mono text-xs text-text-soft">{entry.destination}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            )}
          </section>
        </div>

        <section className="border border-border bg-panel p-4">
          <div className="mb-3 text-sm text-text">Backups</div>
          {backupList.length === 0 ? (
            <div className="text-sm text-text-soft">No backups discovered for the active config path.</div>
          ) : (
            <div className="space-y-3">
              {backupList.map((backup) => (
                <div key={backup.path} className="flex flex-wrap items-start justify-between gap-3 border border-border-soft bg-panel-2/40 p-3">
                  <div>
                    <div className="text-sm text-text">{backup.name}</div>
                    <div className="mt-1 font-mono text-xs text-text-soft">{backup.path}</div>
                    <div className="mt-1 text-xs text-text-soft">{new Date(backup.modified).toLocaleString()} · {formatBytes(backup.size)}</div>
                  </div>
                  <Button variant="outline" size="sm" disabled={!sessionToken || busy !== null} onClick={() => void restoreBackup(backup.path)}>
                    {busy === backup.path ? 'Restoring...' : 'Restore'}
                  </Button>
                </div>
              ))}
            </div>
          )}
        </section>
      </div>
    </div>
  )
}

function Metric({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <div className="text-xs uppercase tracking-wide text-muted">{label}</div>
      <div className={`mt-1 break-words ${mono ? 'font-mono text-xs text-text-soft' : 'text-text'}`}>{value}</div>
    </div>
  )
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

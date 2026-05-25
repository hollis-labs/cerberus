import { Database, FolderTree, RefreshCw, Settings2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import {
  Button,
  EmptyState,
  SettingsField,
  SettingsGrid,
  SettingsNotice,
  SettingsPanel,
  SummaryCards,
} from '@hollis-labs/sysop-ui/ui'
import { DataTable, type ColumnDef } from '@hollis-labs/sysop-ui/data'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, type ConfigBackupInfo, type ConfigMigrateEntry } from '../api/client'

const previewColumns: ColumnDef<ConfigMigrateEntry>[] = [
  {
    key: 'owner',
    header: 'Owner',
    cell: (entry) => entry.owner,
    sortValue: (entry) => entry.owner,
  },
  {
    key: 'project',
    header: 'Project',
    cell: (entry) => entry.project_name || entry.project_id || '—',
    sortValue: (entry) => entry.project_name || entry.project_id || '',
  },
  {
    key: 'counts',
    header: 'Counts',
    width: 'fill',
    cell: (entry) => (
      <span className="text-[11px] text-text-soft">
        {entry.resource_count} resources, {entry.pipeline_count} pipelines
      </span>
    ),
    sortValue: (entry) => `${entry.resource_count}:${entry.pipeline_count}`,
  },
  {
    key: 'destination',
    header: 'Destination',
    width: 'fill',
    cell: (entry) => (
      <span className="block truncate font-mono text-[11px] text-text-subtle">{entry.destination}</span>
    ),
    sortValue: (entry) => entry.destination,
  },
]

const backupColumns: ColumnDef<ConfigBackupInfo>[] = [
  {
    key: 'name',
    header: 'Backup',
    width: 'fill',
    cell: (backup) => (
      <div className="min-w-0">
        <div className="truncate text-[12px] text-text">{backup.name}</div>
        <div className="truncate font-mono text-[11px] text-text-subtle">{backup.path}</div>
      </div>
    ),
    sortValue: (backup) => backup.name,
  },
  {
    key: 'modified',
    header: 'Modified',
    cell: (backup) => new Date(backup.modified).toLocaleString(),
    sortValue: (backup) => backup.modified,
  },
  {
    key: 'size',
    header: 'Size',
    align: 'right',
    cell: (backup) => formatBytes(backup.size),
    sortValue: (backup) => backup.size,
  },
]

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
        action={{
          label: 'Retry',
          onClick: () => {
            void settings.refetch()
            void migratePreview.refetch()
            void backups.refetch()
          },
        }}
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
        setResult(
          [
            `Wrote ${response.written_paths?.length ?? 0} project config(s).`,
            response.backup_path ? `Backup: ${response.backup_path}` : '',
            response.projects_dir ? `Projects dir: ${response.projects_dir}` : '',
          ]
            .filter(Boolean)
            .join('\n'),
        )
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
        setResult(
          [
            response.restored_to ? `Restored to: ${response.restored_to}` : '',
            response.backup_path ? `Source backup: ${response.backup_path}` : '',
            response.pre_restore_path ? `Pre-restore snapshot: ${response.pre_restore_path}` : '',
          ]
            .filter(Boolean)
            .join('\n'),
        )
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
      <div className="space-y-4">
        {error ? <div className="px-4 pt-4"><SettingsNotice tone="danger" title="Operation failed" description={error} /></div> : null}
        {result ? <div className="px-4"><SettingsNotice tone="info" title="Last result" description={result} className="whitespace-pre-wrap" /></div> : null}

        <div className="space-y-0">
          <SettingsPanel title="Current settings" icon={<Settings2 className="h-4 w-4" />}>
            {snapshot ? (
              <SettingsGrid>
                <Field label="Config path" value={snapshot.config_path || '-'} mono />
                <Field label="Registry path" value={snapshot.registry_path || '-'} mono />
                <Field label="Config exists" value={snapshot.config_exists ? 'yes' : 'no'} />
                <Field label="Registry exists" value={snapshot.registry_exists ? 'yes' : 'no'} />
                <Field label="Version" value={String(snapshot.version || 0)} />
                <Field label="Install after build" value={snapshot.install_after_build_default ? 'true' : 'false'} />
                <Field label="Global build config" value={snapshot.has_global_build_config ? 'present' : 'defaulted'} />
                <Field label="Resolved projects" value={String(snapshot.resolved_projects)} />
              </SettingsGrid>
            ) : (
              <div className="px-4 py-3 text-sm text-text-soft">Loading settings...</div>
            )}
          </SettingsPanel>

          <SettingsPanel title="Migration preview" icon={<FolderTree className="h-4 w-4" />}>
            {!preview ? (
              <div className="px-4 py-3 text-sm text-text-soft">Loading migration preview...</div>
            ) : preview.error ? (
              <div className="px-4 py-3 text-sm text-text-soft">{preview.error}</div>
            ) : (
              <div className="space-y-3 px-4 py-3">
                <div className="flex items-center justify-between gap-3">
                  <div className="text-sm text-text-soft">Review destination paths before writing split project configs.</div>
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={!sessionToken || busy !== null || !!preview.error || (preview.validation_errors?.length ?? 0) > 0}
                    onClick={() => void runMigration()}
                  >
                    {busy === 'migrate' ? 'Migrating...' : 'Run migration'}
                  </Button>
                </div>
                <SettingsGrid>
                  <Field label="Projects dir" value={preview.projects_dir || '-'} mono />
                  <Field label="Backup path" value={preview.backup_path || '-'} mono />
                  <Field label="Project configs" value={String(preview.project_count)} />
                  <Field label="Resources" value={String(preview.total_resources)} />
                </SettingsGrid>
                {preview.validation_errors?.length ? (
                  <SettingsNotice
                    tone="danger"
                    title="Validation blockers"
                    description={preview.validation_errors.map((issue) => `${issue.owner}: ${issue.field} - ${issue.message}`).join('\n')}
                    className="whitespace-pre-wrap"
                  />
                ) : null}
                {preview.warnings?.length ? (
                  <SettingsNotice
                    tone="warning"
                    title="Migration warnings"
                    description={preview.warnings.slice(0, 6).join('\n')}
                    className="whitespace-pre-wrap"
                  />
                ) : null}
                <DataTable items={preview.entries} columns={previewColumns} getRowId={(entry) => entry.owner} />
              </div>
            )}
          </SettingsPanel>

          <SettingsPanel title="Backups" icon={<Database className="h-4 w-4" />}>
            {backupList.length === 0 ? (
              <div className="px-4 py-3 text-sm text-text-soft">No backups discovered for the active config path.</div>
            ) : (
              <div className="space-y-3 px-4 py-3">
                <DataTable items={backupList} columns={backupColumns} getRowId={(backup) => backup.path} />
                <div className="flex flex-wrap gap-2">
                  {backupList.map((backup) => (
                    <Button
                      key={backup.path}
                      variant="outline"
                      size="sm"
                      disabled={!sessionToken || busy !== null}
                      onClick={() => void restoreBackup(backup.path)}
                    >
                      {busy === backup.path ? `Restoring ${backup.name}...` : `Restore ${backup.name}`}
                    </Button>
                  ))}
                </div>
              </div>
            )}
          </SettingsPanel>
        </div>
      </div>
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

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

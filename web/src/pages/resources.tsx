import type { ReactNode } from 'react'
import { useEffect, useMemo, useState } from 'react'
import { Activity, AlertTriangle, FileText, Hammer, Info, Pause, Play, RefreshCw, RotateCw, Server, Square } from 'lucide-react'
import {
  Button,
  CopyableId,
  DetailDialog,
  DetailSection,
  EmptyState,
  FilterBar,
  SummaryCards,
  Textarea,
  cn,
  refreshPolledData,
  usePoll,
} from '@hollis-labs/sysop-ui'
import { apiClient, type LogLines, type OpResult, type ResourceAction, type ResourceInfo, type ResourceRuntimeStatus } from '../api/client'

type StatusFilter = 'all' | 'running' | 'attention' | 'stopped'

const ACTIONS: { key: ResourceAction; label: string; icon: ReactNode; variant: 'default' | 'secondary' | 'outline' | 'destructive' }[] = [
  { key: 'apply', label: 'Apply', icon: <Play className="h-3.5 w-3.5" />, variant: 'default' },
  { key: 'deploy', label: 'Deploy', icon: <Hammer className="h-3.5 w-3.5" />, variant: 'secondary' },
  { key: 'reload', label: 'Reload', icon: <RotateCw className="h-3.5 w-3.5" />, variant: 'outline' },
  { key: 'stop', label: 'Stop', icon: <Square className="h-3.5 w-3.5" />, variant: 'destructive' },
]

// DIALOG_WIDTH sizes the resource console modal to 80% of the viewport. The
// kit's DialogContent hardcodes `sm:max-w-sm` in its base classes; because
// tailwind-merge treats variant-prefixed classes as a separate scope, an
// unprefixed `max-w-*` can never override it on screens >= 640px. The
// `sm:`-prefixed entry is what actually takes effect — drop it and the modal
// collapses back to ~24rem.
const DIALOG_WIDTH = 'max-w-[80vw] sm:max-w-[80vw]'

export function ResourcesPage() {
  const resources = usePoll<ResourceInfo[]>((signal) => apiClient.listResources(signal), 5000)
  const [sessionToken, setSessionToken] = useState('')
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [projectFilter, setProjectFilter] = useState('')
  const [selectedID, setSelectedID] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    apiClient.getSession().then((session) => {
      if (!cancelled) setSessionToken(session.action_token)
    })
    return () => {
      cancelled = true
    }
  }, [])

  const items = resources.data ?? []
  const projects = useMemo(() => Array.from(new Set(items.map((item) => item.project).filter(Boolean))).sort(), [items])
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    const matched = items.filter((item) => {
      const status = (item.status || '').toLowerCase()
      const matchesQuery =
        q.length === 0 ||
        [item.id, item.name, item.project, item.connector, item.mode, item.supervisor, item.url, ...(item.tags ?? [])]
          .filter(Boolean)
          .some((value) => String(value).toLowerCase().includes(q))
      const matchesStatus =
        statusFilter === 'all' ||
        (statusFilter === 'running' && status === 'running') ||
        (statusFilter === 'stopped' && (status === 'stopped' || item.operator_stopped)) ||
        (statusFilter === 'attention' && (item.artifact_stale || item.recommended_action || ['failed', 'error', 'degraded'].includes(status)))
      const matchesProject = !projectFilter || item.project === projectFilter
      return matchesQuery && matchesStatus && matchesProject
    })
    // Alphabetical by display name (falls back to id) so the table order is
    // stable across polls regardless of the API's response ordering.
    return matched.sort((a, b) =>
      (a.name || a.id).localeCompare(b.name || b.id, undefined, { sensitivity: 'base' }),
    )
  }, [items, projectFilter, query, statusFilter])

  const summaryCards = useMemo(() => {
    const running = items.filter((item) => item.status === 'running').length
    const attention = items.filter((item) => item.artifact_stale || item.recommended_action).length
    const stopped = items.filter((item) => item.status === 'stopped' || item.operator_stopped).length
    return [
      { label: 'Resources', value: items.length, subtitle: `${filtered.length} shown`, accentColor: 'var(--color-text)' },
      { label: 'Running', value: running, accentColor: 'var(--color-status-done)' },
      { label: 'Attention', value: attention, accentColor: 'var(--color-status-blocked)' },
      { label: 'Stopped', value: stopped, accentColor: 'var(--color-muted)' },
    ]
  }, [filtered.length, items])

  if (resources.error) {
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus daemon"
        title="Could not load resources"
        description={resources.error instanceof Error ? resources.error.message : String(resources.error)}
        action={{ label: 'Retry', onClick: resources.refetch }}
        command="cerberus daemon status"
      />
    )
  }

  return (
    <div className="flex h-full min-h-0 w-full flex-col overflow-hidden">
      <SummaryCards cards={summaryCards} />
      <FilterBar
        searchQuery={query}
        onSearchChange={setQuery}
        searchPlaceholder="Search resources"
        activeFilterCount={(statusFilter !== 'all' ? 1 : 0) + (projectFilter ? 1 : 0)}
        summary={`${filtered.length} matches`}
        onClear={() => {
          setQuery('')
          setStatusFilter('all')
          setProjectFilter('')
        }}
      >
        <SegmentedStatus value={statusFilter} onChange={setStatusFilter} />
        <ProjectFilter projects={projects} value={projectFilter} onChange={setProjectFilter} />
        <Button variant="outline" size="sm" onClick={resources.refetch}>
          <RefreshCw className="h-3.5 w-3.5" />
          Refresh
        </Button>
      </FilterBar>
      <div className="min-h-0 flex-1 overflow-auto">
        {resources.isLoading && items.length === 0 ? (
          <div className="p-4 text-sm text-text-soft">Loading resources...</div>
        ) : filtered.length === 0 ? (
          <div className="p-4">
            <EmptyState
              variant="no-results"
              title="No resources matched."
              description="Try clearing filters or broadening your search."
              action={{ label: 'Clear filters', onClick: () => {
                setQuery('')
                setStatusFilter('all')
                setProjectFilter('')
              } }}
            />
          </div>
        ) : (
          <ResourceTable
            items={filtered}
            token={sessionToken}
            onOpen={setSelectedID}
            onActionDone={() => {
              void resources.refetch()
              refreshPolledData()
            }}
          />
        )}
      </div>
      <ResourceDetailDialog
        resourceID={selectedID}
        actionToken={sessionToken}
        onClose={() => setSelectedID(null)}
        onChanged={() => {
          void resources.refetch()
          refreshPolledData()
        }}
      />
    </div>
  )
}

function ResourceTable({
  items,
  token,
  onOpen,
  onActionDone,
}: {
  items: ResourceInfo[]
  token: string
  onOpen: (id: string) => void
  onActionDone: () => void
}) {
  // busy holds the single in-flight quick action (one at a time, across all
  // rows) so every action button can disable while one is running.
  const [busy, setBusy] = useState<{ id: string; action: ResourceAction } | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  async function runQuickAction(item: ResourceInfo, action: ResourceAction) {
    if (!token || busy) return
    setBusy({ id: item.id, action })
    setActionError(null)
    try {
      const result = await apiClient.runResourceAction(item.id, action, token)
      if (!result.success) {
        setActionError(result.error || `${action} failed for ${item.name || item.id}`)
      }
      onActionDone()
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  return (
    <>
      {actionError && (
        <div className="m-3 flex items-start justify-between gap-3 border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          <span className="whitespace-pre-wrap break-words">{actionError}</span>
          <button
            type="button"
            className="shrink-0 uppercase tracking-wider opacity-70 hover:opacity-100"
            onClick={() => setActionError(null)}
          >
            Dismiss
          </button>
        </div>
      )}
      <div className="w-full overflow-x-auto">
        <table className="w-full min-w-full">
          <thead className="text-[10px] uppercase tracking-[.28em] text-text-subtle">
            <tr className="border-b border-border-strong">
              <th className="px-3 py-1.5 text-left font-medium">Resource</th>
              <th className="w-px whitespace-nowrap px-1.5 py-1.5 text-left font-medium">Status</th>
              <th className="w-px whitespace-nowrap px-3 py-1.5">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border-soft text-[13px] leading-4">
            {items.map((item) => {
              // A resource is "running" only when live and not operator-paused;
              // the start/stop control toggles on this.
              const running =
                !item.operator_stopped && ['running', 'healthy'].includes((item.status || '').toLowerCase())
              const rowBusy = busy?.id === item.id
              const anyBusy = busy !== null
              return (
                <tr
                  key={item.id}
                  className="cursor-pointer bg-bg outline-none hover:bg-panel-hover/60 focus-visible:ring-1 focus-visible:ring-ring"
                  tabIndex={0}
                  role="button"
                  aria-label={`Open ${item.name || item.id}`}
                  onClick={() => onOpen(item.id)}
                  onKeyDown={(event) => {
                    if (event.target !== event.currentTarget) return
                    if (event.key === 'Enter' || event.key === ' ') {
                      event.preventDefault()
                      onOpen(item.id)
                    }
                  }}
                >
                  <td className="w-full max-w-0 px-3 py-1.5 text-left align-top">
                    <div className="min-w-0">
                      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
                        <span className="truncate tracking-[.02em] text-text" title={item.name || item.id}>
                          {item.name || item.id}
                        </span>
                        <DriftChip item={item} />
                        <RuntimeChips item={item} />
                      </div>
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                        {item.project && (
                          <span className="font-mono text-[10px] uppercase tracking-[.12em] text-text-subtle/80">
                            project: <span className="text-text-soft">{item.project}</span>
                          </span>
                        )}
                        <span className="font-mono text-[10px] text-text-subtle/80">id:</span>
                        <CopyableId id={item.id} label={shortID(item.id)} />
                        {item.url && (
                          <>
                            <span className="font-mono text-[10px] text-text-subtle/80">url:</span>
                            <a
                              href={item.url}
                              target="_blank"
                              rel="noopener noreferrer"
                              onClick={(event) => event.stopPropagation()}
                              className="truncate font-mono text-[10px] text-text-soft underline-offset-2 hover:text-text hover:underline"
                              title={`Open ${item.url} in a new tab`}
                            >
                              {item.url}
                            </a>
                          </>
                        )}
                      </div>
                    </div>
                  </td>
                  <td className="w-px whitespace-nowrap px-1.5 py-1.5 align-top">
                    <ResourceStatusBadge status={item.operator_stopped ? 'paused' : item.status || 'unknown'} />
                  </td>
                  <td className="w-px whitespace-nowrap px-3 py-1.5 align-top">
                    <div className="flex items-center justify-end gap-1">
                      <IconAction
                        icon={running ? <Square className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />}
                        label={running ? `Stop ${item.name || item.id}` : `Start ${item.name || item.id}`}
                        disabled={!token || anyBusy}
                        busy={rowBusy && (busy?.action === 'stop' || busy?.action === 'apply')}
                        onClick={() => void runQuickAction(item, running ? 'stop' : 'apply')}
                      />
                      <IconAction
                        icon={<RotateCw className="h-3.5 w-3.5" />}
                        label={`Restart ${item.name || item.id}`}
                        disabled={!token || anyBusy}
                        busy={rowBusy && busy?.action === 'reload'}
                        onClick={() => void runQuickAction(item, 'reload')}
                      />
                      <IconAction
                        icon={<Info className="h-3.5 w-3.5" />}
                        label={`Open console for ${item.name || item.id}`}
                        onClick={() => onOpen(item.id)}
                      />
                    </div>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </>
  )
}

// IconAction is a compact square icon button shared by the resource row's
// quick-action cluster (start/stop, restart, open console). While busy it
// swaps its glyph for a spinner and disables itself.
function IconAction({
  icon,
  label,
  onClick,
  disabled = false,
  busy = false,
}: {
  icon: ReactNode
  label: string
  onClick: () => void
  disabled?: boolean
  busy?: boolean
}) {
  const inactive = disabled || busy
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      disabled={inactive}
      className={cn(
        'inline-flex h-6 w-6 items-center justify-center border border-border bg-panel-2/50 text-text-soft transition-colors',
        inactive ? 'cursor-default opacity-35' : 'hover:border-border-strong hover:bg-panel-hover hover:text-text',
      )}
      onClick={(event) => {
        event.stopPropagation()
        if (!inactive) onClick()
      }}
    >
      {busy ? <RefreshCw className="h-3.5 w-3.5 animate-spin" /> : icon}
    </button>
  )
}

function shortID(id: string) {
  return id.length > 14 ? `${id.slice(0, 14)}...` : id
}

// resourceStatusColor maps a resource runtime status to a semantic theme
// color: green for healthy/running, red for failed states, amber for
// transient or paused states, and a muted gray for off/unknown. The kit's
// StatusBadge only knows task-board status keys, so resources need their
// own good/bad classification.
function resourceStatusColor(status: string): string {
  switch (status.toLowerCase()) {
    case 'running':
    case 'healthy':
      return 'var(--color-status-done)'
    case 'failed':
    case 'error':
    case 'unhealthy':
    case 'degraded':
      return 'var(--color-status-blocked)'
    case 'starting':
    case 'building':
      return 'var(--color-warning)'
    case 'paused':
      return 'var(--color-status-paused)'
    default:
      // stopped, unknown, and anything unrecognized — neutral.
      return 'var(--color-text-subtle)'
  }
}

// ResourceStatusBadge mirrors the kit StatusBadge layout (bordered pill with
// a leading dot) but tints itself by resource-runtime semantics.
function ResourceStatusBadge({ status }: { status: string }) {
  const label = status || 'unknown'
  const color = resourceStatusColor(label)
  return (
    <span
      className="inline-flex items-center gap-2 rounded border px-2 py-1 text-[11px] uppercase tracking-[0.14em]"
      style={{
        borderColor: `color-mix(in srgb, ${color} 40%, transparent)`,
        backgroundColor: `color-mix(in srgb, ${color} 12%, transparent)`,
        color,
      }}
    >
      <span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: color }} />
      <span>{label}</span>
    </span>
  )
}

// DriftChip flags a resource that is up but needs operator attention — most
// commonly a running service whose installed artifact has drifted from the
// repo. The status badge alone only reports process liveness, so a
// stale-but-running resource would otherwise look healthy at a glance.
function DriftChip({ item }: { item: ResourceInfo }) {
  if (!item.artifact_stale && !item.recommended_action) return null
  const label = item.artifact_stale ? 'drift' : 'attention'
  const tip =
    item.recommended_next_step ||
    (item.artifact_stale
      ? 'Installed artifact has drifted from the current repo state.'
      : 'This resource needs operator attention.')
  return (
    <span
      className="inline-flex items-center gap-1 border px-1.5 py-0.5 text-[9px] uppercase leading-none tracking-[.14em]"
      style={{
        borderColor: 'color-mix(in srgb, var(--color-status-blocked) 45%, transparent)',
        backgroundColor: 'color-mix(in srgb, var(--color-status-blocked) 14%, transparent)',
        color: 'var(--color-status-blocked)',
      }}
      title={tip}
    >
      <AlertTriangle className="h-2.5 w-2.5" />
      {label}
    </span>
  )
}

function RuntimeChips({ item }: { item: ResourceInfo }) {
  const chips = [item.connector, item.mode, item.supervisor, item.run_from].filter(Boolean)
  if (chips.length === 0) return null
  return (
    <span className="inline-flex min-w-0 flex-wrap items-center gap-1">
      {chips.map((value) => (
        <span
          key={value}
          className="inline-flex h-4 items-center border border-border bg-panel-2/50 px-1.5 text-[10px] uppercase leading-none tracking-[.12em] text-text-soft"
        >
          {value}
        </span>
      ))}
    </span>
  )
}

function SegmentedStatus({ value, onChange }: { value: StatusFilter; onChange: (value: StatusFilter) => void }) {
  const options: { value: StatusFilter; label: string }[] = [
    { value: 'all', label: 'All' },
    { value: 'running', label: 'Running' },
    { value: 'attention', label: 'Attention' },
    { value: 'stopped', label: 'Stopped' },
  ]
  return (
    <div className="flex flex-wrap items-center gap-1">
      <span className="mr-1 text-[10px] uppercase tracking-wider text-text-subtle">Status:</span>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          onClick={() => onChange(option.value)}
          className={cn(
            'border px-2 py-0.5 text-[10px] uppercase tracking-wider transition-all',
            value === option.value
              ? 'border-border-strong bg-panel-hover text-text ring-1 ring-white/15'
              : 'border-border bg-panel-2/50 text-text-subtle opacity-70 hover:text-text-soft hover:opacity-100',
          )}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}

function ProjectFilter({ projects, value, onChange }: { projects: string[]; value: string; onChange: (value: string) => void }) {
  return (
    <label className="flex items-center gap-1 border-l border-border pl-3">
      <span className="mr-1 text-[10px] uppercase tracking-wider text-text-subtle">Project:</span>
      <Server className="h-3.5 w-3.5 text-text-subtle" />
      <select
        className="border border-border bg-panel-2/50 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-text-soft outline-none transition-colors hover:border-border-strong focus:border-border-strong"
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        <option value="">All projects</option>
        {projects.map((project) => (
          <option key={project} value={project}>
            {project}
          </option>
        ))}
      </select>
    </label>
  )
}

function ResourceDetailDialog({
  resourceID,
  actionToken,
  onClose,
  onChanged,
}: {
  resourceID: string | null
  actionToken: string
  onClose: () => void
  onChanged: () => void
}) {
  const [detail, setDetail] = useState<ResourceRuntimeStatus | null>(null)
  const [logs, setLogs] = useState<LogLines | null>(null)
  const [stream, setStream] = useState('stderr')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [opResult, setOpResult] = useState<OpResult | null>(null)
  const [runningAction, setRunningAction] = useState<ResourceAction | null>(null)

  useEffect(() => {
    if (!resourceID) {
      setDetail(null)
      setLogs(null)
      setError(null)
      setOpResult(null)
      return
    }
    let cancelled = false
    setLoading(true)
    setError(null)
    Promise.all([apiClient.getResource(resourceID), apiClient.getLogs(resourceID, stream)])
      .then(([nextDetail, nextLogs]) => {
        if (!cancelled) {
          setDetail(nextDetail)
          setLogs(nextLogs)
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [resourceID, stream])

  async function run(action: ResourceAction) {
    if (!resourceID || !actionToken) return
    setRunningAction(action)
    setError(null)
    try {
      const result = await apiClient.runResourceAction(resourceID, action, actionToken)
      setOpResult(result)
      const [nextDetail, nextLogs] = await Promise.all([apiClient.getResource(resourceID), apiClient.getLogs(resourceID, stream)])
      setDetail(nextDetail)
      setLogs(nextLogs)
      onChanged()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunningAction(null)
    }
  }

  return (
    <DetailDialog
      open={Boolean(resourceID)}
      onClose={onClose}
      title={detail?.name || resourceID || 'Resource'}
      badge={detail ? <ResourceStatusBadge status={detail.operator_stopped ? 'paused' : detail.status} /> : undefined}
      meta={detail ? <CopyableId id={detail.id} /> : undefined}
      widthClassName={DIALOG_WIDTH}
      footer={
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="text-xs text-muted">{detail?.recommended_next_step || detail?.recommended_reason || detail?.recommended_action}</div>
          <div className="flex flex-wrap gap-2">
            {ACTIONS.map((action) => (
              <Button
                key={action.key}
                variant={action.variant}
                size="sm"
                disabled={!actionToken || runningAction !== null}
                onClick={() => void run(action.key)}
              >
                {runningAction === action.key ? <Pause className="h-3.5 w-3.5" /> : action.icon}
                {action.label}
              </Button>
            ))}
          </div>
        </div>
      }
    >
      {loading && <div className="p-3 text-sm text-muted">Loading...</div>}
      {error && <div className="border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">{error}</div>}
      {detail && (
        <div className="grid gap-4">
          <DetailSection title="Runtime">
            <dl className="grid grid-cols-2 gap-3 text-sm md:grid-cols-4">
              <Meta label="Project" value={detail.project} />
              <Meta label="Connector" value={detail.connector} />
              <Meta label="Mode" value={detail.mode} />
              <Meta label="Supervisor" value={detail.supervisor} />
              <Meta label="Run from" value={detail.run_from} />
              <Meta label="Port" value={detail.port ? String(detail.port) : ''} />
              <Meta label="Service" value={detail.service_name} />
              <Meta label="URL" value={detail.url} />
            </dl>
          </DetailSection>
          <DetailSection title="Artifact">
            <dl className="grid grid-cols-1 gap-3 text-sm md:grid-cols-2">
              <Meta label="Installed" value={String(Boolean(detail.artifact_installed))} />
              <Meta label="Stale" value={String(Boolean(detail.artifact_stale))} />
              <Meta label="Source" value={detail.artifact_source} />
              <Meta label="Synced" value={detail.artifact_synced_at} />
              <Meta label="Path" value={detail.artifact_path} />
              <Meta label="Reason" value={detail.artifact_stale_reason} />
            </dl>
          </DetailSection>
          {(detail.launchd_state || detail.launchd_diagnosis || detail.launchd_highlights?.length) && (
            <DetailSection title="Supervisor">
              <dl className="grid grid-cols-1 gap-3 text-sm md:grid-cols-2">
                <Meta label="Loaded" value={String(Boolean(detail.launchd_loaded))} />
                <Meta label="State" value={detail.launchd_state} />
                <Meta label="PID" value={detail.launchd_pid ? String(detail.launchd_pid) : ''} />
                <Meta label="Reason" value={detail.launchd_reason} />
                <Meta label="Diagnosis" value={detail.launchd_diagnosis} />
                <Meta label="Highlights" value={detail.launchd_highlights?.join('\n')} />
              </dl>
            </DetailSection>
          )}
          <DetailSection title="Logs">
            <div className="mb-2 flex items-center gap-2">
              {['stderr', 'stdout'].map((name) => (
                <Button key={name} variant={stream === name ? 'default' : 'outline'} size="xs" onClick={() => setStream(name)}>
                  <FileText className="h-3 w-3" />
                  {name}
                </Button>
              ))}
              <Button variant="outline" size="xs" onClick={() => resourceID && void apiClient.getLogs(resourceID, stream).then(setLogs)}>
                <Activity className="h-3 w-3" />
                Tail
              </Button>
            </div>
            <Textarea readOnly value={logs?.content || ''} className="min-h-64 resize-none font-mono text-xs" />
            {logs?.log_path && <div className="mt-2 text-xs text-muted">{logs.log_path}</div>}
          </DetailSection>
          {opResult && (
            <DetailSection title="Last operation">
              <Textarea
                readOnly
                value={[opResult.message, opResult.error, opResult.build_output, opResult.install_output].filter(Boolean).join('\n\n')}
                className="min-h-28 resize-none font-mono text-xs"
              />
            </DetailSection>
          )}
        </div>
      )}
    </DetailDialog>
  )
}

function Meta({ label, value }: { label: string; value?: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs uppercase tracking-wide text-muted">{label}</dt>
      <dd className="mt-1 whitespace-pre-wrap break-words text-text">
        {value ? value : <span className="text-muted">-</span>}
      </dd>
    </div>
  )
}

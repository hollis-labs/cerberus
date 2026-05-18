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
  StatusBadge,
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
    return items.filter((item) => {
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
          <ResourceTable items={filtered} onOpen={setSelectedID} />
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

function ResourceTable({ items, onOpen }: { items: ResourceInfo[]; onOpen: (id: string) => void }) {
  const [nextResource, setNextResource] = useState<ResourceInfo | null>(null)

  return (
    <>
      <div className="w-full overflow-x-auto">
        <table className="w-full min-w-full">
          <thead className="text-[10px] uppercase tracking-[.28em] text-text-subtle">
            <tr className="border-b border-border-strong">
              <th className="px-3 py-1.5 text-left font-medium">Resource</th>
              <th className="w-px whitespace-nowrap px-1.5 py-1.5 text-left font-medium">Status</th>
              <th className="w-px whitespace-nowrap px-1.5 py-1.5 text-left font-medium">Project</th>
              <th className="w-px whitespace-nowrap px-3 py-1.5 text-center font-medium">Next</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border-soft text-[13px] leading-4">
            {items.map((item) => {
              const hasNext = Boolean(item.recommended_next_step || item.recommended_action)
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
                        <RuntimeChips item={item} />
                      </div>
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                        <span className="font-mono text-[10px] text-text-subtle/80">id:</span>
                        <CopyableId id={item.id} label={shortID(item.id)} />
                        {item.url && (
                          <>
                            <span className="font-mono text-[10px] text-text-subtle/80">url:</span>
                            <span className="truncate font-mono text-[10px] text-text-soft" title={item.url}>
                              {item.url}
                            </span>
                          </>
                        )}
                      </div>
                    </div>
                  </td>
                  <td className="w-px whitespace-nowrap px-1.5 py-1.5 align-top">
                    <div className="flex flex-col items-start gap-1">
                      <StatusBadge status={item.operator_stopped ? 'paused' : item.status || 'unknown'} />
                      <DriftChip item={item} />
                    </div>
                  </td>
                  <td className="w-px whitespace-nowrap px-1.5 py-1.5 align-top">
                    <span className="text-[11px] uppercase tracking-[.12em] text-text-soft">
                      {item.project || '-'}
                    </span>
                  </td>
                  <td className="w-px whitespace-nowrap px-3 py-1.5 text-center align-top">
                    <button
                      type="button"
                      disabled={!hasNext}
                      title={hasNext ? 'View recommended next step' : 'No recommendation'}
                      aria-label={hasNext ? `View recommended next step for ${item.name || item.id}` : `No recommendation for ${item.name || item.id}`}
                      className={cn(
                        'inline-flex h-6 w-6 items-center justify-center border border-border bg-panel-2/50 text-text-soft transition-colors',
                        hasNext ? 'hover:border-border-strong hover:bg-panel-hover hover:text-text' : 'cursor-default opacity-35',
                      )}
                      onClick={(event) => {
                        event.stopPropagation()
                        if (hasNext) setNextResource(item)
                      }}
                    >
                      <Info className="h-3.5 w-3.5" />
                    </button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <NextStepDialog resource={nextResource} onClose={() => setNextResource(null)} />
    </>
  )
}

function NextStepDialog({ resource, onClose }: { resource: ResourceInfo | null; onClose: () => void }) {
  return (
    <DetailDialog
      open={Boolean(resource)}
      onClose={onClose}
      title={resource?.name || resource?.id || 'Recommendation'}
      badge={resource ? <StatusBadge status={resource.operator_stopped ? 'paused' : resource.status || 'unknown'} /> : undefined}
      meta={resource ? <CopyableId id={resource.id} /> : undefined}
      widthClassName="max-w-xl"
    >
      {resource && (
        <div className="grid gap-4">
          <DetailSection title="Recommended action">
            <p className="whitespace-pre-wrap text-sm leading-6 text-text">
              {resource.recommended_action || 'No action reported.'}
            </p>
          </DetailSection>
          <DetailSection title="Next step">
            <p className="whitespace-pre-wrap text-sm leading-6 text-text-soft">
              {resource.recommended_next_step || 'No next step reported.'}
            </p>
          </DetailSection>
        </div>
      )}
    </DetailDialog>
  )
}

function shortID(id: string) {
  return id.length > 14 ? `${id.slice(0, 14)}...` : id
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
      badge={detail ? <StatusBadge status={detail.operator_stopped ? 'paused' : detail.status} /> : undefined}
      meta={detail ? <CopyableId id={detail.id} /> : undefined}
      widthClassName="max-w-5xl"
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

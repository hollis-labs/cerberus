import { useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from 'react'
import { Activity, AlertTriangle, FileText, Hammer, Play, RefreshCw, RotateCw, Server, Square, Trash2, Upload } from 'lucide-react'
import {
  Button,
  Callout,
  CopyableId,
  DetailDialog,
  DetailSection,
  EmptyState,
  Pill,
  SummaryCards,
  Textarea,
} from '@hollis-labs/sysop-ui/ui'
import {
  DataTable,
  FilterBar,
  FilterChipGroup,
  FilterEntityCombobox,
  RowActionMenu,
  type ColumnDef,
  type FilterChip,
} from '@hollis-labs/sysop-ui/data'
import { refreshPolledData, usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, type LogLines, type OpResult, type ResourceAction, type ResourceInfo, type ResourceRuntimeStatus } from '../api/client'
import { ResolveNotice } from '../components/resolve-notice'
import { ActionConfirm, RESOURCE_ACTION_EFFECT, type PendingConfirm } from '../components/action-confirm'
import { useConfirmOnCall } from '../components/plan-confirm'

type StatusFilter = 'all' | 'running' | 'attention' | 'stopped'

const STATUS_CHIPS: readonly FilterChip[] = [
  { value: 'all', label: 'All' },
  { value: 'running', label: 'Running' },
  { value: 'attention', label: 'Attention' },
  { value: 'stopped', label: 'Stopped' },
]

const ACTIONS: { key: ResourceAction; label: string; icon: ReactNode; variant: 'default' | 'secondary' | 'outline' | 'destructive' }[] = [
  { key: 'apply', label: 'Apply', icon: <Play className="h-3.5 w-3.5" />, variant: 'default' },
  { key: 'deploy', label: 'Deploy', icon: <Hammer className="h-3.5 w-3.5" />, variant: 'secondary' },
  { key: 'sync', label: 'Sync', icon: <Upload className="h-3.5 w-3.5" />, variant: 'outline' },
  { key: 'reload', label: 'Reload', icon: <RotateCw className="h-3.5 w-3.5" />, variant: 'outline' },
  { key: 'stop', label: 'Stop', icon: <Square className="h-3.5 w-3.5" />, variant: 'destructive' },
  { key: 'remove', label: 'Remove', icon: <Trash2 className="h-3.5 w-3.5" />, variant: 'destructive' },
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
  const scrollRef = useRef<HTMLDivElement>(null)

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
      <ResolveNotice />
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
        <FilterChipGroup
          label="Status"
          chips={STATUS_CHIPS}
          selected={[statusFilter]}
          onToggle={(value) => setStatusFilter(value as StatusFilter)}
        />
        <FilterEntityCombobox
          icon={<Server className="h-3.5 w-3.5" />}
          items={projects.map((id) => ({ id, name: id }))}
          value={projectFilter || null}
          onChange={(id) => setProjectFilter(id ?? '')}
          allLabel="All projects"
          ariaLabel="Filter by project"
        />
        <Button variant="outline" size="sm" onClick={resources.refetch}>
          <RefreshCw className="h-3.5 w-3.5" />
          Refresh
        </Button>
      </FilterBar>
      <div ref={scrollRef} className="min-h-0 flex-1 overflow-auto">
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
            scrollRootRef={scrollRef}
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
  scrollRootRef,
}: {
  items: ResourceInfo[]
  token: string
  onOpen: (id: string) => void
  onActionDone: () => void
  scrollRootRef?: RefObject<HTMLElement | null>
}) {
  // busy holds the single in-flight quick action (one at a time, across all
  // rows) so the action menu can disable while one is running.
  const [busy, setBusy] = useState<{ id: string; action: ResourceAction } | null>(null)
  const withConfirm = useConfirmOnCall()
  const [actionError, setActionError] = useState<string | null>(null)
  const [pending, setPending] = useState<PendingConfirm | null>(null)
  const anyBusy = busy !== null

  // A quick action asks first; the request is sent, acknowledged, from the
  // confirm step.
  function runQuickAction(item: ResourceInfo, action: ResourceAction) {
    if (!token || busy) return
    setPending({
      verb: action.charAt(0).toUpperCase() + action.slice(1),
      subject: item.name || item.id,
      effect: RESOURCE_ACTION_EFFECT[action],
      run: () => sendQuickAction(item, action),
    })
  }

  async function sendQuickAction(item: ResourceInfo, action: ResourceAction) {
    setBusy({ id: item.id, action })
    setActionError(null)
    try {
      const result = await withConfirm(() => apiClient.runResourceAction(item.id, action, token, true), {
        plan: () => apiClient.planResourceAction(item.id, action, token),
        confirm: (c) => apiClient.confirmResourceAction(item.id, action, token, c),
      })
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

  const columns: ColumnDef<ResourceInfo>[] = [
    {
      key: 'resource',
      header: 'Resource',
      width: 'fill',
      cell: (item) => (
        <ResourceCell
          item={item}
          onMakeCurrent={() => void runQuickAction(item, 'deploy')}
          busy={!token || anyBusy}
        />
      ),
      sortValue: (item) => item.name || item.id,
    },
    {
      key: 'status',
      header: 'Status',
      cell: (item) => <ResourceStatusBadge status={item.operator_stopped ? 'paused' : item.status || 'unknown'} />,
      sortValue: (item) => item.operator_stopped ? 'paused' : item.status || 'unknown',
    },
    {
      key: 'actions',
      header: '',
      cell: (item) => {
        const running = !item.operator_stopped && ['running', 'healthy'].includes((item.status || '').toLowerCase())
        return (
          <RowActionMenu
            ariaLabel={`Actions for ${item.name || item.id}`}
            actions={[
              {
                label: running ? 'Stop' : 'Start',
                icon: running ? <Square className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />,
                onSelect: () => void runQuickAction(item, running ? 'stop' : 'apply'),
                disabled: !token || anyBusy,
              },
              {
                // Make current = deploy (build + sync + activate). Co-located
                // with Restart so it is obvious which one rebuilds: Restart
                // only relaunches the existing (possibly stale) artifact.
                label: 'Make current (deploy)',
                icon: <Hammer className="h-3.5 w-3.5" />,
                onSelect: () => void runQuickAction(item, 'deploy'),
                disabled: !token || anyBusy,
              },
              {
                label: 'Restart (no rebuild)',
                icon: <RotateCw className="h-3.5 w-3.5" />,
                onSelect: () => void runQuickAction(item, 'reload'),
                disabled: !token || anyBusy,
              },
            ]}
          />
        )
      },
    },
  ]

  return (
    <>
      {actionError && (
        <Callout
          tone="danger"
          className="m-3"
          actions={
            <Button variant="outline" size="xs" onClick={() => setActionError(null)}>
              Dismiss
            </Button>
          }
        >
          <span className="whitespace-pre-wrap break-words">{actionError}</span>
        </Callout>
      )}
      <DataTable
        items={items}
        columns={columns}
        getRowId={(item) => item.id}
        onRowOpen={(id) => onOpen(id)}
        rowAriaLabel={(item) => `Open ${item.name || item.id}`}
        scrollRootRef={scrollRootRef}
      />
      <ActionConfirm pending={pending} busy={anyBusy} onClose={() => setPending(null)} />
    </>
  )
}

function ResourceCell({
  item,
  onMakeCurrent,
  busy,
}: {
  item: ResourceInfo
  onMakeCurrent?: () => void
  busy?: boolean
}) {
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
        <span className="truncate tracking-[.02em] text-text" title={item.name || item.id}>
          {item.name || item.id}
        </span>
        <DriftChip item={item} onMakeCurrent={onMakeCurrent} busy={busy} />
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
  )
}

function shortID(id: string) {
  return id.length > 14 ? `${id.slice(0, 14)}...` : id
}

// The kit's StatusBadge is locked to the task-board status vocabulary, so
// resources map their runtime status onto the generic Pill tones instead.
// Candidate for upstream contribution: a "resource" preset on Pill / StatusBadge.
function resourceStatusTone(status: string): 'success' | 'danger' | 'warning' | 'info' | 'neutral' {
  switch (status.toLowerCase()) {
    case 'running':
    case 'healthy':
      return 'success'
    case 'failed':
    case 'error':
    case 'unhealthy':
    case 'degraded':
      return 'danger'
    case 'starting':
    case 'building':
      return 'warning'
    case 'paused':
      return 'info'
    default:
      return 'neutral'
  }
}

function ResourceStatusBadge({ status }: { status: string }) {
  const label = status || 'unknown'
  return (
    <Pill tone={resourceStatusTone(label)} dot className="uppercase tracking-[0.14em]">
      {label}
    </Pill>
  )
}

// DriftChip flags a resource that is up but needs operator attention — most
// commonly a running service whose installed artifact has drifted from the
// repo. The status badge alone only reports process liveness, so a
// stale-but-running resource would otherwise look healthy at a glance.
function DriftChip({
  item,
  onMakeCurrent,
  busy,
}: {
  item: ResourceInfo
  onMakeCurrent?: () => void
  busy?: boolean
}) {
  if (!item.artifact_stale && !item.recommended_action) return null
  const label = item.artifact_stale ? 'drift' : 'attention'
  const tip =
    item.recommended_next_step ||
    (item.artifact_stale
      ? 'Installed artifact has drifted from the current repo state.'
      : 'This resource needs operator attention.')
  const body = (
    <Pill tone="danger" className="uppercase tracking-[.14em]">
      <AlertTriangle className="h-2.5 w-2.5" />
      {label}
    </Pill>
  )
  // When a deploy handler is available, the chip becomes the fix: click it to
  // make the resource current (deploy), so the warning and its remedy live in
  // the same place. Candidate for upstream contribution: an interactive
  // DriftChip widget that owns this affordance.
  if (onMakeCurrent && item.recommended_action !== 'inspect') {
    return (
      <button
        type="button"
        className="cursor-pointer disabled:opacity-60"
        title={`${tip} — click to make current (deploy)`}
        disabled={busy}
        onClick={onMakeCurrent}
      >
        {body}
      </button>
    )
  }
  return <span title={tip}>{body}</span>
}

function RuntimeChips({ item }: { item: ResourceInfo }) {
  const chips = [item.connector, item.mode, item.supervisor, item.run_from].filter(Boolean)
  if (chips.length === 0) return null
  return (
    <span className="inline-flex min-w-0 flex-wrap items-center gap-1">
      {chips.map((value) => (
        <Pill key={value} tone="neutral" className="uppercase tracking-[.12em]">
          {value}
        </Pill>
      ))}
    </span>
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
  const withConfirm = useConfirmOnCall()
  const [pending, setPending] = useState<PendingConfirm | null>(null)

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

  // An action asks first; the request is sent, acknowledged, from the
  // confirm step.
  function confirm(action: ResourceAction) {
    if (!resourceID || !actionToken) return
    setPending({
      verb: action.charAt(0).toUpperCase() + action.slice(1),
      subject: detail?.name || resourceID,
      effect: RESOURCE_ACTION_EFFECT[action],
      run: () => run(action),
    })
  }

  async function run(action: ResourceAction) {
    if (!resourceID || !actionToken) return
    setRunningAction(action)
    setError(null)
    try {
      const result = await withConfirm(() => apiClient.runResourceAction(resourceID, action, actionToken, true), {
        plan: () => apiClient.planResourceAction(resourceID, action, actionToken),
        confirm: (c) => apiClient.confirmResourceAction(resourceID, action, actionToken, c),
      })
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
                onClick={() => confirm(action.key)}
              >
                {runningAction === action.key ? <RefreshCw className="h-3.5 w-3.5 animate-spin" /> : action.icon}
                {action.label}
              </Button>
            ))}
          </div>
        </div>
      }
    >
      {loading && <div className="p-3 text-sm text-muted">Loading...</div>}
      {error && <Callout tone="danger" className="m-3">{error}</Callout>}
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
      <ActionConfirm pending={pending} busy={runningAction !== null} onClose={() => setPending(null)} />
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

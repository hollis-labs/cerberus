import { Activity, Boxes, FolderTree, Gauge, Route, ServerCog, Waypoints } from 'lucide-react'
import {
  Button,
  Callout,
  EmptyState,
  SettingsNotice,
  StatusBadge,
} from '@hollis-labs/sysop-ui/ui'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import {
  BarList,
  CompositionBars,
  IntelligenceRow,
  Kpi,
  KpiGrid,
  MiniTrend,
  Panel,
  SignalBars,
} from '@hollis-labs/sysop-ui/widgets'
import { apiClient } from '../api/client'

function compact(n: number): string {
  return new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

function ratio(part: number, total: number): string {
  if (!total) return '0%'
  return `${Math.round((part / total) * 100)}%`
}

function sum(arr: number[]): number {
  return arr.reduce((a, b) => a + b, 0)
}

// addSeries pairwise-sums two trend arrays. Used to build the Runtime /
// Interaction composite series the big SignalBars chart renders.
function addSeries(a: number[], b: number[]): number[] {
  const n = Math.max(a.length, b.length)
  return Array.from({ length: n }, (_, i) => (a[i] ?? 0) + (b[i] ?? 0))
}

export function OverviewPage() {
  const overview = usePoll((signal) => apiClient.getOverview(signal), 5000)
  const system = usePoll((signal) => apiClient.getSystem(signal), 5000)

  if (overview.error || system.error) {
    const error = overview.error || system.error
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus web"
        title="Could not load overview"
        description={error instanceof Error ? error.message : String(error)}
        action={{
          label: 'Retry',
          onClick: () => {
            void overview.refetch()
            void system.refetch()
          },
        }}
      />
    )
  }

  const ov = overview.data
  const sys = system.data
  if (!ov || !sys) {
    return <div className="border-b border-border-strong px-4 py-3 text-sm text-text-soft">Loading overview...</div>
  }

  const trends = ov.trends
  const resourcesTrend = trends.resources
  const projectsTrend = trends.projects
  const pipelinesTrend = trends.pipelines
  const registryTrend = trends.registry_entries

  // Runtime = running + attention (active service signal).
  // Interaction = projects + pipelines (declarative-surface activity).
  // Stacked together on the big SignalBars chart so the two layers read
  // distinctly. Matches Tether's runtime/interaction split.
  const runtimeSeries = addSeries(trends.running, trends.attention)
  const interactionSeries = addSeries(trends.projects, trends.pipelines)

  const inventoryMix = [
    { label: 'Resources', value: ov.inventory.resources },
    { label: 'Projects', value: ov.inventory.projects },
    { label: 'Pipelines', value: ov.inventory.pipelines },
    { label: 'Connectors', value: ov.inventory.connectors },
    { label: 'Plugins', value: ov.inventory.plugins },
  ]
  const runtimeStates = [
    { label: 'running', value: ov.runtime.running },
    { label: 'attention', value: ov.runtime.attention },
    { label: 'stopped', value: ov.runtime.stopped },
  ]
  const registryStates = [
    { label: 'healthy', value: ov.registry.healthy },
    { label: 'unhealthy', value: ov.registry.unhealthy },
  ]

  const daemonStatus = ov.daemon.running ? 'done' : 'blocked'
  const registryStatus = ov.registry.unhealthy > 0 ? 'blocked' : 'done'
  const warnings = sys.resolve_warnings ?? []
  const trackedStates = ov.runtime.running + ov.runtime.attention + ov.runtime.stopped

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-bg">
      <div className="flex shrink-0 items-center justify-between border-b border-border-strong bg-bg px-4 py-2">
        <div className="min-w-0">
          <p className="text-[11px] uppercase tracking-[.18em] text-text-subtle">Control Plane Overview</p>
          <p className="mt-0.5 truncate font-mono text-[11px] text-text-subtle">
            {sys.config_path || 'No active config path'}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <StatusBadge status={daemonStatus} />
          <Button variant="outline" size="sm" onClick={() => {
            void overview.refetch()
            void system.refetch()
          }}>
            Refresh
          </Button>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-3">
        <div className="grid gap-3 xl:grid-cols-[minmax(0,2fr)_minmax(20rem,0.85fr)]">
          <Panel
            title="Activity Signal"
            icon={<Activity className="h-3.5 w-3.5" />}
            meta={`${compact(trackedStates)} tracked states`}
          >
            <KpiGrid cols="grid-cols-2 md:grid-cols-6">
              <Kpi label="Resources" value={compact(ov.inventory.resources)} sub={`${ov.runtime.running} running`} />
              <Kpi label="Projects" value={compact(ov.inventory.projects)} sub={`${sys.resolved_projects} resolved`} />
              <Kpi label="Pipelines" value={compact(ov.inventory.pipelines)} sub={`${sys.resolved_pipelines} resolved`} />
              <Kpi label="Registry" value={compact(ov.registry.entries)} sub={`${ov.registry.unhealthy} unhealthy`} />
              <Kpi label="Healthy" value={ratio(ov.runtime.running, ov.inventory.resources)} sub={`${ov.runtime.running}/${ov.inventory.resources}`} />
              <Kpi label="Attention" value={ov.runtime.attention} sub={ov.runtime.attention ? 'needs review' : 'all clear'} accent={ov.runtime.attention ? 'var(--color-status-blocked)' : undefined} />
            </KpiGrid>
            <SignalBars
              data={runtimeSeries}
              secondaryData={interactionSeries}
              heightClassName="h-56"
              primaryLabel="Runtime"
              secondaryLabel="Interaction"
            />
            <div className="grid md:grid-cols-4">
              <MiniTrend label="Resources" value={sum(resourcesTrend)} data={resourcesTrend} />
              <MiniTrend label="Projects" value={sum(projectsTrend)} data={projectsTrend} />
              <MiniTrend label="Pipelines" value={sum(pipelinesTrend)} data={pipelinesTrend} />
              <MiniTrend label="Registry" value={sum(registryTrend)} data={registryTrend} />
            </div>
          </Panel>

          <Panel title="Intelligence" icon={<Gauge className="h-3.5 w-3.5" />}>
            <IntelligenceRow label="Daemon reachability" value={ov.daemon.running ? 'online' : 'offline'} status={daemonStatus} />
            <IntelligenceRow label="Socket availability" value={ov.daemon.socket_exists ? 'present' : 'missing'} status={ov.daemon.socket_exists ? 'done' : 'blocked'} />
            <IntelligenceRow label="Registry status" value={ov.registry.unhealthy ? `${ov.registry.unhealthy} unhealthy` : 'healthy'} status={registryStatus} />
            <IntelligenceRow label="Resolved resources" value={sys.resolved_resources} status="done" />
            <IntelligenceRow label="Resolved pipelines" value={sys.resolved_pipelines} status="done" />
            <IntelligenceRow label="Resolve warnings" value={warnings.length} status={warnings.length > 0 ? 'blocked' : 'done'} />
          </Panel>
        </div>

        <div className="mt-3 grid gap-3 xl:grid-cols-4">
          <Panel
            title="Resources"
            icon={<ServerCog className="h-3.5 w-3.5" />}
            meta={`${ov.runtime.running} running`}
          >
            <KpiGrid>
              <Kpi label="Total" value={ov.inventory.resources} />
              <Kpi label="Running" value={ov.runtime.running} accent="var(--color-status-done)" />
              <Kpi label="Attention" value={ov.runtime.attention} accent={ov.runtime.attention ? 'var(--color-status-blocked)' : undefined} />
              <Kpi label="Stopped" value={ov.runtime.stopped} />
            </KpiGrid>
            <div className="border-b border-border p-3">
              <div className="mb-2 text-[10px] uppercase tracking-[.16em] text-text-subtle">State mix</div>
              <CompositionBars items={runtimeStates} />
            </div>
            <div className="border-t border-border">
              <MiniTrend label="24h resources" value={sum(resourcesTrend)} data={resourcesTrend} />
            </div>
          </Panel>

          <Panel
            title="Projects"
            icon={<Boxes className="h-3.5 w-3.5" />}
            meta={`${sys.resolved_projects} resolved`}
          >
            <KpiGrid>
              <Kpi label="Total" value={ov.inventory.projects} />
              <Kpi label="Resolved" value={sys.resolved_projects} />
              <Kpi label="Pipelines" value={ov.inventory.pipelines} />
              <Kpi label="Connectors" value={ov.inventory.connectors} />
            </KpiGrid>
            <div className="p-3">
              <div className="mb-2 text-[10px] uppercase tracking-[.16em] text-text-subtle">Inventory mix</div>
              <BarList items={inventoryMix} />
            </div>
            <div className="border-t border-border">
              <MiniTrend label="24h projects" value={sum(projectsTrend)} data={projectsTrend} />
            </div>
          </Panel>

          <Panel
            title="Pipelines"
            icon={<Route className="h-3.5 w-3.5" />}
            meta={`${sys.resolved_pipelines} resolved`}
          >
            <KpiGrid>
              <Kpi label="Declared" value={ov.inventory.pipelines} />
              <Kpi label="Resolved" value={sys.resolved_pipelines} />
              <Kpi label="Warnings" value={warnings.length} accent={warnings.length ? 'var(--color-warning)' : undefined} />
              <Kpi label="Plugins" value={ov.inventory.plugins} />
            </KpiGrid>
            <div className="border-t border-border">
              <MiniTrend label="24h pipelines" value={sum(pipelinesTrend)} data={pipelinesTrend} />
            </div>
          </Panel>

          <Panel
            title="Registry"
            icon={<Waypoints className="h-3.5 w-3.5" />}
            meta={`${ov.registry.entries} entries`}
          >
            <KpiGrid>
              <Kpi label="Entries" value={ov.registry.entries} />
              <Kpi label="Healthy" value={ov.registry.healthy} accent="var(--color-status-done)" />
              <Kpi label="Unhealthy" value={ov.registry.unhealthy} accent={ov.registry.unhealthy ? 'var(--color-status-blocked)' : undefined} />
              <Kpi label="Failed svc" value={ov.daemon.services_failed} accent={ov.daemon.services_failed ? 'var(--color-status-blocked)' : undefined} />
            </KpiGrid>
            <div className="border-b border-border p-3">
              <div className="mb-2 text-[10px] uppercase tracking-[.16em] text-text-subtle">Registry health</div>
              <CompositionBars items={registryStates} />
            </div>
            <div className="border-t border-border">
              <MiniTrend label="24h entries" value={sum(registryTrend)} data={registryTrend} />
            </div>
          </Panel>
        </div>

        <div className="mt-3 grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
          <Panel title="Operator Sources" icon={<FolderTree className="h-3.5 w-3.5" />}>
            <div className="space-y-1 p-3 text-[12px] text-text-soft">
              <div className="font-mono text-[11px] text-text-subtle">{sys.config_path || 'No config path'}</div>
              <div className="font-mono text-[11px] text-text-subtle">{sys.registry_path || 'No registry path'}</div>
              <div className="font-mono text-[11px] text-text-subtle">{sys.socket_path || 'No socket path'}</div>
            </div>
          </Panel>

          <Panel title="Operator Notes" icon={<Gauge className="h-3.5 w-3.5" />}>
            <div className="p-3">
              {warnings.length > 0 ? (
                <Callout tone="warning" title="Resolve warnings">
                  <div className="whitespace-pre-wrap font-mono text-[11px]">{warnings.join('\n')}</div>
                </Callout>
              ) : (
                <SettingsNotice
                  tone="info"
                  title="Operator note"
                  description="Use Resources for per-service action. Use Settings and Registry when the whole control plane looks drifted."
                />
              )}
            </div>
          </Panel>
        </div>
      </div>
    </div>
  )
}

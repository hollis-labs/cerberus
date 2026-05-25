import { Activity, FolderKanban, FolderTree, ServerCog, Workflow } from 'lucide-react'
import {
  Button,
  EmptyState,
  SettingsNotice,
  StatusBadge,
  SummaryCards,
} from '@hollis-labs/sysop-ui/ui'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import {
  BarList,
  CompositionBars,
  SignalBars,
  IntelligenceRow,
  Kpi,
  KpiGrid,
  MiniTrend,
  Panel,
} from '@hollis-labs/sysop-ui/widgets'
import { apiClient } from '../api/client'


function compact(n: number): string {
  return new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

function ratio(part: number, total: number): string {
  if (!total) return '0%'
  return `${Math.round((part / total) * 100)}%`
}

function spark(seed: number, variance = 0.18): number[] {
  const base = Math.max(1, seed)
  return Array.from({ length: 12 }, (_, index) => {
    const wave = ((index % 5) - 2) * variance
    return Math.max(0, Math.round(base * (1 + wave)))
  })
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

  const cards = [
    { label: 'Resources', value: ov.inventory.resources, subtitle: `${ov.runtime.running} running`, accentColor: 'var(--color-text)' },
    { label: 'Projects', value: ov.inventory.projects, subtitle: `${sys.resolved_projects} resolved`, accentColor: 'var(--color-status-done)' },
    { label: 'Pipelines', value: ov.inventory.pipelines, subtitle: `${sys.resolved_pipelines} resolved`, accentColor: 'var(--color-warning)' },
    { label: 'Registry', value: ov.registry.entries, subtitle: `${ov.registry.unhealthy} unhealthy`, accentColor: 'var(--color-status-blocked)' },
  ]

  const runtimeBars = [
    ov.runtime.running,
    ov.runtime.attention,
    ov.runtime.stopped,
    ov.daemon.services_failed,
    ov.inventory.projects,
    ov.inventory.pipelines,
    ov.inventory.connectors,
    ov.inventory.plugins,
  ]
  const inventoryMix = [
    { label: 'Resources', value: ov.inventory.resources },
    { label: 'Projects', value: ov.inventory.projects },
    { label: 'Pipelines', value: ov.inventory.pipelines },
    { label: 'Connectors', value: ov.inventory.connectors },
    { label: 'Plugins', value: ov.inventory.plugins },
  ]
  const runtimeList = [
    { label: 'Running', value: ov.runtime.running },
    { label: 'Attention', value: ov.runtime.attention },
    { label: 'Stopped', value: ov.runtime.stopped },
    { label: 'Failed services', value: ov.daemon.services_failed },
  ]
  const daemonStatus = ov.daemon.running ? 'done' : 'blocked'
  const registryStatus = ov.registry.unhealthy > 0 ? 'blocked' : 'done'

  return (
    <div className="flex h-full min-h-0 w-full flex-col overflow-auto">
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

      <SummaryCards cards={cards} />

      <div className="grid gap-3 p-3 xl:grid-cols-[minmax(0,2fr)_minmax(20rem,0.9fr)]">
        <Panel
          title="Runtime Signal"
          icon={<Activity className="h-3.5 w-3.5" />}
          meta={`${compact(ov.runtime.running + ov.runtime.attention + ov.runtime.stopped)} tracked states`}
        >
          <KpiGrid>
            <Kpi label="Running" value={ov.runtime.running} sub={`${ratio(ov.runtime.running, ov.inventory.resources)} of resources`} accent="var(--color-status-done)" />
            <Kpi label="Attention" value={ov.runtime.attention} sub="needs operator review" accent="var(--color-status-blocked)" />
            <Kpi label="Stopped" value={ov.runtime.stopped} sub="inactive or paused" accent="var(--color-warning)" />
            <Kpi label="Registry Health" value={`${ov.registry.healthy}/${ov.registry.entries}`} sub={ov.registry.unhealthy ? `${ov.registry.unhealthy} unhealthy` : 'all healthy'} />
          </KpiGrid>
          <div className="grid gap-0 lg:grid-cols-[1.4fr_.6fr]">
            <div className="border-r border-border">
              <SignalBars
                data={runtimeBars}
                secondaryData={runtimeBars.map((value, index) => Math.max(0, Math.round(value * (0.2 + (index % 3) * 0.08))))}
                primaryLabel="live"
                secondaryLabel="drift"
                className="h-full"
                heightClassName="h-44"
              />
            </div>
            <div className="space-y-0">
              <MiniTrend label="Daemon failures" value={ov.daemon.services_failed} data={spark(ov.daemon.services_failed + 1, 0.35)} />
              <MiniTrend label="Resolve warnings" value={sys.resolve_warnings?.length ?? 0} data={spark((sys.resolve_warnings?.length ?? 0) + 1, 0.4)} />
              <MiniTrend label="Registry drift" value={ov.registry.unhealthy} data={spark(ov.registry.unhealthy + 1, 0.32)} />
            </div>
          </div>
        </Panel>

        <Panel title="Operator Focus" icon={<Workflow className="h-3.5 w-3.5" />}>
          <IntelligenceRow label="Daemon reachability" value={ov.daemon.running ? 'online' : 'offline'} status={daemonStatus} />
          <IntelligenceRow label="Socket availability" value={ov.daemon.socket_exists ? 'present' : 'missing'} status={ov.daemon.socket_exists ? 'done' : 'blocked'} />
          <IntelligenceRow label="Registry status" value={ov.registry.unhealthy ? `${ov.registry.unhealthy} unhealthy` : 'healthy'} status={registryStatus} />
          <IntelligenceRow label="Resolved resources" value={sys.resolved_resources} status="done" />
          <IntelligenceRow label="Resolved pipelines" value={sys.resolved_pipelines} status="done" />
          <IntelligenceRow label="Resolve warnings" value={sys.resolve_warnings?.length ?? 0} status={(sys.resolve_warnings?.length ?? 0) > 0 ? 'blocked' : 'done'} />
        </Panel>

        <Panel title="Inventory Mix" icon={<FolderKanban className="h-3.5 w-3.5" />}>
          <div className="grid gap-0 lg:grid-cols-[.8fr_1.2fr]">
            <div className="border-r border-border px-3 py-3">
              <div className="mb-2 text-[10px] uppercase tracking-[.16em] text-text-subtle">Composition</div>
              <CompositionBars items={inventoryMix} />
            </div>
            <div className="px-3 py-3">
              <div className="mb-2 text-[10px] uppercase tracking-[.16em] text-text-subtle">Counts</div>
              <BarList items={inventoryMix} />
            </div>
          </div>
        </Panel>

        <Panel title="Runtime Breakdown" icon={<ServerCog className="h-3.5 w-3.5" />}>
          <div className="px-3 py-3">
            <BarList items={runtimeList} />
          </div>
        </Panel>

        <Panel title="Registry And Resolve" icon={<FolderTree className="h-3.5 w-3.5" />} className="xl:col-span-2">
          <div className="grid gap-3 px-3 py-3 lg:grid-cols-[1fr_1fr]">
            <div className="space-y-2">
              <div className="text-[10px] uppercase tracking-[.16em] text-text-subtle">Sources</div>
              <div className="space-y-1 text-[12px] text-text-soft">
                <div className="font-mono text-[11px] text-text-subtle">{sys.config_path || 'No config path'}</div>
                <div className="font-mono text-[11px] text-text-subtle">{sys.registry_path || 'No registry path'}</div>
                <div className="font-mono text-[11px] text-text-subtle">{sys.socket_path || 'No socket path'}</div>
              </div>
            </div>
            <div className="space-y-2">
              {(sys.resolve_warnings?.length ?? 0) > 0 ? (
                <SettingsNotice
                  tone="warning"
                  title="Resolve warnings"
                  description={sys.resolve_warnings?.join('\n') || ''}
                  className="whitespace-pre-wrap"
                />
              ) : (
                <SettingsNotice
                  tone="info"
                  title="Operator note"
                  description="Use Resources for per-service action. Use Settings and Registry when the whole control plane looks drifted."
                />
              )}
            </div>
          </div>
        </Panel>
      </div>
    </div>
  )
}

import { Activity, FolderKanban, FolderTree, ServerCog, Workflow } from 'lucide-react'
import { EmptyState, SummaryCards, usePoll } from '@hollis-labs/sysop-ui'
import { apiClient } from '../api/client'

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
        action={{ label: 'Retry', onClick: () => {
          void overview.refetch()
          void system.refetch()
        } }}
      />
    )
  }

  const ov = overview.data
  const sys = system.data
  if (!ov || !sys) {
    return <div className="p-4 text-sm text-text-soft">Loading overview...</div>
  }

  const cards = [
    { label: 'Resources', value: ov.inventory.resources, subtitle: `${ov.runtime.running} running`, accentColor: 'var(--color-text)' },
    { label: 'Projects', value: ov.inventory.projects, subtitle: `${sys.resolved_projects} resolved`, accentColor: 'var(--color-status-done)' },
    { label: 'Pipelines', value: ov.inventory.pipelines, subtitle: `${sys.resolved_pipelines} resolved`, accentColor: 'var(--color-warning)' },
    { label: 'Registry', value: ov.registry.entries, subtitle: `${ov.registry.unhealthy} unhealthy`, accentColor: 'var(--color-status-blocked)' },
  ]

  return (
    <div className="flex h-full min-h-0 w-full flex-col overflow-auto">
      <SummaryCards cards={cards} />
      <div className="grid gap-4 p-4 lg:grid-cols-[1.2fr_.8fr]">
        <section className="border border-border bg-panel p-4">
          <div className="mb-3 flex items-center gap-2 text-sm text-text">
            <Activity className="h-4 w-4" />
            Runtime
          </div>
          <dl className="grid grid-cols-2 gap-3 text-sm md:grid-cols-3">
            <Meta label="Daemon running" value={ov.daemon.running ? 'yes' : 'no'} />
            <Meta label="Socket exists" value={ov.daemon.socket_exists ? 'yes' : 'no'} />
            <Meta label="Services failed" value={String(ov.daemon.services_failed)} />
            <Meta label="Running" value={String(ov.runtime.running)} />
            <Meta label="Attention" value={String(ov.runtime.attention)} />
            <Meta label="Stopped" value={String(ov.runtime.stopped)} />
          </dl>
        </section>

        <section className="border border-border bg-panel p-4">
          <div className="mb-3 flex items-center gap-2 text-sm text-text">
            <ServerCog className="h-4 w-4" />
            System
          </div>
          <dl className="grid gap-3 text-sm">
            <Meta label="Config" value={sys.config_path} />
            <Meta label="Registry" value={sys.registry_path} />
            <Meta label="Socket" value={sys.socket_path} />
            <Meta label="Resolved resources" value={String(sys.resolved_resources)} />
          </dl>
        </section>

        <section className="border border-border bg-panel p-4">
          <div className="mb-3 flex items-center gap-2 text-sm text-text">
            <FolderKanban className="h-4 w-4" />
            Inventory
          </div>
          <dl className="grid grid-cols-2 gap-3 text-sm md:grid-cols-5">
            <Meta label="Projects" value={String(ov.inventory.projects)} />
            <Meta label="Resources" value={String(ov.inventory.resources)} />
            <Meta label="Pipelines" value={String(ov.inventory.pipelines)} />
            <Meta label="Connectors" value={String(ov.inventory.connectors)} />
            <Meta label="Plugins" value={String(ov.inventory.plugins)} />
          </dl>
        </section>

        <section className="border border-border bg-panel p-4">
          <div className="mb-3 flex items-center gap-2 text-sm text-text">
            <FolderTree className="h-4 w-4" />
            Registry And Resolve
          </div>
          <dl className="grid gap-3 text-sm">
            <Meta label="Healthy registry entries" value={`${ov.registry.healthy}/${ov.registry.entries}`} />
            <Meta label="Resolve warnings" value={sys.resolve_warnings?.length ? sys.resolve_warnings.join('\n') : 'none'} />
            <Meta label="Overview warnings" value={ov.error || 'none'} />
            <Meta label="System warnings" value={sys.error || 'none'} />
          </dl>
        </section>

        <section className="border border-border bg-panel p-4 lg:col-span-2">
          <div className="mb-3 flex items-center gap-2 text-sm text-text">
            <Workflow className="h-4 w-4" />
            Operator Notes
          </div>
          <div className="space-y-2 text-sm text-text-soft">
            <p>Use this page to confirm daemon reachability, config resolution, and registry health before debugging an individual resource.</p>
            <p>When the runtime is healthy but a single resource is drifted, use the Resources page for deploy/apply/sync/remove decisions.</p>
          </div>
        </section>
      </div>
    </div>
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

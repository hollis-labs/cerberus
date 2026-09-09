import { EmptyState, Pill, SummaryCards } from '@hollis-labs/sysop-ui/ui'
import { DataTable, type ColumnDef } from '@hollis-labs/sysop-ui/data'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, type ProjectInfo } from '../api/client'
import { ResolveNotice } from '../components/resolve-notice'

const columns: ColumnDef<ProjectInfo>[] = [
  {
    key: 'project',
    header: 'Project',
    width: 'fill',
    cell: (item) => (
      <div className="min-w-0">
        <div className="truncate text-[12px] text-text">{item.name || item.id}</div>
        <div className="truncate font-mono text-[11px] text-text-subtle">{item.id}</div>
      </div>
    ),
    sortValue: (item) => item.name || item.id,
  },
  {
    key: 'resources',
    header: 'Resources',
    align: 'right',
    cell: (item) => item.resource_count,
    sortValue: (item) => item.resource_count,
  },
  {
    key: 'capabilities',
    header: 'Capabilities',
    width: 'fill',
    cell: (item) =>
      item.capabilities?.length ? (
        <div className="flex flex-wrap gap-1">
          {item.capabilities.map((capability) => (
            <Pill key={capability} tone="neutral">
              {capability}
            </Pill>
          ))}
        </div>
      ) : (
        <span className="text-[11px] text-text-subtle">—</span>
      ),
    sortValue: (item) => (item.capabilities ?? []).join(', '),
  },
  {
    key: 'links',
    header: 'Links',
    cell: (item) =>
      item.links?.length ? (
        <span className="text-[11px] text-text-soft" title={item.links.map((l) => `${l.kind}: ${l.target}`).join('\n')}>
          {item.links.map((l) => l.kind).join(', ')}
        </span>
      ) : (
        <span className="text-[11px] text-text-subtle">—</span>
      ),
    sortValue: (item) => (item.links ?? []).length,
  },
  {
    key: 'description',
    header: 'Description',
    width: 'fill',
    cell: (item) => (
      <span className="block truncate text-[11px] text-text-soft">{item.description || '—'}</span>
    ),
    sortValue: (item) => item.description || '',
  },
]

export function ProjectsPage() {
  const projects = usePoll((signal) => apiClient.listProjects(signal), 5000)

  if (projects.error) {
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus daemon"
        title="Could not load projects"
        description={projects.error instanceof Error ? projects.error.message : String(projects.error)}
        action={{ label: 'Retry', onClick: projects.refetch }}
      />
    )
  }

  const items = (projects.data ?? []).slice().sort((a, b) => a.id.localeCompare(b.id))
  const cards = [
    { label: 'Projects', value: items.length, accentColor: 'var(--color-text)' },
    { label: 'Declared resources', value: items.reduce((sum, item) => sum + item.resource_count, 0), accentColor: 'var(--color-status-done)' },
  ]

  return (
    <div className="flex h-full min-h-0 w-full flex-col overflow-auto">
      <SummaryCards cards={cards} />
      <ResolveNotice />
      <div>
        {projects.isLoading && items.length === 0 ? (
          <div className="border-b border-border-strong px-4 py-3 text-sm text-text-soft">Loading projects...</div>
        ) : items.length === 0 ? (
          <div className="px-4 py-4">
            <EmptyState variant="no-results" title="No projects declared." description="The daemon did not resolve any projects from the active config." />
          </div>
        ) : (
          <DataTable
            items={items}
            columns={columns}
            getRowId={(item) => item.id}
          />
        )}
      </div>
    </div>
  )
}

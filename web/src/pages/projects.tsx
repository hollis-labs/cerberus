import { EmptyState, SummaryCards, usePoll } from '@hollis-labs/sysop-ui'
import { apiClient } from '../api/client'

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
      <div className="p-4">
        {projects.isLoading && items.length === 0 ? (
          <div className="text-sm text-text-soft">Loading projects...</div>
        ) : items.length === 0 ? (
          <EmptyState variant="no-results" title="No projects declared." description="The daemon did not resolve any projects from the active config." />
        ) : (
          <div className="overflow-x-auto border border-border bg-panel">
            <table className="w-full min-w-full">
              <thead className="text-[10px] uppercase tracking-[.28em] text-text-subtle">
                <tr className="border-b border-border-strong">
                  <th className="px-3 py-2 text-left font-medium">Project</th>
                  <th className="px-3 py-2 text-left font-medium">ID</th>
                  <th className="px-3 py-2 text-left font-medium">Resources</th>
                  <th className="px-3 py-2 text-left font-medium">Description</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border-soft text-sm">
                {items.map((item) => (
                  <tr key={item.id}>
                    <td className="px-3 py-2 text-text">{item.name || item.id}</td>
                    <td className="px-3 py-2 font-mono text-text-soft">{item.id}</td>
                    <td className="px-3 py-2 text-text-soft">{item.resource_count}</td>
                    <td className="px-3 py-2 text-text-soft">{item.description || '-'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}

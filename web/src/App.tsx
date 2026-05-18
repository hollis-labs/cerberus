import { useState } from 'react'
import { Boxes, Cable, LayoutDashboard, Plug, Route, Server } from 'lucide-react'
import { NavRail, PageHeader, ThemeSwitcher, type NavRailItem } from '@hollis-labs/sysop-ui'
import { ResourcesPage } from './pages/resources'

type RouteKey = 'resources' | 'projects' | 'pipelines' | 'connectors' | 'plugins'

const TITLES: Record<RouteKey, string> = {
  resources: 'Resources',
  projects: 'Projects',
  pipelines: 'Pipelines',
  connectors: 'Connectors',
  plugins: 'Plugins',
}

export function App() {
  const [route, setRoute] = useState<RouteKey>('resources')

  const nav: NavRailItem[] = [
    {
      key: 'resources',
      label: 'Resources',
      icon: <LayoutDashboard className="h-4 w-4" />,
      active: route === 'resources',
      onSelect: () => setRoute('resources'),
    },
    {
      key: 'projects',
      label: 'Projects',
      icon: <Boxes className="h-4 w-4" />,
      active: route === 'projects',
      onSelect: () => setRoute('projects'),
    },
    {
      key: 'pipelines',
      label: 'Pipelines',
      icon: <Route className="h-4 w-4" />,
      active: route === 'pipelines',
      onSelect: () => setRoute('pipelines'),
    },
    {
      key: 'connectors',
      label: 'Connectors',
      icon: <Cable className="h-4 w-4" />,
      active: route === 'connectors',
      onSelect: () => setRoute('connectors'),
    },
    {
      key: 'plugins',
      label: 'Plugins',
      icon: <Plug className="h-4 w-4" />,
      active: route === 'plugins',
      onSelect: () => setRoute('plugins'),
    },
  ]

  return (
    <div className="flex h-dvh w-dvw overflow-hidden bg-bg text-text">
      <NavRail items={nav} logo={<Server className="h-4 w-4" />} logoLabel="Cerberus" />
      <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
        <PageHeader title={TITLES[route]}>
          <ThemeSwitcher />
        </PageHeader>
        <main className="min-h-0 flex-1 overflow-hidden">
          {route === 'resources' ? <ResourcesPage /> : <PendingPage title={TITLES[route]} />}
        </main>
      </div>
    </div>
  )
}

function PendingPage({ title }: { title: string }) {
  return (
    <div className="flex h-full items-center justify-center px-6">
      <div className="max-w-md border border-border bg-panel p-5 text-sm text-muted">
        <div className="mb-2 text-xs font-semibold uppercase tracking-wide text-text">{title}</div>
        <div>Waiting for the service-layer REST endpoint.</div>
      </div>
    </div>
  )
}

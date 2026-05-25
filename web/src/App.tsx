import { useState } from 'react'
import { Boxes, Cable, Gauge, LayoutDashboard, Plug, Rocket, Route, Server, Settings2, Waypoints } from 'lucide-react'
import { NavRail, PageHeader, ThemeSwitcher, type NavRailItem } from '@hollis-labs/sysop-ui/ui'
import { ConnectorsPage } from './pages/connectors'
import { DeploymentsPage } from './pages/deployments'
import { OverviewPage } from './pages/overview'
import { PipelinesPage } from './pages/pipelines'
import { PluginsPage } from './pages/plugins'
import { ProjectsPage } from './pages/projects'
import { RegistryPage } from './pages/registry'
import { ResourcesPage } from './pages/resources'
import { SettingsPage } from './pages/settings'

type RouteKey = 'overview' | 'resources' | 'projects' | 'pipelines' | 'registry' | 'deployments' | 'connectors' | 'plugins' | 'settings'

const TITLES: Record<RouteKey, string> = {
  overview: 'Overview',
  resources: 'Resources',
  projects: 'Projects',
  pipelines: 'Pipelines',
  registry: 'Registry',
  deployments: 'Deployments',
  connectors: 'Connectors',
  plugins: 'Plugins',
  settings: 'Settings',
}

export function App() {
  const [route, setRoute] = useState<RouteKey>('overview')

  const nav: NavRailItem[] = [
    {
      key: 'overview',
      label: 'Overview',
      icon: <Gauge className="h-4 w-4" />,
      active: route === 'overview',
      onSelect: () => setRoute('overview'),
    },
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
      key: 'registry',
      label: 'Registry',
      icon: <Waypoints className="h-4 w-4" />,
      active: route === 'registry',
      onSelect: () => setRoute('registry'),
    },
    {
      key: 'deployments',
      label: 'Deployments',
      icon: <Rocket className="h-4 w-4" />,
      active: route === 'deployments',
      onSelect: () => setRoute('deployments'),
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
    {
      key: 'settings',
      label: 'Settings',
      icon: <Settings2 className="h-4 w-4" />,
      active: route === 'settings',
      onSelect: () => setRoute('settings'),
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
          <RouteView route={route} />
        </main>
      </div>
    </div>
  )
}

function RouteView({ route }: { route: RouteKey }) {
  switch (route) {
    case 'overview':
      return <OverviewPage />
    case 'resources':
      return <ResourcesPage />
    case 'projects':
      return <ProjectsPage />
    case 'pipelines':
      return <PipelinesPage />
    case 'registry':
      return <RegistryPage />
    case 'deployments':
      return <DeploymentsPage />
    case 'connectors':
      return <ConnectorsPage />
    case 'plugins':
      return <PluginsPage />
    case 'settings':
      return <SettingsPage />
    default:
      return <PendingPage title={TITLES[route]} />
  }
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

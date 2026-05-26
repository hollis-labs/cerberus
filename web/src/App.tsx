import { Boxes, Cable, Gauge, LayoutDashboard, Plug, Rocket, Route, Server, Settings2, Waypoints } from 'lucide-react'
import { NavRail, PageHeader, ThemeSwitcher, Toaster, TooltipProvider, type NavRailItem } from '@hollis-labs/sysop-ui/ui'
import { createRouter } from '@hollis-labs/sysop-ui/api'
import { ConnectorsPage } from './pages/connectors'
import { DeploymentsPage } from './pages/deployments'
import { OverviewPage } from './pages/overview'
import { PipelinesPage } from './pages/pipelines'
import { PluginsPage } from './pages/plugins'
import { ProjectsPage } from './pages/projects'
import { RegistryPage } from './pages/registry'
import { ResourcesPage } from './pages/resources'
import { SettingsPage } from './pages/settings'

const ROUTES = [
  'overview',
  'resources',
  'projects',
  'pipelines',
  'registry',
  'deployments',
  'connectors',
  'plugins',
  'settings',
] as const
type RouteKey = (typeof ROUTES)[number]

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

// Overview lives at `/` exactly; the rest mount at `/<name>`.
const useRoute = createRouter({
  routes: ROUTES,
  default: 'overview',
  paths: { overview: '' },
})

export function App() {
  const { route, navigate } = useRoute()

  const nav: NavRailItem[] = [
    {
      key: 'overview',
      label: 'Overview',
      icon: <Gauge className="h-4 w-4" />,
      active: route === 'overview',
      onSelect: () => navigate('overview'),
    },
    {
      key: 'resources',
      label: 'Resources',
      icon: <LayoutDashboard className="h-4 w-4" />,
      active: route === 'resources',
      onSelect: () => navigate('resources'),
    },
    {
      key: 'projects',
      label: 'Projects',
      icon: <Boxes className="h-4 w-4" />,
      active: route === 'projects',
      onSelect: () => navigate('projects'),
    },
    {
      key: 'pipelines',
      label: 'Pipelines',
      icon: <Route className="h-4 w-4" />,
      active: route === 'pipelines',
      onSelect: () => navigate('pipelines'),
    },
    {
      key: 'registry',
      label: 'Registry',
      icon: <Waypoints className="h-4 w-4" />,
      active: route === 'registry',
      onSelect: () => navigate('registry'),
    },
    {
      key: 'deployments',
      label: 'Deployments',
      icon: <Rocket className="h-4 w-4" />,
      active: route === 'deployments',
      onSelect: () => navigate('deployments'),
    },
    {
      key: 'connectors',
      label: 'Connectors',
      icon: <Cable className="h-4 w-4" />,
      active: route === 'connectors',
      onSelect: () => navigate('connectors'),
    },
    {
      key: 'plugins',
      label: 'Plugins',
      icon: <Plug className="h-4 w-4" />,
      active: route === 'plugins',
      onSelect: () => navigate('plugins'),
    },
    {
      key: 'settings',
      label: 'Settings',
      icon: <Settings2 className="h-4 w-4" />,
      footer: true,
      active: route === 'settings',
      onSelect: () => navigate('settings'),
    },
  ]

  return (
    <TooltipProvider>
      <div className="flex h-dvh w-dvw overflow-hidden bg-bg text-text">
        <NavRail
          items={nav}
          logo={<Server className="h-4 w-4" />}
          logoLabel="Cerberus"
          footerExtra={<ThemeSwitcher />}
        />
        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
          <PageHeader title={TITLES[route]} />
          <main className="flex min-h-0 flex-1 flex-col">
            <RouteView route={route} />
          </main>
        </div>
      </div>
      <Toaster />
    </TooltipProvider>
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

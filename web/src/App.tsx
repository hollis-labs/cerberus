import { useEffect, useState } from 'react'
import { Boxes, Cable, CheckCheck, Gauge, LayoutDashboard, LogOut, Plug, Rocket, Route, Server, Settings2, Waypoints } from 'lucide-react'
import { NavRail, PageHeader, ThemeSwitcher, Toaster, TooltipProvider, type NavRailItem } from '@hollis-labs/sysop-ui/ui'
import { ApiError, createRouter } from '@hollis-labs/sysop-ui/api'
import { apiClient, type PostureInfo } from './api/client'
import { ApprovalsPage } from './pages/approvals'
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
  'approvals',
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
  approvals: 'Approvals',
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

type SessionState =
  | { kind: 'checking' }
  | { kind: 'signed-out' }
  | { kind: 'signed-in'; token: string; posture?: PostureInfo }

// The console needs a signed-in session (`cerberus web open`). Without one
// every API route answers 401, so the app shows how to sign in instead.
export function App() {
  const [session, setSession] = useState<SessionState>({ kind: 'checking' })

  useEffect(() => {
    const controller = new AbortController()
    apiClient
      .getSession(controller.signal)
      .then((info) => setSession({ kind: 'signed-in', token: info.action_token, posture: info.posture }))
      .catch((err: unknown) => {
        if (err instanceof ApiError && err.status === 401) setSession({ kind: 'signed-out' })
      })
    return () => controller.abort()
  }, [])

  if (session.kind === 'checking') return null
  if (session.kind === 'signed-out') return <SignedOut />
  const signOut = () => {
    void apiClient.logout(session.token).finally(() => setSession({ kind: 'signed-out' }))
  }
  return <Console onSignOut={signOut} posture={session.posture} />
}

function SignedOut() {
  return (
    <div className="flex h-dvh w-dvw items-center justify-center bg-bg text-text">
      <div className="max-w-md space-y-3 rounded-lg border border-border p-6" data-testid="signed-out">
        <h1 className="text-lg font-semibold">Sign in to Cerberus</h1>
        <p className="text-sm text-text-muted">
          Run <code className="font-mono">cerberus web open</code> in a terminal on this machine. It prints and opens a
          one-time sign-in link, good for two minutes.
        </p>
      </div>
    </div>
  )
}

// PostureBadge names the posture in the header: quiet when secure, loud
// when anything is permissive.
function PostureBadge({ posture }: { posture?: PostureInfo }) {
  const summary = posture?.summary ?? 'secure'
  const permissive = posture?.permissive ?? false
  return (
    <span
      data-testid="posture"
      title={`Posture: ${summary}`}
      className={
        permissive
          ? 'rounded border border-amber-500 bg-amber-500/15 px-2 py-0.5 text-xs font-semibold text-amber-600'
          : 'rounded border border-border px-2 py-0.5 text-xs text-text-muted'
      }
    >
      {permissive ? `PERMISSIVE: ${summary}` : 'secure'}
    </span>
  )
}

function Console({ onSignOut, posture }: { onSignOut: () => void; posture?: PostureInfo }) {
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
      key: 'approvals',
      label: 'Approvals',
      icon: <CheckCheck className="h-4 w-4" />,
      active: route === 'approvals',
      onSelect: () => navigate('approvals'),
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
          footerExtra={
            <>
              <ThemeSwitcher />
              <button
                type="button"
                aria-label="Sign out"
                title="Sign out"
                data-testid="sign-out"
                onClick={onSignOut}
                className="rounded p-2 text-text-muted hover:text-text"
              >
                <LogOut className="h-4 w-4" />
              </button>
            </>
          }
        />
        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
          <PageHeader title={TITLES[route]}>
            <PostureBadge posture={posture} />
          </PageHeader>
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
    case 'approvals':
      return <ApprovalsPage />
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

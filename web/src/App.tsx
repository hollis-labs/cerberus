import { useEffect, useState } from 'react'
import { Boxes, Cable, CheckCheck, Gauge, KeyRound, LayoutDashboard, LogOut, Plug, Route, Server, Settings2, Waypoints } from 'lucide-react'
import { NavRail, PageHeader, ThemeSwitcher, Toaster, TooltipProvider, type NavRailItem } from '@hollis-labs/sysop-ui/ui'
import { ConfirmOnCallProvider } from './components/plan-confirm'
import { BrakesBanner, LockdownButton } from './components/brakes'
import { ApiError, createRouter } from '@hollis-labs/sysop-ui/api'
import { apiClient, type PasskeysAlert, type PostureInfo, type BreakGlassAlert, type EnforcementAlert, type BrakeState } from './api/client'
import { ApprovalsPage } from './pages/approvals'
import { ConnectorsPage } from './pages/connectors'
import { CredentialsPage } from './pages/credentials'
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
  'credentials',
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
  credentials: 'Credentials',
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
  | { kind: 'scoped'; token: string; approval: string }
  | { kind: 'signed-in'; token: string; posture?: PostureInfo; passkeys?: PasskeysAlert | null; breakGlass?: BreakGlassAlert | null; enforcement?: EnforcementAlert | null; brakes?: BrakeState | null }

// The console needs a signed-in session (`cerberus web open`). Without one
// every API route answers 401, so the app shows how to sign in instead.
export function App() {
  const [session, setSession] = useState<SessionState>({ kind: 'checking' })

  useEffect(() => {
    const controller = new AbortController()
    apiClient
      .getSession(controller.signal)
      .then((info) =>
        info.scope
          ? setSession({ kind: 'scoped', token: info.action_token, approval: info.scope })
          : setSession({ kind: 'signed-in', token: info.action_token, posture: info.posture, passkeys: info.passkeys, breakGlass: info.break_glass, enforcement: info.enforcement, brakes: info.brakes }),
      )
      .catch((err: unknown) => {
        if (err instanceof ApiError && err.status === 401) setSession({ kind: 'signed-out' })
      })
    return () => controller.abort()
  }, [])

  if (session.kind === 'checking') return null
  if (session.kind === 'signed-out') return <SignedOut />
  if (session.kind === 'scoped') return <ScopedApproval approval={session.approval} token={session.token} onSignedOut={() => setSession({ kind: 'signed-out' })} />
  const signOut = () => {
    void apiClient.logout(session.token).finally(() => setSession({ kind: 'signed-out' }))
  }
  return <Console onSignOut={signOut} posture={session.posture} passkeys={session.passkeys} breakGlass={session.breakGlass} enforcement={session.enforcement} brakes={session.brakes} token={session.token} />
}

// ScopedApproval is a sign-in from an approval link, the one an MCP client
// was handed (M7): that approval's page and nothing else. Approving it takes
// a passkey. The full console needs `cerberus web open`.
function ScopedApproval({ approval, token, onSignedOut }: { approval: string; token: string; onSignedOut: () => void }) {
  return (
    <div className="flex h-dvh w-dvw flex-col bg-bg text-text" data-testid="scoped-approval">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-3 py-2 text-sm">
        <span>
          Signed in for approval <code className="font-mono">{approval}</code> only. Approving it takes your passkey. For the full console, run{' '}
          <code className="font-mono">cerberus web open</code> in a terminal.
        </span>
        <button className="rounded border border-border px-2 py-0.5 text-xs" onClick={() => void apiClient.logout(token).finally(onSignedOut)}>
          Sign out
        </button>
      </div>
      <div className="min-h-0 flex-1">
        <ApprovalsPage scoped />
      </div>
    </div>
  )
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

// PasskeysBadge is the header's alert about out-of-band approval: shown only
// while it is one (not set up, a recent enrollment, a cool-down).
function PasskeysBadge({ passkeys }: { passkeys?: PasskeysAlert | null }) {
  if (!passkeys?.alert) return null
  return (
    <span
      data-testid="passkeys-alert"
      title={passkeys.summary}
      className={
        passkeys.state === 'cooldown'
          ? 'rounded border border-red-500 bg-red-500/15 px-2 py-0.5 text-xs font-semibold text-red-600'
          : 'rounded border border-amber-500 bg-amber-500/15 px-2 py-0.5 text-xs text-amber-600'
      }
    >
      {passkeys.summary}
    </span>
  )
}

// EnforcementBadge is what is enforced (P3-7): red on a snapshot mismatch,
// amber while anything is enforced, absent in plain shadow.
function EnforcementBadge({ enforcement }: { enforcement?: EnforcementAlert | null }) {
  if (!enforcement || (!enforcement.enforced && !enforcement.mismatch)) return null
  const mismatch = !!enforcement.mismatch
  return (
    <span
      data-testid="enforcement-alert"
      title={enforcement.mismatch || enforcement.summary}
      className={
        mismatch
          ? 'rounded border border-red-500 bg-red-500/15 px-2 py-0.5 text-xs font-semibold text-red-600'
          : 'rounded border border-amber-500 bg-amber-500/15 px-2 py-0.5 text-xs text-amber-600'
      }
    >
      {mismatch ? enforcement.mismatch : `Enforcing: ${enforcement.summary}`}
    </span>
  )
}

// BreakGlassBadge is the header's break-glass alert (P3-5b): red while any
// use in the last day or any unacknowledged one exists.
function BreakGlassBadge({ breakGlass }: { breakGlass?: BreakGlassAlert | null }) {
  if (!breakGlass) return null
  return (
    <span
      data-testid="break-glass-alert"
      title={breakGlass.summary}
      className="rounded border border-red-500 bg-red-500/15 px-2 py-0.5 text-xs font-semibold text-red-600"
    >
      BREAK GLASS · {breakGlass.recent} today · {breakGlass.open} to acknowledge
    </span>
  )
}

function Console({
  onSignOut,
  posture,
  passkeys,
  breakGlass,
  enforcement,
  brakes,
  token,
}: {
  onSignOut: () => void
  brakes?: BrakeState | null
  token: string
  posture?: PostureInfo
  passkeys?: PasskeysAlert | null
  breakGlass?: BreakGlassAlert | null
  enforcement?: EnforcementAlert | null
}) {
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
      key: 'credentials',
      label: 'Credentials',
      icon: <KeyRound className="h-4 w-4" />,
      active: route === 'credentials',
      onSelect: () => navigate('credentials'),
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
      <ConfirmOnCallProvider>
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
            <PasskeysBadge passkeys={passkeys} />
            <BreakGlassBadge breakGlass={breakGlass} />
            <EnforcementBadge enforcement={enforcement} />
            <LockdownButton token={token} />
          </PageHeader>
          <BrakesBanner brakes={brakes} token={token} />
          <main className="flex min-h-0 flex-1 flex-col">
            <RouteView route={route} />
          </main>
        </div>
      </div>
      </ConfirmOnCallProvider>
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
    case 'credentials':
      return <CredentialsPage />
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

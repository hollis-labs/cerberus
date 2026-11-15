import { ManifestShell, type PageComponent } from "@hollis-labs/app-shell"
import { ApiKeysPage, IDENTITY_PAGES, IdentityGate } from "@hollis-labs/kit-identity"
import { IdentityProvider, useIdentity } from "@hollis-labs/kit-identity/client"
import {
  BookOpen,
  FolderTree,
  KeyRound,
  KeySquare,
  LayoutDashboard,
  LogOut,
  Monitor,
  Plug,
  Puzzle,
  Server,
  Settings,
  Shield,
  ShieldCheck,
  Workflow,
} from "lucide-react"
import { useCallback, useEffect, useState } from "react"
import { UNAUTHORIZED_EVENT } from "./api/request"
import { manifest } from "./manifest"
import { ApprovalsPage } from "./pages/approvals"
import { ConnectorsPage } from "./pages/connectors"
import { CredentialsPage } from "./pages/credentials"
import { OverviewPage } from "./pages/overview"
import { PipelinesPage } from "./pages/pipelines"
import { PluginsPage } from "./pages/plugins"
import { ProjectsPage } from "./pages/projects"
import { RegistryPage } from "./pages/registry"
import { ResourcesPage } from "./pages/resources"
import { SettingsPage } from "./pages/settings"

// Kit icon names used by the contributions (cmd/cerberus/ui/app.json), resolved by the shell.
const icons = {
  "layout-dashboard": LayoutDashboard,
  server: Server,
  "folder-tree": FolderTree,
  workflow: Workflow,
  "shield-check": ShieldCheck,
  "book-open": BookOpen,
  "key-square": KeySquare,
  plug: Plug,
  puzzle: Puzzle,
  settings: Settings,
  shield: Shield,
  "key-round": KeyRound,
  monitor: Monitor,
  "log-out": LogOut,
}

// Scopes an access key may be given (docs/substrate-port/console-api-contract.md). cerberus.approve
// is left out on purpose: the server refuses it on any key.
const KEY_SCOPES = ["cerberus.read", "cerberus.operate", "cerberus.admin"]

const pages: Record<string, PageComponent> = {
  overview: () => <OverviewPage />,
  resources: () => <ResourcesPage />,
  projects: () => <ProjectsPage />,
  pipelines: () => <PipelinesPage />,
  approvals: () => <ApprovalsPage />,
  registry: () => <RegistryPage />,
  credentials: () => <CredentialsPage />,
  connectors: () => <ConnectorsPage />,
  plugins: () => <PluginsPage />,
  settings: () => <SettingsPage />,
  "identity-account": () => <IDENTITY_PAGES.account />,
  "identity-keys": () => <ApiKeysPage scopeSuggestions={KEY_SCOPES} />,
  "identity-sessions": () => <IDENTITY_PAGES.sessions />,
  "identity-sign-out": () => <IDENTITY_PAGES.signOut />,
}

// An explicit sign-out leaves the URL on the sign-out page; signing in again must not land there.
const SIGN_OUT_PATH = "/identity/sign-out"

function initialPath() {
  if (window.location.pathname !== SIGN_OUT_PATH) return window.location.pathname
  window.history.replaceState(null, "", "/")
  return "/"
}

function usePath() {
  const [path, setPath] = useState(initialPath)
  useEffect(() => {
    const onPop = () => setPath(window.location.pathname)
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [])
  const navigate = useCallback((to: string) => {
    if (to !== window.location.pathname) window.history.pushState(null, "", to)
    setPath(to)
  }, [])
  return [path, navigate] as const
}

/**
 * The kit moves to its "session expired" sign-in when one of its own requests is answered 401.
 * The app's own requests report the same fact through UNAUTHORIZED_EVENT; this asks the kit a
 * question only a live session answers, so its 401 puts the gate on the sign-in screen.
 */
function SessionLossBridge() {
  const { client } = useIdentity()
  useEffect(() => {
    const onLoss = () => void client.listSessions().catch(() => {})
    window.addEventListener(UNAUTHORIZED_EVENT, onLoss)
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, onLoss)
  }, [client])
  return null
}

function Shell() {
  const [path, navigate] = usePath()
  return (
    <ManifestShell
      manifest={manifest}
      apiBase="/api"
      path={path}
      onNavigate={navigate}
      pages={pages}
      icons={icons}
    />
  )
}

export function App() {
  return (
    // The gate renders sign-in and the no-owner screen outside the shell and mounts the console
    // only once a session exists. The owner is created on the host (`cerberus identity setup`).
    <IdentityProvider>
      <IdentityGate appName="cerberus">
        <SessionLossBridge />
        <Shell />
      </IdentityGate>
    </IdentityProvider>
  )
}

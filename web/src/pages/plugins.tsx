import { useEffect, useState } from 'react'
import { Button, EmptyState, SettingsField, SettingsGrid, SettingsPanel, SummaryCards, Textarea } from '@hollis-labs/sysop-ui/ui'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, type PluginHealth, type PluginTrustOptions } from '../api/client'

export function PluginsPage() {
  const plugins = usePoll((signal) => apiClient.listManagedPlugins(signal), 5000)
  const [sessionToken, setSessionToken] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [installPath, setInstallPath] = useState('')
  const [inspectPath, setInspectPath] = useState('')
  const [devMode, setDevMode] = useState(true)
  const [healthByPlugin, setHealthByPlugin] = useState<Record<string, PluginHealth>>({})
  const [previewHealth, setPreviewHealth] = useState<PluginHealth | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    apiClient.getSession().then((session) => {
      if (!cancelled) setSessionToken(session.action_token)
    })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    const items = plugins.data ?? []
    if (items.length === 0) return
    let cancelled = false
    Promise.all(items.map(async (item) => [item.id, await apiClient.getManagedPluginHealth(item.id)] as const))
      .then((pairs) => {
        if (cancelled) return
        const next: Record<string, PluginHealth> = {}
        for (const [id, health] of pairs) next[id] = health
        setHealthByPlugin(next)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [plugins.data])

  if (plugins.error) {
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus plugins"
        title="Could not load plugin state"
        description={plugins.error instanceof Error ? plugins.error.message : String(plugins.error)}
        action={{ label: 'Retry', onClick: plugins.refetch }}
      />
    )
  }

  const items = (plugins.data ?? []).slice().sort((a, b) => a.id.localeCompare(b.id))
  const cards = [
    { label: 'Installed', value: items.length, accentColor: 'var(--color-text)' },
    { label: 'Loaded', value: items.filter((item) => item.loaded).length, accentColor: 'var(--color-status-done)' },
    { label: 'Healthy', value: items.filter((item) => healthByPlugin[item.id]?.healthy).length, accentColor: 'var(--color-warning)' },
  ]

  async function install() {
    if (!sessionToken || !installPath.trim() || busy) return
    setBusy('install')
    setError(null)
    try {
      await apiClient.installManagedPlugin(installPath.trim(), trust(devMode), sessionToken)
      setInstallPath('')
      await plugins.refetch()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  async function inspect() {
    if (!sessionToken || !inspectPath.trim() || busy) return
    setBusy('inspect')
    setError(null)
    try {
      setPreviewHealth(await apiClient.checkPluginHealth(inspectPath.trim(), trust(devMode), sessionToken))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  async function toggleLoad(id: string, loaded: boolean) {
    if (!sessionToken || busy) return
    setBusy(id)
    setError(null)
    try {
      if (loaded) {
        await apiClient.unloadManagedPlugin(id, sessionToken)
      } else {
        await apiClient.loadManagedPlugin(id, sessionToken)
      }
      setHealthByPlugin((current) => {
        const next = { ...current }
        delete next[id]
        return next
      })
      await plugins.refetch()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  async function refreshHealth(id: string) {
    if (busy) return
    setBusy(`health:${id}`)
    setError(null)
    try {
      const health = await apiClient.getManagedPluginHealth(id)
      setHealthByPlugin((current) => ({ ...current, [id]: health }))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="flex h-full min-h-0 w-full flex-col overflow-auto">
      <SummaryCards cards={cards} />
      <div className="space-y-4">
        {error && <div className="mx-4 border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>}

        <section className="grid gap-4 px-4 xl:grid-cols-2">
          <SettingsPanel title="Install managed plugin">
            <div className="space-y-3">
              <input
                value={installPath}
                onChange={(event) => setInstallPath(event.target.value)}
                placeholder="/absolute/path/to/plugin"
                className="w-full border border-border bg-panel-2/60 px-3 py-2 text-sm text-text outline-none transition-colors focus:border-border-strong"
              />
              <label className="flex items-center gap-2 text-xs text-text-soft">
                <input type="checkbox" checked={devMode} onChange={(event) => setDevMode(event.target.checked)} />
                Developer trust policy
              </label>
              <Button variant="secondary" size="sm" disabled={!sessionToken || busy !== null || installPath.trim() === ''} onClick={() => void install()}>
                {busy === 'install' ? 'Installing...' : 'Install'}
              </Button>
            </div>
          </SettingsPanel>

          <SettingsPanel title="Check plugin directory" className="border-b-0">
            <div className="space-y-3">
              <input
                value={inspectPath}
                onChange={(event) => setInspectPath(event.target.value)}
                placeholder="/absolute/path/to/plugin"
                className="w-full border border-border bg-panel-2/60 px-3 py-2 text-sm text-text outline-none transition-colors focus:border-border-strong"
              />
              <Button variant="outline" size="sm" disabled={!sessionToken || busy !== null || inspectPath.trim() === ''} onClick={() => void inspect()}>
                {busy === 'inspect' ? 'Checking...' : 'Check health'}
              </Button>
              {previewHealth && (
                <Textarea
                  readOnly
                  value={JSON.stringify(previewHealth, null, 2)}
                  className="min-h-28 resize-y font-mono text-xs"
                />
              )}
            </div>
          </SettingsPanel>
        </section>

        {plugins.isLoading && items.length === 0 ? (
          <div className="border-b border-border-strong px-4 py-3 text-sm text-text-soft">Loading plugins...</div>
        ) : items.length === 0 ? (
          <div className="px-4 py-4">
            <EmptyState variant="no-results" title="No managed plugins installed." description="Install a connector plugin directory to manage it from the daemon." />
          </div>
        ) : (
          items.map((plugin, index) => {
            const health = healthByPlugin[plugin.id]
            return (
              <section
                key={plugin.id}
                className={index === 0 ? 'bg-panel px-4 py-4' : 'border-t border-border-strong bg-panel px-4 py-4'}
              >
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <div className="text-sm text-text">{plugin.id}</div>
                    <div className="mt-1 text-xs text-text-soft">v{plugin.version} · {plugin.trust_tier || 'unknown trust tier'}</div>
                    <div className="mt-1 font-mono text-xs text-text-soft">{plugin.path}</div>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button variant="outline" size="sm" disabled={!sessionToken || busy !== null} onClick={() => void refreshHealth(plugin.id)}>
                      {busy === `health:${plugin.id}` ? 'Refreshing...' : 'Health'}
                    </Button>
                    <Button variant="secondary" size="sm" disabled={!sessionToken || busy !== null} onClick={() => void toggleLoad(plugin.id, plugin.loaded)}>
                      {busy === plugin.id ? (plugin.loaded ? 'Unloading...' : 'Loading...') : plugin.loaded ? 'Unload' : 'Load'}
                    </Button>
                  </div>
                </div>
                <div className="mt-3">
                  <SettingsGrid className="grid-cols-1 md:grid-cols-3 px-0 py-0">
                    <Metric label="Loaded" value={plugin.loaded ? 'yes' : 'no'} />
                    <Metric label="Healthy" value={health ? (health.healthy ? 'yes' : 'no') : 'unknown'} />
                    <Metric label="Message" value={health?.message || '-'} />
                  </SettingsGrid>
                </div>
              </section>
            )
          })
        )}
      </div>
    </div>
  )
}

function trust(devMode: boolean): PluginTrustOptions {
  return devMode ? { dev_mode: true } : {}
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <SettingsField label={label}>{value}</SettingsField>
  )
}

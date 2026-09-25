import { useEffect, useState } from 'react'
import { Button, Callout, EmptyState, SettingsField, SettingsGrid, SettingsPanel, SummaryCards } from '@hollis-labs/sysop-ui/ui'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, type PluginHealth } from '../api/client'

export function PluginsPage() {
  const plugins = usePoll((signal) => apiClient.listManagedPlugins(signal), 5000)
  const [sessionToken, setSessionToken] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [healthByPlugin, setHealthByPlugin] = useState<Record<string, PluginHealth>>({})
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
        {error && <Callout tone="danger" className="mx-4">{error}</Callout>}

        {/*
          Installing or inspecting a plugin directory runs code the directory
          names, so the console does neither: a browser page must not be able
          to choose code for the daemon to run. Install from a shell, then
          load, unload and check the plugin here by id.
        */}
        <section className="px-4">
          <SettingsPanel title="Install a plugin">
            <div className="space-y-2 text-sm text-text-soft">
              <p>Plugins are installed from your shell, not from the console. Then load it here.</p>
              <pre className="overflow-x-auto border border-border bg-panel-2/60 px-3 py-2 font-mono text-xs text-text">cerberus connectors plugin managed install /absolute/path/to/plugin</pre>
              <p>To try a plugin directory without installing it, run <code className="font-mono text-xs">cerberus connectors plugin health &lt;dir&gt;</code>.</p>
            </div>
          </SettingsPanel>
        </section>

        {plugins.isLoading && items.length === 0 ? (
          <div className="border-b border-border-strong px-4 py-3 text-sm text-text-soft">Loading plugins...</div>
        ) : items.length === 0 ? (
          <div className="px-4 py-4">
            <EmptyState variant="no-results" title="No managed plugins installed." description="Install one from your shell with cerberus connectors plugin managed install <dir>." />
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
                    <div className="mt-1 text-xs text-text-soft">v{plugin.version} · {plugin.origin === 'dev' ? 'dev install · destructive operations refused' : 'installed'}</div>
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

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <SettingsField label={label}>{value}</SettingsField>
  )
}

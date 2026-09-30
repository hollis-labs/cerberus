import { KeyRound } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Button, Callout, EmptyState, Input, Pill, SettingsPanel, SummaryCards } from '@hollis-labs/sysop-ui/ui'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient, type CredentialProvider } from '../api/client'
import { useConsoleWrite } from '../components/plan-confirm'

// The credential editor: every connector that declares a secret, built-in or
// installed plugin, with the secrets it declares. The daemon writes only a
// declared secret, and a value is never shown, only whether one is stored.
export function CredentialsPage() {
  const credentials = usePoll((signal) => apiClient.getCredentials(signal), 5000)
  const [sessionToken, setSessionToken] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const write = useConsoleWrite()
  const [drafts, setDrafts] = useState<Record<string, Record<string, string>>>({})
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    apiClient.getSession().then((session) => {
      if (!cancelled) setSessionToken(session.action_token)
    })
    return () => {
      cancelled = true
    }
  }, [])

  if (credentials.error) {
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus credentials"
        title="Could not load the credential editor"
        description={credentials.error instanceof Error ? credentials.error.message : String(credentials.error)}
        action={{ label: 'Retry', onClick: credentials.refetch }}
      />
    )
  }

  const providers = credentials.data?.providers ?? []
  const secrets = providers.flatMap((provider) => provider.secrets)
  const cards = [
    { label: 'Connectors', value: providers.length, accentColor: 'var(--color-text)' },
    { label: 'Stored', value: secrets.filter((secret) => secret.present).length, accentColor: 'var(--color-status-done)' },
    { label: 'Required, missing', value: secrets.filter((secret) => secret.required && !secret.present).length, accentColor: 'var(--color-text)' },
  ]

  async function save(provider: CredentialProvider) {
    if (!sessionToken || busy) return
    setBusy(provider.id)
    setError(null)
    setNotice(null)
    try {
      const result = await write(apiClient.saveCredentials(provider.id, { secrets: drafts[provider.id] ?? {} }, sessionToken))
      setDrafts((current) => ({ ...current, [provider.id]: {} }))
      if (result?.plugin_reload_error) setError(result.plugin_reload_error)
      else if (result?.plugin_reloaded) setNotice(`Saved, and reloaded the ${provider.id} plugin so it uses the new value.`)
      await credentials.refetch()
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
        {notice && <Callout tone="info" className="mx-4">{notice}</Callout>}

        <SettingsPanel title="Credentials" icon={<KeyRound className="h-4 w-4" />} className="border-b-0">
          <div className="px-4 py-3">
            {providers.length === 0 ? (
              <div className="text-sm text-text-soft">No installed connector declares a credential.</div>
            ) : (
              <div className="grid gap-4 xl:grid-cols-2">
                {providers.map((provider) => (
                  <div key={provider.id} className="rounded-none border border-border bg-bg p-4">
                    <div className="mb-3 flex items-center justify-between gap-3">
                      <div>
                        <div className="text-sm text-text">{provider.id}</div>
                        {provider.version && <div className="mt-1 text-xs text-text-soft">{provider.version}</div>}
                      </div>
                      <Pill tone="neutral">{provider.secrets.length} secrets</Pill>
                    </div>
                    <div className="space-y-3">
                      {provider.secrets.map((secret) => (
                        <label key={secret.name} className="block">
                          <div className="mb-1 flex items-center justify-between gap-3 text-xs uppercase tracking-wide text-text-subtle">
                            <span>{secret.name}{secret.required ? ' (required)' : ''}</span>
                            <Pill tone={secret.present ? 'success' : 'warning'}>{secret.present ? 'Stored' : 'Missing'}</Pill>
                          </div>
                          <Input
                            type={secret.kind === 'credential' ? 'password' : 'text'}
                            value={drafts[provider.id]?.[secret.name] ?? ''}
                            onChange={(event) => setDrafts((current) => ({
                              ...current,
                              [provider.id]: { ...(current[provider.id] ?? {}), [secret.name]: event.target.value },
                            }))}
                            placeholder={secret.present ? 'Leave blank to keep the stored value' : 'Enter a value'}
                          />
                          {secret.description && <div className="mt-1 text-xs text-text-soft">{secret.description}</div>}
                          {secret.env && <div className="mt-1 text-xs text-text-soft">Also read from {secret.env}</div>}
                        </label>
                      ))}
                      <Button variant="secondary" size="sm" disabled={!sessionToken || busy !== null} onClick={() => void save(provider)}>
                        {busy === provider.id ? 'Saving...' : 'Save credentials'}
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </SettingsPanel>
      </div>
    </div>
  )
}

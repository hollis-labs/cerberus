import { useEffect, useState } from 'react'
import { Button, EmptyState, SummaryCards, Textarea, usePoll } from '@hollis-labs/sysop-ui'
import { apiClient, type DeploymentProfile, type DeploymentRunResult, type InfraProvider } from '../api/client'

export function DeploymentsPage() {
  const infra = usePoll((signal) => apiClient.getInfra(signal), 5000)
  const [sessionToken, setSessionToken] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [providerDrafts, setProviderDrafts] = useState<Record<string, Record<string, string>>>({})
  const [profileDrafts, setProfileDrafts] = useState<Record<string, DeploymentProfile>>({})
  const [secretDrafts, setSecretDrafts] = useState<Record<string, Record<string, string>>>({})
  const [runResults, setRunResults] = useState<Record<string, DeploymentRunResult>>({})
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
    const data = infra.data
    if (!data) return
    setProviderDrafts((current) => {
      const next = { ...current }
      for (const provider of data.providers) {
        next[provider.id] = { ...(provider.values ?? {}), ...(current[provider.id] ?? {}) }
      }
      return next
    })
    setProfileDrafts((current) => {
      const next = { ...current }
      for (const profile of [...data.deployments, ...(data.suggestions ?? [])]) {
        next[profile.id] = { ...profile, ...(current[profile.id] ?? {}) }
      }
      return next
    })
  }, [infra.data])

  if (infra.error) {
    return (
      <EmptyState
        variant="error"
        eyebrow="Cerberus infra"
        title="Could not load deployment surfaces"
        description={infra.error instanceof Error ? infra.error.message : String(infra.error)}
        action={{ label: 'Retry', onClick: infra.refetch }}
      />
    )
  }

  const providers = infra.data?.providers ?? []
  const deployments = infra.data?.deployments ?? []
  const suggestions = infra.data?.suggestions ?? []
  const cards = [
    { label: 'Providers', value: providers.length, accentColor: 'var(--color-text)' },
    { label: 'Profiles', value: deployments.length, accentColor: 'var(--color-status-done)' },
    { label: 'Suggestions', value: suggestions.length, accentColor: 'var(--color-warning)' },
    { label: 'Deployable now', value: deployments.filter((profile) => profile.provider === 'vercel').length, accentColor: 'var(--color-text)' },
  ]

  async function saveProvider(provider: InfraProvider) {
    if (!sessionToken || busy) return
    setBusy(`provider:${provider.id}`)
    setError(null)
    try {
      await apiClient.saveInfraProvider(provider.id, {
        values: providerDrafts[provider.id] ?? {},
        secrets: secretDrafts[provider.id] ?? {},
      }, sessionToken)
      setSecretDrafts((current) => ({ ...current, [provider.id]: {} }))
      await infra.refetch()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  async function saveProfile(profileID: string) {
    if (!sessionToken || busy) return
    const profile = profileDrafts[profileID]
    if (!profile) return
    setBusy(`save:${profileID}`)
    setError(null)
    try {
      await apiClient.saveDeployment(profile, sessionToken)
      await infra.refetch()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  async function deleteProfile(profileID: string) {
    if (!sessionToken || busy) return
    setBusy(`delete:${profileID}`)
    setError(null)
    try {
      await apiClient.deleteDeployment(profileID, sessionToken)
      await infra.refetch()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  async function runProfile(profileID: string) {
    if (!sessionToken || busy) return
    setBusy(`run:${profileID}`)
    setError(null)
    try {
      const result = await apiClient.runDeployment(profileID, sessionToken)
      setRunResults((current) => ({ ...current, [profileID]: result }))
      await infra.refetch()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="flex h-full min-h-0 w-full flex-col overflow-auto">
      <SummaryCards cards={cards} />
      <div className="space-y-4 p-4">
        {error && <div className="border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>}

        <section className="border border-border bg-panel p-4">
          <div className="mb-3 text-sm text-text">Provider settings</div>
          <div className="grid gap-4 xl:grid-cols-2">
            {providers.map((provider) => (
              <div key={provider.id} className="border border-border-soft bg-panel-2/40 p-4">
                <div className="mb-3 text-sm text-text">{provider.label}</div>
                <div className="space-y-3">
                  {provider.fields.map((field) => (
                    <label key={field.name} className="block">
                      <div className="mb-1 text-xs uppercase tracking-wide text-muted">{field.label}</div>
                      <input
                        value={providerDrafts[provider.id]?.[field.name] ?? ''}
                        onChange={(event) => setProviderDrafts((current) => ({
                          ...current,
                          [provider.id]: { ...(current[provider.id] ?? {}), [field.name]: event.target.value },
                        }))}
                        className="w-full border border-border bg-panel px-3 py-2 text-sm text-text outline-none transition-colors focus:border-border-strong"
                      />
                      {field.description && <div className="mt-1 text-xs text-text-soft">{field.description}</div>}
                    </label>
                  ))}
                  {provider.secrets.map((field) => (
                    <label key={field.name} className="block">
                      <div className="mb-1 flex items-center justify-between gap-3 text-xs uppercase tracking-wide text-muted">
                        <span>{field.label}</span>
                        <span>{field.present ? 'Stored' : 'Missing'}</span>
                      </div>
                      <input
                        type="password"
                        value={secretDrafts[provider.id]?.[field.name] ?? ''}
                        onChange={(event) => setSecretDrafts((current) => ({
                          ...current,
                          [provider.id]: { ...(current[provider.id] ?? {}), [field.name]: event.target.value },
                        }))}
                        placeholder={field.present ? 'Leave blank to keep current secret' : 'Enter secret'}
                        className="w-full border border-border bg-panel px-3 py-2 text-sm text-text outline-none transition-colors focus:border-border-strong"
                      />
                      {field.description && <div className="mt-1 text-xs text-text-soft">{field.description}</div>}
                    </label>
                  ))}
                  <Button variant="secondary" size="sm" disabled={!sessionToken || busy !== null} onClick={() => void saveProvider(provider)}>
                    {busy === `provider:${provider.id}` ? 'Saving...' : 'Save provider'}
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </section>

        <section className="border border-border bg-panel p-4">
          <div className="mb-3 text-sm text-text">Deployment profiles</div>
          {deployments.length === 0 && suggestions.length === 0 ? (
            <div className="text-sm text-text-soft">No deployment profiles yet.</div>
          ) : (
            <div className="space-y-4">
              {deployments.map((profile) => (
                <DeploymentCard
                  key={profile.id}
                  profile={profileDrafts[profile.id] ?? profile}
                  result={runResults[profile.id]}
                  busy={busy}
                  onChange={(next) => setProfileDrafts((current) => ({ ...current, [profile.id]: next }))}
                  onSave={() => void saveProfile(profile.id)}
                  onDelete={() => void deleteProfile(profile.id)}
                  onRun={() => void runProfile(profile.id)}
                />
              ))}
              {suggestions.length > 0 && (
                <div className="space-y-4">
                  <div className="text-xs uppercase tracking-wide text-muted">Suggested</div>
                  {suggestions.map((profile) => (
                    <DeploymentCard
                      key={profile.id}
                      profile={profileDrafts[profile.id] ?? profile}
                      result={runResults[profile.id]}
                      busy={busy}
                      onChange={(next) => setProfileDrafts((current) => ({ ...current, [profile.id]: next }))}
                      onSave={() => void saveProfile(profile.id)}
                      onDelete={undefined}
                      onRun={undefined}
                    />
                  ))}
                </div>
              )}
            </div>
          )}
        </section>
      </div>
    </div>
  )
}

function DeploymentCard({
  profile,
  result,
  busy,
  onChange,
  onSave,
  onDelete,
  onRun,
}: {
  profile: DeploymentProfile
  result?: DeploymentRunResult
  busy: string | null
  onChange: (profile: DeploymentProfile) => void
  onSave: () => void
  onDelete?: () => void
  onRun?: () => void
}) {
  return (
    <div className="border border-border-soft bg-panel-2/40 p-4">
      <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="text-sm text-text">{profile.name}</div>
          <div className="mt-1 text-xs text-text-soft">{profile.provider} · {profile.repo_path}</div>
        </div>
        <div className="flex flex-wrap gap-2">
          {onRun && (
            <Button variant="secondary" size="sm" disabled={busy !== null} onClick={onRun}>
              {busy === `run:${profile.id}` ? 'Deploying...' : 'Run'}
            </Button>
          )}
          <Button variant="outline" size="sm" disabled={busy !== null} onClick={onSave}>
            {busy === `save:${profile.id}` ? 'Saving...' : profile.suggested ? 'Adopt' : 'Save'}
          </Button>
          {onDelete && (
            <Button variant="outline" size="sm" disabled={busy !== null} onClick={onDelete}>
              {busy === `delete:${profile.id}` ? 'Removing...' : 'Delete'}
            </Button>
          )}
        </div>
      </div>
      <div className="grid gap-3 lg:grid-cols-2">
        <Field label="ID" value={profile.id} onChange={(value) => onChange({ ...profile, id: value })} />
        <Field label="Name" value={profile.name} onChange={(value) => onChange({ ...profile, name: value })} />
        <Field label="Provider" value={profile.provider} onChange={(value) => onChange({ ...profile, provider: value })} />
        <Field label="Repo path" value={profile.repo_path} onChange={(value) => onChange({ ...profile, repo_path: value })} />
        <Field label="Domain" value={profile.domain ?? ''} onChange={(value) => onChange({ ...profile, domain: value })} />
        <Field label="DNS provider" value={profile.dns_provider ?? ''} onChange={(value) => onChange({ ...profile, dns_provider: value })} />
        <Field label="Git remote" value={profile.git_remote ?? ''} onChange={(value) => onChange({ ...profile, git_remote: value })} />
        <Field label="Production branch" value={profile.production_branch ?? ''} onChange={(value) => onChange({ ...profile, production_branch: value })} />
        <Field label="Git owner" value={profile.git_owner ?? ''} onChange={(value) => onChange({ ...profile, git_owner: value })} />
        <Field label="Git repo" value={profile.git_repo ?? ''} onChange={(value) => onChange({ ...profile, git_repo: value })} />
        <Field label="Vercel project" value={profile.vercel_project ?? ''} onChange={(value) => onChange({ ...profile, vercel_project: value })} />
        <Field label="Vercel scope" value={profile.vercel_scope ?? ''} onChange={(value) => onChange({ ...profile, vercel_scope: value })} />
        <Field label="Cloudflare zone ID" value={profile.cloudflare_zone_id ?? ''} onChange={(value) => onChange({ ...profile, cloudflare_zone_id: value })} />
        <Field label="Namecheap domain" value={profile.namecheap_domain ?? ''} onChange={(value) => onChange({ ...profile, namecheap_domain: value })} />
        <Field label="Preflight command" value={profile.preflight_command ?? ''} onChange={(value) => onChange({ ...profile, preflight_command: value })} />
        <Field label="Build command" value={profile.build_command ?? ''} onChange={(value) => onChange({ ...profile, build_command: value })} />
      </div>
      <div className="mt-3">
        <Field label="Deploy command" value={profile.deploy_command ?? ''} onChange={(value) => onChange({ ...profile, deploy_command: value })} />
      </div>
      {result && (
        <div className="mt-4 space-y-3">
          <div className="text-xs uppercase tracking-wide text-muted">Last run</div>
          <div className="grid gap-3 md:grid-cols-2 text-sm">
            <div className="text-text-soft">Branch: {result.git.branch || '-'}</div>
            <div className="text-text-soft">Commit: {result.git.commit || '-'}</div>
            <div className="text-text-soft">Dirty: {result.git.dirty ? 'yes' : 'no'}</div>
            <div className="text-text-soft">URL: {result.deployment_url || '-'}</div>
          </div>
          {result.steps.map((step) => (
            <Textarea
              key={`${profile.id}:${step.name}`}
              readOnly
              value={[`${step.name}: ${step.success ? 'ok' : 'failed'}`, step.command, step.error, step.output].filter(Boolean).join('\n\n')}
              className="min-h-28 resize-y font-mono text-xs"
            />
          ))}
        </div>
      )}
    </div>
  )
}

function Field({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  return (
    <label className="block">
      <div className="mb-1 text-xs uppercase tracking-wide text-muted">{label}</div>
      <input
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className="w-full border border-border bg-panel px-3 py-2 text-sm text-text outline-none transition-colors focus:border-border-strong"
      />
    </label>
  )
}

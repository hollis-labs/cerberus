import type { PlanTarget, PlanView } from '../api/client'

// PlanBody is a plan as a person reads it before confirming or approving
// it: what would run, what it binds, and the plans it runs in turn.

export function targetLine(t: PlanTarget): string {
  const fields = t.fields ?? {}
  const name = t.resource || fields[Object.keys(fields).sort()[0]] || ''
  const labels = [`env ${t.env || 'unknown'}`, `owner ${t.owner || 'unknown'}`, `admin ${t.admin || 'unknown'}`]
  if (t.tags && t.tags.length > 0) labels.push(`tags ${t.tags.join(',')}`)
  return `${[t.kind, name].filter(Boolean).join(' ')} (${labels.join(', ')})`
}

export function PlanBody({ plan }: { plan: PlanView }) {
  const lines: string[] = []
  if (plan.state) lines.push(`State:    ${plan.state}`)
  if (plan.source) lines.push(`Source:   ${plan.source.path} at ${plan.source.head}${plan.source.dirty ? ' (uncommitted changes)' : ''}`)
  if (plan.artifact) lines.push(`Artifact: ${plan.artifact}`)
  if (plan.preview !== undefined) lines.push(`Preview (${plan.preview_kind || 'host'}): ${JSON.stringify(plan.preview)}`)
  if (plan.plugin_entrypoint_sha256) lines.push(`Plugin:   ${plan.plugin_entrypoint_sha256}`)
  ;(plan.steps ?? []).forEach((s, i) => {
    lines.push(`Step ${i + 1}:   ${s.name}: ${s.command}`)
    if (s.dir) lines.push(`          in ${s.dir}`)
    if (s.env && s.env.length > 0) lines.push(`          env: ${s.env.join(' ')}`)
  })
  if (plan.digests) lines.push(`Binds:    ${Object.keys(plan.digests).sort().join(', ')}`)
  return (
    <div className="space-y-2">
      {lines.length > 0 && (
        <pre className="max-h-48 overflow-auto rounded border border-border-strong p-2 font-mono text-xs whitespace-pre-wrap break-all">{lines.join('\n')}</pre>
      )}
      {(plan.actions ?? []).map((a, i) => (
        <div key={i} className="space-y-1 pl-3">
          <div className="font-semibold">
            {a.operation} {targetLine(a.target)}
          </div>
          <PlanBody plan={a} />
        </div>
      ))}
    </div>
  )
}

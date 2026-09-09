import { Callout } from '@hollis-labs/sysop-ui/ui'
import { usePoll } from '@hollis-labs/sysop-ui/api'
import { apiClient } from '../api/client'

/**
 * ResolveNotice tells the operator a list may be short because registry
 * resolution dropped a config, rather than letting a partial list read
 * as the whole inventory.
 *
 * It polls the registry audit rather than the list endpoint: the list
 * wire shapes are bare arrays shared with the CLI and MCP, and this
 * page-level explanation does not belong inside a row. Poll interval is
 * deliberately slower than the lists' — registry contents change when
 * someone registers or edits a config, not per tick.
 */
export function ResolveNotice() {
  const registry = usePoll((signal) => apiClient.listRegistry(signal), 15000)

  const skipped = registry.data?.skipped ?? []
  const warned = registry.data?.warned ?? []
  if (skipped.length === 0 && warned.length === 0) return null

  const parts: string[] = []
  if (skipped.length > 0) parts.push(`${skipped.length} config(s) skipped`)
  if (warned.length > 0) parts.push(`${warned.length} with warnings`)

  return (
    <Callout tone={skipped.length > 0 ? 'warning' : 'info'} className="m-3" title={parts.join(', ')}>
      <div className="text-[11px]">
        {skipped.length > 0 && (
          <div>
            <span className="text-text-subtle">Skipped — not in this list: </span>
            <span className="font-mono">{skipped.map((r) => r.owner).join(', ')}</span>
          </div>
        )}
        {warned.length > 0 && (
          <div>
            <span className="text-text-subtle">Resolved with warnings: </span>
            <span className="font-mono">{warned.map((r) => r.owner).join(', ')}</span>
          </div>
        )}
        <div className="mt-1">
          <a className="underline" href="/registry">
            Open Registry
          </a>{' '}
          <span className="text-text-subtle">for the reason each one was dropped.</span>
        </div>
      </div>
    </Callout>
  )
}

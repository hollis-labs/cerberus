import { ConfirmDialog } from '@hollis-labs/sysop-ui/ui'
import type { ResourceAction } from '../api/client'

// The effect class of each resource action, from the local connector's
// contract. Every one needs acknowledgment, which the console gives only
// from this confirm step: the request carries acknowledged=true because the
// operator confirmed it, never by default.
export const RESOURCE_ACTION_EFFECT: Record<ResourceAction, string> = {
  deploy: 'lifecycle',
  apply: 'lifecycle',
  reload: 'lifecycle',
  stop: 'lifecycle',
  sync: 'write',
  remove: 'destructive',
}

export interface PendingConfirm {
  // What is being done, as a verb: "Deploy", "Run".
  verb: string
  // What it is done to: a resource or pipeline name.
  subject: string
  effect: string
  // For exec: the commands that will run, in order, and where. The operator
  // confirms against the plan they saw (Decision 3).
  commands?: string[]
  cwd?: string
  run: () => Promise<void>
}

export function ActionConfirm({
  pending,
  busy,
  onClose,
}: {
  pending: PendingConfirm | null
  busy: boolean
  onClose: () => void
}) {
  return (
    <ConfirmDialog
      open={pending !== null}
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
      title={pending ? `${pending.verb} ${pending.subject}?` : ''}
      description={
        pending ? (
          <div className="space-y-2">
            <div>{`This is ${/^[aeiou]/.test(pending.effect) ? 'an' : 'a'} ${pending.effect} operation, and Cerberus runs it only with your acknowledgment. Confirming sends it.`}</div>
            {pending.commands && (
              <div className="space-y-1">
                <div className="text-xs text-text-soft">{pending.cwd ? `Runs in ${pending.cwd}:` : 'Runs:'}</div>
                <pre className="max-h-48 overflow-auto rounded border border-border-strong p-2 font-mono text-xs whitespace-pre-wrap break-all">
                  {pending.commands.join('\n')}
                </pre>
              </div>
            )}
          </div>
        ) : undefined
      }
      confirmLabel={pending ? pending.verb : 'Confirm'}
      destructive={pending?.effect === 'destructive' || pending?.effect === 'exec'}
      busy={busy}
      onConfirm={async () => {
        if (!pending) return
        await pending.run()
        onClose()
      }}
    />
  )
}

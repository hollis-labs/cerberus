import { Pill } from "@hollis-labs/design-components"
import { useState } from "react"
import { cerberus } from "../api/cerberus"
import type { ResourceAction, ResourceInfo } from "../api/types"
import { GatedButton, Loadable, Page, Section, useLoad } from "./common"
import { cells, DataTable, dash } from "./table"

const POLL_MS = 5_000

// The verbs and how they read; remove is the destructive one (console-api-contract.md 3.3).
const VERBS: ResourceAction[] = ["apply", "deploy", "reload", "stop", "sync", "remove"]

function tone(status?: string) {
  switch (status) {
    case "running":
      return "success" as const
    case "failed":
    case "crashed":
      return "danger" as const
    case "stopped":
    case "unknown":
      return "warning" as const
  }
  return undefined
}

// One resource: its status (GET /api/resources/{id}/status), its logs (GET .../logs) and the verbs.
function ResourceDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const status = useLoad(() => cerberus.getResource(id), POLL_MS)
  const [stream, setStream] = useState("stdout")
  const logs = useLoad(() => cerberus.getLogs(id, stream, 120), POLL_MS)
  return (
    <Section title={`Resource ${id}`}>
      <button type="button" className="text-fg-muted" onClick={onClose}>
        Back to the list
      </button>
      <Loadable load={status} what="the resource's status">
        {(st) => (
          <ul>
            <li>
              Status: <Pill tone={tone(st.status)}>{st.status}</Pill>
            </li>
            <li>Mode: {dash(st.mode)}</li>
            <li>Supervisor: {dash(st.supervisor)}</li>
            <li>Unit: {dash(st.service_name)}</li>
            {st.recommended_reason ? <li>Context: {st.recommended_reason}</li> : null}
            {st.recommended_next_step ? <li>Next step: {st.recommended_next_step}</li> : null}
          </ul>
        )}
      </Loadable>
      <div className="flex flex-wrap gap-2">
        {VERBS.map((v) => (
          <GatedButton
            key={v}
            label={v}
            danger={v === "remove"}
            write={() => cerberus.resourceAction(id, v, true)}
            onDone={status.reload}
          />
        ))}
      </div>
      <label className="flex items-center gap-2">
        <span className="text-fg-muted">Logs</span>
        <select
          className="rounded-md border border-border bg-bg px-3 py-2"
          value={stream}
          onChange={(e) => setStream(e.target.value)}
        >
          <option value="stdout">stdout</option>
          <option value="stderr">stderr</option>
        </select>
      </label>
      <Loadable load={logs} what="the logs">
        {(l) => <pre className="mono body-text panel">{l.content || "(empty)"}</pre>}
      </Loadable>
    </Section>
  )
}

// Resources (GET /api/resources), each opening its detail.
export function ResourcesPage() {
  const list = useLoad(() => cerberus.listResources(), POLL_MS)
  const [open, setOpen] = useState("")
  return (
    <Page>
      {open ? (
        <ResourceDetail id={open} onClose={() => setOpen("")} />
      ) : (
        <Section title="Resources">
          <Loadable load={list} what="resources">
            {(rows: ResourceInfo[]) => (
              <DataTable
                label="Resources"
                head={["ID", "Project", "Type", "Mode", "Status", "Env", "Owner"]}
                rows={rows}
                row={(r) =>
                  cells(
                    <button type="button" className="mono underline" onClick={() => setOpen(r.id)}>
                      {r.id}
                    </button>,
                    r.project,
                    r.type,
                    dash(r.mode),
                    <Pill tone={tone(r.status)}>{dash(r.status)}</Pill>,
                    dash(r.env),
                    dash(r.owner),
                  )
                }
                empty="No resources are registered."
              />
            )}
          </Loadable>
        </Section>
      )}
    </Page>
  )
}

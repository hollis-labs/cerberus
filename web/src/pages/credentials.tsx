import { Input } from "@hollis-labs/design-components"
import { useState } from "react"
import { cerberus } from "../api/cerberus"
import type { CredentialProvider } from "../api/types"
import { GatedButton, Loadable, Page, Section, useLoad } from "./common"
import { cells, DataTable, dash } from "./table"

// One provider's secrets: presence only, never values; setting one is a gated admin write.
function Provider({ p, onSaved }: { p: CredentialProvider; onSaved: () => void }) {
  const [name, setName] = useState(p.secrets[0]?.name ?? "")
  const [value, setValue] = useState("")
  return (
    <Section title={p.id}>
      <DataTable
        label={`${p.id} secrets`}
        head={["Name", "Kind", "Stored", "Required"]}
        rows={p.secrets}
        row={(s) =>
          cells(<span className="mono">{s.name}</span>, s.kind, s.stored, s.required ? "yes" : "no")
        }
        empty="This provider declares no secrets."
      />
      {p.secrets.length > 0 ? (
        <div className="flex flex-wrap items-end gap-2">
          <select
            aria-label="Secret"
            className="rounded-md border border-border bg-bg px-3 py-2"
            value={name}
            onChange={(e) => setName(e.target.value)}
          >
            {p.secrets.map((s) => (
              <option key={s.name} value={s.name}>
                {s.name}
              </option>
            ))}
          </select>
          <Input
            aria-label="New value"
            type="password"
            autoComplete="off"
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
          <GatedButton
            label="Save"
            write={() => cerberus.saveCredentials(p.id, { secrets: { [name]: value } })}
            onDone={() => {
              setValue("")
              onSaved()
            }}
          />
        </div>
      ) : null}
      <p className="text-fg-muted">{dash(p.version)}</p>
    </Section>
  )
}

// Connector credentials (GET /api/credentials).
export function CredentialsPage() {
  const creds = useLoad(() => cerberus.listCredentials(), 5_000)
  return (
    <Page>
      <Loadable load={creds} what="credentials">
        {(c) =>
          c.providers.length === 0 ? (
            <p className="text-fg-muted">No connector needs credentials.</p>
          ) : (
            c.providers.map((p) => <Provider key={p.id} p={p} onSaved={creds.reload} />)
          )
        }
      </Loadable>
    </Page>
  )
}

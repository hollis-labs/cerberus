import { Input } from "@hollis-labs/design-components"
import { useState } from "react"
import { cerberus } from "../api/cerberus"
import { GatedButton, Loadable, Page, Section, useLoad } from "./common"
import { cells, DataTable, dash } from "./table"

// Registered descriptors, their health and validation (GET /api/registry, /registry/health,
// /config/validate); register and deregister are gated admin writes.
export function RegistryPage() {
  const registry = useLoad(() => cerberus.listRegistry(), 5_000)
  const validate = useLoad(() => cerberus.configValidate(), 0)
  const [path, setPath] = useState("")
  return (
    <Page>
      <Section title="Registry">
        <Loadable load={registry} what="the registry">
          {(r) => (
            <>
              <p className="text-fg-muted">
                {r.summary.entries} registered, {r.summary.healthy} healthy, {r.summary.unhealthy}{" "}
                unhealthy
              </p>
              <DataTable
                label="Registered descriptors"
                head={["Owner", "Kind", "Resources", "Health", "Path", ""]}
                rows={r.entries}
                row={(e) =>
                  cells(
                    <span className="mono">{e.owner}</span>,
                    e.kind,
                    e.resource_count,
                    dash(e.health_status),
                    <span className="mono">{e.path}</span>,
                    <GatedButton
                      label="Deregister"
                      danger
                      write={() => cerberus.deregisterOwner(e.owner)}
                      onDone={registry.reload}
                    />,
                  )
                }
                empty="Nothing is registered."
              />
            </>
          )}
        </Loadable>
      </Section>
      <Section title="Register a descriptor">
        <Input
          aria-label="Descriptor path"
          placeholder="/path/to/app.cerberus.yaml"
          value={path}
          onChange={(e) => setPath(e.target.value)}
        />
        <GatedButton
          label="Register"
          write={() => cerberus.registerConfig(path)}
          onDone={registry.reload}
        />
      </Section>
      <Section title="Validation">
        <Loadable load={validate} what="validation">
          {(v) => (
            <DataTable
              label="Validation"
              head={["File", "Kind", "Result"]}
              rows={v.registered}
              row={(f) =>
                cells(
                  <span className="mono">{f.path}</span>,
                  f.kind,
                  f.ok ? "ok" : (f.errors ?? []).join("; "),
                )
              }
              empty="No registered files to validate."
            />
          )}
        </Loadable>
      </Section>
    </Page>
  )
}

import { Pill } from "@hollis-labs/design-components"
import { cerberus } from "../api/cerberus"
import { GatedButton, Loadable, Page, Section, useLoad } from "./common"
import { cells, DataTable, dash } from "./table"

// Managed connector plugins (GET /api/plugins/connectors; load and unload are gated admin writes).
export function PluginsPage() {
  const plugins = useLoad(() => cerberus.listPlugins(), 5_000)
  return (
    <Page>
      <Section title="Plugins">
        <Loadable load={plugins} what="plugins">
          {(list) => (
            <DataTable
              label="Plugins"
              head={["ID", "Version", "Origin", "State", ""]}
              rows={list}
              row={(p) =>
                cells(
                  <span className="mono">{p.id}</span>,
                  p.version,
                  dash(p.origin),
                  <Pill tone={p.loaded ? "success" : undefined}>
                    {p.loaded ? "loaded" : "unloaded"}
                  </Pill>,
                  p.loaded ? (
                    <GatedButton
                      label="Unload"
                      write={() => cerberus.unloadPlugin(p.id)}
                      onDone={plugins.reload}
                    />
                  ) : (
                    <GatedButton
                      label="Load"
                      write={() => cerberus.loadPlugin(p.id)}
                      onDone={plugins.reload}
                    />
                  ),
                )
              }
              empty="No managed plugins are installed."
            />
          )}
        </Loadable>
      </Section>
    </Page>
  )
}

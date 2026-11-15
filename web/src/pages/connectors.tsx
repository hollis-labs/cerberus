import { Pill } from "@hollis-labs/design-components"
import { cerberus } from "../api/cerberus"
import { GatedButton, Loadable, Page, Section, useLoad } from "./common"
import { cells, DataTable, dash } from "./table"

// Connector definitions and their operations (GET /api/connectors); an operation run is a gated
// admin write, sent as a dry run where the operation supports one.
export function ConnectorsPage() {
  const connectors = useLoad(() => cerberus.listConnectors(), 15_000)
  return (
    <Page>
      <Loadable load={connectors} what="connectors">
        {(list) =>
          list.length === 0 ? (
            <p className="text-fg-muted">No connectors are available.</p>
          ) : (
            list.map((c) => (
              <Section key={c.id} title={`${c.id} ${c.version}`}>
                <DataTable
                  label={`${c.id} operations`}
                  head={["Operation", "Effect", "Description", ""]}
                  rows={c.operations}
                  row={(op) =>
                    cells(
                      <span className="mono">{op.name}</span>,
                      op.destructive ? (
                        <Pill tone="danger">{dash(op.effect)}</Pill>
                      ) : (
                        dash(op.effect)
                      ),
                      dash(op.description),
                      <GatedButton
                        label={op.supports_dry ? "Dry run" : "Run"}
                        danger={op.destructive}
                        write={() =>
                          cerberus.connectorOperation(c.id, op.name, {
                            dry_run: Boolean(op.supports_dry),
                            acknowledged: true,
                          })
                        }
                      />,
                    )
                  }
                  empty="This connector has no operations."
                />
              </Section>
            ))
          )
        }
      </Loadable>
    </Page>
  )
}

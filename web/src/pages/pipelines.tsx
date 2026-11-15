import { cerberus } from "../api/cerberus"
import { GatedButton, Loadable, Page, Section, useLoad } from "./common"
import { cells, DataTable, dash } from "./table"

// Pipelines and running one (GET /api/pipelines; POST /api/pipelines/{id}/run, gated).
export function PipelinesPage() {
  const pipelines = useLoad(() => cerberus.listPipelines(), 5_000)
  return (
    <Page>
      <Section title="Pipelines">
        <Loadable load={pipelines} what="pipelines">
          {(list) => (
            <DataTable
              label="Pipelines"
              head={["ID", "Name", "Stages", "Description", ""]}
              rows={list}
              row={(p) =>
                cells(
                  <span className="mono">{p.id}</span>,
                  p.name,
                  p.stage_count,
                  dash(p.description),
                  <GatedButton label="Run" write={() => cerberus.runPipeline(p.id, true)} />,
                )
              }
              empty="No pipelines are defined."
            />
          )}
        </Loadable>
      </Section>
    </Page>
  )
}

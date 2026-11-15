import { cerberus } from "../api/cerberus"
import { Loadable, Page, Section, useLoad } from "./common"
import { cells, DataTable, dash } from "./table"

// Projects from the registered descriptors (GET /api/projects).
export function ProjectsPage() {
  const projects = useLoad(() => cerberus.listProjects(), 5_000)
  return (
    <Page>
      <Section title="Projects">
        <Loadable load={projects} what="projects">
          {(list) => (
            <DataTable
              label="Projects"
              head={["ID", "Name", "Description"]}
              rows={list}
              row={(p) => cells(<span className="mono">{p.id}</span>, p.name, dash(p.description))}
              empty="No projects are registered."
            />
          )}
        </Loadable>
      </Section>
    </Page>
  )
}

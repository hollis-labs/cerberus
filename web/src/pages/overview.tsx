import { Metric } from "@hollis-labs/design-components"
import { cerberus } from "../api/cerberus"
import { Loadable, Page, Section, useLoad } from "./common"

const POLL_MS = 5_000

// Counts and health at a glance (GET /api/overview, GET /api/system).
export function OverviewPage() {
  const overview = useLoad(() => cerberus.overview(), POLL_MS)
  const system = useLoad(() => cerberus.system(), POLL_MS)
  return (
    <Page>
      <Section title="Overview">
        <Loadable load={overview} what="the overview">
          {(o) => (
            <div className="flex flex-wrap gap-4">
              <Metric label="Resources" value={o.inventory.resources} />
              <Metric label="Running" value={o.runtime.running} />
              <Metric label="Attention" value={o.runtime.attention} />
              <Metric label="Stopped" value={o.runtime.stopped} />
              <Metric label="Projects" value={o.inventory.projects} />
              <Metric label="Pipelines" value={o.inventory.pipelines} />
              <Metric label="Connectors" value={o.inventory.connectors} />
              <Metric label="Plugins" value={o.inventory.plugins} />
            </div>
          )}
        </Loadable>
      </Section>
      <Section title="System">
        <Loadable load={system} what="the system summary">
          {(s) => (
            <ul>
              <li>
                Resolved: {s.resolved_projects} projects, {s.resolved_resources} resources,{" "}
                {s.resolved_pipelines} pipelines
              </li>
              {(s.resolve_warnings ?? []).map((w) => (
                <li key={w} className="text-warning">
                  {w}
                </li>
              ))}
            </ul>
          )}
        </Loadable>
      </Section>
    </Page>
  )
}

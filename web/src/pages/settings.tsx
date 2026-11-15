import { cerberus } from "../api/cerberus"
import { GatedButton, Loadable, Page, Section, useLoad } from "./common"
import { cells, DataTable } from "./table"

// The configuration summary and backups (GET /api/settings, GET /api/config/backups; restore is a
// gated admin write).
export function SettingsPage() {
  const settings = useLoad(() => cerberus.settings(), 5_000)
  const backups = useLoad(() => cerberus.listConfigBackups(), 5_000)
  return (
    <Page>
      <Section title="Settings">
        <Loadable load={settings} what="settings">
          {(s) => (
            <ul>
              <li>Config version {s.version}</li>
              <li>
                Install after build by default: {s.install_after_build_default ? "yes" : "no"}
              </li>
              <li>
                Resolved: {s.resolved_projects} projects, {s.resolved_resources} resources,{" "}
                {s.resolved_pipelines} pipelines
              </li>
            </ul>
          )}
        </Loadable>
      </Section>
      <Section title="Backups">
        <Loadable load={backups} what="configuration backups">
          {(b) => (
            <DataTable
              label="Backups"
              head={["Name", "Modified", "Size", ""]}
              rows={b.backups}
              row={(x) =>
                cells(
                  <span className="mono">{x.name}</span>,
                  x.modified,
                  x.size,
                  <GatedButton
                    label="Restore"
                    danger
                    write={() => cerberus.restoreConfigBackup(x.path)}
                  />,
                )
              }
              empty="No configuration backups."
            />
          )}
        </Loadable>
      </Section>
    </Page>
  )
}

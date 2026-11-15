package legacyimport

// rulings are the treatments the lead ruled on for slice 10a (the rows the spec left pending).
// They are fixed behavior, not options; the report lists them so a reader sees what a run did
// with each without reading the code.
//
//   - R8 hosts.history.jsonl and its rotations, R15 hosts.yaml and R18 plugin-connectors.json stay
//     in place in the adopted root, which the new build reads in place, and are excluded from the
//     D3 manifest like registry.yaml: new edits them while it runs, and a rollback keeps those
//     edits rather than refusing on them.
//   - R11 a credential-issuance/<id>/ with no receipt (a crash between the reservation and its
//     first receipt) is reported as reservations_without_receipt and left in place.
//   - R13 the transformed config.yaml, events.yaml and connector-config.yaml keys are written to
//     <data>/migration/config-fragment.yaml; the operator merges it into the SUBSTRATE_CONFIG
//     override file in a runbook step. An unknown key refuses the run.
//   - R21 oauth/ and alerts/ are copied inert to <data>/oauth and <data>/alerts and reported;
//     nothing in the new build reads them yet.
//   - D3 the manifest's max_time is host_status by last_attempt and host_rollup_state by
//     watermark (hostdump.Tables).
func rulings() map[string]string {
	return map[string]string{
		"R8 hosts.history.jsonl":  "in place in the adopted root; excluded from the D3 manifest",
		"R11 empty reservations":  "reported as reservations_without_receipt; left in place",
		"R13 config output":       "fragment file <data>/migration/config-fragment.yaml; unknown keys refused",
		"R15 hosts.yaml":          "in place in the adopted root; excluded from the D3 manifest",
		"R18 plugin-connectors":   "in place in the adopted root; excluded from the D3 manifest",
		"R21 oauth/, alerts/":     "carried inert to <data>/oauth and <data>/alerts",
		"D3 manifest max_time":    "host_status by last_attempt, host_rollup_state by watermark",
		"D3 manifest extra files": "hosts.history.jsonl*, hosts.yaml, plugin-connectors.json excluded (in place; rollback keeps new's edits)",
	}
}

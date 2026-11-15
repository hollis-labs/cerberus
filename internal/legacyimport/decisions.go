package legacyimport

// Decisions are the rows whose treatment waits on a lead ruling (slice 10a spec, "PENDING").
// Each is one field with its default here, so a ruling is a one-line change; the report lists
// the values a run applied. Rows that are settled are not configurable.
type Decisions struct {
	// HostsHistory is R8's hosts.history.jsonl and its rotations: DecisionInPlace (default: they
	// stay beside hosts.yaml in the adopted root, which the new build reads in place) or
	// DecisionCopy (copied to <data>/state/).
	HostsHistory string
	// EmptyReservations is R11's credential-issuance/<id>/ with no receipt (a crash between the
	// reservation and its first receipt): DecisionReport (default: listed as
	// reservations_without_receipt and left in place) or DecisionRefuse.
	EmptyReservations string
	// OAuthAlerts is R21's oauth/ and alerts/: DecisionCarry (default: copied inert to
	// <data>/oauth and <data>/alerts; nothing in the new build reads them yet) or DecisionInPlace.
	OAuthAlerts string
	// Config is R13's output: where the transformed config.yaml, events.yaml and
	// connector-config.yaml keys go (default FragmentFile: <data>/migration/config-fragment.yaml,
	// for the operator to merge into the override file).
	Config ConfigWriter
}

// Decision values.
const (
	DecisionInPlace = "in_place"
	DecisionCopy    = "copy"
	DecisionReport  = "report"
	DecisionRefuse  = "refuse"
	DecisionCarry   = "carry"
)

// DefaultDecisions are the defaults until the lead rules.
func DefaultDecisions() Decisions {
	return Decisions{HostsHistory: DecisionInPlace, EmptyReservations: DecisionReport, OAuthAlerts: DecisionCarry, Config: FragmentFile{}}
}

func (d Decisions) withDefaults() Decisions {
	def := DefaultDecisions()
	if d.HostsHistory == "" {
		d.HostsHistory = def.HostsHistory
	}
	if d.EmptyReservations == "" {
		d.EmptyReservations = def.EmptyReservations
	}
	if d.OAuthAlerts == "" {
		d.OAuthAlerts = def.OAuthAlerts
	}
	if d.Config == nil {
		d.Config = def.Config
	}
	return d
}

func (d Decisions) report() map[string]string {
	return map[string]string{
		"R8 hosts.history.jsonl":  d.HostsHistory,
		"R11 empty reservations":  d.EmptyReservations,
		"R13 config output":       d.Config.Name(),
		"R15 hosts.yaml":          DecisionInPlace,
		"R21 oauth/, alerts/":     d.OAuthAlerts,
		"D3 manifest max_time":    "host_status by last_attempt",
		"D3 manifest extra files": "hosts.history.jsonl, hosts.yaml, plugin-connectors.json excluded",
	}
}

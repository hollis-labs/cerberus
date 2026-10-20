package eventexport

import (
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
)

// The envelope is the shared event-stream envelope v1.1 (libs harness/core
// adapters/runtimeevents). Cerberus used to carry a reduced copy of it; these
// aliases are the same types now, so what the exporter writes is exactly what a
// relay sends and the hub checks.
type (
	Envelope = runtimeevents.Event
	Source   = runtimeevents.Source
	Corr     = runtimeevents.Corr
	Actor    = runtimeevents.Actor
	Class    = runtimeevents.Class
)

// AppName is Cerberus's name on the event bus: the envelope's app, which the hub
// binds to the credential that publishes it.
const AppName = "cerberus"

// Source channels. Each has its own outbox, sequence and generation.
const (
	ChannelAudit      = "audit"
	ChannelScheduling = "scheduling"
)

// Visibility constants, pending D9.
const (
	VisibilityOperator    = "operator"
	VisibilityEnvironment = "environment"
)

// newEventID is evt_ plus 32 hex digits, the id scheme of the contract (D6).
func newEventID() string { return runtimeevents.NewEventID() }

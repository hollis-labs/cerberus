package storage

import (
	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/substrate/module"
	"github.com/hollis-labs/substrate/modules/database"
)

// Stores are the app's stores by table class (docs/substrate-port-plan.md, section 5.1). Code that
// reads or writes a table takes the store of that table's class, never database.From directly, so
// moving a class is one change here.
type Stores struct {
	// Local holds class A: pause state, applied digests, credential-issuance receipts (and,
	// with their slices, identity and standby state). Always the local SQLite store: Cerberus
	// must boot and act while Postgres is down.
	Local *store.Store
	// Telemetry holds class B: the host_* tables (and, with the scheduling slice, the
	// scheduler's run logs). The local store at cutover; after acceptance it may become a named
	// Postgres store (Substrate's database.Named(app, "data")), switched in FromApp alone.
	Telemetry *store.Store
}

// FromApp returns the stores for a started app. Today both classes share the database
// module's one store.
func FromApp(app *module.App) Stores {
	st := database.From(app)
	return Stores{Local: st, Telemetry: st}
}

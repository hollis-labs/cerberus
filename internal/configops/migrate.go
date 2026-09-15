package configops

import "errors"

// ErrCentralizedMigrationRetired is shared by the old CLI and HTTP entry points.
// No migration writer remains: configs must be authored in the owning repos.
var ErrCentralizedMigrationRetired = errors.New("centralized config migration is retired; put each project config in its owning repo, commit it on main, then run `cerberus register <repo>/<project>.cerberus.yaml`")

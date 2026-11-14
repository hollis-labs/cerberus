// Package storage is Cerberus's schema on Substrate: one embedded goose migration that the
// database module applies at start, and typed functions for the tables only Cerberus has.
// Cerberus is SQLite only.
//
// Store rules (hollis-labs/substrate docs/adopting-substrate.md, section 4): reads go through
// Reader(), writes through WriteTx, and nothing writes while a cursor is open.
package storage

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrations embed.FS

// Migrations is the embedded migration tree: root "migrations" holds one directory per
// dialect (here only sqlite).
func Migrations() (fsys fs.FS, root string) { return migrations, "migrations" }

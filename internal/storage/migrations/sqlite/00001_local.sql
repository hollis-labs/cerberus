-- Cerberus schema, class A: local state (docs/substrate-port-plan.md, section 5.1). These tables always live in
-- the local SQLite store in the data directory: supervision needs them to run, and Cerberus must boot and act
-- while the shared Postgres is down (it supervises that Postgres). Never moved to Postgres.
--
-- Until the first release each file is edited in place (no deployed database yet); after it, changes go in new
-- numbered files. Times are unix seconds, UTC, unless a column says otherwise. The postgres/ twin of this file
-- must stay equivalent; the store tests run on both.

-- +goose Up

-- Supervision state that the old build kept as files under ~/.cerberus.

-- Pause markers (old: the files paused and paused.<id>). target is a resource id, or '*' for
-- everything. A row means paused; resuming deletes it.
CREATE TABLE pause_state (
    target TEXT PRIMARY KEY,
    paused_at INTEGER NOT NULL
);

-- The digest of the definition each resource was last applied with (old: runtime/applied.json).
-- Digests only, never definitions: a definition's environment can carry values.
CREATE TABLE applied_definitions (
    resource_id TEXT PRIMARY KEY,
    digest TEXT NOT NULL,
    applied_at INTEGER NOT NULL
);

-- The fingerprint of the key the digests above are keyed with (the audit chain's digest key,
-- audit/.digest_key). One row. The migration records it with the digests it imports; supervise
-- mode refuses to start when the key in the data dir has another fingerprint, since every
-- migrated digest would then compare unequal.
CREATE TABLE applied_digest_key (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    fingerprint TEXT NOT NULL,
    recorded_at INTEGER NOT NULL
);

-- Credential-issuance receipts (old: credential-issuance/<id>/<state>.json, each created once).
-- One row per issuance and lifecycle state; the primary key keeps the old create-once rule.
-- refs is the JSON object of name -> file:// reference (never a value). at is RFC 3339 with
-- nanoseconds, exactly as the old receipt recorded it.
CREATE TABLE credential_issuance_receipts (
    issuance_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('preparing', 'prepared', 'dispatching', 'completed', 'uncertain')),
    connector TEXT NOT NULL,
    operation TEXT NOT NULL,
    binding_digest TEXT NOT NULL,
    refs TEXT NOT NULL DEFAULT '{}',
    at TEXT NOT NULL,
    PRIMARY KEY (issuance_id, state)
) WITHOUT ROWID;

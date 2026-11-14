-- The Cerberus schema on Substrate, one migration (clean break from the old numbered files in
-- internal/store/sqlite/migrations). Cerberus is SQLite only.
--
-- Until the first release this is the ONLY migration: a schema change edits this file, because
-- there is no deployed database yet. After the first release changes go in new numbered files.
--
-- Not here, on purpose: the old projects and resources tables (empty, definitions live in the
-- *.cerberus.yaml descriptors) and the old schema_migrations table (goose keeps its own).
-- The scheduling tables arrive with the scheduling slice. Times are unix seconds, UTC, unless a
-- column says otherwise.

-- +goose Up

-- Host monitoring. Same tables and columns as the old build (migrations 006 to 008), so the
-- import (`cerberus migrate-from`, slice 10) copies rows across unchanged.

-- One row per successful probe.
CREATE TABLE host_samples (
    host TEXT NOT NULL,
    ts INTEGER NOT NULL,
    load1 REAL,
    load5 REAL,
    load15 REAL,
    cpu INTEGER,
    mem_total INTEGER,
    mem_available INTEGER,
    disk_total INTEGER,
    disk_free INTEGER,
    uptime_s INTEGER,
    probe_ms INTEGER,
    PRIMARY KEY (host, ts)
) WITHOUT ROWID;

-- Derived health, one row per host.
CREATE TABLE host_status (
    host TEXT PRIMARY KEY,
    status TEXT NOT NULL,
    reasons TEXT NOT NULL DEFAULT '[]',
    since INTEGER NOT NULL,
    last_ok INTEGER,
    last_attempt INTEGER,
    next_due INTEGER,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    candidate TEXT NOT NULL DEFAULT '',
    candidate_count INTEGER NOT NULL DEFAULT 0
);

-- One grain-sized bucket (5m, 1h, 1d) of a host's samples: sums, counts and extremes, so
-- coarser grains combine exactly.
CREATE TABLE host_sample_rollups (
    host TEXT NOT NULL,
    grain TEXT NOT NULL,
    bucket_ts INTEGER NOT NULL,
    n INTEGER NOT NULL,
    load1_n INTEGER NOT NULL DEFAULT 0,
    load1_sum REAL,
    load1_min REAL,
    load1_max REAL,
    mem_avail_sum INTEGER,
    mem_avail_min INTEGER,
    mem_avail_max INTEGER,
    disk_free_sum INTEGER,
    disk_free_min INTEGER,
    disk_free_max INTEGER,
    probe_ms_sum INTEGER,
    probe_ms_max INTEGER,
    uptime_max INTEGER,
    cpu_max INTEGER,
    mem_total_max INTEGER,
    disk_total_max INTEGER,
    PRIMARY KEY (host, grain, bucket_ts)
) WITHOUT ROWID;

-- Per host and grain, the end of the last committed bucket: raw rows (and finer rollups)
-- are deleted only below it.
CREATE TABLE host_rollup_state (
    host TEXT NOT NULL,
    grain TEXT NOT NULL,
    watermark INTEGER NOT NULL,
    PRIMARY KEY (host, grain)
) WITHOUT ROWID;

-- Append-only status changes, read by id (SSE Last-Event-ID, the MCP poll tool). AUTOINCREMENT
-- so ids never repeat; the import carries the old sqlite_sequence value. Reasons are codes only.
CREATE TABLE host_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts INTEGER NOT NULL,
    host TEXT NOT NULL,
    kind TEXT NOT NULL,
    from_status TEXT NOT NULL,
    to_status TEXT NOT NULL,
    reasons TEXT NOT NULL DEFAULT '[]',
    expected INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX host_events_host ON host_events (host, id);

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

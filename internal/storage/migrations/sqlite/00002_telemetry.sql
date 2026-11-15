-- Cerberus schema, class B: telemetry (docs/substrate-port-plan.md, section 5.1). The host-monitoring tables,
-- with the old build's columns (old migrations 006 to 008), so the import copies rows unchanged. SQLite at
-- cutover; they may move to Postgres after acceptance, as one step (host_rollup_state moves with the rollups).
-- When they do, this file moves to that store's migration set.
--
-- Until the first release this file is edited in place; after it, changes go in new numbered files. Times are
-- unix seconds, UTC. The postgres/ twin of this file must stay equivalent; the store tests run on both.

-- +goose Up
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

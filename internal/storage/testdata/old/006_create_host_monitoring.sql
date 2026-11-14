-- Host monitoring (Hosts E1): one row per successful probe, and one row of
-- derived health per host. Times are unix seconds, UTC. Rollups, events and
-- retention arrive in later migrations.
CREATE TABLE IF NOT EXISTS host_samples (
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

CREATE TABLE IF NOT EXISTS host_status (
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

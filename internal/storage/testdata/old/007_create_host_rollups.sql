-- Host sample rollups (Hosts E2). A rollup row summarizes one grain-sized
-- bucket (5m, 1h, 1d) of a host's samples with sums, counts and extremes, so
-- coarser grains combine exactly. host_rollup_state records, per host and
-- grain, the end of the last bucket committed: raw rows (and finer rollups)
-- are deleted only below it. Times are unix seconds, UTC.
CREATE TABLE IF NOT EXISTS host_sample_rollups (
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

CREATE TABLE IF NOT EXISTS host_rollup_state (
    host TEXT NOT NULL,
    grain TEXT NOT NULL,
    watermark INTEGER NOT NULL,
    PRIMARY KEY (host, grain)
) WITHOUT ROWID;

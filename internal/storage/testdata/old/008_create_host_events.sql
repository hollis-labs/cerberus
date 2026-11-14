-- Host status events (Hosts E3): an append-only log of status changes that
-- subscribers read by id (SSE Last-Event-ID, the MCP poll tool). Reasons are
-- codes only: no serials, addresses or probed text. Times are unix seconds.
CREATE TABLE IF NOT EXISTS host_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts INTEGER NOT NULL,
    host TEXT NOT NULL,
    kind TEXT NOT NULL,
    from_status TEXT NOT NULL,
    to_status TEXT NOT NULL,
    reasons TEXT NOT NULL DEFAULT '[]',
    expected INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS host_events_host ON host_events (host, id);

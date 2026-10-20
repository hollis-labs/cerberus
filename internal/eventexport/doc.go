// Package eventexport turns Cerberus's hash-chained audit chain, approval event
// store and scheduling notifications into event-stream envelopes (event stream
// contract C1/C2, task CW-20261016-0142), and keeps them in a local, durable
// outbox for a relay to publish later. Nothing here talks to a network.
//
// Lifecycle events (resource.state_changed, deploy.state_changed) are derived from
// the same audit chain records; see MapLifecycle for what is and is not covered.
//
// The authoritative records are never touched. The exporter reads the audit
// chain and the approval store with the strict readers in internal/audit and
// internal/approval, maps each record to at most one envelope built field by
// field from an allowlist, and appends it to a JSONL outbox. The outbox is its
// own ledger: each line carries the envelope (with its evt_ id, minted once)
// and a deduplication key naming the source record, so a replay after a crash
// appends nothing twice.
//
// Known limits (planned, not done here): the outbox is never rotated and its keys
// are held in memory, After and a duplicate lookup scan the file from the start,
// and an approval store line that did not parse is reported once and never
// cleared. All are fine at Cerberus's volume today; rotation arrives with the
// relay, which is what decides when an entry may be dropped.
//
// Export is off by default (see Settings). The first time it runs it starts at
// the verified tail of each source and does not back-fill.
package eventexport

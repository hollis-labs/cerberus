// Package eventexport turns Cerberus's hash-chained audit chain, approval event
// store and scheduling notifications into event-stream envelopes (event stream
// contract C1/C2, task CW-20261016-0142), and keeps them in a local, durable
// outbox for a relay to publish later. Nothing here talks to a network.
//
// The authoritative records are never touched. The exporter reads the audit
// chain and the approval store with the strict readers in internal/audit and
// internal/approval, maps each record to at most one envelope built field by
// field from an allowlist, and appends it to a JSONL outbox. The outbox is its
// own ledger: each line carries the envelope (with its evt_ id, minted once)
// and a deduplication key naming the source record, so a replay after a crash
// appends nothing twice.
//
// Export is off by default (see Settings). The first time it runs it starts at
// the verified tail of each source and does not back-fill.
package eventexport

-- Cerberus schema, class A: local state (docs/substrate-port-plan.md, section 5.1). A new file rather than an
-- edit of 00001: 00001 is already applied on substrate-port, and an edited applied migration breaks a data dir
-- already at that version. The postgres/ twin of this file must stay equivalent; the store tests run on both.

-- +goose Up

-- The fingerprint of the key applied_definitions' digests are keyed with (the audit chain's digest key,
-- audit/.digest_key). One row. The migration records it with the digests it imports; supervise mode refuses
-- to start when the key in the data dir has another fingerprint, since every migrated digest would then
-- compare unequal.
CREATE TABLE applied_digest_key (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    fingerprint TEXT NOT NULL,
    recorded_at INTEGER NOT NULL
);

-- Nullable: legacy rows (plugins that predate the probe marker) stay NULL.
ALTER TABLE host_samples ADD COLUMN probe_version INTEGER;

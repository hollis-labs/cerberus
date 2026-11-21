-- Nullable additions preserve the difference between old samples and idle CPUs.
ALTER TABLE host_samples ADD COLUMN cpu_percent REAL;
ALTER TABLE host_samples ADD COLUMN mounts TEXT;
ALTER TABLE host_samples ADD COLUMN mem_available_unknown INTEGER;

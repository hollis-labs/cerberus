# Fleet health snapshot

`cerberus_fleet_snapshot` is a read-effect MCP tool. The same read is available
on the daemon socket as `GET /hosts/_fleet_snapshot` and on the console API as
`GET /api/hosts/_fleet_snapshot`. It uses the host monitor's existing SQLite
samples and status, without SSH, Outpost or per-host network calls at read time.
The serving runtime gates and audits operation `host.fleet_snapshot`; HTTP
requires the same `cerberus:hosts.read` scope as the other host reads.

Input: `offset` (nonnegative integer, default 0), `limit` (1–100, default 100).
The API also accepts `limit=0` as the default, for Go clients with zero values.
Hosts are sorted by name. Follow `next_offset` until null to read every known
host in hosts.yaml, including hosts never probed or without a usable transport.
Each request reflects the current host file; refresh from offset 0 when that file
changes during pagination. Monitoring must be attached to the serving daemon;
otherwise the read returns the monitoring-unavailable error. Disabled monitoring
still returns historical measurements and known hosts, with unknown reachability.

Response schema `cerberus.fleet.snapshot.v1`:

```json
{
  "schema": "cerberus.fleet.snapshot.v1",
  "generated_at": "2026-11-21T05:00:00Z",
  "window_start": "2026-11-21T04:00:00Z",
  "step_seconds": 300,
  "total_hosts": 1,
  "next_offset": null,
  "hosts": [{
    "name": "example-host",
    "roles": ["worker"],
    "reachable": true,
    "last_seen": "2026-11-21T04:59:00Z",
    "stale": false,
    "latest": {
      "ts": "2026-11-21T04:59:00Z",
      "cpu_percent": 12.5,
      "memory_used": 4294967296,
      "memory_total": 8589934592,
      "disks": [{"mount": "/var/tmp", "used": 1073741824, "total": 10737418240}],
      "load1": 0.5, "load5": 0.4, "load15": 0.3
    },
    "series": []
  }]
}
```

The abbreviated example omits the series points. Every host has exactly twelve
points, oldest first, one per five-minute interval in `[window_start, generated_at)`.
A point has the same fields as `latest`, plus `sample_at`. Its `ts` is the bucket
start; `sample_at` is the timestamp of the latest successful sample in that
bucket. Empty buckets have `sample_at: null`, null numeric fields and `disks: []`.
Selection happens in SQL and returns at most twelve rows per host regardless of
probe frequency. These are representative samples, **not averages**; there is no
interpolation or carry-forward. The newest sample at or before `generated_at`
is returned separately as `latest`, even if older than the series window.

- `roles` contains the declared singular host role, or is empty.
- `reachable` describes the latest monitor attempt: true after success, false
  after failure, null when unobserved, monitoring is disabled, or the attempt is
  older than three configured probe intervals. `stale` is true in those unknown
  cases. A fresh failed attempt is false and not stale. `last_seen` is the last
  successful observation, retained across failures. Historical `latest.ts` and
  `sample_at` expose metric age independently of attempt freshness.
- `latest` is null for a host with no stored successful sample. Individual
  unmeasured metrics are null; zero CPU usage and zero used disk are real values.
- CPU is actual busy percentage (0–100 across all cores), **not core count or load**.
  The SSH POSIX probe samples `/proc/stat` counters one second apart on Linux;
  macOS reads the second `top` CPU summary. Unsupported or absent measurements
  remain null. Memory used is total minus available, in bytes. Load values are
  the OS load averages and can exceed one.
- Disk paths are the bounded main-path allow-list: `/`, `/data`, `/home`, `/var`,
  `/var/tmp`, `/tmp`, `/System/Volumes/Data`. Existing directories with a valid
  `df -Pk` reading are included, with used and total bytes. Paths can share one
  backing filesystem; do not sum them. Missing paths are omitted. Names outside
  this allow-list and malformed numbers are dropped.

Migration 009 adds nullable CPU and per-path disk columns to `host_samples`.
Existing rows remain valid, but core counts are never interpreted as CPU usage,
and anonymous legacy disk totals are never assigned an invented mount path.
New SSH monitor probes populate the extra fields. Outposts must emit the added
`cpu_percent` and `mount_kb=<path> <total-kB> <used-kB>` facts to populate them;
older Outposts continue to work with null CPU and empty disks. This change does
not deploy Outposts. Raw sample retention (at least one hour) supplies the series;
existing coarser rollups are unchanged.

The response is bounded to 100 hosts, 12 points per host, 7 disk paths per point,
and one latest measurement. It contains only host names, declared roles,
observation timestamps and numeric metrics. It excludes host addresses,
credentials, labels, raw probe output, units, processes, and agent/session data.
Agent membership belongs to the separate Tether live-agents read.

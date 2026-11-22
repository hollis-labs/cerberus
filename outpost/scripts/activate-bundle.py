#!/usr/bin/env python3
"""Switch a staged Outpost bundle in and roll back automatically if it fails.

Backs up the inventory, repoints each plugin command at the new bundle,
restarts the supervisor, then requires the supervised host to stay up for a
settle period, requires every configured plugin healthy through /api/readyz,
and runs an optional health command. On any failure the
previous inventory is restored and the supervisor restarted again.

This restarts the host, which restarts every plugin. Run it only after checking
the machine's live sessions (see the README).
"""
import argparse
import http.client
import ipaddress
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import time
from urllib.parse import urlsplit

from outpost_units import normalize

LAUNCHD_LABEL = "dev.hollis-labs.outpost"


def verify_bundle(bundle):
    import hashlib
    for line in (bundle / "SHA256SUMS").read_text().splitlines():
        digest, name = line.split()
        if hashlib.sha256((bundle / name.lstrip("*")).read_bytes()).hexdigest() != digest:
            raise SystemExit("Bundle checksum mismatch: " + name)


def repoint(config, bundle, system):
    for plugin in config["plugins"]:
        plugin["command"] = str(bundle / Path(plugin["command"]).name)
        if plugin["id"] == "cerberus":
            settings = plugin.setdefault("config", {})
            settings["protected_units"] = normalize(system, settings.get("protected_units", ""))
    return config


def write_private(path, text):
    temporary = path.with_name(path.name + ".new")
    with temporary.open("w") as output:
        os.chmod(temporary, 0o600)
        output.write(text)
        output.flush()
        os.fsync(output.fileno())
    temporary.replace(path)


def run(argv):
    return subprocess.run(argv, capture_output=True, text=True)


class Supervisor:
    """Restart and observe the host through the platform's own supervisor."""

    def __init__(self, system, unit):
        self.system = system
        self.unit = unit

    def restart(self):
        if self.system == "Darwin":
            target = "gui/%d/%s" % (os.getuid(), self.unit)
            result = run(["launchctl", "kickstart", "-k", target])
        else:
            result = run(["systemctl", "--user", "restart", self.unit])
        if result.returncode:
            raise RuntimeError("restart failed: " + result.stderr.strip())

    def main_pid(self):
        """PID of the running host, or 0 when it is not running."""
        if self.system == "Darwin":
            out = run(["launchctl", "print", "gui/%d/%s" % (os.getuid(), self.unit)]).stdout
            for line in out.splitlines():
                key, _, value = line.strip().partition(" = ")
                if key == "pid":
                    return int(value)
            return 0
        out = run(["systemctl", "--user", "show", self.unit, "-p", "MainPID", "--value"]).stdout
        return int(out.strip() or 0)


def settled(supervisor, seconds, sleep=time.sleep, clock=time.monotonic, interval=1.0):
    """True when one host process stays up for the whole period."""
    deadline = clock() + seconds
    first = 0
    while clock() < deadline:
        pid = supervisor.main_pid()
        if not pid or (first and pid != first):
            return False
        first = first or pid
        sleep(interval)
    return first


def ready(config, pid):
    """Require the supervised process to report every configured plugin healthy.

    Use the inventory's literal bind address, without proxies or redirects.
    Older hosts lacking this endpoint fail closed; upgrade the host first.
    """
    connection = None
    try:
        address = urlsplit("http://" + config.get("address", "127.0.0.1:8977"))
        ip = ipaddress.ip_address(address.hostname)
        # The host validates bind ranges. This operator-owned probe only needs
        # a literal address, so DNS, proxies and redirects cannot retarget it.
        if address.path or address.query or address.fragment or address.username:
            raise ValueError("invalid bind address")
        if not address.port:
            raise ValueError("missing port")
        connection = http.client.HTTPConnection(str(ip), address.port, timeout=5)
        connection.request("GET", "/api/readyz")
        response = connection.getresponse()
        body = response.read(4097)
        if response.status != 200 or len(body) > 4096:
            raise ValueError("readiness refused")
        state = json.loads(body)
        total = len(config["plugins"])
        return (state.get("status") == "ok" and total > 0
                and type(state.get("pid")) is int and state["pid"] == pid
                and type(state.get("plugins")) is int and state["plugins"] == total
                and type(state.get("healthy")) is int and state["healthy"] == total)
    except (OSError, ValueError, TypeError, AttributeError, http.client.HTTPException):
        return False
    finally:
        if connection:
            connection.close()


def activate(config_path, bundle, supervisor, system, settle, health_command=None,
             sleep=time.sleep, clock=time.monotonic, readiness=ready):
    """Returns the backup path on success; raises RuntimeError after rolling back."""
    original = config_path.read_text()
    backup = config_path.with_name("%s.bak-%s-pre-%s" % (
        config_path.name, time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()), bundle.name))
    write_private(backup, original)
    config = repoint(json.loads(original), bundle, system)
    write_private(config_path, json.dumps(config, indent=2) + "\n")
    failure = None
    try:
        supervisor.restart()
        pid = settled(supervisor, settle, sleep, clock)
        if not pid:
            failure = "host did not stay running for %ss" % settle
        elif not readiness(config, pid) or supervisor.main_pid() != pid:
            failure = "not all configured plugins are ready in the supervised host"
        elif health_command and subprocess.run(health_command, shell=True).returncode:
            failure = "health command failed"
    except RuntimeError as error:
        failure = str(error)
    if failure is None:
        return backup
    write_private(config_path, original)
    try:
        supervisor.restart()
    except RuntimeError as error:
        failure += "; rollback restart also failed: " + str(error)
    raise RuntimeError(failure + "; restored " + str(config_path))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--config", type=Path, default=Path.home() / ".config/outpost/outpost.json")
    parser.add_argument("--unit", help="supervisor unit (default: outpost.service on Linux, the launchd label on macOS)")
    parser.add_argument("--settle", type=float, default=15, help="seconds the host must stay up (default 15)")
    parser.add_argument("--health-command", help="shell command that must exit 0 after the host settles")
    args = parser.parse_args()
    system = platform.system()
    if system not in ("Linux", "Darwin"):
        raise SystemExit("Unsupported platform")
    bundle = args.bundle.resolve(strict=True)
    verify_bundle(bundle)
    unit = args.unit or (LAUNCHD_LABEL if system == "Darwin" else "outpost.service")
    try:
        backup = activate(args.config, bundle, Supervisor(system, unit), system, args.settle, args.health_command)
    except RuntimeError as error:
        print("Rolled back: " + str(error), file=sys.stderr)
        return 1
    print("Activated", bundle.name, "- previous inventory kept at", backup)
    return 0


if __name__ == "__main__":
    sys.exit(main())

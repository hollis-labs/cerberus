#!/usr/bin/env python3
"""Verify an immutable bundle and prepare machine config and native supervision.

Does not stop, start, or enroll anything. Activation is an explicit deployment
step after the controller has checked the machine's live sessions.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import shutil

from outpost_units import protected_units


def write_private(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists():
        backup = path.with_name(path.name + ".before-outpost")
        if not backup.exists():
            shutil.copy2(path, backup)
    temporary = path.with_name(path.name + ".staged")
    with temporary.open("wb") as output:
        os.chmod(temporary, 0o600)
        output.write(data)
        output.flush()
        os.fsync(output.fileno())
    temporary.replace(path)


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--bundle", type=Path, required=True)
parser.add_argument("--worker-root", type=Path, required=True)
parser.add_argument("--address", required=True)
parser.add_argument("--supervisor", choices=["cerberus", "native"], required=True)
parser.add_argument("--cerberus-service-name", help="exact service_name reported by Cerberus resource status")
args = parser.parse_args()
if args.supervisor == "cerberus" and not args.cerberus_service_name:
    parser.error("--cerberus-service-name is required for an existing Cerberus resource")
bundle = args.bundle.resolve(strict=True)
root = args.worker_root.resolve(strict=True)
home = Path.home()

manifest = {}
for line in (bundle / "SHA256SUMS").read_text().splitlines():
    digest, name = line.split()
    manifest[name] = digest
for name in ("outpost", "tether-worker", "cerberus-outpost"):
    binary = bundle / name
    if hashlib.sha256(binary.read_bytes()).hexdigest() != manifest.get(name):
        raise SystemExit("Bundle checksum mismatch: " + name)
    binary.chmod(0o755)

worker = json.loads((root / "config.json").read_text())
if not (root / "device.token").is_file():
    raise SystemExit("Enroll the worker before staging Outpost")
search_path = worker.get("path") or os.environ.get("PATH", "/usr/bin:/bin")
environment = {
    "HOME": str(home),
    "USER": home.name,
    "LOGNAME": home.name,
    "SHELL": os.environ.get("SHELL", "/bin/sh"),
    "PATH": search_path,
}
if platform.system() == "Linux":
    environment["XDG_RUNTIME_DIR"] = "/run/user/" + str(os.getuid())
    environment["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + environment["XDG_RUNTIME_DIR"] + "/bus"
env = [key + "=" + value for key, value in environment.items()]
state = home / ".local/share/outpost"
config_path = home / ".config/outpost/outpost.json"
protected = protected_units(platform.system(), args.cerberus_service_name)
config = {
    "address": args.address,
    "state_dir": str(state),
    "credentials_file": str(home / ".config/outpost/pairings.json"),
    "plugins": [
        {"id": "tether", "command": str(bundle / "tether-worker"), "args": ["plugin"],
         "dir": str(home), "env": env, "config": {"worker_root": str(root)}},
        {"id": "cerberus", "command": str(bundle / "cerberus-outpost"),
         "dir": str(home), "env": env, "config": {"protected_units": protected}},
    ],
}
write_private(config_path, (json.dumps(config, indent=2) + "\n").encode())
state.mkdir(parents=True, exist_ok=True)

if platform.system() == "Linux":
    if args.supervisor == "cerberus":
        path = home / (".config/systemd/user/" + args.cerberus_service_name + ".service.d/lifecycle.conf")
        write_private(path, b"[Service]\nKillMode=process\n")
    else:
        def quote(value):
            value = str(value).replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%").replace("$", "$$")
            return '"' + value + '"'
        unit = "[Unit]\nDescription=Shared Outpost plugin host\nAfter=network-online.target\n"
        unit += "[Service]\nExecStart=" + quote(bundle / "outpost") + " --config " + quote(config_path) + " run\n"
        unit += "Restart=always\nRestartSec=3\nKillMode=process\nTimeoutStopSec=20\n"
        unit += "[Install]\nWantedBy=default.target\n"
        path = home / ".config/systemd/user/outpost.service"
        write_private(path, unit.encode())
elif platform.system() == "Darwin":
    if args.supervisor != "native":
        raise SystemExit("Use the native supervisor on a Mac without an existing Cerberus resource")
    log = state / "outpost.log"
    path = home / "Library/LaunchAgents/dev.hollis-labs.outpost.plist"
    write_private(path, plistlib.dumps({
        "Label": "dev.hollis-labs.outpost",
        "ProgramArguments": [str(bundle / "outpost"), "--config", str(config_path), "run"],
        "RunAtLoad": True, "KeepAlive": True,
        "StandardOutPath": str(log), "StandardErrorPath": str(log),
    }))
else:
    raise SystemExit("Unsupported native supervisor")

print("Verified bundle and staged", config_path)
print("Prepared supervisor configuration", path)
print("Worker enrollment and state preserved at", root)
print("No service was stopped or started")

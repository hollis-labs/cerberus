"""Protected-unit names for the Cerberus plugin, which differ by platform.

The plugin accepts only service units on Linux (systemd names ending in
".service"); the launchd label belongs to macOS alone.
"""
LAUNCHD_LABEL = "dev.hollis-labs.outpost"
SYSTEMD_UNIT = "outpost.service"


def protected_units(system, cerberus_service_name=None):
    names = [LAUNCHD_LABEL] if system == "Darwin" else [SYSTEMD_UNIT]
    if cerberus_service_name:
        names.append(cerberus_service_name + ".service")
    return ",".join(names)


def normalize(system, csv):
    """Drop names that the platform's supervisor cannot own; keep order."""
    names = [n.strip() for n in csv.split(",") if n.strip()]
    if system == "Linux":
        names = [n for n in names if n.endswith(".service")]
    return ",".join(names)

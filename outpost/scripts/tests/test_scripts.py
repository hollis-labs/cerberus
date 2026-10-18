import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import importlib.util

from outpost_units import normalize, protected_units

spec = importlib.util.spec_from_file_location("activate", Path(__file__).resolve().parent.parent / "activate-bundle.py")
activate_mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(activate_mod)


class Units(unittest.TestCase):
    def test_linux_has_only_service_units(self):
        self.assertEqual(protected_units("Linux", "agent"), "outpost.service,agent.service")
        self.assertEqual(protected_units("Linux"), "outpost.service")

    def test_darwin_has_only_the_launchd_label(self):
        self.assertEqual(protected_units("Darwin"), "dev.hollis-labs.outpost")

    def test_normalize_drops_launchd_label_on_linux(self):
        self.assertEqual(normalize("Linux", "outpost.service,dev.hollis-labs.outpost,x.service"),
                         "outpost.service,x.service")
        self.assertEqual(normalize("Darwin", "dev.hollis-labs.outpost"), "dev.hollis-labs.outpost")


class FakeSupervisor:
    def __init__(self, pids, fail_restart_at=()):
        self.pids = list(pids)
        self.restarts = 0
        self.fail_restart_at = fail_restart_at

    def restart(self):
        self.restarts += 1
        if self.restarts in self.fail_restart_at:
            raise RuntimeError("restart failed")

    def main_pid(self):
        return self.pids.pop(0) if len(self.pids) > 1 else self.pids[0]


class Activate(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.cfg = self.tmp / "outpost.json"
        self.bundle = self.tmp / "new"
        self.bundle.mkdir()
        self.original = json.dumps({"plugins": [
            {"id": "tether", "command": "/old/tether-worker"},
            {"id": "cerberus", "command": "/old/cerberus-outpost",
             "config": {"protected_units": "outpost.service,dev.hollis-labs.outpost"}}]})
        self.cfg.write_text(self.original)
        self.clock = iter(range(0, 1000))

    def go(self, supervisor, **kw):
        return activate_mod.activate(self.cfg, self.bundle, supervisor, "Linux", 3,
                                     sleep=lambda _: None, clock=lambda: next(self.clock), **kw)

    def test_success_repoints_and_keeps_backup(self):
        backup = self.go(FakeSupervisor([42]))
        config = json.loads(self.cfg.read_text())
        self.assertEqual(config["plugins"][0]["command"], str(self.bundle / "tether-worker"))
        self.assertEqual(config["plugins"][1]["config"]["protected_units"], "outpost.service")
        self.assertEqual(backup.read_text(), self.original)

    def test_restart_loop_rolls_back(self):
        supervisor = FakeSupervisor([42, 43, 44])
        with self.assertRaisesRegex(RuntimeError, "did not stay running"):
            self.go(supervisor)
        self.assertEqual(self.cfg.read_text(), self.original)
        self.assertEqual(supervisor.restarts, 2)

    def test_host_not_running_rolls_back(self):
        with self.assertRaises(RuntimeError):
            self.go(FakeSupervisor([0]))
        self.assertEqual(self.cfg.read_text(), self.original)

    def test_failed_health_command_rolls_back(self):
        with self.assertRaisesRegex(RuntimeError, "health command"):
            self.go(FakeSupervisor([42]), health_command="false")
        self.assertEqual(self.cfg.read_text(), self.original)

    def test_failed_restart_rolls_back_and_reports_second_failure(self):
        supervisor = FakeSupervisor([42], fail_restart_at=(1, 2))
        with self.assertRaisesRegex(RuntimeError, "rollback restart also failed"):
            self.go(supervisor)
        self.assertEqual(self.cfg.read_text(), self.original)


if __name__ == "__main__":
    unittest.main()

import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("postgres_fence", Path(__file__).parents[1] / "modules/postgres_fence.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class FenceTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        self.state, self.data = root / "state", root / "data"
        self.state.mkdir(mode=0o700)
        self.data.mkdir()
        machine = root / "machine-id"
        machine.write_text("a" * 32)
        self.wanted = {"schemaVersion": 1, "targetID": "target", "recoverySetID": "set",
                       "frontierDigest": "sha256:" + "b" * 64, "machineID": "a" * 32,
                       "clusterIdentity": "cluster", "systemIdentifier": "12345678"}
        self.running, self.fail_stop, self.calls = True, False, []
        self.fence = module.Fence(self.state, self.data, machine, "control", "systemctl", self.execute)
        self.fence.system = root / "system"
        self.fence.profiles = root / "profiles"
        self.fence.profiles.mkdir()
        unit = self.fence.system / "etc/systemd/system/postgresql.service"
        unit.parent.mkdir(parents=True)
        unit.write_text("[Unit]\nConditionPathExists=!/var/lib/leapview-recovery/postgresql-fence.json\n")
        (self.fence.profiles / "system").symlink_to(self.fence.system)

    def execute(self, *args):
        self.calls.append(args)
        if args[0] == "control":
            return "Database system identifier: 12345678\nDatabase cluster state: " + ("in production" if self.running else "shut down")
        if args[1] == "stop":
            # The durable boot guard must exist BEFORE stopping the writer.
            self.assertEqual(json.loads((self.state / module.MARKER).read_text()), self.wanted)
            if self.fail_stop:
                raise OSError("stop failure")
            self.running = False
            return ""
        return "ActiveState=inactive\nSubState=dead\nMainPID=0\n" if not self.running else "ActiveState=active\nSubState=running\nMainPID=42\n"

    def test_fence_is_durable_before_stop_and_check_does_not_mutate(self):
        result = self.fence.apply(self.wanted)
        self.assertEqual(result["identity"], self.wanted)
        self.assertTrue(result["restartFenced"])
        self.assertEqual((self.state / module.MARKER).stat().st_mode & 0o777, 0o600)
        self.calls.clear()
        self.assertEqual(self.fence.apply(self.wanted, check=True), result)
        self.assertNotIn(("systemctl", "stop", "postgresql.service"), self.calls)

    def test_wrong_host_or_cluster_never_fences(self):
        for key, value in [("machineID", "f" * 32), ("systemIdentifier", "999")]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.fence.apply(dict(self.wanted, **{key: value}))
            self.assertFalse((self.state / module.MARKER).exists())
        self.assertTrue(self.running)

    def test_stop_failure_leaves_fence_for_explicit_retry(self):
        self.fail_stop = True
        with self.assertRaises(OSError):
            self.fence.apply(self.wanted)
        self.assertTrue((self.state / module.MARKER).exists())
        with self.assertRaises(ValueError):
            self.fence.apply(self.wanted, check=True)
        self.fail_stop = False
        self.fence.apply(self.wanted)
        self.assertFalse(self.running)

    def test_foreign_recovery_cannot_adopt_fence(self):
        self.fence.apply(self.wanted)
        for key, value in [("targetID", "other"), ("recoverySetID", "other"), ("frontierDigest", "sha256:" + "c" * 64)]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.fence.apply(dict(self.wanted, **{key: value}))

    def test_check_requires_existing_fence_even_when_postgres_is_stopped(self):
        self.running = False
        with self.assertRaises(ValueError):
            self.fence.apply(self.wanted, check=True)
        self.assertFalse((self.state / module.MARKER).exists())

    def test_direct_writer_and_symlink_markers_fail_closed(self):
        self.fence.apply(self.wanted)
        (self.data / "postmaster.pid").write_text("42")
        with self.assertRaises(ValueError):
            self.fence.apply(self.wanted, check=True)
        (self.data / "postmaster.pid").unlink()
        marker = self.state / module.MARKER
        marker.rename(self.state / "original")
        marker.symlink_to(self.state / "original")
        with self.assertRaises(ValueError):
            self.fence.apply(self.wanted, check=True)

    def test_strict_request(self):
        for raw in [json.dumps(dict(self.wanted, extra=True)), json.dumps(dict(self.wanted, schemaVersion=True)),
                    '{"schemaVersion":1,"schemaVersion":1}', "[]", "{}"]:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                module.request(raw)

    def test_unguarded_retained_generation_prevents_fence(self):
        older = self.fence.system.parent / "old-system"
        unit = older / "etc/systemd/system/postgresql.service"
        unit.parent.mkdir(parents=True)
        unit.write_text("[Unit]\nDescription=unguarded PostgreSQL\n")
        (self.fence.profiles / "system-1-link").symlink_to(older)
        with self.assertRaises(ValueError):
            self.fence.apply(self.wanted)
        self.assertFalse((self.state / module.MARKER).exists())
        self.assertTrue(self.running)

    def test_guard_lost_after_fencing_invalidates_observation(self):
        self.fence.apply(self.wanted)
        (self.fence.system / "etc/systemd/system/postgresql.service").write_text("[Unit]\n")
        with self.assertRaises(ValueError):
            self.fence.apply(self.wanted, check=True)

    def test_unguarded_boot_specialisation_prevents_fence(self):
        unit = self.fence.system / "specialisation/unsafe/etc/systemd/system/postgresql.service"
        unit.parent.mkdir(parents=True)
        unit.write_text("[Unit]\n")
        with self.assertRaises(ValueError):
            self.fence.apply(self.wanted)
        self.assertFalse((self.state / module.MARKER).exists())

    def test_generation_dropin_cannot_reset_boot_guard(self):
        override = self.fence.system / "etc/systemd/system/postgresql.service.d/override.conf"
        override.parent.mkdir()
        override.write_text("[Unit]\nConditionPathExists=\n")
        with self.assertRaises(ValueError):
            self.fence.apply(self.wanted)
        self.assertFalse((self.state / module.MARKER).exists())


if __name__ == "__main__":
    unittest.main()

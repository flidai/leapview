import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_compose_host_guest as host_guest


class DockerRebootReadinessTests(unittest.TestCase):
    def setUp(self):
        self.elapsed = 0.0
        self.sleeps = []
        self.clock = patch.object(host_guest.time, "monotonic", side_effect=lambda: self.elapsed)
        self.sleep = patch.object(host_guest.time, "sleep", side_effect=self.advance)
        self.clock.start()
        self.sleep.start()
        self.addCleanup(self.clock.stop)
        self.addCleanup(self.sleep.stop)

    def advance(self, duration):
        self.sleeps.append(duration)
        self.elapsed += duration

    def test_already_active_docker_returns_actual_observation(self):
        guest = SimpleNamespace(run=Mock(return_value=b"active\n"))
        self.assertEqual(host_guest._wait_for_docker_active(guest, timeout=12), b"active\n")
        self.assertEqual(self.sleeps, [])

    def test_ssh_can_return_before_docker_finishes_automatic_startup(self):
        guest = SimpleNamespace(run=Mock(side_effect=[
            host_guest.HostGuestError("SSH guest command failed (3)"),
            b"active\n",
        ]))
        self.assertEqual(host_guest._wait_for_docker_active(guest, timeout=12), b"active\n")
        self.assertEqual(guest.run.call_count, 2)
        for call in guest.run.call_args_list:
            self.assertEqual(call.args, ("systemctl is-active docker",))
        self.assertGreater(self.elapsed, 0)
        self.assertLess(self.elapsed, 12)

    def test_persistent_failure_is_bounded_and_never_restarts_docker(self):
        guest = SimpleNamespace(run=Mock(side_effect=host_guest.HostGuestError("SSH guest command failed (3)")))
        with self.assertRaisesRegex(host_guest.HostGuestError, "Docker did not become active after reboot"):
            host_guest._wait_for_docker_active(guest, timeout=7)
        self.assertEqual(self.elapsed, 7)
        self.assertEqual(self.sleeps, [5, 2])
        self.assertEqual([call.kwargs["timeout"] for call in guest.run.call_args_list], [7, 2])
        for call in guest.run.call_args_list:
            self.assertEqual(call.args, ("systemctl is-active docker",))

    def test_nonactive_output_never_qualifies_as_automatic_restart(self):
        guest = SimpleNamespace(run=Mock(return_value=b"activating\n"))
        with self.assertRaisesRegex(host_guest.HostGuestError, "Docker did not become active after reboot"):
            host_guest._wait_for_docker_active(guest, timeout=3)
        self.assertEqual(self.elapsed, 3)


if __name__ == "__main__":
    unittest.main()

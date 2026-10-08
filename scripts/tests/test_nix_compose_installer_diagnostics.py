import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_compose_host_guest as guest


class LocalGuest:
    def run(self, command, *, timeout):
        result = subprocess.run(["bash", "-c", command], capture_output=True, timeout=timeout, check=True)
        return result.stdout


class InstallerDiagnosticsTests(unittest.TestCase):
    def test_actual_controller_argv_failure_retains_only_classifications(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence = root / "evidence"
            evidence.mkdir()
            controller = root / "controller with spaces"
            controller.write_text("#!/bin/sh\n"
                "test \"$LEAPVIEWCTL_ROOT\" = /opt/leapview || exit 98\n"
                "test \"$#\" = 10 && test \"$1 $2\" = 'host install' || exit 99\n"
                "printf '%s\\n' 'arbitrary-private-output' 'LEAPVIEW_CSRF_KEY=generated-private-key' "
                "'{\"temporaryPassword\":\"generated-secret\"}' >&2\n"
                "printf '%s\\n' 'leapviewctl: prepare production PostgreSQL and delivery-pool bootstrap: "
                "dry-run first-install physical-pool bootstrap: permission denied: postgres://private-url' >&2\n"
                "exit 42\n")
            controller.chmod(0o700)
            command = guest._host_install_command(mode="nix-controller", docker_env="DOCKER_CONFIG=/tmp/unused",
                controller_path=str(controller), config_path="/tmp/config with spaces", payload_path="/tmp/payload",
                image="ghcr.io/flidai/leapview@sha256:" + "a" * 64)
            with patch.dict(os.environ, {"TMPDIR": str(root)}):
                with self.assertRaisesRegex(guest.HostGuestError, r"installer failed \(42\)"):
                    guest._run_host_installer(LocalGuest(), evidence, command, timeout=10,
                        fixture_secrets=["private-url"])
            self.assertEqual(set(root.iterdir()), {controller, evidence})
            record = json.loads((evidence / "host-install-diagnostic.json").read_bytes())
            self.assertEqual(record["exitCode"], 42)
            self.assertEqual(record["boundaries"], ["prepare-postgres-pool", "pool-dry-run"])
            self.assertIn("permission-denied", record["causes"])
            self.assertEqual((evidence / "host-install-exit-code.txt").read_bytes(), b"42\n")
            retained = b"".join(path.read_bytes() for path in evidence.iterdir())
            for value in [b"private-url", b"generated-private-key", b"generated-secret", b"arbitrary-private-output"]:
                self.assertNotIn(value, retained)

    def test_success_does_not_retain_bootstrap_responses(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = Path(directory)
            guest._run_host_installer(LocalGuest(), evidence,
                "printf '%s\\n' '{\"password\":\"secret\"}'; exit 0", timeout=10, fixture_secrets=[])
            record = json.loads((evidence / "host-install-diagnostic.json").read_bytes())
            self.assertEqual(record["exitCode"], 0)
            self.assertEqual(record["boundaries"], [])
            self.assertNotIn(b"secret", b"".join(path.read_bytes() for path in evidence.iterdir()))

    def test_unknown_error_and_large_output_are_bounded(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = Path(directory)
            command = "head -c 150000 /dev/zero; printf 'private-unknown-error'; exit 7"
            with self.assertRaisesRegex(guest.HostGuestError, r"installer failed \(7\)"):
                guest._run_host_installer(LocalGuest(), evidence, command, timeout=10, fixture_secrets=[])
            record = json.loads((evidence / "host-install-diagnostic.json").read_bytes())
            self.assertEqual(record["outputTailBytes"], 65536)
            self.assertEqual(record["causes"], ["unclassified"])
            self.assertEqual(record["boundaries"], [])

    def test_guest_timeout_retains_status_before_transport_deadline(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = Path(directory)
            with self.assertRaisesRegex(guest.HostGuestError, r"installer failed \(124\)"):
                guest._run_host_installer(LocalGuest(), evidence, "sleep 20", timeout=1, fixture_secrets=[])
            self.assertEqual(json.loads((evidence / "host-install-diagnostic.json").read_bytes())["causes"], ["timeout"])

    def test_malformed_transport_result_is_not_retained(self):
        class InvalidGuest:
            def run(self, command, *, timeout):
                return b"private-malformed-output"
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(guest.HostGuestError, "invalid exit status"):
                guest._run_host_installer(InvalidGuest(), Path(directory), "unused", timeout=1, fixture_secrets=[])
            self.assertEqual(list(Path(directory).iterdir()), [])


if __name__ == "__main__":
    unittest.main()

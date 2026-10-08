"""Unprivileged failure contracts for the real transport runner."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import kamal_transport_test as fixture


class TransportRunnerContract(unittest.TestCase):
    runner = Path(__file__).with_name("run-kamal-transport.sh").resolve()
    repository = runner.parents[4]

    def test_failed_build_cannot_reuse_successful_evidence(self):
        # /var/tmp is a common ambient TMPDIR, but is intentionally hidden by
        # the fixture. This test needs a permitted evidence location.
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            root = Path(directory)
            evidence = root / "evidence"
            evidence.mkdir()
            receipt = evidence / "transport.json"
            receipt.write_text(json.dumps({"passed": True}))
            tools = root / "bin"
            tools.mkdir()
            nix = tools / "nix"
            nix.write_text("#!/bin/sh\nexit 42\n")
            nix.chmod(0o755)
            result = subprocess.run(
                ["bash", str(self.runner), str(evidence)],
                cwd=self.repository,
                env=dict(os.environ, PATH=str(tools) + ":" + os.environ["PATH"]),
                capture_output=True, text=True, timeout=10,
            )
            self.assertNotEqual(result.returncode, 0)
            fresh = json.loads(receipt.read_text())
            self.assertFalse(fresh["passed"])
            self.assertFalse(fresh["fullManagedProfileQualified"])
            self.assertEqual(fresh["stage"], "setup")

    def test_hidden_evidence_directory_is_rejected_before_creation(self):
        for path in ("/root/hidden-transport-evidence", "/var/tmp/hidden-transport-evidence"):
            with self.subTest(path=path):
                result = subprocess.run(
                    ["bash", str(self.runner), path], cwd=self.repository,
                    capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode, 2)
                self.assertIn("outside fixture-hidden roots", result.stderr)

    def test_var_inputs_cannot_disappear_into_the_private_mount(self):
        for path in ("/var/tmp/image.tar", "/var/cache/tools", "/var/lib/evidence", "/var"):
            with self.subTest(path=path):
                with self.assertRaisesRegex(SystemExit, "outside fixture-hidden roots"):
                    fixture.validate_visible_paths([path])
        fixture.validate_visible_paths(["/nix/store/fixture", "/tmp/evidence", "/various/image.tar"])

    def test_empty_private_var_provisions_nix_openssh_and_docker_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.assertFalse((root / "empty").exists())
            fixture.prepare_private_var(root)
            self.assertTrue((root / "empty").is_dir())
            self.assertEqual((root / "empty").stat().st_mode & 0o777, 0o755)
            self.assertEqual(list((root / "empty").iterdir()), [])
            self.assertTrue((root / "lib").is_dir())
            self.assertEqual(os.readlink(root / "run"), "/run")


if __name__ == "__main__":
    unittest.main()

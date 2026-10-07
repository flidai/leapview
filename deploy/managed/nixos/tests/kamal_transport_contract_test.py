"""Unprivileged failure contracts for the real transport runner."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class TransportRunnerContract(unittest.TestCase):
    runner = Path(__file__).with_name("run-kamal-transport.sh").resolve()
    repository = runner.parents[4]

    def test_failed_build_cannot_reuse_successful_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
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
        result = subprocess.run(
            ["bash", str(self.runner), "/root/hidden-transport-evidence"],
            cwd=self.repository,
            capture_output=True, text=True, timeout=10,
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("outside fixture-hidden roots", result.stderr)


if __name__ == "__main__":
    unittest.main()

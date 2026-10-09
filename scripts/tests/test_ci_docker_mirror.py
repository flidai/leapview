import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import configure_ci_docker_mirror as mirror


class DockerMirrorTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.config = Path(self.temp.name) / "daemon.json"
        self.environment = patch.dict(os.environ, {
            "GITHUB_ACTIONS": "true", "RUNNER_OS": "Linux",
            "RUNNER_ENVIRONMENT": "github-hosted",
        })
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def test_preserves_settings_and_mirrors_and_is_idempotent(self):
        original = {"log-driver": "local", "features": {"containerd-snapshotter": True},
                    "registry-mirrors": ["https://existing.example", mirror.MIRROR]}
        self.config.write_text(json.dumps(original))
        with patch.object(mirror.subprocess, "run") as restart, patch.object(
                mirror.subprocess, "check_output", return_value=json.dumps([mirror.MIRROR + "/"])):
            mirror.configure(self.config)
            after = self.config.read_text()
            mirror.configure(self.config)
        expected = dict(original, **{"registry-mirrors": [mirror.MIRROR, "https://existing.example"]})
        self.assertEqual(json.loads(after), expected)
        self.assertEqual(self.config.read_text(), after)
        restart.assert_called_once_with(["systemctl", "restart", "docker"], check=True)

    def test_creates_missing_config(self):
        with patch.object(mirror.subprocess, "run") as restart, patch.object(
                mirror.subprocess, "check_output", return_value=json.dumps([mirror.MIRROR])):
            mirror.configure(self.config)
        self.assertEqual(json.loads(self.config.read_text()), {"registry-mirrors": [mirror.MIRROR]})
        restart.assert_called_once()

    def test_reloads_preexisting_but_inactive_configuration(self):
        self.config.write_text(json.dumps({"registry-mirrors": [mirror.MIRROR]}))
        with patch.object(mirror.subprocess, "run") as restart, patch.object(
                mirror.subprocess, "check_output", side_effect=["[]", json.dumps([mirror.MIRROR])]):
            mirror.configure(self.config)
        restart.assert_called_once()

    def test_rejects_local_non_linux_and_self_hosted_before_mutation(self):
        for overrides in [{"GITHUB_ACTIONS": ""}, {"RUNNER_OS": "Darwin"},
                          {"RUNNER_ENVIRONMENT": "self-hosted"}]:
            with self.subTest(overrides=overrides), patch.dict(os.environ, overrides), patch.object(
                    mirror.subprocess, "run") as restart, patch.object(mirror.subprocess, "check_output") as info:
                with self.assertRaises(RuntimeError):
                    mirror.configure(self.config)
                self.assertFalse(self.config.exists())
                restart.assert_not_called()
                info.assert_not_called()

    def test_rejects_invalid_configuration_without_overwriting(self):
        for value in ["{broken", "[]", '{"registry-mirrors":"wrong"}', '{"registry-mirrors":[1]}']:
            with self.subTest(value=value):
                self.config.write_text(value)
                with patch.object(mirror.subprocess, "run") as restart, self.assertRaises(ValueError):
                    mirror.configure(self.config)
                self.assertEqual(self.config.read_text(), value)
                restart.assert_not_called()

    def test_restart_failure_is_fatal(self):
        with patch.object(mirror.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "systemctl")):
            with self.assertRaises(subprocess.CalledProcessError):
                mirror.configure(self.config)

    def test_missing_active_mirror_is_fatal(self):
        with patch.object(mirror.subprocess, "run"), patch.object(
                mirror.subprocess, "check_output", return_value="[]"), self.assertRaises(RuntimeError):
            mirror.configure(self.config)

    def test_daemon_info_failure_is_fatal(self):
        with patch.object(mirror.subprocess, "run"), patch.object(
                mirror.subprocess, "check_output", side_effect=subprocess.CalledProcessError(1, "docker")):
            with self.assertRaises(subprocess.CalledProcessError):
                mirror.configure(self.config)

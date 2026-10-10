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
        preload = patch.object(mirror, "preload_ryuk")
        self.preload = preload.start()
        self.addCleanup(preload.stop)
        postgres = patch.object(mirror, "preload_postgres")
        self.postgres = postgres.start()
        self.addCleanup(postgres.stop)

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
                self.preload.assert_not_called()
                self.postgres.assert_not_called()

    def test_rejects_invalid_configuration_without_overwriting(self):
        for value in ["{broken", "[]", '{"registry-mirrors":"wrong"}', '{"registry-mirrors":[1]}']:
            with self.subTest(value=value):
                self.config.write_text(value)
                with patch.object(mirror.subprocess, "run") as restart, self.assertRaises(ValueError):
                    mirror.configure(self.config)
                self.assertEqual(self.config.read_text(), value)
                restart.assert_not_called()
                self.preload.assert_not_called()

    def test_restart_failure_is_fatal(self):
        with patch.object(mirror.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "systemctl")):
            with self.assertRaises(subprocess.CalledProcessError):
                mirror.configure(self.config)
        self.preload.assert_not_called()

    def test_missing_active_mirror_is_fatal(self):
        with patch.object(mirror.subprocess, "run"), patch.object(
                mirror.subprocess, "check_output", return_value="[]"), self.assertRaises(RuntimeError):
            mirror.configure(self.config)
        self.preload.assert_not_called()

    def test_daemon_info_failure_is_fatal(self):
        with patch.object(mirror.subprocess, "run"), patch.object(
                mirror.subprocess, "check_output", side_effect=subprocess.CalledProcessError(1, "docker")):
            with self.assertRaises(subprocess.CalledProcessError):
                mirror.configure(self.config)
        self.preload.assert_not_called()

    def test_preloads_only_after_verified_daemon_configuration(self):
        operations = []
        with patch.object(mirror.subprocess, "run", side_effect=lambda *args, **kwargs: operations.append("restart")), patch.object(
                mirror.subprocess, "check_output", side_effect=lambda *args, **kwargs: operations.append("verified") or json.dumps([mirror.MIRROR])):
            self.preload.side_effect = lambda: operations.append("preload")
            self.postgres.side_effect = lambda: operations.append("postgres")
            mirror.configure(self.config, postgres=True)
        self.assertEqual(operations, ["restart", "verified", "preload", "postgres"])

    def test_preload_failure_is_fatal(self):
        self.preload.side_effect = RuntimeError("Ryuk image identity mismatch")
        with patch.object(mirror.subprocess, "run"), patch.object(
                mirror.subprocess, "check_output", return_value=json.dumps([mirror.MIRROR])):
            with self.assertRaisesRegex(RuntimeError, "Ryuk image identity"):
                mirror.configure(self.config)
        self.postgres.assert_not_called()

    def test_postgres_preload_failure_is_fatal(self):
        self.postgres.side_effect = RuntimeError("Pinned PostgreSQL image pull failed")
        with patch.object(mirror.subprocess, "run"), patch.object(
                mirror.subprocess, "check_output", return_value=json.dumps([mirror.MIRROR])):
            with self.assertRaisesRegex(RuntimeError, "PostgreSQL image pull"):
                mirror.configure(self.config, postgres=True)

    def test_non_postgres_jobs_do_not_pull_the_conformance_image(self):
        with patch.object(mirror.subprocess, "run"), patch.object(
                mirror.subprocess, "check_output", return_value=json.dumps([mirror.MIRROR])):
            mirror.configure(self.config)
        self.postgres.assert_not_called()


class RyukPreloadTests(unittest.TestCase):
    image_id = "sha256:" + "a" * 64

    def test_pulls_pinned_publisher_image_before_alias_and_verifies_ids(self):
        operations = []

        def run(command, **kwargs):
            self.assertEqual(kwargs, {"check": True})
            operations.append(command)

        def inspect(command, **kwargs):
            self.assertEqual(kwargs, {"text": True})
            operations.append(command)
            return self.image_id + "\n"

        with patch.object(mirror.subprocess, "run", side_effect=run), patch.object(
                mirror.subprocess, "check_output", side_effect=inspect):
            mirror.preload_ryuk()
        self.assertEqual(operations, [
            ["docker", "pull", mirror.RYUK_SOURCE],
            ["docker", "image", "inspect", mirror.RYUK_SOURCE, "--format", "{{.Id}}"],
            ["docker", "tag", mirror.RYUK_SOURCE, mirror.RYUK_STOCK_IMAGE],
            ["docker", "image", "inspect", mirror.RYUK_STOCK_IMAGE, "--format", "{{.Id}}"],
        ])
        self.assertEqual(mirror.RYUK_SOURCE,
                         "ghcr.io/testcontainers/ryuk:0.13.0@sha256:31b31269d06603366cbfd0284708dcd2e281e8a4188e53fce3d3304439d0df3d")
        self.assertEqual(mirror.RYUK_STOCK_IMAGE, "testcontainers/ryuk:0.13.0")

    def test_pull_failure_never_tags_or_inspects(self):
        with patch.object(mirror.subprocess, "run", side_effect=subprocess.CalledProcessError(
                1, "docker pull")) as run, patch.object(mirror.subprocess, "check_output") as inspect:
            with self.assertRaises(subprocess.CalledProcessError):
                mirror.preload_ryuk()
        self.assertEqual(run.call_count, 1)
        inspect.assert_not_called()

    def test_tag_failure_does_not_report_a_verified_alias(self):
        with patch.object(mirror.subprocess, "run", side_effect=[
                None, subprocess.CalledProcessError(1, "docker tag")]), patch.object(
                mirror.subprocess, "check_output", return_value=self.image_id) as inspect:
            with self.assertRaises(subprocess.CalledProcessError):
                mirror.preload_ryuk()
        self.assertEqual(inspect.call_count, 1)

    def test_alias_identity_mismatch_is_fatal(self):
        with patch.object(mirror.subprocess, "run"), patch.object(
                mirror.subprocess, "check_output", side_effect=[self.image_id, "sha256:" + "b" * 64]):
            with self.assertRaisesRegex(RuntimeError, "Ryuk image identity"):
                mirror.preload_ryuk()

    def test_invalid_source_identity_fails_before_tagging(self):
        for invalid in ["", "sha256:not-a-digest", self.image_id + " unexpected"]:
            with self.subTest(invalid=invalid), patch.object(mirror.subprocess, "run") as run, patch.object(
                    mirror.subprocess, "check_output", return_value=invalid):
                with self.assertRaisesRegex(RuntimeError, "image identity"):
                    mirror.preload_ryuk()
                self.assertEqual(run.call_count, 1)


class PostgresPreloadTests(unittest.TestCase):
    def test_uses_the_conformance_harness_digest_without_a_mutable_alias(self):
        image = mirror.postgres_image()
        self.assertRegex(image, r"^public\.ecr\.aws/docker/library/postgres:18-alpine@sha256:[a-f0-9]{64}$")
        operations = []
        with patch.object(mirror.subprocess, "run", side_effect=lambda command, **kwargs: operations.append(command)), patch.object(
                mirror, "image_id", side_effect=lambda image: operations.append(["inspect", image])):
            mirror.preload_postgres()
        self.assertEqual(operations, [["docker", "pull", image], ["inspect", image]])

    def test_rate_limit_retries_before_verified_preload(self):
        limited = subprocess.CalledProcessError(1, "docker", stderr="toomanyrequests: Rate exceeded")
        with patch.object(mirror.subprocess, "run", side_effect=[limited, None]) as pull, patch.object(
                mirror.time, "sleep") as sleep, patch.object(mirror, "image_id") as inspect:
            mirror.preload_postgres()
        self.assertEqual(pull.call_count, 2)
        sleep.assert_called_once_with(5)
        inspect.assert_called_once_with(mirror.postgres_image())
        self.assertEqual(pull.call_args.kwargs, {"check": True, "capture_output": True, "text": True, "timeout": 180})

    def test_persistent_throttling_fails_without_claiming_a_cached_image(self):
        limited = subprocess.CalledProcessError(1, "docker", stderr="toomanyrequests: Rate exceeded")
        with patch.object(mirror.subprocess, "run", side_effect=limited) as pull, patch.object(
                mirror.time, "sleep") as sleep, patch.object(mirror, "image_id") as inspect:
            with self.assertRaisesRegex(RuntimeError, "PostgreSQL image pull"):
                mirror.preload_postgres()
        self.assertEqual(pull.call_count, 3)
        self.assertEqual([call.args[0] for call in sleep.call_args_list], [5, 15])
        inspect.assert_not_called()

    def test_other_pull_errors_fail_immediately_without_retries(self):
        failed = subprocess.CalledProcessError(1, "docker", stderr="manifest unknown")
        with patch.object(mirror.subprocess, "run", side_effect=failed) as pull, patch.object(
                mirror.time, "sleep") as sleep, patch.object(mirror, "image_id") as inspect:
            with self.assertRaisesRegex(RuntimeError, "PostgreSQL image pull"):
                mirror.preload_postgres()
        self.assertEqual(pull.call_count, 1)
        sleep.assert_not_called()
        inspect.assert_not_called()

    def test_pull_timeout_is_bounded_and_fatal(self):
        with patch.object(mirror.subprocess, "run", side_effect=subprocess.TimeoutExpired("docker", 180)) as pull, patch.object(
                mirror.time, "sleep") as sleep, patch.object(mirror, "image_id") as inspect:
            with self.assertRaises(subprocess.TimeoutExpired):
                mirror.preload_postgres()
        self.assertEqual(pull.call_count, 1)
        sleep.assert_not_called()
        inspect.assert_not_called()

    def test_pull_success_without_verified_local_identity_is_fatal(self):
        with patch.object(mirror.subprocess, "run"), patch.object(
                mirror, "image_id", side_effect=RuntimeError("Invalid Docker image identity")):
            with self.assertRaisesRegex(RuntimeError, "image identity"):
                mirror.preload_postgres()

    def test_invalid_or_missing_harness_pin_fails_before_pull(self):
        for source in ["", 'const PostgreSQL18Image = "postgres:latest"',
                       'const PostgreSQL18Image = "public.ecr.aws/docker/library/postgres:18-alpine@sha256:bad"']:
            with self.subTest(source=source), patch.object(Path, "read_text", return_value=source), patch.object(
                    mirror.subprocess, "run") as pull:
                with self.assertRaisesRegex(ValueError, "PostgreSQL image pin"):
                    mirror.preload_postgres()
                pull.assert_not_called()

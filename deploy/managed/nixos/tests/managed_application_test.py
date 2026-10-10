"""Pure failure-boundary tests; real execution is the separate privileged runner."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from managed_application import Runtime, offline, release_request, verify_retained, validate_visible_paths, command_diagnostic, COMMAND_DIAGNOSTIC_PREFIX


class ApplicationBoundaryTests(unittest.TestCase):
    def controller_runtime(self):
        runtime = object.__new__(Runtime)
        runtime.controller = "/private/leapviewctl"
        runtime.profile_path = Path("/private/profile.json")
        runtime.evidence = {"passed": False}
        return runtime

    def test_failed_controller_retains_only_closed_diagnostics(self):
        runtime = self.controller_runtime()
        private = "postgres://operator:private-password@example.invalid/database"
        failure = SimpleNamespace(returncode=7, stdout=private, stderr=private)
        status = SimpleNamespace(returncode=0, stdout=json.dumps({"phase": "starting", "recovering": False,
                                 "commitEstablished": False, "private": private}), stderr=private)
        with patch("managed_application.run", side_effect=[failure, status]) as execute:
            with self.assertRaisesRegex(RuntimeError, "qualification controller command failed") as error:
                runtime.control("enroll", Path("/private/enrollment.json"))
        self.assertEqual(runtime.evidence["failure"], {"action": "enroll", "controllerOutcome": "exit",
                         "controllerExitCode": 7, "journalStatus": "available", "phase": "starting",
                         "recovering": False, "commitEstablished": False})
        self.assertFalse(runtime.evidence["passed"])
        self.assertNotIn(private, json.dumps(runtime.evidence) + str(error.exception))
        self.assertEqual(execute.call_count, 2)
        self.assertEqual(execute.call_args.args[3], "status")
        self.assertFalse(execute.call_args.kwargs["check"])

    def test_safe_controller_marker_retained_without_private_output(self):
        runtime = self.controller_runtime()
        diagnostic = {"schemaVersion": 1, "stage": "proxy-reboot", "command": "bundle",
                      "reason": "ssh-authentication", "exitCode": 23}
        failure = SimpleNamespace(returncode=1, stdout="private-output",
            stderr=COMMAND_DIAGNOSTIC_PREFIX + json.dumps(diagnostic) + "\nprivate-password")
        status = SimpleNamespace(returncode=1, stdout="private-status")
        with patch("managed_application.run", side_effect=[failure, status]):
            with self.assertRaises(RuntimeError):
                runtime.control("enroll", Path("/private/request.json"))
        self.assertEqual(runtime.evidence["failure"]["commandDiagnostic"], diagnostic)
        self.assertNotIn("private", json.dumps(runtime.evidence))

    def test_command_marker_rejects_unknown_oversize_and_duplicate_fields(self):
        valid = {"schemaVersion": 1, "stage": "inventory", "command": "docker", "reason": "unknown", "exitCode": -1}
        marker = COMMAND_DIAGNOSTIC_PREFIX + json.dumps(valid)
        self.assertEqual(command_diagnostic(marker), valid)
        invalid = [marker + "\n" + marker, "private" * 3000 + marker,
                   COMMAND_DIAGNOSTIC_PREFIX + json.dumps(valid)[:-1] + ', "exitCode": 2}',
                   COMMAND_DIAGNOSTIC_PREFIX + '[]', COMMAND_DIAGNOSTIC_PREFIX + '{']
        for key, value in (("stage", "private"), ("reason", "private"), ("command", "private"),
                           ("exitCode", True), ("exitCode", 256), ("schemaVersion", True), ("stage", []),
                           ("private", "secret")):
            invalid.append(COMMAND_DIAGNOSTIC_PREFIX + json.dumps(dict(valid, **{key: value})))
        for value in invalid:
            self.assertIsNone(command_diagnostic(value))

    def test_unavailable_or_invalid_status_never_masks_controller_failure(self):
        failure = SimpleNamespace(returncode=1, stdout="secret", stderr="secret")
        invalid = [SimpleNamespace(returncode=1, stdout="secret"),
                   SimpleNamespace(returncode=0, stdout="not JSON"),
                   SimpleNamespace(returncode=0, stdout=json.dumps({"phase": "secret", "recovering": False, "commitEstablished": False})),
                   SimpleNamespace(returncode=0, stdout=json.dumps({"phase": "starting", "recovering": "secret", "commitEstablished": False})),
                   SimpleNamespace(returncode=0, stdout="x" * 16385),
                   OSError("secret diagnostic exception")]
        for status in invalid:
            with self.subTest(status=status):
                runtime = self.controller_runtime()
                with patch("managed_application.run", side_effect=[failure, status]):
                    with self.assertRaisesRegex(RuntimeError, "qualification controller command failed"):
                        runtime.control("enroll", Path("/private/enrollment.json"))
                self.assertEqual(runtime.evidence["failure"], {"action": "enroll", "controllerOutcome": "exit",
                                 "controllerExitCode": 1, "journalStatus": "unavailable"})

    def test_controller_timeout_is_redacted_even_when_diagnostic_times_out(self):
        runtime = self.controller_runtime()
        timeout = subprocess.TimeoutExpired(["private-command", "private-password"], 1200,
                                            output="private-output", stderr="private-error")
        with patch("managed_application.run", side_effect=[timeout, timeout]):
            with self.assertRaisesRegex(RuntimeError, "qualification controller command timed out") as error:
                runtime.control("recover", Path("/private/recovery.json"))
        self.assertEqual(runtime.evidence["failure"], {"action": "recover", "controllerOutcome": "timeout",
                         "journalStatus": "unavailable"})
        self.assertNotIn("private-password", str(error.exception))

    def test_successful_controller_keeps_original_result(self):
        runtime = self.controller_runtime()
        result = SimpleNamespace(returncode=0, stdout="unchanged", stderr="")
        with patch("managed_application.run", return_value=result) as execute:
            self.assertIs(runtime.control("enroll", Path("/private/request.json")), result)
        execute.assert_called_once()
        self.assertNotIn("failure", runtime.evidence)

    def test_controller_failure_preserves_cleanup_and_public_report(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "inputs").mkdir()
            (root / "inputs/inputs.json").write_text("{}")
            staged = {"images": {}, "helpers": {}, "helperBuildFiles": {},
                      "onlineNamespaceIsolated": True, "userspaceNetworkStopped": True}
            (root / "staged.json").write_text(json.dumps(staged))
            evidence_dir = root / "evidence"
            evidence_dir.mkdir()
            controller = root / "leapviewctl"
            controller.write_text("fixture controller")
            args = SimpleNamespace(work_root=root, evidence_dir=evidence_dir, tools=root,
                                   docker_package=root, controller=controller)
            runtime = self.controller_runtime()
            runtime.cleanup = Mock()
            def construct(_args, _manifest, _staged, evidence):
                runtime.evidence = evidence
                runtime.prepare = Mock()
                runtime.bootstrap = Mock()
                runtime.managed_profile = Mock()
                runtime.request = Mock(return_value=root / "private-request.json")
                return runtime
            result = SimpleNamespace(returncode=1, stdout="private-value", stderr="private-value")
            with patch("managed_application.Runtime", side_effect=construct), patch("managed_application.run", return_value=result):
                with self.assertRaisesRegex(RuntimeError, "qualification controller command failed"):
                    offline(args)
            runtime.cleanup.assert_called_once()
            report = (evidence_dir / "application.json").read_text()
            public = json.loads(report)
            self.assertFalse(public["passed"])
            self.assertTrue(public["cleanupCompleted"])
            self.assertEqual(public["stage"], "enrollment")
            self.assertEqual(public["failure"]["controllerExitCode"], 1)
            self.assertNotIn("private-value", report)

    def test_support_daemon_cannot_delete_the_application_bridge(self):
        runtime = object.__new__(Runtime)
        runtime.staged = {"appDataRoot": "/tmp/app/data", "supportDataRoot": "/tmp/support/data"}
        runtime.dockerd_bin = "/pinned/dockerd"
        runtime.docker = Mock(return_value=SimpleNamespace(returncode=0))
        runtime.support_docker = Mock(return_value=SimpleNamespace(returncode=0))
        bridge_exists = False
        def launch(arguments, name):
            nonlocal bridge_exists
            if "--bridge=none" in arguments:
                self.assertFalse(bridge_exists, "bridge-disabled Docker startup deletes an existing docker0")
            else:
                bridge_exists = True
        runtime.launch = launch
        with patch("managed_application.private_file"):
            runtime.start_daemons()
        self.assertTrue(bridge_exists)
        runtime.docker.assert_called_once_with("info", check=False)
        runtime.support_docker.assert_called_once_with("info", check=False)

    def test_namespace_hides_installation_and_rejects_hidden_inputs(self):
        for path in ("/var/tmp/input", "/root/input", "/run/tool", "/opt/leapview/input", "/etc/leapview/config", "/usr/local/sbin/leapviewctl"):
            with self.assertRaises(ValueError):
                validate_visible_paths([Path(path)])

    def test_offline_restart_requires_original_repository_digest_and_id(self):
        expected = {"image": "ghcr.io/flidai/leapview@sha256:" + "a" * 64,
                    "imageID": "sha256:" + "b" * 64, "platform": "linux/amd64"}
        actual = {"Id": expected["imageID"], "RepoDigests": [expected["image"]], "Os": "linux", "Architecture": "amd64"}
        verify_retained(expected, actual)
        for field, changed in (("RepoDigests", []), ("Id", "sha256:" + "c" * 64), ("Architecture", "arm64")):
            broken = copy.deepcopy(actual)
            broken[field] = changed
            with self.assertRaises(ValueError):
                verify_retained(expected, broken)

    def test_request_uses_inspected_identity_and_authenticated_admission(self):
        first = {"image": "ghcr.io/flidai/leapview@sha256:" + "a" * 64, "sourceRevision": "b" * 40, "admissionDigest": "sha256:" + "c" * 64}
        second = {**first, "image": "ghcr.io/flidai/leapview@sha256:" + "d" * 64, "sourceRevision": "e" * 40}
        projection = {"configurationDigest": "sha256:" + "f" * 64, "credentialDigest": "sha256:" + "1" * 64}
        source = {"schema": 58, "migrations": {"001.sql": "hash"}}
        request = release_request(first, second, projection, projection, source)
        self.assertEqual(request["predecessor"]["artifactAdmissionDigest"], first["admissionDigest"])
        self.assertEqual(request["candidate"]["configurationDigest"], projection["configurationDigest"])
        with self.assertRaises(ValueError):
            release_request(first, second, projection, {**projection, "credentialDigest": "different"}, source)
        with self.assertRaises(ValueError):
            release_request(first, second, projection, projection, source, enrollment=True)


if __name__ == "__main__":
    unittest.main()

"""Pure failure-boundary tests; real execution is the separate privileged runner."""
import copy
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from managed_application import Runtime, release_request, verify_retained, validate_visible_paths


class ApplicationBoundaryTests(unittest.TestCase):
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

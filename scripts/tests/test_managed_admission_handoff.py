import copy
import errno
from datetime import datetime, timezone
import hashlib
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import warnings
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import managed_admission_handoff as handoff

REVISION = "a" * 40
IMAGE = "ghcr.io/flidai/leapview@sha256:" + "b" * 64
NOW = datetime(2026, 10, 8, tzinfo=timezone.utc)


def metadata():
    repository = {"full_name": "flidai/leapview", "id": 100}
    run = {"id": 12, "run_attempt": 2, "workflow_id": 34,
           "path": ".github/workflows/artifacts.yml",
           "repository": repository, "head_repository": repository,
           "event": "push", "status": "completed", "conclusion": "success",
           "head_branch": "main", "head_sha": REVISION,
           "run_started_at": "2026-10-07T10:00:00Z", "updated_at": "2026-10-07T11:00:00Z"}
    workflow = {"id": 34, "path": ".github/workflows/artifacts.yml"}
    artifact = {"id": 56, "name": "managed-admission-12-2-amd64", "digest": "sha256:" + "c" * 64,
                "expired": False, "created_at": "2026-10-07T10:30:00Z", "expires_at": "2026-11-07T10:30:00Z",
                "size_in_bytes": 1000, "workflow_run": {"id": 12, "repository_id": 100,
                "head_repository_id": 100, "head_branch": "main", "head_sha": REVISION}}
    return run, workflow, artifact


def fixture():
    files = {name: b"{}" for name in handoff.BUNDLE_FILES - {"binding.json"}}
    files["admission.digest"] = ("sha256:" + "d" * 64 + "\n").encode()
    binding = {"schemaVersion": 1, "repository": "flidai/leapview", "workflow": handoff.WORKFLOW,
               "event": "push", "ref": "refs/heads/main",
               "sourceRevision": REVISION, "image": IMAGE, "platform": "linux/amd64",
               "admissionDigest": "sha256:" + "d" * 64, "runId": "12", "runAttempt": "2",
               "files": {name: "sha256:" + hashlib.sha256(data).hexdigest() for name, data in files.items()}}
    files["binding.json"] = json.dumps(binding).encode()
    return files


def archive(files, extra=None):
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w") as output:
        for name, contents in files.items():
            output.writestr(name, contents)
        if extra:
            with warnings.catch_warnings():
                warnings.simplefilter("ignore", UserWarning)
                output.writestr(*extra)
    return buffer.getvalue()


class AuthenticatedHandoffTests(unittest.TestCase):
    def verify(self, run, workflow, artifact):
        return handoff.authorize(run, workflow, artifact, run_id=12, attempt=2,
                                 artifact_id=56, source=REVISION, platform="linux/amd64", now=NOW)

    def test_exact_successful_producer(self):
        self.assertEqual(self.verify(*metadata())["artifactId"], 56)

    def test_release_main_dispatch_is_separate_producer(self):
        run, workflow, artifact = metadata()
        run["event"] = "workflow_dispatch"
        workflow["path"] = ".github/workflows/release.yml"
        run["path"] = workflow["path"]
        self.assertEqual(self.verify(run, workflow, artifact)["event"], "workflow_dispatch")
        run["event"] = "push"
        with self.assertRaises(handoff.HandoffError):
            self.verify(run, workflow, artifact)

    def test_rejects_substituted_or_expired_authority(self):
        variants = [(0, "id", 99), (0, "run_attempt", 1), (0, "workflow_id", 35),
                    (0, "path", ".github/workflows/other.yml"),
                    (0, "event", "pull_request"), (0, "head_branch", "topic"),
                    (0, "conclusion", "failure"), (0, "head_sha", "f" * 40),
                    (1, "path", ".github/workflows/nix-oci-candidate.yml"),
                    (2, "id", 57), (2, "name", "managed-admission-12-1-amd64"),
                    (2, "expired", True), (2, "expires_at", "2026-10-01T00:00:00Z"),
                    (2, "created_at", "2026-10-07T09:00:00Z"), (2, "digest", ""),
                    (2, "size_in_bytes", handoff.MAX_ZIP_BYTES + 1)]
        for index, key, value in variants:
            with self.subTest(key=key, value=value):
                data = copy.deepcopy(metadata())
                data[index][key] = value
                with self.assertRaises(handoff.HandoffError):
                    self.verify(*data)
        for key, value in [("id", 99), ("head_sha", "f" * 40), ("head_repository_id", 101)]:
            with self.subTest(artifact_run=key):
                data = copy.deepcopy(metadata())
                data[2]["workflow_run"][key] = value
                with self.assertRaises(handoff.HandoffError):
                    self.verify(*data)

    def test_archive_and_binding_match_authenticated_selection(self):
        data = archive(fixture())
        result = handoff.verify_bundle(data, "sha256:" + hashlib.sha256(data).hexdigest(),
                                       self.verify(*metadata()), image=IMAGE)
        self.assertEqual(result["admission.json"], b"{}")

    def test_archive_rejects_changed_bytes_and_unsafe_members(self):
        link = zipfile.ZipInfo("admission.json")
        link.create_system = 3
        link.external_attr = (stat.S_IFLNK | 0o777) << 16
        for extra in [("../admission.json", b"{}"), ("/other", b"{}"),
                      ("admission.json", b"{}"), (link, b"admission.json"), ("other", b"{}")]:
            with self.subTest(extra=str(extra[0])):
                files = fixture()
                del files["image-config.json"]
                if isinstance(extra[0], zipfile.ZipInfo):
                    files["image-config.json"] = files.pop("admission.json")
                data = archive(files, extra)
                with self.assertRaises(handoff.HandoffError):
                    handoff.verify_bundle(data, "sha256:" + hashlib.sha256(data).hexdigest(),
                                          self.verify(*metadata()), image=IMAGE)
        with self.assertRaises(handoff.HandoffError):
            handoff.verify_bundle(archive(fixture()), "sha256:" + "0" * 64,
                                  self.verify(*metadata()), image=IMAGE)

    def test_binding_rejects_wrong_identity_hash_and_unknown_files(self):
        for key, value in [("sourceRevision", "f" * 40), ("runAttempt", "1"),
                           ("image", IMAGE.replace("b", "c")), ("platform", "linux/arm64"),
                           ("workflow", "flidai/leapview/.github/workflows/release.yml"),
                           ("schemaVersion", True), ("admissionDigest", "sha256:" + "e" * 64)]:
            files = fixture()
            binding = json.loads(files["binding.json"])
            binding[key] = value
            files["binding.json"] = json.dumps(binding).encode()
            data = archive(files)
            with self.subTest(key=key), self.assertRaises(handoff.HandoffError):
                handoff.verify_bundle(data, "sha256:" + hashlib.sha256(data).hexdigest(),
                                      self.verify(*metadata()), image=IMAGE)
        files = fixture()
        files["sbom.json"] = b"substituted"
        data = archive(files)
        with self.assertRaises(handoff.HandoffError):
            handoff.verify_bundle(data, "sha256:" + hashlib.sha256(data).hexdigest(),
                                  self.verify(*metadata()), image=IMAGE)

    def test_fetch_uses_exact_attempt_and_authenticated_artifact_endpoint(self):
        run, workflow, artifact = metadata()
        payload = archive(fixture())
        artifact["digest"] = "sha256:" + hashlib.sha256(payload).hexdigest()
        artifact["expires_at"] = "2099-01-01T00:00:00Z"
        with patch.object(handoff, "_github", side_effect=[json.dumps(run).encode(), json.dumps(workflow).encode(),
                                                         json.dumps(artifact).encode(), payload]) as github:
            authorization, downloaded = handoff.fetch(12, 2, 56, REVISION, "linux/amd64")
        self.assertEqual(downloaded, payload)
        self.assertEqual([call.args[0] for call in github.call_args_list],
                         ["actions/runs/12/attempts/2", "actions/workflows/34", "actions/artifacts/56", "actions/artifacts/56/zip"])
        self.assertEqual(handoff.verify_bundle(downloaded, authorization["artifactDigest"], authorization, image=IMAGE), fixture())

    def test_failed_producer_never_downloads_or_installs(self):
        run, workflow, artifact = metadata()
        run["conclusion"] = "failure"
        with patch.object(handoff, "_github", side_effect=[json.dumps(run).encode(), json.dumps(workflow).encode(),
                                                         json.dumps(artifact).encode()]) as github:
            with self.assertRaises(handoff.HandoffError):
                handoff.fetch(12, 2, 56, REVISION, "linux/amd64")
        self.assertEqual(github.call_count, 3)

    def test_invalid_selection_never_calls_github(self):
        with patch.object(handoff, "_github") as github:
            for values in [(-1, 2, 56, REVISION, "linux/amd64"), (12, 0, 56, REVISION, "linux/amd64"),
                           (12, 2, -1, REVISION, "linux/amd64"), (12, 2, 56, "main", "linux/amd64")]:
                with self.assertRaises(handoff.HandoffError):
                    handoff.fetch(*values)
        github.assert_not_called()

    def test_github_reader_bounds_bytes_and_time_and_reaps_child(self):
        real_popen = subprocess.Popen
        children = []

        def process(script):
            def start(*args, **kwargs):
                child = real_popen([sys.executable, "-c", script], **kwargs)
                children.append(child)
                return child
            return start

        with patch.object(handoff.subprocess, "Popen", side_effect=process("import os; os.write(1, b'x' * 65536)")):
            with self.assertRaisesRegex(handoff.HandoffError, "size limit"):
                handoff._github("unused", limit=100)
        with patch.object(handoff, "REQUEST_TIMEOUT_SECONDS", 0.05), patch.object(
                handoff.subprocess, "Popen", side_effect=process("import time; time.sleep(10)")):
            with self.assertRaisesRegex(handoff.HandoffError, "timed out"):
                handoff._github("unused", limit=100)
        self.assertTrue(all(child.poll() is not None for child in children))

    def test_duplicate_binding_fields_rejected(self):
        files = fixture()
        files["binding.json"] = files["binding.json"].replace(b'{', b'{"schemaVersion":1,', 1)
        data = archive(files)
        with self.assertRaises(handoff.HandoffError):
            handoff.verify_bundle(data, "sha256:" + hashlib.sha256(data).hexdigest(), self.verify(*metadata()), image=IMAGE)

    @unittest.skipUnless(os.geteuid() == 0, "root-owned receipt installation")
    def test_install_is_private_immutable_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            digest = "sha256:" + "a" * 64
            installed = handoff.install_receipt(root, digest, b"canonical receipt")
            self.assertEqual(installed.read_bytes(), b"canonical receipt")
            self.assertEqual(stat.S_IMODE(installed.stat().st_mode), 0o400)
            self.assertEqual(installed.stat().st_uid, 0)
            with self.assertRaises(handoff.HandoffError):
                handoff.install_receipt(root, digest, b"replacement")
            self.assertEqual(installed.read_bytes(), b"canonical receipt")

    @unittest.skipUnless(os.geteuid() == 0, "root-owned receipt installation")
    def test_install_rejects_symlink_and_writable_root(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = root / "target"
            target.mkdir(mode=0o700)
            (root / "link").symlink_to(target, target_is_directory=True)
            digest = "sha256:" + "a" * 64
            with self.assertRaises(handoff.HandoffError):
                handoff.install_receipt(root / "link", digest, b"receipt")
            target.chmod(0o770)
            with self.assertRaises(handoff.HandoffError):
                handoff.install_receipt(target, digest, b"receipt")
            target.chmod(0o700)
            outside = root / "outside"
            outside.write_bytes(b"original")
            (target / ("a" * 64 + ".json")).symlink_to(outside)
            with self.assertRaises(handoff.HandoffError):
                handoff.install_receipt(target, digest, b"receipt")
            self.assertEqual(outside.read_bytes(), b"original")

    @unittest.skipUnless(os.geteuid() == 0, "root-owned receipt installation")
    def test_install_collision_preserves_unowned_temporary_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            existing = root / ".import-collision"
            existing.write_bytes(b"existing")
            with patch.object(handoff.secrets, "token_hex", return_value="collision"):
                with self.assertRaises(handoff.HandoffError):
                    handoff.install_receipt(root, "sha256:" + "a" * 64, b"receipt")
            self.assertEqual(existing.read_bytes(), b"existing")

    @unittest.skipUnless(os.geteuid() == 0, "root-owned receipt installation")
    def test_install_cleans_own_temporary_on_write_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with patch.object(handoff.os, "fchmod", side_effect=OSError(errno.EROFS, "read-only filesystem")):
                with self.assertRaises(handoff.HandoffError):
                    handoff.install_receipt(root, "sha256:" + "a" * 64, b"receipt")
            self.assertEqual(list(root.iterdir()), [])

    @unittest.skipUnless(os.geteuid() == 0, "root-owned receipt installation")
    def test_install_rejects_untrusted_ancestor(self):
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary) / "untrusted"
            parent.mkdir(mode=0o700)
            root = parent / "admissions"
            root.mkdir(mode=0o700)
            os.chown(parent, 65534, 65534)
            try:
                with self.assertRaisesRegex(handoff.HandoffError, "untrusted ancestor"):
                    handoff.install_receipt(root, "sha256:" + "a" * 64, b"receipt")
            finally:
                os.chown(parent, 0, 0)
            parent.chmod(0o777)
            with self.assertRaisesRegex(handoff.HandoffError, "untrusted ancestor"):
                handoff.install_receipt(root, "sha256:" + "a" * 64, b"receipt")
            parent.chmod(0o700)
            self.assertEqual(list(root.iterdir()), [])

    @unittest.skipUnless(os.geteuid() == 0, "root-owned evidence retention")
    def test_retains_exact_authenticated_bundle_and_never_overwrites(self):
        with tempfile.TemporaryDirectory() as temporary:
            destination = Path(temporary) / "evidence"
            files = fixture()
            payload = archive(files)
            authorization = self.verify(*metadata())
            handoff.retain_evidence(destination, files, payload, authorization)
            self.assertEqual((destination / "artifact.zip").read_bytes(), payload)
            self.assertEqual(json.loads((destination / "github-authorization.json").read_bytes()), authorization)
            for name, content in files.items():
                retained = destination / name
                self.assertEqual(retained.read_bytes(), content)
                self.assertEqual(stat.S_IMODE(retained.stat().st_mode), 0o400)
            with self.assertRaises(handoff.HandoffError):
                handoff.retain_evidence(destination, files, b"replacement", authorization)
            self.assertEqual((destination / "artifact.zip").read_bytes(), payload)

    @unittest.skipUnless(os.geteuid() == 0, "root-owned evidence retention")
    def test_failed_retention_removes_only_own_partial_directory(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            destination = root / "evidence"
            existing = root / "existing"
            existing.write_bytes(b"preserved")
            with patch.object(handoff.os, "fchmod", side_effect=OSError(errno.EROFS, "read-only filesystem")):
                with self.assertRaises(handoff.HandoffError):
                    handoff.retain_evidence(destination, fixture(), b"zip", self.verify(*metadata()))
            self.assertFalse(destination.exists())
            self.assertEqual(existing.read_bytes(), b"preserved")


if __name__ == "__main__":
    unittest.main()

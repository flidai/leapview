import copy
from datetime import datetime, timedelta, timezone
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path, PurePosixPath
import stat
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import zipfile


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import nix_compose_controller_evidence as controller_evidence
import nix_compose_qualification as qualification
import package_compose_bundle as composer


IMAGE = "ghcr.io/flidai/leapview@sha256:" + "a" * 64
VERSION = "1.2.3"
BUILD_TIME = "2026-10-04T05:06:07Z"
GO_PHASES = (
    ("preflight", 900),
    ("target bootstrap", 1200),
    ("enterprise authoring", 1800),
    ("application upgrade", 900),
    ("performance", 2700),
    ("governance", 600),
    ("interruption recovery", 3600),
    ("restart persistence", 900),
    ("multi-node process", 1200),
)
GO_ASSERTIONS = (
    "oneTimeCredentials", "browserJourney", "performanceBudgets", "governedQuery", "auditedDenial",
    "interruptionRecovery", "restartPersistence", "multiNodeProcess", "upgradePersistence", "nativePostgresOnly",
)


def go_metadata(platform="linux/amd64"):
    goos, goarch = composer.PLATFORMS[platform]
    return {
        "GoVersion": "go1.26.8",
        "Path": composer.GO_PACKAGE,
        "Settings": [
            {"Key": "GOOS", "Value": goos},
            {"Key": "GOARCH", "Value": goarch},
            {"Key": "CGO_ENABLED", "Value": "0"},
        ],
    }


def identity_bytes(revision, **updates):
    value = {
        "version": VERSION,
        "revision": revision,
        "buildTime": BUILD_TIME,
        "dirty": False,
        "development": False,
        "image": IMAGE,
    }
    value.update(updates)
    return (json.dumps(value, indent=2) + "\n").encode()


def admission_bytes(*, platform=None, image=IMAGE, source_revision=None):
    evidence = {
        "schemaVersion": 1,
        "image": image,
        "digest": image.rsplit("@", 1)[1],
        "registryDigest": image.rsplit("@", 1)[1],
        "attestation": {
            "verified": True,
            "repository": "flidai/leapview",
            "workflow": "flidai/leapview/.github/workflows/release.yml",
            "sourceRevision": source_revision,
        },
        "sbom": {"discoverable": True, "predicateType": "https://spdx.dev/Document/v2.3"},
        "vulnerabilityPolicy": {"sha256": "b" * 64, "scanner": "trivy", "passed": True},
    }
    if platform is not None:
        evidence["vulnerabilityPolicy"]["platform"] = platform
    return (json.dumps(evidence) + "\n").encode()


def timestamp(value):
    value = value.astimezone(timezone.utc)
    result = value.strftime("%Y-%m-%dT%H:%M:%S")
    if value.microsecond:
        result += "." + f"{value.microsecond:06d}".rstrip("0")
    return result + "Z"


class ComposeQualificationTests(unittest.TestCase):
    def test_conventional_receipt_artifact_requires_exact_successful_release_binding(self):
        binding = self._release_binding()
        artifact = {"id": 552, "name": "compose-controller-build-identities-1001-2",
                    "digest": "sha256:" + "b" * 64, "expired": False,
                    "workflow_run": {"id": 1001, "head_branch": "main", "head_sha": self.revision}}
        self.assertEqual(qualification.select_conventional_receipt_artifact([artifact], binding), artifact)
        for field, value in (("id", True), ("expired", True), ("name", "compose-controller-build-identities-1001-1")):
            with self.subTest(field=field), self.assertRaises(qualification.QualificationError):
                qualification.select_conventional_receipt_artifact([{**artifact, field: value}], binding)
        for field, value in (("id", 1002), ("head_branch", "feature"), ("head_sha", "c" * 40)):
            changed = {**artifact, "workflow_run": {**artifact["workflow_run"], field: value}}
            with self.subTest(field=field), self.assertRaises(qualification.QualificationError):
                qualification.select_conventional_receipt_artifact([changed], binding)
        with self.assertRaises(qualification.QualificationError):
            qualification.select_conventional_receipt_artifact([artifact, artifact], binding)

    def _conventional_inputs(self, *, wrong_receipt=False):
        files = {}
        receipts = {}
        for arch in ("amd64", "arm64"):
            controller = self.root / ("controller-" + arch)
            controller.write_bytes(("controller " + arch).encode())
            controller.chmod(0o755)
            receipt = self.root / (arch + ".json")
            args = type("Args", (), {
                "platform": "linux/" + arch, "image_reference": IMAGE,
                "release_identity": self.release_identity, "controller": controller, "output": receipt,
                "controller_build_identity": receipt, "source_root": self.source,
                "package_name": f"leapview-compose-candidate-1001-2-linux-{arch}",
                "output_dir": self.root / ("assembled-" + arch),
            })()
            reader = lambda _path, arch=arch: go_metadata("linux/" + arch)
            composer.record_build_identity(args, metadata_reader=reader)
            _, archive, sidecar = composer.assemble(args, metadata_reader=reader)
            files["dist/" + archive.name] = archive.read_bytes()
            files["dist/" + sidecar.name] = sidecar.read_bytes()
            receipts[f"linux-{arch}.json"] = receipt.read_bytes()
        with zipfile.ZipFile(self.release_artifact_zip) as archive:
            files.update({name: archive.read(name) for name in archive.namelist() if name not in files})
        with zipfile.ZipFile(self.release_artifact_zip, "w") as archive:
            for name, data in files.items():
                archive.writestr(name, data)
        if wrong_receipt:
            altered = json.loads(receipts["linux-amd64.json"])
            altered["binarySHA256"] = "sha256:" + "f" * 64
            receipts["linux-amd64.json"] = json.dumps(altered).encode()
        receipt_zip = self.root / "receipts.zip"
        with zipfile.ZipFile(receipt_zip, "w") as archive:
            for name, data in receipts.items():
                archive.writestr(name, data)
        artifact = {"id": 552, "name": "compose-controller-build-identities-1001-2",
                    "digest": qualification._digest_bytes(receipt_zip.read_bytes()), "expired": False,
                    "workflow_run": {"id": 1001, "head_branch": "main", "head_sha": self.revision}}
        return receipt_zip, artifact, files, receipts

    def test_conventional_original_archives_and_receipts_survive_without_rebuilding(self):
        receipt_zip, artifact, files, receipts = self._conventional_inputs()
        output = self.root / "conventional"
        qualification.extract_conventional_bundles(self.release_artifact_zip, receipt_zip, artifact,
            self._release_binding(), self.source, output,
            metadata_reader=lambda path: go_metadata("linux/arm64" if b"arm64" in path.read_bytes() else "linux/amd64"))
        for arch in ("amd64", "arm64"):
            path = output / arch
            name = f"leapview-compose-candidate-1001-2-linux-{arch}.tar.gz"
            self.assertEqual((path / name).read_bytes(), files["dist/" + name])
            self.assertEqual((path / "controller-build-identity.json").read_bytes(), receipts[f"linux-{arch}.json"])
            producer = json.loads((path / "bundle-producer.json").read_bytes())
            self.assertEqual(producer["producer"], "conventional")
            self.assertFalse(producer["nixQualification"])
            self.assertFalse(producer["releaseAdmission"])
            self.assertEqual(producer["receiptArtifactDigest"], artifact["digest"])

    def test_conventional_receipt_must_match_actual_archived_controller(self):
        receipt_zip, artifact, _, _ = self._conventional_inputs(wrong_receipt=True)
        with self.assertRaisesRegex(qualification.QualificationError, "hash differs"):
            qualification.extract_conventional_bundles(self.release_artifact_zip, receipt_zip, artifact,
                self._release_binding(), self.source, self.root / "conventional",
                metadata_reader=lambda path: go_metadata())

    def test_conventional_receipt_zip_must_match_authenticated_api_digest(self):
        receipt_zip, artifact, _, _ = self._conventional_inputs()
        receipt_zip.write_bytes(receipt_zip.read_bytes() + b"changed")
        with self.assertRaisesRegex(qualification.QualificationError, "API digest"):
            qualification.extract_conventional_bundles(self.release_artifact_zip, receipt_zip, artifact,
                self._release_binding(), self.source, self.root / "conventional")

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.source = self.root / "source"
        self._source_checkout()
        self.revision = subprocess.check_output(["git", "-C", str(self.source), "rev-parse", "HEAD"], text=True).strip()
        self.release_identity = self.root / "handoff" / "release-identity.json"
        self.release_identity.parent.mkdir()
        self.release_identity.write_bytes(identity_bytes(self.revision))
        (self.release_identity.parent / "image-reference.txt").write_text(IMAGE + "\n", encoding="ascii")
        (self.release_identity.parent / "assembled-image-admission.json").write_bytes(
            admission_bytes(source_revision=self.revision)
        )
        self.release_artifact_zip = self.root / "release-artifact.zip"
        self._write_release_artifact_zip()
        self.controller = self.root / "nix-output" / "bin" / "leapviewctl"
        self.controller.parent.mkdir(parents=True)
        self.controller.write_bytes(b"trusted test controller contents\n")
        self.controller.chmod(0o755)
        self.receipt = self.root / "nix-output" / "controller-build-identity.json"
        self._write_build_receipt()
        self.output_dir = self.root / "bundle-output"
        self.bundle_root, self.archive, self.sidecar = self._assemble()

    def tearDown(self):
        self.temporary.cleanup()

    def _source_checkout(self):
        files = [
            "deploy/compose/compose.yaml",
            "deploy/compose/compose.postgres.yaml",
            "deploy/compose/compose.https.yaml",
            "deploy/compose/compose.first-install-bootstrap.yaml",
            "deploy/compose/Caddyfile",
            "deploy/compose/Caddyfile.first-install-bootstrap",
            "deploy/compose/first-install.env",
            "deploy/compose/README.md",
            "deploy/compose/QUALIFICATION.md",
            "deploy/compose/leapview.env.example",
            "deploy/compose/deployment.env.example",
            "deploy/compose/postgres/bundled-entrypoint.sh",
            "deploy/compose/postgres/bundled-init.sh",
            "deploy/host/files/leapviewctl-wrapper",
            "deploy/host/bootstrap-linux.sh",
            "deploy/local/compose.yaml",
            "deploy/local/README.md",
            "deploy/local/runtime-package.schema.json",
            "deploy/postgres/init.sh",
        ]
        for relative in files:
            source = ROOT / relative
            target = self.source / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
        shutil.copytree(ROOT / "deploy/compose/qualification", self.source / "deploy/compose/qualification")
        subprocess.run(["git", "init", "--quiet", str(self.source)], check=True)
        subprocess.run(["git", "-C", str(self.source), "add", "deploy"], check=True)
        subprocess.run(
            ["git", "-C", str(self.source), "-c", "user.name=LeapView Tests", "-c", "user.email=tests@example.invalid",
             "commit", "--quiet", "-m", "canonical fixture"], check=True,
        )

    def _write_build_receipt(self):
        identity_path = self.root / "release-identity-input.json"
        identity_path.write_bytes(identity_bytes(self.revision))
        composer.record_build_identity(
            type("Args", (), {
                "platform": "linux/amd64", "image_reference": IMAGE,
                "release_identity": identity_path, "controller": self.controller, "output": self.receipt,
            })(),
            metadata_reader=lambda _path: go_metadata(),
        )

    def _assemble(self, *, archive_name=None):
        package_name = archive_name or "leapview-compose-v1.2.3-linux-amd64"
        identity_path = self.root / "release-identity-input.json"
        args = type("Args", (), {
            "platform": "linux/amd64", "image_reference": IMAGE,
            "release_identity": identity_path, "controller": self.controller,
            "controller_build_identity": self.receipt, "package_name": package_name,
            "output_dir": self.output_dir, "source_root": self.source,
        })()
        package_root, archive, sidecar = composer.assemble(args, metadata_reader=lambda _path: go_metadata())
        return package_root, archive, sidecar

    def _verify(self, **updates):
        arguments = {
            "archive_path": self.archive,
            "sidecar_path": self.sidecar,
            "build_receipt_path": self.receipt,
            "source_root": self.source,
            "release_identity_path": self.release_identity,
            "platform": "linux/amd64",
            "source_revision": self.revision,
            "image": IMAGE,
            "metadata_reader": lambda _path: go_metadata(),
        }
        arguments.update(updates)
        return qualification.verify_bundle(**arguments)

    def _release_api_inputs(self):
        run = {
            "repository": "flidai/leapview", "workflowId": 73, "runId": 1001, "runAttempt": 2,
            "event": "workflow_dispatch", "status": "completed", "conclusion": "success",
            "branch": "main", "sourceRevision": self.revision,
        }
        workflow = {"id": 73, "path": ".github/workflows/release.yml"}
        artifact = {
            "id": 551, "name": "release-candidate-candidate-1001-2",
            "digest": "sha256:" + hashlib.sha256(self.release_artifact_zip.read_bytes()).hexdigest(),
            "expired": False, "runId": 1001, "runAttempt": 2, "repository": "flidai/leapview",
            "branch": "main", "sourceRevision": self.revision,
        }
        return run, workflow, artifact

    def _write_release_artifact_zip(self):
        with zipfile.ZipFile(self.release_artifact_zip, "w", compression=zipfile.ZIP_DEFLATED) as archive:
            archive.writestr("image-reference.txt", IMAGE + "\n")
            archive.writestr("release-identity.json", self.release_identity.read_bytes())
            archive.writestr("assembled-image-admission.json", (self.release_identity.parent / "assembled-image-admission.json").read_bytes())
            for os_name in ("linux", "darwin"):
                for arch in ("amd64", "arm64"):
                    name = f"dist/leapview-compose-candidate-1001-2-{os_name}-{arch}.tar.gz"
                    archive.writestr(name, b"opaque retained artifact")
                    archive.writestr(name + ".sha256", b"0" * 64 + b"  unused.tar.gz\n")

    def _release_binding(self):
        return qualification.verify_release_run(
            *self._release_api_inputs(), protected_revision=self.revision, source_root=self.source,
        )

    def _report(self, now=None):
        now = now or datetime.now(timezone.utc).replace(microsecond=0)
        started = now - timedelta(minutes=10)
        phases = []
        for index, (name, timeout) in enumerate(GO_PHASES):
            phases.append({
                "name": name,
                "result": "success",
                "startedAt": timestamp(started + timedelta(seconds=index * 60)),
                "durationMillis": 1000,
                "timeoutSeconds": timeout,
                "cleanupGuaranteed": True,
            })
        return {
            "schemaVersion": 1,
            "result": "success",
            "image": IMAGE,
            "architecture": "amd64",
            "startedAt": timestamp(started),
            "completedAt": timestamp(now),
            "elapsedSeconds": 600,
            "phases": phases,
            "assertions": {key: True for key in GO_ASSERTIONS},
            "multiNode": {
                "nodeCount": 2, "generationId": "generation:fixture",
                "abruptNodeLoss": True, "recovery": True, "rollingRestart": True,
                "durableConvergence": True,
            },
        }

    def _write_runtime_files(self):
        controller_runtime = {
            "product": "leapviewctl", "version": VERSION, "revision": self.revision,
            "buildTime": BUILD_TIME, "dirty": False, "development": False,
        }
        image_runtime = {
            "product": "leapview", "version": VERSION, "revision": self.revision,
            "buildTime": BUILD_TIME, "dirty": False, "development": False,
        }
        controller_path = self.root / "controller-runtime.json"
        image_path = self.root / "image-runtime.json"
        controller_path.write_text(json.dumps(controller_runtime) + "\n", encoding="utf-8")
        image_path.write_text(json.dumps(image_runtime) + "\n", encoding="utf-8")
        admission_path = self.root / "native-image-admission.json"
        admission_path.write_bytes(admission_bytes(platform="linux/amd64", source_revision=self.revision))
        evidence_dir = self.root / "installed-evidence"
        if evidence_dir.exists():
            shutil.rmtree(evidence_dir)
        evidence_dir.mkdir()
        report_path = evidence_dir / "qualification-report.json"
        report_path.write_text(json.dumps(self._report()) + "\n", encoding="utf-8")
        (evidence_dir / "performance-report.json").write_bytes(b"retained raw performance evidence\n")
        nested_evidence = evidence_dir / "browser"
        nested_evidence.mkdir()
        (nested_evidence / "capture.bin").write_bytes(b"raw nested capture\x00\xff")
        return controller_path, image_path, admission_path, report_path

    def _record_args(self):
        controller_runtime, image_runtime, admission_path, report_path = self._write_runtime_files()
        controller_evidence_dir = self.root / "controller-evidence"
        controller_evidence_dir.mkdir(exist_ok=True)
        (controller_evidence_dir / "controller-evidence.json").write_text(
            '{"fixture":"protected-controller-evidence"}\n', encoding="utf-8",
        )
        verifier = self.root / "protected-securitydependencies"
        verifier.write_text("protected fixture verifier\n", encoding="utf-8")
        verifier.chmod(0o755)
        return {
            "archive": self.archive, "sidecar": self.sidecar, "build_receipt": self.receipt,
            "source_root": self.source, "release_identity": self.release_identity,
            "release_artifact_zip": self.release_artifact_zip,
            "platform": "linux/amd64", "source_revision": self.revision, "image": IMAGE,
            "admission_evidence": admission_path, "controller_runtime_identity": controller_runtime,
            "runtime_identity": image_runtime, "qualification_report": report_path,
            "qualification_evidence_dir": report_path.parent,
            "controller_evidence_dir": controller_evidence_dir,
            "controller_binary_verifier": verifier,
            "release_authorization": self._release_binding(), "signer_revision": self.revision,
            "metadata_reader": lambda _path: go_metadata(),
        }

    def _controller_evidence_binding(self, *args, **kwargs):
        archive = Path(args[0])
        evidence_dir = Path(kwargs["evidence_dir"])
        try:
            receipt = (evidence_dir / "controller-evidence.json").read_bytes()
        except OSError as exc:
            raise qualification.QualificationError("controller evidence receipt is missing") from exc
        return {
            "bundle": {
                "archiveSHA256": qualification._digest_bytes(archive.read_bytes()),
                "controllerSHA256": qualification._digest_bytes(self.controller.read_bytes()),
                "sourceRevision": kwargs["source_revision"],
                "platform": kwargs["platform"],
            },
            "controllerEvidenceBindingDigest": qualification._digest_bytes(
                b"fixture-controller-evidence-binding\n" + receipt
            ),
            "reports": {
                "inventorySHA256": qualification._digest_bytes(b"fixture-controller-report-inventory"),
            },
        }

    def _record_qualification(self, **arguments):
        with patch.object(qualification, "_verify_controller_evidence", side_effect=self._controller_evidence_binding):
            return qualification.record_qualification(**arguments)

    def _verify_qualification(self, receipt_path, **arguments):
        with patch.object(qualification, "_verify_controller_evidence", side_effect=self._controller_evidence_binding):
            return qualification.verify_qualification(receipt_path, **arguments)

    def test_bundle_verification_binds_deterministic_outer_and_inner_content(self):
        extraction = self.root / "safe-extraction"
        original_umask = os.umask(0o077)
        try:
            binding = self._verify(extract_dir=extraction)
        finally:
            os.umask(original_umask)
        self.assertEqual(binding["archiveSHA256"], "sha256:" + hashlib.sha256(self.archive.read_bytes()).hexdigest())
        self.assertEqual(binding["controllerSHA256"], "sha256:" + hashlib.sha256(self.controller.read_bytes()).hexdigest())
        self.assertEqual(binding["controllerBuildIdentitySHA256"], "sha256:" + hashlib.sha256(self.receipt.read_bytes()).hexdigest())
        self.assertEqual(binding["sourceRevision"], self.revision)
        self.assertEqual(binding["platform"], "linux/amd64")
        self.assertEqual(binding["image"], IMAGE)
        self.assertEqual(binding["releaseAdmission"], False)
        self.assertEqual(Path(binding["extractedController"]).read_bytes(), self.controller.read_bytes())
        package_root = Path(binding["extractedController"]).parent
        self.assertEqual(package_root.stat().st_mode & 0o777, 0o755)
        self.assertEqual((package_root / "qualification").stat().st_mode & 0o777, 0o755)
        self.assertEqual((package_root / "compose.yaml").stat().st_mode & 0o777, 0o644)
        self.assertEqual((package_root / "leapviewctl").stat().st_mode & 0o777, 0o755)

    def test_bundle_verification_rejects_checksum_identity_receipt_source_and_go_platform_mismatch(self):
        sidecar = self.sidecar.read_bytes()
        self.sidecar.write_text("0" * 64 + sidecar[64:].decode(), encoding="ascii")
        with self.assertRaisesRegex(qualification.QualificationError, "checksum"):
            self._verify()
        self.sidecar.write_bytes(sidecar)

        identity = self.release_identity.read_bytes()
        self.release_identity.write_bytes(identity_bytes("f" * 40))
        with self.assertRaisesRegex(qualification.QualificationError, "protected release identity"):
            self._verify()
        self.release_identity.write_bytes(identity)

        bad_receipt = self.root / "bad-receipt.json"
        receipt = json.loads(self.receipt.read_text())
        receipt["binarySHA256"] = "sha256:" + "0" * 64
        bad_receipt.write_text(json.dumps(receipt), encoding="utf-8")
        with self.assertRaisesRegex(qualification.QualificationError, "build identity hash"):
            self._verify(build_receipt_path=bad_receipt)

        with self.assertRaisesRegex(qualification.QualificationError, "GOARCH"):
            self._verify(metadata_reader=lambda _path: go_metadata("linux/arm64"))

    def test_bundle_verification_rejects_unreviewed_source_asset(self):
        added = self.source / "deploy/compose/qualification/extra-unreviewed.txt"
        added.write_text("unexpected", encoding="utf-8")
        with self.assertRaisesRegex(qualification.QualificationError, "clean"):
            self._verify()
        added.unlink()

    def test_bundle_verification_accepts_canonical_long_package_name_pax_headers(self):
        package_name = "leapview-compose-" + "candidate." * 10 + "-linux-amd64"
        output = self.root / "long-name-output"
        args = type("Args", (), {
            "platform": "linux/amd64", "image_reference": IMAGE,
            "release_identity": self.root / "release-identity-input.json", "controller": self.controller,
            "controller_build_identity": self.receipt, "package_name": package_name,
            "output_dir": output, "source_root": self.source,
        })()
        _package_root, archive, sidecar = composer.assemble(args, metadata_reader=lambda _path: go_metadata())
        with tarfile.open(archive, "r:gz") as packed:
            self.assertTrue(any(member.pax_headers == {"path": member.name} for member in packed))
        binding = qualification.verify_bundle(
            archive, sidecar, self.receipt, self.source, self.release_identity,
            platform="linux/amd64", source_revision=self.revision, image=IMAGE,
            metadata_reader=lambda _path: go_metadata(),
        )
        self.assertEqual(binding["archiveSHA256"], "sha256:" + hashlib.sha256(archive.read_bytes()).hexdigest())

    def test_bundle_verification_accepts_native_arm64_build_receipt_and_metadata(self):
        arm_receipt = self.root / "nix-output" / "controller-build-identity-arm64.json"
        composer.record_build_identity(
            type("Args", (), {
                "platform": "linux/arm64", "image_reference": IMAGE,
                "release_identity": self.root / "release-identity-input.json",
                "controller": self.controller, "output": arm_receipt,
            })(),
            metadata_reader=lambda _path: go_metadata("linux/arm64"),
        )
        package_name = "leapview-compose-v1.2.3-linux-arm64"
        output = self.root / "arm64-output"
        args = type("Args", (), {
            "platform": "linux/arm64", "image_reference": IMAGE,
            "release_identity": self.root / "release-identity-input.json", "controller": self.controller,
            "controller_build_identity": arm_receipt, "package_name": package_name,
            "output_dir": output, "source_root": self.source,
        })()
        _package_root, archive, sidecar = composer.assemble(args, metadata_reader=lambda _path: go_metadata("linux/arm64"))
        binding = qualification.verify_bundle(
            archive, sidecar, arm_receipt, self.source, self.release_identity,
            platform="linux/arm64", source_revision=self.revision, image=IMAGE,
            metadata_reader=lambda _path: go_metadata("linux/arm64"),
        )
        self.assertEqual(binding["platform"], "linux/arm64")

    def test_bundle_verification_rejects_duplicate_tar_members_even_when_payload_matches(self):
        duplicate = self.root / self.archive.name
        self._rewrite_outer_archive(duplicate, duplicate_member=self.bundle_root.name + "/compose.yaml")
        sidecar = self.root / (duplicate.name + ".sha256")
        sidecar.write_text(hashlib.sha256(duplicate.read_bytes()).hexdigest() + "  " + duplicate.name + "\n", encoding="ascii")
        with self.assertRaisesRegex(qualification.QualificationError, "repeats a member"):
            self._verify(archive_path=duplicate, sidecar_path=sidecar)

    def test_bundle_verification_rejects_missing_inner_checksum_manifest(self):
        incomplete = self.root / "missing-sums" / self.archive.name
        incomplete.parent.mkdir()
        self._rewrite_outer_archive(incomplete, omit_member=self.bundle_root.name + "/SHA256SUMS")
        sidecar = incomplete.with_name(incomplete.name + ".sha256")
        sidecar.write_text(hashlib.sha256(incomplete.read_bytes()).hexdigest() + "  " + incomplete.name + "\n", encoding="ascii")
        with self.assertRaisesRegex(qualification.QualificationError, "file inventory"):
            self._verify(archive_path=incomplete, sidecar_path=sidecar)

    def _rewrite_outer_archive(self, output, *, duplicate_member=None, symlink_member=None, omit_member=None, traversal=False):
        with tarfile.open(self.archive, "r:gz") as source:
            members = []
            for original in source:
                stream = source.extractfile(original) if original.isfile() else None
                data = stream.read() if stream is not None else None
                member = copy.copy(original)
                if member.isdir() and not member.name.endswith("/"):
                    member.name += "/"
                members.append((member, data))
        raw = output.open("wb")
        with raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0, compresslevel=9) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as target:
                    for member, data in members:
                        if omit_member == member.name.rstrip("/"):
                            continue
                        if symlink_member == member.name.rstrip("/"):
                            link = tarfile.TarInfo(member.name.rstrip("/"))
                            link.type = tarfile.SYMTYPE
                            link.linkname = "../../outside"
                            link.mode = 0o777
                            link.uid = link.gid = 0
                            link.uname = link.gname = ""
                            link.mtime = 0
                            target.addfile(link)
                            continue
                        target.addfile(member, io.BytesIO(data) if data is not None else None)
                        if duplicate_member == member.name.rstrip("/"):
                            target.addfile(copy.copy(member), io.BytesIO(data))
                    if traversal:
                        bad = tarfile.TarInfo(self.bundle_root.name + "/../escape")
                        bad.mode = 0o644
                        bad.uid = bad.gid = 0
                        bad.uname = bad.gname = ""
                        bad.mtime = 0
                        bad.size = len(b"escape")
                        target.addfile(bad, io.BytesIO(b"escape"))

    def test_bundle_verification_rejects_deterministic_traversal_and_link_members(self):
        for case, rewrite_args, message in (
            ("symlink", {"symlink_member": self.bundle_root.name + "/leapviewctl"}, "symlink, hard link or special"),
            ("traversal", {"traversal": True}, "unsafe tar member path"),
        ):
            with self.subTest(case=case):
                malicious = self.root / case / self.archive.name
                malicious.parent.mkdir()
                self._rewrite_outer_archive(malicious, **rewrite_args)
                sidecar = malicious.with_name(malicious.name + ".sha256")
                sidecar.write_text(hashlib.sha256(malicious.read_bytes()).hexdigest() + "  " + malicious.name + "\n", encoding="ascii")
                with self.assertRaisesRegex(qualification.QualificationError, message):
                    self._verify(archive_path=malicious, sidecar_path=sidecar)

    def test_release_run_binding_requires_exact_successful_main_dispatch_and_ancestry(self):
        run, workflow, artifact = self._release_api_inputs()
        binding = qualification.verify_release_run(
            run, workflow, artifact, protected_revision=self.revision, source_root=self.source,
        )
        self.assertEqual(binding["releaseArtifactId"], artifact["id"])
        self.assertEqual(binding["releaseArtifactDigest"], artifact["digest"])
        self.assertEqual(binding["protectedWorkflowRevision"], self.revision)
        self.assertFalse(binding["releaseAdmission"])
        for field, value in (("status", "in_progress"), ("event", "push"), ("branch", "release")):
            rejected = copy.deepcopy(run)
            rejected[field] = value
            with self.subTest(field=field), self.assertRaises(qualification.QualificationError):
                qualification.verify_release_run(rejected, workflow, artifact, protected_revision=self.revision, source_root=self.source)
        unrelated = copy.deepcopy(run)
        unrelated["sourceRevision"] = "f" * 40
        unrelated_artifact = copy.deepcopy(artifact)
        unrelated_artifact["sourceRevision"] = "f" * 40
        with self.assertRaisesRegex(qualification.QualificationError, "ancestor"):
            qualification.verify_release_run(unrelated, workflow, unrelated_artifact, protected_revision=self.revision, source_root=self.source)
        rejected_artifact = copy.deepcopy(artifact)
        rejected_artifact["runId"] += 1
        with self.assertRaisesRegex(qualification.QualificationError, "artifact"):
            qualification.verify_release_run(run, workflow, rejected_artifact, protected_revision=self.revision, source_root=self.source)

    def test_release_artifact_zip_digest_and_safe_handoff_extraction(self):
        archive_path = self.release_artifact_zip
        artifact_digest = "sha256:" + hashlib.sha256(archive_path.read_bytes()).hexdigest()
        output = self.root / "extracted-handoff"
        binding = self._release_binding()
        result = qualification.extract_release_handoff(archive_path, artifact_digest, output, release_authorization=binding)
        self.assertEqual(result["sourceRevision"], self.revision)
        self.assertEqual(result["image"], IMAGE)
        self.assertFalse(result["releaseAdmission"])
        self.assertEqual(sorted(path.name for path in output.iterdir()), [
            "assembled-image-admission.json", "image-reference.txt", "release-identity.json",
        ])
        with self.assertRaisesRegex(qualification.QualificationError, "digest"):
            qualification.extract_release_handoff(archive_path, "sha256:" + "0" * 64, self.root / "wrong-digest")

    def test_release_artifact_zip_rejects_traversal_and_zip_symlink(self):
        for case, info in (
            ("traversal", "../outside"),
            ("symlink", "dist/link"),
        ):
            with self.subTest(case=case):
                path = self.root / (case + ".zip")
                with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED) as archive:
                    archive.writestr("image-reference.txt", IMAGE + "\n")
                    archive.writestr("release-identity.json", self.release_identity.read_bytes())
                    archive.writestr("assembled-image-admission.json", (self.release_identity.parent / "assembled-image-admission.json").read_bytes())
                    for os_name in ("linux", "darwin"):
                        for arch in ("amd64", "arm64"):
                            name = f"dist/leapview-compose-candidate-1001-2-{os_name}-{arch}.tar.gz"
                            archive.writestr(name, b"archive")
                            archive.writestr(name + ".sha256", b"sidecar")
                    if case == "traversal":
                        archive.writestr(info, b"escape")
                    else:
                        link = zipfile.ZipInfo(info)
                        link.create_system = 3
                        link.external_attr = (stat.S_IFLNK | 0o777) << 16
                        archive.writestr(link, "../../outside")
                digest = "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()
                with self.assertRaises(qualification.QualificationError):
                    qualification.extract_release_handoff(path, digest, self.root / (case + "-out"))

    def test_native_journey_receipt_binds_all_success_evidence_and_verifies_read_only(self):
        arguments = self._record_args()
        receipt = self._record_qualification(**arguments)
        self.assertEqual(receipt["bundle"]["archiveSHA256"], "sha256:" + hashlib.sha256(self.archive.read_bytes()).hexdigest())
        self.assertEqual(receipt["runtime"]["controllerIdentitySHA256"], "sha256:" + hashlib.sha256(arguments["controller_runtime_identity"].read_bytes()).hexdigest())
        self.assertEqual(receipt["runtime"]["imageIdentitySHA256"], "sha256:" + hashlib.sha256(arguments["runtime_identity"].read_bytes()).hexdigest())
        self.assertEqual(receipt["ociAdmission"]["evidenceSHA256"], "sha256:" + hashlib.sha256(arguments["admission_evidence"].read_bytes()).hexdigest())
        self.assertEqual(receipt["qualification"]["result"], "success")
        self.assertEqual(receipt["controllerEvidence"]["sourceRevision"], self.revision)
        self.assertEqual(receipt["controllerEvidence"]["platform"], "linux/amd64")
        self.assertEqual(receipt["controllerEvidence"]["receiptSHA256"], "sha256:" + hashlib.sha256(
            (arguments["controller_evidence_dir"] / "controller-evidence.json").read_bytes()
        ).hexdigest())
        self.assertFalse(receipt["releaseAdmission"])
        evidence = receipt["qualification"]["evidence"]
        self.assertEqual(evidence["fileCount"], 3)
        self.assertEqual(evidence["totalBytes"], sum(path.stat().st_size for path in arguments["qualification_evidence_dir"].rglob("*") if path.is_file()))
        self.assertEqual([entry["path"] for entry in evidence["files"]], [
            "browser/capture.bin", "performance-report.json", "qualification-report.json",
        ])
        self.assertEqual(evidence["inventorySHA256"], qualification._digest_bytes(
            b"leapview/nix-compose-qualification-evidence/v1\n"
            + qualification._canonical_bytes(evidence["files"])
        ))
        receipt_path = self.root / "qualification-receipt.json"
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        before = {path: path.read_bytes() for path in (
            self.archive, self.sidecar, self.receipt, self.release_identity,
            arguments["admission_evidence"], arguments["runtime_identity"],
            arguments["controller_runtime_identity"], arguments["qualification_report"], receipt_path,
        )}
        evidence_before = {
            path.relative_to(arguments["qualification_evidence_dir"]): path.read_bytes()
            for path in arguments["qualification_evidence_dir"].rglob("*") if path.is_file()
        }
        checked = self._verify_qualification(receipt_path, **arguments)
        self.assertEqual(checked, receipt)
        self.assertEqual(before, {path: path.read_bytes() for path in before})
        self.assertEqual(evidence_before, {
            path.relative_to(arguments["qualification_evidence_dir"]): path.read_bytes()
            for path in arguments["qualification_evidence_dir"].rglob("*") if path.is_file()
        })

        changed_artifact = self.root / "changed-release-artifact.zip"
        changed_artifact.write_bytes(arguments["release_artifact_zip"].read_bytes() + b"changed")
        changed = dict(arguments, release_artifact_zip=changed_artifact)
        with self.assertRaisesRegex(qualification.QualificationError, "API digest"):
            self._verify_qualification(receipt_path, **changed)

    def test_native_journey_receipt_rejects_changed_added_removed_and_unsafe_raw_evidence(self):
        arguments = self._record_args()
        receipt = self._record_qualification(**arguments)
        receipt_path = self.root / "evidence-inventory-receipt.json"
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        evidence_dir = arguments["qualification_evidence_dir"]

        cases = (
            ("altered", lambda: (evidence_dir / "performance-report.json").write_bytes(b"changed raw evidence"),
             lambda: (evidence_dir / "performance-report.json").write_bytes(b"retained raw performance evidence\n")),
            ("added", lambda: (evidence_dir / "additional.log").write_bytes(b"new file"),
             lambda: (evidence_dir / "additional.log").unlink()),
            ("removed", lambda: (evidence_dir / "browser" / "capture.bin").unlink(),
             lambda: (evidence_dir / "browser" / "capture.bin").write_bytes(b"raw nested capture\x00\xff")),
        )
        for name, mutate, restore in cases:
            with self.subTest(name=name):
                mutate()
                try:
                    with self.assertRaisesRegex(qualification.QualificationError, "evidence|inventory"):
                        self._verify_qualification(receipt_path, **arguments)
                finally:
                    restore()

        symlink = evidence_dir / "unsafe-link"
        symlink.symlink_to(evidence_dir / "performance-report.json")
        try:
            with self.assertRaisesRegex(qualification.QualificationError, "symlink|unsafe|regular"):
                self._verify_qualification(receipt_path, **arguments)
        finally:
            symlink.unlink()

        fifo = evidence_dir / "not-a-regular-file"
        os.mkfifo(fifo)
        try:
            with self.assertRaisesRegex(qualification.QualificationError, "regular|special|unsafe"):
                self._verify_qualification(receipt_path, **arguments)
        finally:
            fifo.unlink()

    def test_native_journey_receipt_requires_current_controller_evidence(self):
        arguments = self._record_args()
        receipt = self._record_qualification(**arguments)
        receipt_path = self.root / "controller-evidence-receipt.json"
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        controller_receipt = arguments["controller_evidence_dir"] / "controller-evidence.json"
        original = controller_receipt.read_bytes()

        controller_receipt.unlink()
        with self.assertRaisesRegex(qualification.QualificationError, "controller evidence"):
            self._verify_qualification(receipt_path, **arguments)

        controller_receipt.write_bytes(original + b"changed")
        with self.assertRaisesRegex(qualification.QualificationError, "freshly verified evidence"):
            self._verify_qualification(receipt_path, **arguments)

        with patch.object(controller_evidence, "verify_controller_evidence",
                          side_effect=ValueError("fixture evidence verifier failure")):
            with self.assertRaisesRegex(qualification.QualificationError, "protected controller evidence verification failed"):
                qualification.verify_qualification(receipt_path, **arguments)

    def test_native_journey_receipt_rejects_each_incomplete_identity_or_phase(self):
        arguments = self._record_args()
        report_path = arguments["qualification_report"]
        report = json.loads(report_path.read_text())
        report["result"] = "failure"
        report_path.write_text(json.dumps(report), encoding="utf-8")
        with self.assertRaisesRegex(qualification.QualificationError, "complete success"):
            self._record_qualification(**arguments)
        report = self._report()
        report["assertions"]["browserJourney"] = False
        report_path.write_text(json.dumps(report), encoding="utf-8")
        with self.assertRaisesRegex(qualification.QualificationError, "assertion"):
            self._record_qualification(**arguments)
        report = self._report()
        report["phases"][4]["result"] = "failure"
        report_path.write_text(json.dumps(report), encoding="utf-8")
        with self.assertRaisesRegex(qualification.QualificationError, "phases"):
            self._record_qualification(**arguments)
        report_path.write_text(json.dumps(self._report()), encoding="utf-8")

        arguments["controller_runtime_identity"].write_text(json.dumps({"version": VERSION}), encoding="utf-8")
        with self.assertRaisesRegex(qualification.QualificationError, "controller runtime identity"):
            self._record_qualification(**arguments)
        arguments = self._record_args()
        evidence = json.loads(arguments["admission_evidence"].read_text())
        evidence["registryDigest"] = "sha256:" + "0" * 64
        arguments["admission_evidence"].write_text(json.dumps(evidence), encoding="utf-8")
        with self.assertRaisesRegex(qualification.QualificationError, "OCI admission evidence"):
            self._record_qualification(**arguments)

    def test_report_timing_preserves_go_nanoseconds(self):
        report = self._report()
        report["startedAt"] = report["startedAt"].removesuffix("Z") + ".0000009Z"
        report["completedAt"] = report["completedAt"].removesuffix("Z") + ".0000001Z"
        report["elapsedSeconds"] = 599
        report["phases"][0]["startedAt"] = report["phases"][0]["startedAt"].removesuffix("Z") + ".000001Z"
        data = json.dumps(report).encode()
        self.assertEqual(
            qualification._validate_installed_report(data, IMAGE, "linux/amd64")["elapsedSeconds"], 599
        )
        report["elapsedSeconds"] = 600
        with self.assertRaisesRegex(qualification.QualificationError, "elapsed time"):
            qualification._validate_installed_report(json.dumps(report).encode(), IMAGE, "linux/amd64")
        report["elapsedSeconds"] = 599
        report["phases"][0]["startedAt"] = report["startedAt"].replace(".0000009Z", ".0000001Z")
        with self.assertRaisesRegex(qualification.QualificationError, "overlap"):
            qualification._validate_installed_report(json.dumps(report).encode(), IMAGE, "linux/amd64")

    def test_release_handoff_rejects_wrong_source_image_and_admission_identity(self):
        files = {
            "image-reference.txt": (IMAGE + "\n").encode(),
            "release-identity.json": self.release_identity.read_bytes(),
            "assembled-image-admission.json": (self.release_identity.parent / "assembled-image-admission.json").read_bytes(),
        }
        qualification.verify_release_handoff(files, self._release_binding())
        wrong_image = dict(files)
        wrong_image["image-reference.txt"] = ("ghcr.io/flidai/leapview@sha256:" + "d" * 64 + "\n").encode()
        with self.assertRaisesRegex(qualification.QualificationError, "image reference"):
            qualification.verify_release_handoff(wrong_image, self._release_binding())
        wrong_admission = dict(files)
        wrong_admission["assembled-image-admission.json"] = admission_bytes(source_revision="f" * 40)
        with self.assertRaisesRegex(qualification.QualificationError, "admission evidence"):
            qualification.verify_release_handoff(wrong_admission, self._release_binding())


if __name__ == "__main__":
    unittest.main()

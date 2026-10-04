import contextlib
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import nix_archive_go_evidence as go_evidence
import nix_cli_publication as publication
import nix_compose_controller_evidence as controller_evidence
import nix_compose_qualification as compose_qualification
import package_compose_bundle as composer


IMAGE = "ghcr.io/flidai/leapview@sha256:" + "a" * 64
VERSION = "1.2.3"
BUILD_TIME = "2026-10-04T05:06:07Z"
REVISION_USER = "Controller Evidence Tests"
EVIDENCE_TIME = datetime(2026, 10, 4, 12, 0, 0, tzinfo=timezone.utc)
BUILD_INFO = {
    "GoVersion": "go1.26.8",
    "Path": "github.com/flidai/leapview/cmd/leapviewctl",
    "Main": {"Path": "github.com/flidai/leapview", "Version": "(devel)"},
    "Deps": [{"Path": "example.com/dependency", "Version": "v1.2.3"}],
    "Settings": [
        {"Key": "GOOS", "Value": "linux"},
        {"Key": "GOARCH", "Value": "amd64"},
        {"Key": "CGO_ENABLED", "Value": "0"},
        {"Key": "GOAMD64", "Value": "v1"},
    ],
}
GO_RAW = b'{"config":"trusted fixture","sbom":"trusted fixture"}\n'
DB_MODIFIED = "2026-10-04T10:00:00Z"
SCANNED_AT = "2026-10-04T11:00:00Z"


def _elf_controller(machine=62):
    identification = b"\x7fELF" + bytes([2, 1, 1]) + bytes(9)
    header = struct.pack("<HHIQQQIHHHHHH", 2, machine, 1, 0, 64, 0, 0, 64, 56, 1, 0, 0, 0)
    segment = struct.pack("<IIQQQQQQ", 1, 5, 0, 0, 0, 0, 0, 1)
    return identification + header + segment


def _identity_bytes(revision):
    identity = {
        "version": VERSION,
        "revision": revision,
        "buildTime": BUILD_TIME,
        "dirty": False,
        "development": False,
        "image": IMAGE,
    }
    return (json.dumps(identity, indent=2) + "\n").encode()


def _bundle_go_metadata():
    return {
        "GoVersion": BUILD_INFO["GoVersion"],
        "Path": BUILD_INFO["Path"],
        "Settings": BUILD_INFO["Settings"],
    }


def _timestamp(value):
    return value.strftime("%Y-%m-%dT%H:%M:%SZ")


class ControllerEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.source = self.root / "source"
        self._create_source_checkout()
        self.revision = subprocess.check_output(
            ["git", "-C", str(self.source), "rev-parse", "HEAD"], text=True,
        ).strip()
        self.release_identity = self.root / "handoff" / "release-identity.json"
        self.release_identity.parent.mkdir()
        self.release_identity.write_bytes(_identity_bytes(self.revision))
        (self.release_identity.parent / "image-reference.txt").write_text(IMAGE + "\n", encoding="ascii")

        self.controller = self.root / "nix" / "bin" / "leapviewctl"
        self.controller.parent.mkdir(parents=True)
        self.controller.write_bytes(_elf_controller())
        self.controller.chmod(0o755)
        self.build_receipt = self.root / "nix" / "controller-build-identity.json"
        self._write_build_receipt()
        self.package_name = "leapview-compose-v1.2.3-linux-amd64"
        self.bundle_root, self.archive, self.sidecar = self._assemble()

        self.binary_verifier = self.root / "securitydependencies"
        self.binary_verifier.write_text("protected-test-verifier\n", encoding="utf-8")
        self.binary_verifier.chmod(0o755)
        self.evidence_dir = self.root / "controller-evidence"
        self.go_calls = []
        self.host_calls = []
        self.fail_offline = False
        self.fail_syft_version = False
        self.host_runtime_override = None
        self.host_os_override = None

    def tearDown(self):
        self.temporary.cleanup()

    def _create_source_checkout(self):
        files = [
            "deploy/compose/compose.yaml",
            "deploy/compose/compose.https.yaml",
            "deploy/compose/Caddyfile",
            "deploy/compose/README.md",
            "deploy/compose/QUALIFICATION.md",
            "deploy/compose/leapview.env.example",
            "deploy/compose/deployment.env.example",
            "deploy/host/files/leapviewctl-wrapper",
            "deploy/host/bootstrap-linux.sh",
            "deploy/local/compose.yaml",
            "deploy/local/README.md",
            "deploy/local/runtime-package.schema.json",
            "deploy/postgres/init.sh",
        ]
        for relative in files:
            target = self.source / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / relative, target)
        shutil.copytree(ROOT / "deploy/compose/qualification", self.source / "deploy/compose/qualification")
        subprocess.run(["git", "init", "--quiet", str(self.source)], check=True)
        subprocess.run(["git", "-C", str(self.source), "add", "deploy"], check=True)
        subprocess.run(
            ["git", "-C", str(self.source), "-c", f"user.name={REVISION_USER}",
             "-c", "user.email=tests@example.invalid", "commit", "--quiet", "-m", "controller evidence fixture"],
            check=True,
        )

    def _write_build_receipt(self):
        identity_input = self.root / "release-identity-input.json"
        identity_input.write_bytes(_identity_bytes(self.revision))
        composer.record_build_identity(
            type("Args", (), {
                "platform": "linux/amd64",
                "image_reference": IMAGE,
                "release_identity": identity_input,
                "controller": self.controller,
                "output": self.build_receipt,
            })(),
            metadata_reader=lambda _path: _bundle_go_metadata(),
        )

    def _assemble(self):
        identity_input = self.root / "release-identity-input.json"
        output_dir = self.root / "assembled"
        args = type("Args", (), {
            "platform": "linux/amd64",
            "image_reference": IMAGE,
            "release_identity": identity_input,
            "controller": self.controller,
            "controller_build_identity": self.build_receipt,
            "package_name": self.package_name,
            "output_dir": output_dir,
            "source_root": self.source,
        })()
        return (*composer.assemble(args, metadata_reader=lambda _path: _bundle_go_metadata()),)

    def _module_package(self, name, version):
        locator = "pkg:golang/" + name
        if version != "UNKNOWN":
            locator += "@" + version
        return {
            "SPDXID": "SPDXRef-" + name.replace("/", "-"),
            "name": name,
            "versionInfo": version,
            "externalRefs": [{
                "referenceCategory": "PACKAGE-MANAGER",
                "referenceType": "purl",
                "referenceLocator": locator,
            }],
        }

    def _spdx(self, binary):
        binary_bytes = Path(binary).read_bytes()
        binary_sha = hashlib.sha256(binary_bytes).hexdigest()
        stdlib = self._module_package("stdlib", "1.26.8")
        stdlib["versionInfo"] = "go1.26.8"
        packages = [
            self._module_package("github.com/flidai/leapview", "UNKNOWN"),
            self._module_package("example.com/dependency", "v1.2.3"),
            stdlib,
            {
                "SPDXID": "SPDXRef-Controller-File",
                "name": "leapviewctl",
                "versionInfo": "sha256:" + binary_sha,
                "primaryPackagePurpose": "FILE",
                "checksums": [{"algorithm": "SHA256", "checksumValue": binary_sha}],
            },
        ]
        if self.fail_syft_version:
            packages[1]["versionInfo"] = "v9.9.9"
            packages[1]["externalRefs"][0]["referenceLocator"] = "pkg:golang/example.com/dependency@v9.9.9"
        document = {
            "spdxVersion": "SPDX-2.3",
            "SPDXID": "SPDXRef-DOCUMENT",
            "documentNamespace": "https://example.invalid/controller-evidence",
            "creationInfo": {
                "creators": ["Tool: syft-" + publication.SYFT_VERSION],
                "created": _timestamp(EVIDENCE_TIME - timedelta(minutes=10)),
            },
            "packages": packages,
            "relationships": [{
                "spdxElementId": "SPDXRef-DOCUMENT",
                "relatedSpdxElement": "SPDXRef-Controller-File",
                "relationshipType": "DESCRIBES",
            }],
        }
        return json.dumps(document, separators=(",", ":")).encode()

    def _fake_go_verifier(self, verifier, binary, entry, platform, directory, *, offline):
        self.go_calls.append((Path(binary).read_bytes(), dict(entry), platform, Path(directory), offline))
        self.assertEqual(Path(verifier), self.binary_verifier)
        self.assertEqual(entry, controller_evidence.GO_ENTRY)
        self.assertEqual(platform, "linux/amd64")
        if offline:
            if self.fail_offline:
                raise ValueError("fixture offline verifier rejected evidence")
            raw = (Path(directory) / "govulncheck.json").read_bytes()
            summary = json.loads((Path(directory) / "summary.json").read_bytes())
            if (summary["binarySHA256"] != compose_qualification._digest_bytes(Path(binary).read_bytes())
                    or summary["reportSHA256"] != compose_qualification._digest_bytes(raw)
                    or raw != GO_RAW):
                raise ValueError("fixture offline verifier rejected binary evidence")
            return
        Path(directory).mkdir(parents=True)
        (Path(directory) / "govulncheck.json").write_bytes(GO_RAW)
        summary = {
            "schemaVersion": 1,
            "scope": "go-binary-only",
            "binarySHA256": compose_qualification._digest_bytes(Path(binary).read_bytes()),
            "buildInfo": BUILD_INFO,
            "scanner": {
                **controller_evidence.SCANNER_IDENTITY,
                "db_last_modified": DB_MODIFIED,
            },
            "reportSHA256": compose_qualification._digest_bytes(GO_RAW),
            "scannedAt": SCANNED_AT,
        }
        (Path(directory) / "summary.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")

    def _fake_syft(self, binary, output_path):
        body = self._spdx(binary)
        Path(output_path).write_bytes(body)
        return body, publication.SYFT_VERSION

    def _fake_host(self, binary, arch, output_directory):
        self.host_calls.append((Path(binary).read_bytes(), arch))
        self.assertEqual(arch, "amd64")
        hosts = []
        os_releases = {
            "debian12": b'ID=debian\nVERSION_ID="12"\n',
            "ubuntu2404": b'ID=ubuntu\nVERSION_ID="24.04"\n',
            "debian13": b'ID=debian\nVERSION_ID="13"\n',
        }
        for fixture in publication.HOST_FIXTURES:
            fixture_id = fixture["id"]
            release = self.host_os_override or os_releases[fixture_id]
            runtime = self.host_runtime_override or {
                "product": "leapviewctl",
                "version": VERSION,
                "revision": self.revision,
                "buildTime": BUILD_TIME,
                "dirty": False,
                "development": False,
            }
            (Path(output_directory) / f"{fixture_id}-os-release.txt").write_bytes(release)
            (Path(output_directory) / f"{fixture_id}-runtime-version.json").write_text(
                json.dumps(runtime) + "\n", encoding="utf-8",
            )
            (Path(output_directory) / f"{fixture_id}-runtime-help.txt").write_bytes(
                b"leapviewctl help fixture\n",
            )
            (Path(output_directory) / f"{fixture_id}-runtime-host-help.txt").write_bytes(
                b"leapviewctl host help fixture\n",
            )
            hosts.append({
                "id": fixture_id,
                "image": fixture["image"],
                "platform": "linux/amd64",
                "machine": "x86_64",
                "runtimeIdentity": runtime,
            })
        return hosts, "x86_64"

    @contextlib.contextmanager
    def _protected_tools(self):
        with (
            patch.object(compose_qualification.compose_bundle, "read_go_build_metadata",
                         return_value=_bundle_go_metadata()),
            patch.object(publication, "_build_info", return_value=BUILD_INFO),
            patch.object(go_evidence, "run_verifier", side_effect=self._fake_go_verifier),
            patch.object(publication, "_syft_spdx", side_effect=self._fake_syft),
            patch.object(publication, "_run_host", side_effect=self._fake_host),
            patch.object(controller_evidence.os, "geteuid", return_value=1000),
        ):
            yield

    def _qualify(self, *, now=EVIDENCE_TIME):
        with self._protected_tools():
            return controller_evidence.qualify_controller_evidence(
                self.archive, self.sidecar, self.build_receipt, self.source, self.release_identity,
                platform="linux/amd64", source_revision=self.revision, image=IMAGE,
                binary_verifier=self.binary_verifier, evidence_dir=self.evidence_dir, now=now,
            )

    def _verify(self, *, evidence_dir=None, now=EVIDENCE_TIME, **overrides):
        arguments = {
            "archive": self.archive,
            "sidecar": self.sidecar,
            "build_receipt": self.build_receipt,
            "source_root": self.source,
            "release_identity": self.release_identity,
            "platform": "linux/amd64",
            "source_revision": self.revision,
            "image": IMAGE,
            "binary_verifier": self.binary_verifier,
            "evidence_dir": evidence_dir or self.evidence_dir,
            "now": now,
        }
        arguments.update(overrides)
        with self._protected_tools():
            return controller_evidence.verify_controller_evidence(**arguments)

    def test_protected_evidence_binds_exact_bundle_go_spdx_and_all_native_hosts(self):
        with self._protected_tools():
            receipt = controller_evidence.qualify_controller_evidence(
                self.archive, self.sidecar, self.build_receipt, self.source, self.release_identity,
                platform="linux/amd64", source_revision=self.revision, image=IMAGE,
                binary_verifier=self.binary_verifier, evidence_dir=self.evidence_dir, now=EVIDENCE_TIME,
            )
        self.assertFalse(receipt["releaseAdmission"])
        self.assertEqual(receipt["bundle"]["archiveSHA256"],
                         compose_qualification._digest_bytes(self.archive.read_bytes()))
        self.assertEqual(receipt["bundle"]["controllerSHA256"],
                         compose_qualification._digest_bytes(self.controller.read_bytes()))
        self.assertEqual(receipt["bundle"]["controllerBuildIdentitySHA256"],
                         compose_qualification._digest_bytes(self.build_receipt.read_bytes()))
        self.assertEqual(receipt["goEvidence"]["binarySHA256"], receipt["bundle"]["controllerSHA256"])
        self.assertEqual(receipt["spdx"]["version"], publication.SYFT_VERSION)
        self.assertEqual([host["id"] for host in receipt["hosts"]],
                         [fixture["id"] for fixture in publication.HOST_FIXTURES])
        self.assertTrue(all(host["runtimeIdentity"]["development"] is False for host in receipt["hosts"]))
        self.assertEqual({entry["path"] for entry in receipt["reports"]["files"]},
                         controller_evidence.REPORT_PATH_SET)
        self.assertEqual([call[-1] for call in self.go_calls], [False, True])
        self.assertEqual(len(self.host_calls), 1)

        verified = self._verify()
        self.assertEqual(verified, receipt)
        self.assertEqual([call[-1] for call in self.go_calls], [False, True, True])

    def test_offline_compare_requires_exact_independent_artifact_copy(self):
        self._qualify()
        original = self.root / "original-controller-evidence"
        shutil.copytree(self.evidence_dir, original)
        with self._protected_tools():
            result = controller_evidence.compare_controller_evidence(
                self.archive, self.sidecar, self.build_receipt, self.source, self.release_identity,
                platform="linux/amd64", source_revision=self.revision, image=IMAGE,
                binary_verifier=self.binary_verifier, left_evidence_dir=original,
                right_evidence_dir=self.evidence_dir, now=EVIDENCE_TIME,
            )
        self.assertEqual(result["inventory"], json.loads(
            (self.evidence_dir / "controller-evidence.json").read_bytes()
        )["reports"])
        self.assertFalse(result["releaseAdmission"])

        (self.evidence_dir / "debian12-runtime-help.txt").write_bytes(b"changed after journey\n")
        # Help output is intentionally opaque: a freshly bound artifact with changed
        # output is internally valid, but it is not the exact artifact used by the journey.
        receipt_path = self.evidence_dir / "controller-evidence.json"
        prior_receipt = json.loads(receipt_path.read_bytes())
        with self._protected_tools():
            with controller_evidence._verify_bundle_context(
                self.archive, self.sidecar, self.build_receipt, self.source, self.release_identity,
                platform="linux/amd64", source_revision=self.revision, image=IMAGE,
            ) as (bundle, identity, _controller):
                refreshed = controller_evidence._expected_receipt(
                    bundle, identity, self.evidence_dir, prior_receipt["generatedAt"],
                    receipt_required=True, now=EVIDENCE_TIME,
                )
        receipt_path.write_bytes(controller_evidence._pretty_json(refreshed))
        self.assertEqual(self._verify(), refreshed)

        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "differs|inventory"):
            with self._protected_tools():
                controller_evidence.compare_controller_evidence(
                    self.archive, self.sidecar, self.build_receipt, self.source, self.release_identity,
                    platform="linux/amd64", source_revision=self.revision, image=IMAGE,
                    binary_verifier=self.binary_verifier, left_evidence_dir=original,
                    right_evidence_dir=self.evidence_dir, now=EVIDENCE_TIME,
                )

    def test_evidence_directory_rejects_extra_symlink_and_hardlinked_reports(self):
        self._qualify()
        extra = self.evidence_dir / "extra.log"
        extra.write_bytes(b"unbound extra data")
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "inventory"):
            self._verify()
        extra.unlink()

        symlink = self.evidence_dir / "unsafe-link"
        symlink.symlink_to(self.evidence_dir / "debian12-runtime-help.txt")
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "symlink"):
            self._verify()
        symlink.unlink()

        hardlink = self.evidence_dir / "debian12-runtime-help-copy.txt"
        os.link(self.evidence_dir / "debian12-runtime-help.txt", hardlink)
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "unlinked regular"):
            self._verify()
        hardlink.unlink()

    def test_verifier_rejects_wrong_bundle_source_platform_and_build_receipt(self):
        self._qualify()
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "checksum"):
            self._verify(sidecar=self.root / "wrong-sidecar")
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "source"):
            self._verify(source_revision="f" * 40)
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "platform"):
            self._verify(platform="linux/arm64")
        changed = self.root / "changed-controller-build-identity.json"
        changed.write_bytes(self.build_receipt.read_bytes().replace(b"amd64", b"arm64"))
        with self.assertRaises(controller_evidence.ControllerEvidenceError):
            self._verify(build_receipt=changed)

    def test_verifier_rejects_runtime_fixture_scanner_spdx_missing_and_stale_evidence(self):
        self._qualify()
        runtime_path = self.evidence_dir / "debian12-runtime-version.json"
        original_runtime = runtime_path.read_bytes()
        runtime = json.loads(original_runtime)
        runtime["development"] = True
        runtime_path.write_text(json.dumps(runtime), encoding="utf-8")
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "development|identity"):
            self._verify()
        runtime_path.write_bytes(original_runtime)

        os_path = self.evidence_dir / "ubuntu2404-os-release.txt"
        original_os = os_path.read_bytes()
        os_path.write_bytes(b"ID=wrong\nVERSION_ID=24.04\n")
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "fixture"):
            self._verify()
        os_path.write_bytes(original_os)

        raw_path = self.evidence_dir / "go" / "leapviewctl" / "govulncheck.json"
        raw_path.write_bytes(GO_RAW + b"tampered")
        self.fail_offline = True
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "offline"):
            self._verify()
        self.fail_offline = False
        raw_path.write_bytes(GO_RAW)

        spdx_path = self.evidence_dir / "sbom.spdx.json"
        original_spdx = spdx_path.read_bytes()
        spdx = json.loads(original_spdx)
        spdx["packages"][1]["versionInfo"] = "v8.8.8"
        spdx_path.write_text(json.dumps(spdx), encoding="utf-8")
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "SPDX"):
            self._verify()
        spdx_path.write_bytes(original_spdx)

        (self.evidence_dir / "debian13-runtime-host-help.txt").unlink()
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "inventory"):
            self._verify()

    def test_verifier_rejects_stale_receipt_and_modified_receipt_binding(self):
        self._qualify()
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "stale"):
            self._verify(now=EVIDENCE_TIME + timedelta(hours=121))

        receipt_path = self.evidence_dir / "controller-evidence.json"
        original = json.loads(receipt_path.read_bytes())
        original["bundle"]["controllerSHA256"] = "sha256:" + "0" * 64
        core = dict(original)
        core.pop("controllerEvidenceBindingDigest")
        original["controllerEvidenceBindingDigest"] = compose_qualification._digest_bytes(
            b"leapview/nix-compose-controller-evidence/v1\n" + compose_qualification._canonical_bytes(core)
        )
        receipt_path.write_text(json.dumps(original, indent=2) + "\n", encoding="utf-8")
        with self.assertRaisesRegex(controller_evidence.ControllerEvidenceError, "differs"):
            self._verify()


if __name__ == "__main__":
    unittest.main()

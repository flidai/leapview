import argparse
import hashlib
import importlib.util
import json
import os
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/package_compose_bundle.py"
SPEC = importlib.util.spec_from_file_location("package_compose_bundle", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
bundle = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bundle)

REVISION = "0123456789abcdef0123456789abcdef01234567"
IMAGE = "ghcr.io/flidai/leapview@sha256:" + "a" * 64
VERSION = "0.8.1+candidate.4"
BUILD_TIME = "2026-10-04T05:06:07Z"


def identity_bytes(**updates):
    identity = {
        "version": VERSION,
        "revision": REVISION,
        "buildTime": BUILD_TIME,
        "dirty": False,
        "development": True,
        "image": IMAGE,
    }
    identity.update(updates)
    return (json.dumps(identity, indent=2) + "\n").encode()


def settings_for(platform="linux/amd64"):
    goos, goarch = bundle.PLATFORMS[platform]
    return {
        "GoVersion": "go1.27.1",
        "Path": bundle.GO_PACKAGE,
        "Settings": [
            {"Key": "GOOS", "Value": goos},
            {"Key": "GOARCH", "Value": goarch},
            {"Key": "CGO_ENABLED", "Value": "0"},
        ],
    }


def write_candidate_inputs(directory, *, identity=None, controller_data=b"test controller bytes"):
    directory.mkdir(parents=True, exist_ok=True)
    controller = directory / "leapviewctl"
    controller.write_bytes(controller_data)
    controller.chmod(0o755)
    release_identity = directory / "release-identity.json"
    release_identity.write_bytes(identity if identity is not None else identity_bytes())
    return controller, release_identity


def record_receipt(controller, release_identity, output, *, platform="linux/amd64", image=IMAGE, metadata=None):
    arguments = argparse.Namespace(
        controller=controller,
        platform=platform,
        image_reference=image,
        release_identity=release_identity,
        output=output,
    )
    return bundle.record_build_identity(
        arguments,
        metadata_reader=(lambda _controller: metadata) if metadata is not None else bundle.read_go_build_metadata,
    )


def args_for(controller, release_identity, controller_build_identity, output_dir, *, package_name="leapview-compose-v0.8.1-linux-amd64", image=IMAGE):
    return argparse.Namespace(
        controller=controller,
        controller_build_identity=controller_build_identity,
        platform="linux/amd64",
        image_reference=image,
        release_identity=release_identity,
        package_name=package_name,
        output_dir=output_dir,
        source_root=ROOT,
    )


class ComposeBundleAssemblerTests(unittest.TestCase):
    def test_release_identity_step_uses_the_same_utc_commit_time_as_nix(self):
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        step = workflow.split("      - name: Resolve authoritative build identity\n", 1)[1].split("\n  image-platform:", 1)[0]
        shell = step.split("        run: |\n", 1)[1]
        shell = "\n".join(line[10:] for line in shell.splitlines())
        for timestamp in ("2026-10-04T05:06:07+00:00", "2026-10-04T10:36:07+05:30"):
            with self.subTest(timestamp=timestamp), tempfile.TemporaryDirectory() as temporary:
                checkout = Path(temporary)
                (checkout / "VERSION").write_text("0.8.1\n")
                (checkout / "package.json").write_text('{"version":"0.8.1"}\n')
                subprocess.run(["git", "init", "--quiet", str(checkout)], check=True)
                subprocess.run(["git", "-C", str(checkout), "add", "."], check=True)
                environment = dict(os.environ, GIT_AUTHOR_NAME="Candidate test", GIT_AUTHOR_EMAIL="candidate@example.invalid",
                                   GIT_COMMITTER_NAME="Candidate test", GIT_COMMITTER_EMAIL="candidate@example.invalid",
                                   GIT_AUTHOR_DATE=timestamp, GIT_COMMITTER_DATE=timestamp)
                subprocess.run(["git", "-C", str(checkout), "commit", "--quiet", "-m", "test: release identity"], env=environment, check=True)
                for event in ("push", "workflow_dispatch"):
                    output = checkout / (event + ".output")
                    environment.update(EVENT_NAME=event, RELEASE_TAG="v0.8.1", RUN_ID="42", RUN_ATTEMPT="1", GITHUB_OUTPUT=str(output))
                    subprocess.run(["bash", "-euo", "pipefail", "-c", shell], cwd=checkout, env=environment, check=True, capture_output=True)
                    values = dict(line.split("=", 1) for line in output.read_text().splitlines())
                    self.assertEqual(values["build_time"], BUILD_TIME)
                    self.assertTrue(bundle._canonical_utc(values["build_time"]))
                    _, identity = write_candidate_inputs(checkout / event, identity=identity_bytes(buildTime=values["build_time"]))
                    parsed, _ = bundle._load_identity(identity, IMAGE)
                    self.assertEqual(parsed["buildTime"], BUILD_TIME)

    def test_canonical_assets_modes_hashes_and_archives_are_stable_under_umask_0077(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            controller, release_identity = write_candidate_inputs(root / "inputs")
            receipt = record_receipt(controller, release_identity, root / "controller-build-identity.json", metadata=settings_for())
            first_output = root / "first"
            second_output = root / "second"
            metadata = settings_for()
            original_umask = os.umask(0o077)
            try:
                first = bundle.assemble(
                    args_for(controller, release_identity, receipt, first_output),
                    metadata_reader=lambda _controller: metadata,
                )
                second = bundle.assemble(
                    args_for(controller, release_identity, receipt, second_output),
                    metadata_reader=lambda _controller: metadata,
                )
                long_name = "leapview-compose-v1.2.3+" + "candidate." * 9 + "linux-amd64"
                long_named = bundle.assemble(
                    args_for(controller, release_identity, receipt, root / "long", package_name=long_name),
                    metadata_reader=lambda _controller: metadata,
                )
            finally:
                os.umask(original_umask)

            package_root, archive_path, sidecar = first
            repeated_root, repeated_archive, repeated_sidecar = second
            self.assertEqual(archive_path.read_bytes(), repeated_archive.read_bytes())
            self.assertEqual(sidecar.read_bytes(), repeated_sidecar.read_bytes())
            with tarfile.open(long_named[1], "r:gz") as archive:
                self.assertEqual(archive.getmembers()[0].name, long_named[0].name)
            self.assertEqual((package_root / "compose.yaml").read_bytes(), (ROOT / "deploy/compose/compose.yaml").read_bytes())
            self.assertEqual((package_root / "qualification/package.json").read_bytes(), (ROOT / "deploy/compose/qualification/package.json").read_bytes())
            self.assertEqual((package_root / "qualification/postgres-init.sh").read_bytes(), (ROOT / "deploy/postgres/init.sh").read_bytes())
            self.assertEqual((package_root / "local-runtime/postgres-init.sh").read_bytes(), (ROOT / "deploy/postgres/init.sh").read_bytes())
            for packaged, canonical in {
                "leapviewctl-wrapper": "deploy/host/files/leapviewctl-wrapper",
                "bootstrap-linux.sh": "deploy/host/bootstrap-linux.sh",
                "Caddyfile": "deploy/compose/Caddyfile",
                "compose.postgres.yaml": "deploy/compose/compose.postgres.yaml",
                "compose.https.yaml": "deploy/compose/compose.https.yaml",
                "compose.first-install-bootstrap.yaml": "deploy/compose/compose.first-install-bootstrap.yaml",
                "Caddyfile.first-install-bootstrap": "deploy/compose/Caddyfile.first-install-bootstrap",
                "first-install.env": "deploy/compose/first-install.env",
                "local-runtime/compose.yaml": "deploy/local/compose.yaml",
                "local-runtime/runtime-package.schema.json": "deploy/local/runtime-package.schema.json",
            }.items():
                self.assertEqual((package_root / packaged).read_bytes(), (ROOT / canonical).read_bytes(), packaged)
            self.assertEqual((package_root / "image-reference.txt").read_text(), IMAGE + "\n")
            self.assertIn(IMAGE, (package_root / "deployment.env.example").read_text())
            self.assertNotIn("<release-digest>", (package_root / "deployment.env.example").read_text())
            packaged_identity = json.loads((package_root / "release-identity.json").read_text())
            self.assertTrue(packaged_identity["development"], "development candidates are assemblable for qualification")
            runtime_package = json.loads((package_root / "local-runtime/runtime-package.json").read_text())
            self.assertEqual(runtime_package["leapview"]["image"], IMAGE)
            self.assertEqual(runtime_package["leapview"]["revision"], REVISION)

            executable = {"leapviewctl", "leapviewctl-wrapper", "bootstrap-linux.sh", "qualification/postgres-init.sh", "local-runtime/postgres-init.sh", "postgres/bundled-entrypoint.sh", "postgres/bundled-init.sh"}
            for path in package_root.rglob("*"):
                if path.is_dir():
                    self.assertEqual(path.stat().st_mode & 0o777, 0o755, path.relative_to(package_root))
                elif path.is_file():
                    relative = path.relative_to(package_root).as_posix()
                    expected = 0o755 if relative in executable or relative.startswith("qualification/") and relative.endswith(".sh") else 0o644
                    self.assertEqual(path.stat().st_mode & 0o777, expected, relative)

            checksums = {}
            for line in (package_root / "SHA256SUMS").read_text().splitlines():
                digest, relative = line.split("  ", 1)
                self.assertTrue(relative.startswith("./"))
                checksums[relative[2:]] = digest
            package_files = {path.relative_to(package_root).as_posix() for path in package_root.rglob("*") if path.is_file()} - {"SHA256SUMS"}
            self.assertEqual(set(checksums), package_files)
            for relative, digest in checksums.items():
                self.assertEqual(hashlib.sha256((package_root / relative).read_bytes()).hexdigest(), digest)
            self.assertEqual(hashlib.sha256(archive_path.read_bytes()).hexdigest(), sidecar.read_text().split()[0])

            with tarfile.open(archive_path, "r:gz") as archive:
                members = archive.getmembers()
                self.assertEqual(members[0].name, package_root.name)
                self.assertTrue(all(member.mtime == 0 and member.uid == 0 and member.gid == 0 for member in members))
                self.assertTrue(all(member.uname == "" and member.gname == "" for member in members))
                self.assertEqual(archive.extractfile(f"{package_root.name}/SHA256SUMS").read(), (package_root / "SHA256SUMS").read_bytes())

    def test_real_go_reader_inspects_trimpath_controllers_for_every_supported_platform(self):
        go = shutil.which("go")
        self.assertIsNotNone(go, "the assembler requires a trusted Go toolchain")
        with tempfile.TemporaryDirectory() as temporary:
            module = Path(temporary)
            (module / "go.mod").write_text("module github.com/flidai/leapview\n\ngo 1.23\n")
            command_dir = module / "cmd/leapviewctl"
            command_dir.mkdir(parents=True)
            (command_dir / "main.go").write_text("package main\nfunc main() {}\n")
            input_dir = module / "candidate-inputs"
            input_dir.mkdir()
            release_identity = input_dir / "release-identity.json"
            release_identity.write_bytes(identity_bytes())
            for platform in bundle.PLATFORMS:
                with self.subTest(platform=platform):
                    goos, goarch = bundle.PLATFORMS[platform]
                    controller = module / f"leapviewctl-{goos}-{goarch}"
                    environment = dict(os.environ, GOOS=goos, GOARCH=goarch, CGO_ENABLED="0", GOTOOLCHAIN="local")
                    subprocess.run(
                        [go, "build", "-trimpath", "-o", str(controller), "./cmd/leapviewctl"],
                        cwd=module,
                        env=environment,
                        check=True,
                        timeout=120,
                        capture_output=True,
                    )
                    metadata = bundle.read_go_build_metadata(controller)
                    self.assertIn("Settings", metadata)
                    bundle._validate_controller_metadata(metadata, platform)
                    receipt_path = input_dir / f"controller-build-identity-{goos}-{goarch}.json"
                    record_receipt(controller, release_identity, receipt_path, platform=platform)
                    receipt = json.loads(receipt_path.read_text())
                    self.assertEqual(set(receipt), bundle.BUILD_RECEIPT_FIELDS)
                    self.assertEqual(receipt["platform"], platform)
                    self.assertEqual(receipt["binarySHA256"], bundle._hash_regular(controller, "controller", bundle.MAX_CONTROLLER_BYTES))
                    bundle._load_build_receipt(receipt_path, json.loads(identity_bytes()), platform, receipt["binarySHA256"])

    def test_rejects_identity_platform_and_nonimmutable_reference_before_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            controller, release_identity = write_candidate_inputs(root / "inputs")
            receipt = record_receipt(controller, release_identity, root / "controller-build-identity.json", metadata=settings_for())
            output = root / "out"
            mismatched_receipt = root / "mismatched-build-identity.json"
            claimed = json.loads(receipt.read_text())
            claimed["version"] = "0.8.2+candidate.4"
            mismatched_receipt.write_text(json.dumps(claimed))
            with self.assertRaisesRegex(bundle.BundleError, "build identity version"):
                bundle.assemble(args_for(controller, release_identity, mismatched_receipt, output), metadata_reader=lambda _path: settings_for())
            wrong_platform_receipt = root / "wrong-platform-build-identity.json"
            claimed["version"] = VERSION
            claimed["platform"] = "linux/arm64"
            wrong_platform_receipt.write_text(json.dumps(claimed))
            with self.assertRaisesRegex(bundle.BundleError, "build identity platform"):
                bundle.assemble(args_for(controller, release_identity, wrong_platform_receipt, output), metadata_reader=lambda _path: settings_for())
            platform_metadata = settings_for()
            platform_metadata["Settings"][1]["Value"] = "arm64"
            with self.assertRaisesRegex(bundle.BundleError, "GOARCH"):
                bundle.assemble(args_for(controller, release_identity, receipt, output), metadata_reader=lambda _path: platform_metadata)
            with self.assertRaisesRegex(bundle.BundleError, "immutable"):
                bundle.assemble(args_for(controller, release_identity, receipt, output, image="ghcr.io/flidai/leapview:latest"), metadata_reader=lambda _path: self.fail("metadata inspected before image validation"))
            self.assertFalse(output.exists(), "invalid inputs must fail before output creation")

    def test_rejects_receipt_after_controller_bytes_change(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            controller, release_identity = write_candidate_inputs(root / "inputs")
            receipt = record_receipt(controller, release_identity, root / "controller-build-identity.json", metadata=settings_for())
            controller.write_bytes(b"mutated controller bytes")
            controller.chmod(0o755)
            output = root / "out"
            with self.assertRaisesRegex(bundle.BundleError, "binarySHA256 does not match"):
                bundle.assemble(args_for(controller, release_identity, receipt, output), metadata_reader=lambda _path: settings_for())
            self.assertFalse(output.exists(), "a stale receipt must fail before output creation")

    def test_rejects_invalid_receipt_schema_cgo_and_go_package(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            controller, release_identity = write_candidate_inputs(root / "inputs")
            receipt = record_receipt(controller, release_identity, root / "controller-build-identity.json", metadata=settings_for())
            output = root / "out"

            with self.subTest(reason="unsupported schema version"):
                value = json.loads(receipt.read_text())
                value["schemaVersion"] = 2
                invalid_schema = root / "unsupported-schema.json"
                invalid_schema.write_text(json.dumps(value))
                with self.assertRaisesRegex(bundle.BundleError, "schemaVersion must be 1"):
                    bundle.assemble(args_for(controller, release_identity, invalid_schema, output), metadata_reader=lambda _path: settings_for())

            with self.subTest(reason="receipt has an unexpected field"):
                value = json.loads(receipt.read_text())
                value["runtimeQualified"] = True
                invalid_schema = root / "extra-field.json"
                invalid_schema.write_text(json.dumps(value))
                with self.assertRaisesRegex(bundle.BundleError, "must contain exactly"):
                    bundle.assemble(args_for(controller, release_identity, invalid_schema, output), metadata_reader=lambda _path: settings_for())

            with self.subTest(reason="CGO enabled"):
                metadata = settings_for()
                metadata["Settings"][2]["Value"] = "1"
                with self.assertRaisesRegex(bundle.BundleError, "CGO_ENABLED"):
                    bundle.assemble(args_for(controller, release_identity, receipt, output), metadata_reader=lambda _path: metadata)

            with self.subTest(reason="wrong Go package"):
                metadata = settings_for()
                metadata["Path"] = "github.com/flidai/leapview/cmd/leapview"
                with self.assertRaisesRegex(bundle.BundleError, "Go package must"):
                    bundle.assemble(args_for(controller, release_identity, receipt, output), metadata_reader=lambda _path: metadata)

            self.assertFalse(output.exists(), "invalid receipt or controller metadata must fail before output creation")

    def test_validator_failure_leaves_no_bundle_archive_or_checksum(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            for relative in ("deploy/compose", "deploy/host/files", "deploy/postgres", "deploy/local"):
                (source / relative).mkdir(parents=True, exist_ok=True)
            for relative in (
                "deploy/compose/compose.yaml",
                "deploy/compose/compose.postgres.yaml",
                "deploy/compose/compose.https.yaml",
                "deploy/compose/compose.first-install-bootstrap.yaml",
                "deploy/compose/Caddyfile",
                "deploy/compose/Caddyfile.first-install-bootstrap",
                "deploy/compose/first-install.env",
                "deploy/compose/README.md",
                "deploy/compose/QUALIFICATION.md",
                "deploy/compose/deployment.env.example",
                "deploy/compose/leapview.env.example",
                "deploy/host/files/leapviewctl-wrapper",
                "deploy/host/bootstrap-linux.sh",
                "deploy/postgres/init.sh",
                "deploy/local/compose.yaml",
                "deploy/local/README.md",
                "deploy/local/runtime-package.schema.json",
            ):
                target = source / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(ROOT / relative, target)
            shutil.copytree(ROOT / "deploy/compose/qualification", source / "deploy/compose/qualification")
            shutil.copytree(ROOT / "deploy/compose/postgres", source / "deploy/compose/postgres")
            caddy = source / "deploy/compose/Caddyfile"
            caddy.write_text("example.invalid {\n  respond ok\n}\n")
            (source / "deploy/compose/qualification").chmod(0o755)
            controller, release_identity = write_candidate_inputs(root / "inputs")
            receipt = record_receipt(controller, release_identity, root / "controller-build-identity.json", metadata=settings_for())
            output = root / "out"
            arguments = args_for(controller, release_identity, receipt, output)
            arguments.source_root = source
            with self.assertRaisesRegex(bundle.BundleError, "validation failed"):
                bundle.assemble(arguments, metadata_reader=lambda _path: settings_for())
            self.assertFalse((output / arguments.package_name).exists())
            self.assertFalse((output / f"{arguments.package_name}.tar.gz").exists())
            self.assertFalse((output / f"{arguments.package_name}.tar.gz.sha256").exists())
            self.assertFalse(output.exists(), "failed validation should remove temporary staging output")

    def test_rejects_symlink_in_qualification_source_tree(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            shutil.copytree(ROOT / "deploy", source / "deploy", symlinks=True)
            qualification = source / "deploy/compose/qualification"
            (qualification / "linked-secret").symlink_to(Path(__file__))
            with self.assertRaisesRegex(bundle.BundleError, "symlink"):
                bundle._asset_inventory(source)


if __name__ == "__main__":
    unittest.main()

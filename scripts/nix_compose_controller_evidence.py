#!/usr/bin/env python3
"""Qualify exact Compose controller security and native-host evidence."""

from __future__ import annotations

import argparse
import calendar
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import tempfile

import nix_archive_go_evidence as go_evidence
import nix_cli_publication as publication
import nix_compose_qualification as compose_qualification


SCHEMA_VERSION = 1
SCOPE = "nix-compose-controller-evidence"
GO_ENTRY = {
    "id": "leapviewctl",
    "path": "leapviewctl",
    "package": "github.com/flidai/leapview/cmd/leapviewctl",
}
MAX_SUMMARY_BYTES = 2 * 1024**2
MAX_GO_REPORT_BYTES = 32 * 1024**2
MAX_SPDX_BYTES = 128 * 1024**2
MAX_RUNTIME_BYTES = publication.MAX_RUNTIME_BYTES
MAX_EVIDENCE_TOTAL_BYTES = 256 * 1024**2
MAX_FILE_COUNT = 32
MAX_AGE_NS = int(timedelta(hours=120).total_seconds() * 1_000_000_000)
FUTURE_SKEW_NS = 5 * 60 * 1_000_000_000
SCANNER_IDENTITY = {
    "protocol_version": "v1.0.0",
    "scanner_name": "govulncheck",
    "scanner_version": "v1.6.0",
    "db": "https://vuln.go.dev",
    "scan_level": "symbol",
    "scan_mode": "binary",
}
REPORT_PATHS = (
    "static.json",
    "go/leapviewctl/summary.json",
    "go/leapviewctl/govulncheck.json",
    "sbom.spdx.json",
    *(f"{fixture['id']}-{suffix}" for fixture in publication.HOST_FIXTURES
      for suffix in publication.HOST_REPORT_SUFFIXES),
)
REPORT_PATH_SET = set(REPORT_PATHS)
REPORT_DIRECTORIES = {"go", "go/leapviewctl"}
REPORT_LIMITS = {
    "static.json": compose_qualification.MAX_JSON_BYTES,
    "go/leapviewctl/summary.json": MAX_SUMMARY_BYTES,
    "go/leapviewctl/govulncheck.json": MAX_GO_REPORT_BYTES,
    "sbom.spdx.json": MAX_SPDX_BYTES,
}
for _fixture in publication.HOST_FIXTURES:
    for _suffix in publication.HOST_REPORT_SUFFIXES:
        REPORT_LIMITS[f"{_fixture['id']}-{_suffix}"] = MAX_RUNTIME_BYTES


class ControllerEvidenceError(ValueError):
    """Protected controller evidence failed verification."""


def _canonical_bytes(value):
    return compose_qualification._canonical_bytes(value)


def _json_bytes(data, label, limit=compose_qualification.MAX_JSON_BYTES):
    try:
        return compose_qualification._json_bytes(data, label, limit)
    except compose_qualification.QualificationError as exc:
        raise ControllerEvidenceError(str(exc)) from exc


def _digest(data):
    return compose_qualification._digest_bytes(data)


def _read(path, label, limit):
    try:
        return compose_qualification._read_regular(path, label, limit)
    except compose_qualification.QualificationError as exc:
        raise ControllerEvidenceError(str(exc)) from exc


def _timestamp_ns(value, label):
    if not isinstance(value, str):
        raise ControllerEvidenceError(f"{label} must be a canonical UTC RFC3339Nano timestamp")
    match = re.fullmatch(r"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?Z", value)
    if match is None or (match.group(2) is not None and match.group(2).endswith("0")):
        raise ControllerEvidenceError(f"{label} must be a canonical UTC RFC3339Nano timestamp")
    try:
        base = datetime.strptime(match.group(1), "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    except ValueError:
        raise ControllerEvidenceError(f"{label} is not a valid timestamp") from None
    fraction = int((match.group(2) or "").ljust(9, "0") or "0")
    return calendar.timegm(base.utctimetuple()) * 1_000_000_000 + fraction


def _now_ns(now=None):
    current = now or datetime.now(timezone.utc)
    if not isinstance(current, datetime) or current.tzinfo is None or current.utcoffset() != timedelta(0):
        raise ControllerEvidenceError("verification time must be an aware UTC datetime")
    return calendar.timegm(current.utctimetuple()) * 1_000_000_000 + current.microsecond * 1_000


def _format_timestamp(now=None):
    current = now or datetime.now(timezone.utc)
    if not isinstance(current, datetime) or current.tzinfo is None or current.utcoffset() != timedelta(0):
        raise ControllerEvidenceError("evidence generation time must be UTC")
    current = current.astimezone(timezone.utc)
    value = current.strftime("%Y-%m-%dT%H:%M:%S")
    if current.microsecond:
        value += "." + f"{current.microsecond:06d}".rstrip("0")
    return value + "Z"


def _fresh_timestamp(value, now_ns, label):
    timestamp = _timestamp_ns(value, label)
    if timestamp > now_ns + FUTURE_SKEW_NS or now_ns - timestamp >= MAX_AGE_NS:
        raise ControllerEvidenceError(f"{label} is future-dated or stale")
    return timestamp


def _safe_evidence_tree(root, *, receipt_required):
    root = Path(os.path.abspath(os.fspath(root)))
    for component in (root, *root.parents):
        try:
            info = component.lstat()
        except OSError as exc:
            raise ControllerEvidenceError(f"cannot inspect controller evidence directory: {exc}") from exc
        if stat.S_ISLNK(info.st_mode):
            raise ControllerEvidenceError("controller evidence cannot traverse symlinked directories")
        if component != root and not stat.S_ISDIR(info.st_mode):
            raise ControllerEvidenceError("controller evidence parent is not a real directory")
    if not stat.S_ISDIR(root.lstat().st_mode):
        raise ControllerEvidenceError("controller evidence root must be a real directory")

    expected_files = REPORT_PATH_SET | ({"controller-evidence.json"} if receipt_required else set())
    files, directories = set(), set()
    pending = [(root, PurePosixPath())]
    while pending:
        directory, relative_directory = pending.pop()
        try:
            with os.scandir(directory) as scan:
                children = sorted(scan, key=lambda entry: entry.name)
        except OSError as exc:
            raise ControllerEvidenceError(f"cannot enumerate controller evidence: {exc}") from exc
        for child in children:
            name = child.name
            if not name or name in {".", ".."} or "/" in name or "\\" in name or "\x00" in name:
                raise ControllerEvidenceError("controller evidence contains a noncanonical path")
            relative = relative_directory / name
            path = relative.as_posix()
            try:
                encoded = path.encode("utf-8", "strict")
                info = child.stat(follow_symlinks=False)
            except (OSError, UnicodeEncodeError) as exc:
                raise ControllerEvidenceError(f"controller evidence path cannot be inspected safely: {exc}") from exc
            if len(encoded) > 4096 or PurePosixPath(path).as_posix() != path:
                raise ControllerEvidenceError("controller evidence contains an unsafe or oversized path")
            if stat.S_ISLNK(info.st_mode):
                raise ControllerEvidenceError("controller evidence cannot contain symlinks")
            if stat.S_ISDIR(info.st_mode):
                directories.add(path)
                pending.append((Path(child.path), relative))
            elif stat.S_ISREG(info.st_mode) and info.st_nlink == 1:
                files.add(path)
            else:
                raise ControllerEvidenceError("controller evidence must contain only unlinked regular files and directories")
            if len(files) > MAX_FILE_COUNT:
                raise ControllerEvidenceError("controller evidence contains too many files")
    if files != expected_files or directories != REPORT_DIRECTORIES:
        missing = sorted(expected_files - files)
        unexpected = sorted(files - expected_files)
        raise ControllerEvidenceError(
            f"controller evidence inventory is incomplete or unexpected (missing={missing[:3]}, unexpected={unexpected[:3]})"
        )
    return root


def _report_inventory(root, *, receipt_required):
    root = _safe_evidence_tree(root, receipt_required=receipt_required)
    records, data_by_path, total = [], {}, 0
    for path in sorted(REPORT_PATH_SET):
        data = _read(root / path, "controller evidence " + path, REPORT_LIMITS[path])
        total += len(data)
        if total > MAX_EVIDENCE_TOTAL_BYTES:
            raise ControllerEvidenceError("controller evidence exceeds its total byte limit")
        records.append({"path": path, "sizeBytes": len(data), "sha256": _digest(data)})
        data_by_path[path] = data
    inventory = {
        "fileCount": len(records),
        "totalBytes": total,
        "files": records,
        "inventorySHA256": _digest(
            b"leapview/nix-compose-controller-reports/v1\n" + _canonical_bytes(records)
        ),
    }
    return root, inventory, data_by_path


def _go_summary(data, raw_report, binary_sha256, build_info, now_ns, *, generation_ns=None):
    summary = _json_bytes(data, "Go binary security summary", MAX_SUMMARY_BYTES)
    expected_fields = {
        "schemaVersion", "scope", "binarySHA256", "buildInfo", "scanner", "reportSHA256", "scannedAt",
    }
    if (
        not isinstance(summary, dict) or set(summary) != expected_fields
        or type(summary["schemaVersion"]) is not int or summary["schemaVersion"] != 1
        or summary["scope"] != "go-binary-only" or summary["binarySHA256"] != binary_sha256
        or _canonical_bytes(summary["buildInfo"]) != _canonical_bytes(build_info)
        or summary["reportSHA256"] != _digest(raw_report)
    ):
        raise ControllerEvidenceError("Go security summary differs from the exact controller or raw report")
    scanner = summary["scanner"]
    if (
        not isinstance(scanner, dict) or set(scanner) != set(SCANNER_IDENTITY) | {"db_last_modified"}
        or any(scanner.get(key) != value for key, value in SCANNER_IDENTITY.items())
    ):
        raise ControllerEvidenceError("Go security summary does not use the protected binary scanner contract")
    scanned_ns = _fresh_timestamp(summary["scannedAt"], now_ns, "Go vulnerability scan")
    db_modified_ns = _timestamp_ns(scanner["db_last_modified"], "Go vulnerability database timestamp")
    if db_modified_ns > scanned_ns:
        raise ControllerEvidenceError("Go vulnerability database timestamp is later than its scan")
    if generation_ns is not None and scanned_ns > generation_ns + FUTURE_SKEW_NS:
        raise ControllerEvidenceError("Go vulnerability scan postdates controller evidence generation")
    return {
        "binarySHA256": binary_sha256,
        "summarySHA256": _digest(data),
        "reportSHA256": _digest(raw_report),
        "buildInfoSHA256": _digest(_canonical_bytes(build_info)),
        "scanner": scanner,
        "scannedAt": summary["scannedAt"],
    }


def _validate_spdx(data, build_info, binary_sha256, now_ns, *, generation_ns=None):
    try:
        document = publication.validate_spdx(data, build_info, binary_sha256)
    except (ValueError, KeyError, TypeError) as exc:
        raise ControllerEvidenceError(f"controller SPDX evidence is invalid: {exc}") from exc
    creation = document.get("creationInfo")
    if not isinstance(creation, dict) or not isinstance(creation.get("created"), str):
        raise ControllerEvidenceError("controller SPDX evidence has no creation timestamp")
    created_ns = _fresh_timestamp(creation["created"], now_ns, "Syft SPDX creation time")
    if generation_ns is not None and created_ns > generation_ns + FUTURE_SKEW_NS:
        raise ControllerEvidenceError("Syft SPDX report postdates controller evidence generation")
    return {
        "tool": "syft-" + publication.SYFT_VERSION,
        "reportSHA256": _digest(data),
        "documentNamespace": document["documentNamespace"],
        "createdAt": creation["created"],
    }


def _host_records(data_by_path, identity, arch, machine):
    hosts = []
    for fixture in publication.HOST_FIXTURES:
        fixture_id = fixture["id"]
        os_data = data_by_path[f"{fixture_id}-os-release.txt"]
        version_data = data_by_path[f"{fixture_id}-runtime-version.json"]
        help_data = data_by_path[f"{fixture_id}-runtime-help.txt"]
        host_help_data = data_by_path[f"{fixture_id}-runtime-host-help.txt"]
        if not os_data or not help_data or not host_help_data:
            raise ControllerEvidenceError("pinned host OS or CLI compatibility report is empty")
        try:
            publication._validate_os_release(os_data, fixture)
        except ValueError as exc:
            raise ControllerEvidenceError(f"pinned host fixture identity is invalid: {exc}") from exc
        runtime = _json_bytes(version_data, fixture_id + " controller runtime identity", MAX_RUNTIME_BYTES)
        try:
            compose_qualification._runtime_identity(
                version_data, identity, "leapviewctl", fixture_id + " controller runtime identity",
            )
        except compose_qualification.QualificationError as exc:
            raise ControllerEvidenceError(str(exc)) from exc
        hosts.append({
            "id": fixture_id,
            "image": fixture["image"],
            "platform": "linux/" + arch,
            "machine": machine,
            "runtimeIdentity": runtime,
        })
    return hosts


def _expected_receipt(bundle_binding, identity, evidence_dir, generated_at, *, receipt_required, now=None):
    now_ns = _now_ns(now)
    generated_ns = _fresh_timestamp(generated_at, now_ns, "controller evidence generation time")
    root, inventory, report_data = _report_inventory(evidence_dir, receipt_required=receipt_required)

    static_report = _json_bytes(report_data["static.json"], "controller static report")
    arch = bundle_binding["platform"].removeprefix("linux/")
    machine = publication.ARCH_MACHINE[arch]
    with tempfile.TemporaryDirectory(prefix="leapview-compose-controller-verify-") as temporary:
        controller_path = Path(temporary) / "leapviewctl"
        # Read from verify_bundle's extracted path only; never follow evidence paths.
        controller_bytes = _read(
            bundle_binding["extractedController"], "verified controller executable", go_evidence.MAX_BINARY_BYTES,
        )
        controller_path.write_bytes(controller_bytes)
        controller_path.chmod(0o700)
        try:
            expected_static, build_info = publication._static_report(controller_path, arch)
        except (ValueError, KeyError, TypeError, OSError) as exc:
            raise ControllerEvidenceError(f"protected static controller checks failed: {exc}") from exc
    if _canonical_bytes(static_report) != _canonical_bytes(expected_static):
        raise ControllerEvidenceError("static report differs from exact protected controller checks")

    binary_sha256 = bundle_binding["controllerSHA256"]
    go_record = _go_summary(
        report_data["go/leapviewctl/summary.json"],
        report_data["go/leapviewctl/govulncheck.json"],
        binary_sha256,
        build_info,
        now_ns,
        generation_ns=generated_ns,
    )
    spdx_record = _validate_spdx(
        report_data["sbom.spdx.json"], build_info, binary_sha256, now_ns, generation_ns=generated_ns,
    )
    hosts = _host_records(report_data, identity, arch, machine)

    receipt = {
        "schemaVersion": SCHEMA_VERSION,
        "scope": SCOPE,
        "generatedAt": generated_at,
        "bundle": {
            key: bundle_binding[key] for key in (
                "archiveSHA256", "controllerSHA256", "controllerBuildIdentitySHA256", "sourceRevision",
                "platform", "image", "releaseIdentitySHA256",
            )
        },
        "static": static_report,
        "goEvidence": go_record,
        "spdx": {"version": publication.SYFT_VERSION, **spdx_record},
        "hosts": hosts,
        "reports": inventory,
        "result": "success",
        "releaseAdmission": False,
    }
    receipt["controllerEvidenceBindingDigest"] = _digest(
        b"leapview/nix-compose-controller-evidence/v1\n" + _canonical_bytes(receipt)
    )
    encoded = _pretty_json(receipt)
    if len(encoded) > compose_qualification.MAX_JSON_BYTES:
        raise ControllerEvidenceError("controller evidence receipt exceeds its byte limit")
    return receipt


def _pretty_json(value):
    try:
        return (json.dumps(value, indent=2, ensure_ascii=False, allow_nan=False) + "\n").encode("utf-8")
    except (TypeError, ValueError) as exc:
        raise ControllerEvidenceError(f"cannot serialize controller evidence receipt: {exc}") from exc


@contextmanager
def _verify_bundle_context(archive, sidecar, build_receipt, source_root, release_identity, *,
                           platform, source_revision, image):
    """Keep the protected bundle extraction alive for all evidence operations."""
    with tempfile.TemporaryDirectory(prefix="leapview-compose-controller-bundle-") as temporary:
        extract_dir = Path(temporary) / "extracted"
        try:
            binding = compose_qualification.verify_bundle(
                archive, sidecar, build_receipt, source_root, release_identity,
                platform=platform, source_revision=source_revision, image=image, extract_dir=extract_dir,
            )
            identity_bytes = _read(
                release_identity, "protected release identity", compose_qualification.compose_bundle.MAX_IDENTITY_BYTES,
            )
            identity = compose_qualification._canonical_identity(
                _json_bytes(identity_bytes, "protected release identity")
            )
            controller = Path(binding["extractedController"])
            controller_bytes = _read(controller, "verified controller executable", go_evidence.MAX_BINARY_BYTES)
            if _digest(controller_bytes) != binding["controllerSHA256"]:
                raise ControllerEvidenceError("verified controller changed after bundle extraction")
        except compose_qualification.QualificationError as exc:
            raise ControllerEvidenceError(str(exc)) from exc
        yield binding, identity, controller


def _verify_binary_verifier(path):
    try:
        return publication._validate_binary_verifier(path)
    except ValueError as exc:
        raise ControllerEvidenceError(str(exc)) from exc


def _scan_controller_go(binary, platform, binary_verifier, evidence_root):
    evidence_root.mkdir(mode=0o700)
    directory = evidence_root / "leapviewctl"
    try:
        go_evidence.run_verifier(
            binary_verifier, binary, GO_ENTRY, platform, directory, offline=False,
        )
        go_evidence.run_verifier(
            binary_verifier, binary, GO_ENTRY, platform, directory, offline=True,
        )
    except (ValueError, OSError, subprocess.SubprocessError) as exc:
        raise ControllerEvidenceError(f"protected Go binary scan or offline verification failed: {exc}") from exc


def qualify_controller_evidence(archive, sidecar, build_receipt, source_root, release_identity, *,
                                platform, source_revision, image, binary_verifier, evidence_dir, now=None):
    """Generate protected controller evidence from one verified exact bundle."""
    if os.geteuid() == 0:
        raise ControllerEvidenceError("controller qualification must run as an unprivileged user")
    binary_verifier = _verify_binary_verifier(binary_verifier)
    evidence_root = Path(evidence_dir)
    if evidence_root.exists() or evidence_root.is_symlink():
        raise ControllerEvidenceError("controller evidence output directory must be new")
    evidence_root.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.chmod(evidence_root, 0o700)

    with _verify_bundle_context(
        archive, sidecar, build_receipt, source_root, release_identity,
        platform=platform, source_revision=source_revision, image=image,
    ) as (bundle, identity, controller):
        arch = platform.removeprefix("linux/")
        try:
            static_report, _build_info = publication._static_report(controller, arch)
        except (ValueError, KeyError, TypeError, OSError) as exc:
            raise ControllerEvidenceError(f"protected static controller checks failed: {exc}") from exc
        compose_qualification._write_json_new(evidence_root / "static.json", static_report)

        _scan_controller_go(controller, platform, binary_verifier, evidence_root / "go")

        try:
            spdx_bytes, syft_version = publication._syft_spdx(controller, evidence_root / "sbom.spdx.json")
            if syft_version != publication.SYFT_VERSION:
                raise ControllerEvidenceError("controller SPDX inventory did not use pinned Syft")
        except ControllerEvidenceError:
            raise
        except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError) as exc:
            raise ControllerEvidenceError(f"protected controller SPDX generation failed: {exc}") from exc

        try:
            hosts, machine = publication._run_host(controller, arch, evidence_root)
        except (ValueError, OSError, subprocess.SubprocessError) as exc:
            raise ControllerEvidenceError(f"pinned native controller host probes failed: {exc}") from exc
        if machine != publication.ARCH_MACHINE[arch]:
            raise ControllerEvidenceError("controller host probe machine differs from native candidate platform")
        generated_at = _format_timestamp(now)
        receipt = _expected_receipt(
            bundle, identity, evidence_root, generated_at, receipt_required=False, now=now,
        )
        if hosts != receipt["hosts"]:
            raise ControllerEvidenceError("native host probe return values differ from retained report files")
        compose_qualification._write_new(evidence_root / "controller-evidence.json", _pretty_json(receipt))
        return receipt


def verify_controller_evidence(archive, sidecar, build_receipt, source_root, release_identity, *,
                               platform, source_revision, image, binary_verifier, evidence_dir,
                               receipt_path=None, now=None):
    """Read-only offline verifier; never executes the candidate controller."""
    binary_verifier = _verify_binary_verifier(binary_verifier)
    root = Path(os.path.abspath(os.fspath(evidence_dir)))
    expected_receipt_path = root / "controller-evidence.json"
    if receipt_path is not None and Path(os.path.abspath(os.fspath(receipt_path))) != expected_receipt_path:
        raise ControllerEvidenceError("controller evidence receipt must be the fixed file inside its evidence directory")
    receipt_path = expected_receipt_path
    receipt_bytes = _read(receipt_path, "controller evidence receipt", compose_qualification.MAX_JSON_BYTES)
    supplied = _json_bytes(receipt_bytes, "controller evidence receipt", compose_qualification.MAX_JSON_BYTES)
    if (
        not isinstance(supplied, dict) or type(supplied.get("schemaVersion")) is not int
        or supplied.get("schemaVersion") != SCHEMA_VERSION or supplied.get("scope") != SCOPE
    ):
        raise ControllerEvidenceError("controller evidence receipt has an unsupported schema or scope")
    if supplied.get("releaseAdmission") is not False or supplied.get("result") != "success":
        raise ControllerEvidenceError("controller evidence receipt does not represent a non-admitting success")
    generated_at = supplied.get("generatedAt")
    with _verify_bundle_context(
        archive, sidecar, build_receipt, source_root, release_identity,
        platform=platform, source_revision=source_revision, image=image,
    ) as (bundle, identity, controller):
        go_evidence_dir = root / "go" / "leapviewctl"
        try:
            go_evidence.run_verifier(
                binary_verifier, controller, GO_ENTRY, platform, go_evidence_dir, offline=True,
            )
        except (ValueError, OSError, subprocess.SubprocessError) as exc:
            raise ControllerEvidenceError(f"protected Go binary offline verification failed: {exc}") from exc
        expected = _expected_receipt(bundle, identity, root, generated_at, receipt_required=True, now=now)
        if _canonical_bytes(supplied) != _canonical_bytes(expected):
            raise ControllerEvidenceError("controller evidence receipt differs from current bundle or report files")
    if _read(receipt_path, "controller evidence receipt", compose_qualification.MAX_JSON_BYTES) != receipt_bytes:
        raise ControllerEvidenceError("controller evidence receipt changed during offline verification")
    return expected


def compare_controller_evidence(archive, sidecar, build_receipt, source_root, release_identity, *,
                                platform, source_revision, image, binary_verifier,
                                left_evidence_dir, right_evidence_dir, now=None):
    """Offline-verify and require exact original-to-retained controller evidence bytes."""
    common = {
        "archive": archive,
        "sidecar": sidecar,
        "build_receipt": build_receipt,
        "source_root": source_root,
        "release_identity": release_identity,
        "platform": platform,
        "source_revision": source_revision,
        "image": image,
        "binary_verifier": binary_verifier,
        "now": now,
    }
    left = verify_controller_evidence(**common, evidence_dir=left_evidence_dir)
    right = verify_controller_evidence(**common, evidence_dir=right_evidence_dir)
    left_receipt = _read(Path(left_evidence_dir) / "controller-evidence.json", "original controller evidence receipt", compose_qualification.MAX_JSON_BYTES)
    right_receipt = _read(Path(right_evidence_dir) / "controller-evidence.json", "retained controller evidence receipt", compose_qualification.MAX_JSON_BYTES)
    if left_receipt != right_receipt or left["reports"] != right["reports"]:
        raise ControllerEvidenceError("independent controller evidence artifact differs from the retained qualification copy")
    left_files = {record["path"]: record["sha256"] for record in left["reports"]["files"]}
    right_files = {record["path"]: record["sha256"] for record in right["reports"]["files"]}
    if left_files != right_files:
        raise ControllerEvidenceError("controller evidence report inventory differs between protected artifacts")
    return {
        "schemaVersion": SCHEMA_VERSION,
        "controllerEvidenceBindingDigest": left["controllerEvidenceBindingDigest"],
        "inventory": left["reports"],
        "receiptSHA256": _digest(left_receipt),
        "releaseAdmission": False,
    }


def _common_arguments(parser):
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--sidecar", type=Path, required=True)
    parser.add_argument("--controller-build-identity", type=Path, required=True)
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--release-identity", type=Path, required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--source-revision", required=True)
    parser.add_argument("--platform", choices=sorted(compose_qualification.PLATFORMS), required=True)
    parser.add_argument("--binary-verifier", type=Path, required=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    operations = parser.add_subparsers(dest="operation", required=True)
    qualify = operations.add_parser("qualify-controller")
    _common_arguments(qualify)
    qualify.add_argument("--evidence-dir", type=Path, required=True)
    qualify.add_argument("--output", type=Path, required=True)
    verify = operations.add_parser("verify-controller-evidence")
    _common_arguments(verify)
    verify.add_argument("--evidence-dir", type=Path, required=True)
    verify.add_argument("--receipt", type=Path, required=True)
    compare = operations.add_parser("compare-controller-evidence")
    _common_arguments(compare)
    compare.add_argument("--left-evidence-dir", type=Path, required=True)
    compare.add_argument("--right-evidence-dir", type=Path, required=True)

    args = parser.parse_args()
    os.umask(0o077)
    common = {
        "archive": args.archive,
        "sidecar": args.sidecar,
        "build_receipt": args.controller_build_identity,
        "source_root": args.source_root,
        "release_identity": args.release_identity,
        "platform": args.platform,
        "source_revision": args.source_revision,
        "image": args.image,
        "binary_verifier": args.binary_verifier,
    }
    try:
        if args.operation == "qualify-controller":
            expected_receipt = Path(os.path.abspath(os.fspath(args.evidence_dir))) / "controller-evidence.json"
            if Path(os.path.abspath(os.fspath(args.output))) != expected_receipt:
                raise ControllerEvidenceError("receipt output must be controller-evidence.json inside evidence directory")
            receipt = qualify_controller_evidence(**common, evidence_dir=args.evidence_dir)
            result = {
                "controllerEvidenceBindingDigest": receipt["controllerEvidenceBindingDigest"],
                "inventory": receipt["reports"],
                "releaseAdmission": False,
            }
        elif args.operation == "verify-controller-evidence":
            receipt = verify_controller_evidence(
                **common, evidence_dir=args.evidence_dir, receipt_path=args.receipt,
            )
            result = {
                "controllerEvidenceBindingDigest": receipt["controllerEvidenceBindingDigest"],
                "inventory": receipt["reports"],
                "releaseAdmission": False,
            }
        else:
            result = compare_controller_evidence(
                **common, left_evidence_dir=args.left_evidence_dir,
                right_evidence_dir=args.right_evidence_dir,
            )
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    except (ControllerEvidenceError, compose_qualification.QualificationError, ValueError,
            KeyError, TypeError, OSError, EOFError, subprocess.SubprocessError) as exc:
        raise SystemExit("Nix Compose controller evidence rejected: " + str(exc)) from exc


if __name__ == "__main__":
    main()

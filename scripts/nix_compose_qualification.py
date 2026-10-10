#!/usr/bin/env python3
"""Verify and bind protected Nix Compose candidate qualification evidence."""

from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import tarfile
import tempfile
import zipfile
import zlib

import package_compose_bundle as compose_bundle


REPOSITORY = "flidai/leapview"
RELEASE_WORKFLOW = ".github/workflows/release.yml"
RELEASE_WORKFLOW_IDENTITY = "flidai/leapview/.github/workflows/release.yml"
SIGNER_WORKFLOW_IDENTITY = "flidai/leapview/.github/workflows/nix-compose-candidate.yml"
IMAGE_RE = re.compile(r"^ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}$")
SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
REVISION_RE = re.compile(r"^[0-9a-f]{40}$")
ARCHIVE_NAME_RE = re.compile(r"^leapview-compose-[A-Za-z0-9._+-]+-(linux|darwin)-(amd64|arm64)\.tar\.gz$")
PACKAGE_NAME_RE = re.compile(r"^leapview-compose-[A-Za-z0-9._+-]+-linux-(amd64|arm64)$")
PLATFORMS = {"linux/amd64", "linux/arm64"}
MAX_ARTIFACT_ZIP_BYTES = 3 * 1024**3
MAX_ARTIFACT_MEMBER_BYTES = 1024**3
MAX_ARTIFACT_TOTAL_BYTES = 4 * 1024**3
MAX_BUNDLE_BYTES = 1024**3
MAX_BUNDLE_UNPACKED_BYTES = compose_bundle.MAX_CONTROLLER_BYTES + compose_bundle.MAX_TOTAL_ASSET_BYTES + 4 * 1024**2
MAX_JSON_BYTES = 4 * 1024**2
MAX_ADMISSION_BYTES = 1024**2
MAX_QUALIFICATION_EVIDENCE_FILE_BYTES = 1024**3
MAX_QUALIFICATION_EVIDENCE_TOTAL_BYTES = 2 * 1024**3
MAX_QUALIFICATION_EVIDENCE_FILES = 1024
QUALIFICATION_MAX_AGE = timedelta(hours=120)
PHASES = (
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
ASSERTIONS = (
    "oneTimeCredentials",
    "browserJourney",
    "performanceBudgets",
    "governedQuery",
    "auditedDenial",
    "interruptionRecovery",
    "restartPersistence",
    "multiNodeProcess",
    "upgradePersistence",
    "nativePostgresOnly",
)


class QualificationError(ValueError):
    """An untrusted Compose candidate or evidence record failed verification."""


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise QualificationError(f"duplicate JSON field {key!r}")
        result[key] = value
    return result


def _reject_constant(value):
    raise QualificationError(f"non-finite JSON value {value!r} is forbidden")


def _json_bytes(data: bytes, label: str, limit: int = MAX_JSON_BYTES):
    if not data or len(data) > limit:
        raise QualificationError(f"{label} is empty or exceeds its byte limit")
    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=_unique_object, parse_constant=_reject_constant)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise QualificationError(f"{label} is not valid UTF-8 JSON: {exc}") from exc
    return value


def _canonical_bytes(value) -> bytes:
    try:
        return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode("utf-8")
    except (TypeError, ValueError) as exc:
        raise QualificationError(f"cannot encode canonical evidence JSON: {exc}") from exc


def _digest_bytes(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def _revision(value, label):
    if not isinstance(value, str) or REVISION_RE.fullmatch(value) is None:
        raise QualificationError(f"{label} must be a full lowercase 40-character commit SHA")
    return value


def _digest(value, label):
    if not isinstance(value, str) or SHA256_RE.fullmatch(value) is None:
        raise QualificationError(f"{label} must be sha256:<64 lowercase hexadecimal characters>")
    return value


def _stat_signature(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def _open_regular(path, label, limit):
    path = Path(path)
    for component in (path, *path.parents):
        try:
            info = component.lstat()
        except OSError as exc:
            raise QualificationError(f"cannot inspect {label}: {exc}") from exc
        if stat.S_ISLNK(info.st_mode):
            raise QualificationError(f"{label} cannot traverse symlinks")
        if component != path and not stat.S_ISDIR(info.st_mode):
            raise QualificationError(f"{label} parent is not a real directory")
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1:
        raise QualificationError(f"{label} must be a regular file without additional hard links")
    if before.st_size < 0 or before.st_size > limit:
        raise QualificationError(f"{label} exceeds the {limit}-byte size limit")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as exc:
        raise QualificationError(f"cannot open {label}: {exc}") from exc
    opened = os.fstat(descriptor)
    if not stat.S_ISREG(opened.st_mode) or _stat_signature(opened) != _stat_signature(before):
        os.close(descriptor)
        raise QualificationError(f"{label} changed while it was opened")
    return path, descriptor, _stat_signature(before)


def _assert_unchanged(path, descriptor, signature, label):
    try:
        current = path.lstat()
        opened = os.fstat(descriptor)
    except OSError as exc:
        raise QualificationError(f"cannot recheck {label}: {exc}") from exc
    if _stat_signature(current) != signature or _stat_signature(opened) != signature:
        raise QualificationError(f"{label} changed during verification")


def _read_regular(path, label, limit):
    path, descriptor, signature = _open_regular(path, label, limit)
    try:
        chunks = []
        total = 0
        while True:
            chunk = os.read(descriptor, min(1024 * 1024, limit + 1 - total))
            if not chunk:
                break
            chunks.append(chunk)
            total += len(chunk)
            if total > limit:
                raise QualificationError(f"{label} exceeds its byte limit")
        _assert_unchanged(path, descriptor, signature, label)
        if total != signature[4]:
            raise QualificationError(f"{label} changed size while it was read")
        return b"".join(chunks)
    finally:
        os.close(descriptor)


def _hash_regular(path, label, limit):
    path, descriptor, signature = _open_regular(path, label, limit)
    try:
        digest = hashlib.sha256()
        total = 0
        while True:
            chunk = os.read(descriptor, 1024 * 1024)
            if not chunk:
                break
            total += len(chunk)
            if total > limit:
                raise QualificationError(f"{label} exceeds its byte limit")
            digest.update(chunk)
        _assert_unchanged(path, descriptor, signature, label)
        if total != signature[4]:
            raise QualificationError(f"{label} changed size while it was hashed")
        return total, "sha256:" + digest.hexdigest()
    finally:
        os.close(descriptor)


def _read_json_file(path, label, limit=MAX_JSON_BYTES):
    return _json_bytes(_read_regular(path, label, limit), label, limit)


def _qualification_evidence_inventory(evidence_root):
    root = Path(evidence_root)
    root = Path(os.path.abspath(os.fspath(root)))
    for component in (root, *root.parents):
        try:
            info = component.lstat()
        except OSError as exc:
            raise QualificationError(f"cannot inspect qualification evidence directory: {exc}") from exc
        if stat.S_ISLNK(info.st_mode):
            raise QualificationError("qualification evidence directory cannot traverse symlinks")
        if component != root and not stat.S_ISDIR(info.st_mode):
            raise QualificationError("qualification evidence directory parent is not a real directory")
    if not stat.S_ISDIR(root.lstat().st_mode):
        raise QualificationError("qualification evidence root must be a real directory")

    entries = []
    pending = [(root, PurePosixPath())]
    total_bytes = 0
    while pending:
        directory, relative_directory = pending.pop()
        try:
            with os.scandir(directory) as scan:
                children = sorted(scan, key=lambda entry: entry.name)
        except OSError as exc:
            raise QualificationError(f"cannot enumerate qualification evidence directory: {exc}") from exc
        for child in children:
            name = child.name
            if not name or name in (".", "..") or "/" in name or "\\" in name or "\x00" in name:
                raise QualificationError("qualification evidence contains a noncanonical path")
            relative = relative_directory / name
            relative_name = relative.as_posix()
            try:
                encoded_name = relative_name.encode("utf-8", "strict")
                info = child.stat(follow_symlinks=False)
            except (OSError, UnicodeEncodeError) as exc:
                raise QualificationError(f"qualification evidence path cannot be safely inspected: {exc}") from exc
            if len(encoded_name) > 4096 or PurePosixPath(relative_name).as_posix() != relative_name:
                raise QualificationError("qualification evidence contains an oversized or noncanonical path")
            if stat.S_ISLNK(info.st_mode):
                raise QualificationError("qualification evidence cannot contain symlinks")
            if stat.S_ISDIR(info.st_mode):
                pending.append((Path(child.path), relative))
                continue
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                raise QualificationError("qualification evidence must contain only unlinked regular files and directories")
            if len(entries) >= MAX_QUALIFICATION_EVIDENCE_FILES:
                raise QualificationError("qualification evidence contains too many files")
            size, digest = _hash_regular(
                Path(child.path), f"qualification evidence file {relative_name}",
                MAX_QUALIFICATION_EVIDENCE_FILE_BYTES,
            )
            total_bytes += size
            if total_bytes > MAX_QUALIFICATION_EVIDENCE_TOTAL_BYTES:
                raise QualificationError("qualification evidence exceeds its total byte limit")
            entries.append({"path": relative_name, "sizeBytes": size, "sha256": digest})
    entries.sort(key=lambda entry: entry["path"])
    if not entries or not any(entry["path"] == "qualification-report.json" for entry in entries):
        raise QualificationError("qualification evidence must include qualification-report.json and at least one file")
    inventory = {
        "fileCount": len(entries),
        "totalBytes": total_bytes,
        "files": entries,
        "inventorySHA256": _digest_bytes(
            b"leapview/nix-compose-qualification-evidence/v1\n" + _canonical_bytes(entries)
        ),
    }
    return inventory


def _write_new(path, data: bytes, mode=0o644):
    path = Path(path)
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0)
    descriptor = os.open(path, flags, mode)
    try:
        view = memoryview(data)
        while view:
            written = os.write(descriptor, view)
            view = view[written:]
        os.fchmod(descriptor, mode)
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def _validate_platform(platform):
    if platform not in PLATFORMS:
        raise QualificationError("Compose native qualification supports only linux/amd64 and linux/arm64")
    return platform


def _git(root, *args):
    try:
        return subprocess.check_output(["git", "-C", str(root), *args], text=True, stderr=subprocess.DEVNULL, timeout=30).strip()
    except (OSError, subprocess.SubprocessError):
        raise QualificationError("protected Git source metadata is unavailable") from None


def _verify_clean_revision(root, revision):
    root = Path(root)
    revision = _revision(revision, "source revision")
    if _git(root, "rev-parse", "HEAD") != revision:
        raise QualificationError("source checkout does not match the authorized source revision")
    if _git(root, "status", "--porcelain", "--untracked-files=all"):
        raise QualificationError("canonical source asset checkout must be clean")
    return root


def verify_release_run(run, workflow, artifact, *, protected_revision, source_root):
    """Bind the exact successful protected release run and REST artifact."""
    protected_revision = _revision(protected_revision, "protected workflow revision")
    if not isinstance(run, dict) or set(run) != {
        "repository", "workflowId", "runId", "runAttempt", "event", "status", "conclusion", "branch", "sourceRevision"
    }:
        raise QualificationError("release run metadata has an unsupported shape")
    if not isinstance(workflow, dict) or set(workflow) != {"id", "path"}:
        raise QualificationError("release workflow metadata has an unsupported shape")
    if not isinstance(artifact, dict) or set(artifact) != {
        "id", "name", "digest", "expired", "runId", "runAttempt", "repository", "branch", "sourceRevision"
    }:
        raise QualificationError("release artifact metadata has an unsupported shape")
    run_id, attempt, workflow_id = run["runId"], run["runAttempt"], workflow["id"]
    artifact_id = artifact["id"]
    if any(type(value) is not int or value <= 0 for value in (run_id, attempt, workflow_id, artifact_id)):
        raise QualificationError("release run, attempt, workflow and artifact IDs must be positive integers")
    source_revision = _revision(run["sourceRevision"], "release source revision")
    _revision(artifact["sourceRevision"], "artifact source revision")
    _digest(artifact["digest"], "release artifact digest")
    if (
        run["repository"] != REPOSITORY or workflow["path"] != RELEASE_WORKFLOW
        or run["workflowId"] != workflow_id or run["event"] != "workflow_dispatch"
        or run["status"] != "completed" or run["conclusion"] != "success" or run["branch"] != "main"
    ):
        raise QualificationError("release run is not a completed successful main-branch release workflow_dispatch")
    expected_name = f"release-candidate-candidate-{run_id}-{attempt}"
    if (
        artifact["name"] != expected_name or artifact["expired"] is not False
        or artifact["runId"] != run_id or artifact["runAttempt"] != attempt
        or artifact["repository"] != REPOSITORY or artifact["branch"] != "main"
        or artifact["sourceRevision"] != source_revision
    ):
        raise QualificationError("release artifact is not the exact immutable artifact of the authorized run")
    source_root = Path(source_root)
    if _git(source_root, "rev-parse", "HEAD") != protected_revision:
        raise QualificationError("protected workflow checkout differs from its declared revision")
    try:
        subprocess.run(
            ["git", "-C", str(source_root), "merge-base", "--is-ancestor", source_revision, protected_revision],
            check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30,
        )
    except (OSError, subprocess.SubprocessError):
        raise QualificationError("release source is not an ancestor of the protected workflow revision") from None
    binding = {
        "schemaVersion": 1,
        "releaseRepository": REPOSITORY,
        "releaseWorkflowPath": RELEASE_WORKFLOW,
        "releaseWorkflowId": workflow_id,
        "releaseRunId": run_id,
        "releaseRunAttempt": attempt,
        "releaseEvent": run["event"],
        "releaseBranch": run["branch"],
        "sourceRevision": source_revision,
        "releaseArtifactId": artifact_id,
        "releaseArtifactName": artifact["name"],
        "releaseArtifactDigest": artifact["digest"],
        "protectedWorkflowRevision": protected_revision,
        "releaseAdmission": False,
    }
    binding["releaseAuthorizationBindingDigest"] = _digest_bytes(
        b"leapview/nix-compose-release-authorization/v1\n" + _canonical_bytes(binding)
    )
    return binding


def select_conventional_receipt_artifact(artifacts, authorization):
    """Select original builder receipts from the already authorized release attempt."""
    _validate_release_authorization(authorization)
    name = f"compose-controller-build-identities-{authorization['releaseRunId']}-{authorization['releaseRunAttempt']}"
    matches = [item for item in artifacts if item.get("name") == name]
    if len(matches) != 1:
        raise QualificationError("expected one original conventional controller receipt artifact")
    artifact = matches[0]
    run = artifact.get("workflow_run") or {}
    if (type(artifact.get("id")) is not int or artifact["id"] <= 0
            or artifact.get("expired") is not False
            or run.get("id") != authorization["releaseRunId"]
            or run.get("head_branch") != "main"
            or run.get("head_sha") != authorization["sourceRevision"]):
        raise QualificationError("conventional controller receipt artifact differs from the authorized release")
    _digest(artifact.get("digest"), "original conventional controller receipt artifact digest")
    return artifact


def extract_conventional_bundles(release_zip, receipt_zip, receipt_artifact, authorization,
                                source_root, output_dir, *, metadata_reader=None):
    """Preserve original release archives and builder receipts; never rebuild either."""
    artifact = select_conventional_receipt_artifact([receipt_artifact], authorization)
    extract_release_handoff(release_zip, authorization["releaseArtifactDigest"],
                            release_authorization=authorization)
    receipt_data = _read_regular(receipt_zip, "original controller receipt ZIP", 1024 * 1024)
    if _digest_bytes(receipt_data) != artifact["digest"]:
        raise QualificationError("original controller receipt ZIP differs from its immutable API digest")
    receipts = {}
    with zipfile.ZipFile(io.BytesIO(receipt_data)) as archive:
        for info in archive.infolist():
            name, directory = _zip_member_name(info)
            if directory or name not in {"linux-amd64.json", "linux-arm64.json"} or name in receipts:
                raise QualificationError("original controller receipt ZIP has an unexpected inventory")
            receipts[name] = _zip_bytes(archive, info, name, compose_bundle.MAX_IDENTITY_BYTES)
    if set(receipts) != {"linux-amd64.json", "linux-arm64.json"}:
        raise QualificationError("original controller receipt ZIP is missing a Linux platform")
    output = Path(output_dir)
    if output.exists() or output.is_symlink():
        raise QualificationError("conventional bundle output directory must be new")
    output.mkdir(parents=True, mode=0o700)
    path, descriptor, signature = _open_regular(release_zip, "original release ZIP", MAX_ARTIFACT_ZIP_BYTES)
    try:
        with os.fdopen(os.dup(descriptor), "rb") as stream:
            if "sha256:" + hashlib.file_digest(stream, "sha256").hexdigest() != authorization["releaseArtifactDigest"]:
                raise QualificationError("original release ZIP changed before extraction")
            stream.seek(0)
            with zipfile.ZipFile(stream) as archive:
                for arch in ("amd64", "arm64"):
                    target = output / arch
                    target.mkdir(mode=0o700)
                    package = f"leapview-compose-candidate-{authorization['releaseRunId']}-{authorization['releaseRunAttempt']}-linux-{arch}"
                    for name in (package + ".tar.gz", package + ".tar.gz.sha256"):
                        member = archive.getinfo("dist/" + name)
                        _write_new(target / name, _zip_bytes(archive, member, name, MAX_ARTIFACT_MEMBER_BYTES))
                    for name in ("release-identity.json", "image-reference.txt", "assembled-image-admission.json"):
                        data = _zip_bytes(archive, archive.getinfo(name), name, MAX_ADMISSION_BYTES)
                        _write_new(target / ("release-artifact-admission.json" if name == "assembled-image-admission.json" else name), data)
                    _write_new(target / "controller-build-identity.json", receipts[f"linux-{arch}.json"])
                    _write_json_new(target / "release-run-binding.json", authorization)
                    identity = _load_json_argument(target / "release-identity.json", "release identity")
                    binding = verify_bundle(target / (package + ".tar.gz"), target / (package + ".tar.gz.sha256"),
                        target / "controller-build-identity.json", source_root, target / "release-identity.json",
                        platform="linux/" + arch, source_revision=authorization["sourceRevision"],
                        image=identity["image"], metadata_reader=metadata_reader)
                    _write_json_new(target / "bundle-binding.json", binding)
                    _write_json_new(target / "bundle-producer.json", {
                        "schemaVersion": 1, "producer": "conventional", "releaseAdmission": False,
                        "nixQualification": False, "releaseArtifactId": authorization["releaseArtifactId"],
                        "releaseArtifactDigest": authorization["releaseArtifactDigest"],
                        "receiptArtifactId": artifact["id"], "receiptArtifactDigest": artifact["digest"],
                        "releaseRunId": authorization["releaseRunId"],
                        "releaseRunAttempt": authorization["releaseRunAttempt"],
                        "bundle": binding,
                    })
        _assert_unchanged(path, descriptor, signature, "original release ZIP")
    finally:
        os.close(descriptor)


def _zip_member_name(info):
    name = info.filename
    if not isinstance(name, str) or not name or "\\" in name or name.startswith("/"):
        raise QualificationError("release artifact ZIP has an unsafe member path")
    is_directory = name.endswith("/")
    canonical = name[:-1] if is_directory else name
    path = PurePosixPath(canonical)
    if path.is_absolute() or not path.parts or any(part in ("", ".", "..") for part in path.parts) or path.as_posix() != canonical:
        raise QualificationError("release artifact ZIP has a noncanonical member path")
    mode = info.external_attr >> 16
    file_type = stat.S_IFMT(mode)
    if is_directory:
        if canonical != "dist" or (file_type and file_type != stat.S_IFDIR) or info.file_size != 0:
            raise QualificationError("release artifact ZIP contains an unexpected directory")
    elif file_type not in (0, stat.S_IFREG):
        raise QualificationError("release artifact ZIP contains a link or special file")
    if info.flag_bits & 0x1 or info.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED):
        raise QualificationError("release artifact ZIP uses unsupported encryption or compression")
    if info.file_size < 0 or info.file_size > MAX_ARTIFACT_MEMBER_BYTES:
        raise QualificationError("release artifact ZIP member exceeds its byte limit")
    return canonical, is_directory


def _zip_bytes(archive, info, label, limit):
    if info.file_size > limit:
        raise QualificationError(f"{label} exceeds its byte limit")
    try:
        with archive.open(info, "r") as source:
            chunks = []
            size = 0
            while True:
                chunk = source.read(min(1024 * 1024, limit + 1 - size))
                if not chunk:
                    break
                chunks.append(chunk)
                size += len(chunk)
                if size > limit:
                    raise QualificationError(f"{label} exceeds its byte limit")
    except (OSError, RuntimeError, zipfile.BadZipFile, EOFError) as exc:
        raise QualificationError(f"cannot read {label}: {exc}") from exc
    if size != info.file_size:
        raise QualificationError(f"{label} size disagrees with ZIP metadata")
    return b"".join(chunks)


def extract_release_handoff(artifact_zip, artifact_digest, output_dir=None, *, release_authorization=None):
    """Verify the REST artifact ZIP and optionally extract only its three handoff files."""
    _digest(artifact_digest, "release artifact digest")
    path, descriptor, signature = _open_regular(artifact_zip, "release artifact ZIP", MAX_ARTIFACT_ZIP_BYTES)
    archive_stream = None
    try:
        digest = hashlib.sha256()
        offset = 0
        while offset < signature[4]:
            chunk = os.pread(descriptor, min(1024 * 1024, signature[4] - offset), offset)
            if not chunk:
                raise QualificationError("release artifact ZIP was truncated while hashing")
            digest.update(chunk)
            offset += len(chunk)
        actual_digest = "sha256:" + digest.hexdigest()
        if actual_digest != artifact_digest:
            raise QualificationError("downloaded release artifact ZIP differs from its immutable API digest")
        _assert_unchanged(path, descriptor, signature, "release artifact ZIP")
        archive_stream = os.fdopen(os.dup(descriptor), "rb")
        with zipfile.ZipFile(archive_stream, "r") as archive:
            infos = archive.infolist()
            if not infos or len(infos) > 32:
                raise QualificationError("release artifact ZIP has an unsupported member count")
            members = {}
            directories = set()
            total = 0
            for info in infos:
                name, is_directory = _zip_member_name(info)
                if name in members or name in directories:
                    raise QualificationError("release artifact ZIP repeats a member path")
                if is_directory:
                    directories.add(name)
                    continue
                members[name] = info
                total += info.file_size
                if total > MAX_ARTIFACT_TOTAL_BYTES:
                    raise QualificationError("release artifact ZIP exceeds its total unpacked size limit")
            archives = sorted(name for name in members if name.startswith("dist/") and name.endswith(".tar.gz"))
            if len(archives) != 4:
                raise QualificationError("release artifact ZIP must contain exactly four platform Compose archives")
            expected_paths = {"image-reference.txt", "release-identity.json", "assembled-image-admission.json"}
            archive_platforms = set()
            for name in archives:
                basename = name.removeprefix("dist/")
                match = ARCHIVE_NAME_RE.fullmatch(basename)
                if match is None:
                    raise QualificationError("release artifact contains an unsupported Compose archive name")
                platform_name = match.group(1) + "/" + match.group(2)
                if platform_name in archive_platforms:
                    raise QualificationError("release artifact repeats a Compose platform")
                archive_platforms.add(platform_name)
                sidecar = name + ".sha256"
                if sidecar not in members:
                    raise QualificationError("release artifact is missing an archive checksum sidecar")
                expected_paths.add(name)
                expected_paths.add(sidecar)
            if archive_platforms != {"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"}:
                raise QualificationError("release artifact platform inventory is incomplete")
            if set(members) != expected_paths or directories - {"dist"}:
                raise QualificationError("release artifact ZIP has missing or unexpected paths")
            extracted = {}
            for name in sorted(("image-reference.txt", "release-identity.json", "assembled-image-admission.json")):
                limit = MAX_ADMISSION_BYTES if name == "assembled-image-admission.json" else MAX_JSON_BYTES
                data = _zip_bytes(archive, members[name], name, limit)
                extracted[name] = data
            if output_dir is not None:
                output_dir = Path(output_dir)
                if output_dir.exists() or output_dir.is_symlink():
                    raise QualificationError("release handoff output directory must be new")
                output_dir.mkdir(parents=True, mode=0o700)
                for name, data in extracted.items():
                    _write_new(output_dir / name, data)
            _assert_unchanged(path, descriptor, signature, "release artifact ZIP")
    except (zipfile.BadZipFile, OSError, EOFError) as exc:
        raise QualificationError(f"release artifact ZIP is invalid: {exc}") from exc
    finally:
        if archive_stream is not None:
            archive_stream.close()
        os.close(descriptor)
    return verify_release_handoff(extracted, release_authorization)


def _canonical_identity(identity):
    if not isinstance(identity, dict) or set(identity) != compose_bundle.IDENTITY_FIELDS:
        raise QualificationError("release identity must contain exactly the six canonical build identity fields")
    image = identity.get("image")
    if not isinstance(image, str) or IMAGE_RE.fullmatch(image) is None:
        raise QualificationError("release identity image must be the canonical immutable LeapView GHCR reference")
    if identity.get("dirty") is not False or identity.get("development") is not False:
        raise QualificationError("release identity must be clean and non-development")
    if not isinstance(identity.get("revision"), str) or REVISION_RE.fullmatch(identity["revision"]) is None:
        raise QualificationError("release identity revision must be a full lowercase commit SHA")
    if not compose_bundle._canonical_utc(identity.get("buildTime")):
        raise QualificationError("release identity build time must be canonical UTC")
    if not isinstance(identity.get("version"), str) or compose_bundle.VERSION_RE.fullmatch(identity["version"]) is None:
        raise QualificationError("release identity version is not canonical")
    return identity


def _check_image_reference(data, identity, expected_image=None):
    try:
        image = data.decode("ascii").removesuffix("\n")
    except UnicodeDecodeError:
        raise QualificationError("image-reference.txt must be ASCII") from None
    if data != (image + "\n").encode("ascii") or IMAGE_RE.fullmatch(image) is None:
        raise QualificationError("image-reference.txt must contain one canonical immutable image reference")
    if image != identity["image"] or (expected_image is not None and image != expected_image):
        raise QualificationError("release image reference differs from the exact release identity")
    return image


def _validate_admission_evidence(data, image, source_revision, platform, *, check_platform=True):
    evidence = _json_bytes(data, "OCI admission evidence", MAX_ADMISSION_BYTES)
    if not isinstance(evidence, dict) or set(evidence) != {
        "schemaVersion", "image", "digest", "registryDigest", "attestation", "sbom", "vulnerabilityPolicy"
    }:
        raise QualificationError("OCI admission evidence has an unsupported schema")
    image_digest = image.rsplit("@", 1)[1]
    attestation = evidence["attestation"]
    sbom = evidence["sbom"]
    policy = evidence["vulnerabilityPolicy"]
    expected_policy = {"sha256", "scanner", "passed", "platform"} if check_platform else {"sha256", "scanner", "passed"}
    if (
        type(evidence["schemaVersion"]) is not int or evidence["schemaVersion"] != 1
        or evidence["image"] != image or evidence["digest"] != image_digest or evidence["registryDigest"] != image_digest
        or not isinstance(attestation, dict) or set(attestation) != {"verified", "repository", "workflow", "sourceRevision"}
        or attestation["verified"] is not True or attestation["repository"] != REPOSITORY
        or attestation["workflow"] != RELEASE_WORKFLOW_IDENTITY or attestation["sourceRevision"] != source_revision
        or not isinstance(sbom, dict) or set(sbom) != {"discoverable", "predicateType"}
        or sbom["discoverable"] is not True or sbom["predicateType"] != "https://spdx.dev/Document/v2.3"
        or not isinstance(policy, dict) or set(policy) != expected_policy
        or not isinstance(policy.get("sha256"), str) or re.fullmatch(r"[0-9a-f]{64}", policy["sha256"]) is None
        or policy.get("scanner") != "trivy" or policy.get("passed") is not True
        or (check_platform and policy.get("platform") != platform)
    ):
        raise QualificationError("OCI admission evidence does not prove the exact release image, source, platform and policy")
    return evidence


def verify_release_handoff(files, release_authorization):
    """Verify the common release artifact files after digest-bound ZIP extraction."""
    if not isinstance(files, dict) or set(files) != {"image-reference.txt", "release-identity.json", "assembled-image-admission.json"}:
        raise QualificationError("release handoff must contain exactly image reference, release identity and admission evidence")
    identity = _canonical_identity(_json_bytes(files["release-identity.json"], "release identity"))
    canonical_identity = (json.dumps(identity, indent=2, ensure_ascii=False, allow_nan=False) + "\n").encode("utf-8")
    if files["release-identity.json"] != canonical_identity:
        raise QualificationError("release identity JSON is not in the canonical release serialization")
    image = _check_image_reference(files["image-reference.txt"], identity)
    if release_authorization is not None:
        _validate_release_authorization(release_authorization)
        if identity["revision"] != release_authorization["sourceRevision"]:
            raise QualificationError("release identity source differs from the authorized release run")
    _validate_admission_evidence(files["assembled-image-admission.json"], image, identity["revision"], "", check_platform=False)
    return {
        "schemaVersion": 1,
        "image": image,
        "sourceRevision": identity["revision"],
        "releaseIdentitySHA256": _digest_bytes(files["release-identity.json"]),
        "assembledImageAdmissionSHA256": _digest_bytes(files["assembled-image-admission.json"]),
        "releaseAdmission": False,
    }


def _validate_release_authorization(binding):
    fields = {
        "schemaVersion", "releaseRepository", "releaseWorkflowPath", "releaseWorkflowId", "releaseRunId",
        "releaseRunAttempt", "releaseEvent", "releaseBranch", "sourceRevision", "releaseArtifactId",
        "releaseArtifactName", "releaseArtifactDigest", "protectedWorkflowRevision", "releaseAdmission",
        "releaseAuthorizationBindingDigest",
    }
    if not isinstance(binding, dict) or set(binding) != fields:
        raise QualificationError("release authorization binding has an unsupported schema")
    core = dict(binding)
    supplied = core.pop("releaseAuthorizationBindingDigest")
    expected = _digest_bytes(b"leapview/nix-compose-release-authorization/v1\n" + _canonical_bytes(core))
    if supplied != expected or core.get("releaseAdmission") is not False or type(core.get("schemaVersion")) is not int or core.get("schemaVersion") != 1:
        raise QualificationError("release authorization binding digest or admission state is invalid")
    if (
        core["releaseRepository"] != REPOSITORY or core["releaseWorkflowPath"] != RELEASE_WORKFLOW
        or type(core["releaseWorkflowId"]) is not int or core["releaseWorkflowId"] <= 0
        or type(core["releaseRunId"]) is not int or core["releaseRunId"] <= 0
        or type(core["releaseRunAttempt"]) is not int or core["releaseRunAttempt"] <= 0
        or core["releaseEvent"] != "workflow_dispatch" or core["releaseBranch"] != "main"
        or core["releaseArtifactName"] != f"release-candidate-candidate-{core['releaseRunId']}-{core['releaseRunAttempt']}"
        or type(core["releaseArtifactId"]) is not int or core["releaseArtifactId"] <= 0
    ):
        raise QualificationError("release authorization fields do not match the protected workflow contract")
    _revision(core["sourceRevision"], "release source revision")
    _revision(core["protectedWorkflowRevision"], "protected workflow revision")
    _digest(core["releaseArtifactDigest"], "release artifact digest")
    return binding


def _expected_package_files(source_root, identity, identity_bytes, image, controller_bytes):
    source_root = Path(source_root)
    source_files = compose_bundle._asset_inventory(source_root)
    assets = compose_bundle._read_assets(source_files, source_root)
    deployment_path = PurePosixPath("deployment.env.example")
    deployment = assets.get(deployment_path)
    marker = b"ghcr.io/flidai/leapview@sha256:<release-digest>"
    if deployment is None or deployment.count(marker) != 1:
        raise QualificationError("canonical deployment environment has no unique release image slot")
    assets[deployment_path] = deployment.replace(marker, image.encode("ascii"))
    assets[PurePosixPath("image-reference.txt")] = (image + "\n").encode("ascii")
    assets[PurePosixPath("release-identity.json")] = identity_bytes
    assets[PurePosixPath("leapviewctl")] = controller_bytes
    schema = _json_bytes(assets[PurePosixPath("local-runtime/runtime-package.schema.json")], "runtime package schema")
    try:
        postgres_schema = schema["$defs"]["postgres"]["properties"]
        postgres_major = postgres_schema["major"]["const"]
        postgres_image = postgres_schema["image"]["const"]
    except (TypeError, KeyError):
        raise QualificationError("canonical runtime schema is missing pinned PostgreSQL fields") from None
    if postgres_major != 18 or not isinstance(postgres_image, str):
        raise QualificationError("canonical runtime schema has an unsupported PostgreSQL contract")
    assets[PurePosixPath("local-runtime/runtime-package.json")] = (
        json.dumps({
            "schemaVersion": 1,
            "persistentStateSchemaVersion": 1,
            "composeMinimumVersion": "2.17.0",
            "leapview": {"version": identity["version"], "revision": identity["revision"], "image": image},
            "postgres": {"major": 18, "image": postgres_image},
        }, indent=2) + "\n"
    ).encode("utf-8")
    checksums = []
    for relative in sorted(assets, key=lambda value: value.as_posix()):
        checksums.append(f"{hashlib.sha256(assets[relative]).hexdigest()}  ./{relative.as_posix()}\n")
    assets[PurePosixPath("SHA256SUMS")] = "".join(checksums).encode("utf-8")
    return assets


def _validate_build_receipt(receipt, identity, platform, controller_digest):
    if not isinstance(receipt, dict) or set(receipt) != compose_bundle.BUILD_RECEIPT_FIELDS:
        raise QualificationError("Nix controller build identity must contain exactly the hash-bound receipt fields")
    if type(receipt["schemaVersion"]) is not int or receipt["schemaVersion"] != 1:
        raise QualificationError("Nix controller build identity schema version must be 1")
    if receipt["platform"] != platform:
        raise QualificationError("Nix controller build identity platform differs from the selected native output")
    if receipt["binarySHA256"] != controller_digest:
        raise QualificationError("Nix controller build identity hash differs from the inner controller bytes")
    for field in ("version", "revision", "buildTime", "dirty", "development"):
        if receipt[field] != identity[field] or type(receipt[field]) is not type(identity[field]):
            raise QualificationError(f"Nix controller build identity {field} differs from release identity")


def _verify_deterministic_gzip(descriptor, size):
    header = os.pread(descriptor, 10, 0)
    if len(header) != 10 or header[:4] != b"\x1f\x8b\x08\x00" or header[4:8] != b"\x00\x00\x00\x00" or header[8:] != b"\x02\xff":
        raise QualificationError("outer bundle gzip header is not deterministic")
    inflater = zlib.decompressobj(16 + zlib.MAX_WBITS)
    offset = 0
    uncompressed = 0
    while offset < size:
        chunk = os.pread(descriptor, min(1024 * 1024, size - offset), offset)
        if not chunk:
            raise QualificationError("outer bundle archive was truncated")
        offset += len(chunk)
        pending = chunk
        while pending:
            try:
                output = inflater.decompress(pending, 16 * 1024 * 1024)
            except zlib.error as exc:
                raise QualificationError(f"outer bundle gzip stream is invalid: {exc}") from exc
            uncompressed += len(output)
            if uncompressed > MAX_BUNDLE_UNPACKED_BYTES:
                raise QualificationError("outer bundle exceeds its unpacked byte limit")
            pending = inflater.unconsumed_tail
            if inflater.unused_data:
                raise QualificationError("outer bundle has trailing data or multiple gzip members")
            if not pending:
                break
    if not inflater.eof or inflater.unused_data:
        raise QualificationError("outer bundle gzip stream is incomplete or has trailing data")
    return uncompressed


def verify_bundle(archive_path, sidecar_path, build_receipt_path, source_root, release_identity_path, *,
                  platform, source_revision, image, extract_dir=None, metadata_reader=None):
    """Verify an exact Nix controller bundle without running any packaged code."""
    platform = _validate_platform(platform)
    source_revision = _revision(source_revision, "source revision")
    _verify_clean_revision(source_root, source_revision)
    if not isinstance(image, str) or IMAGE_RE.fullmatch(image) is None:
        raise QualificationError("candidate image must be an immutable LeapView GHCR reference")
    archive_path = Path(archive_path)
    sidecar = _read_regular(sidecar_path, "outer bundle checksum sidecar", 512)
    try:
        sidecar_text = sidecar.decode("ascii")
    except UnicodeDecodeError:
        raise QualificationError("outer bundle checksum sidecar must be ASCII") from None
    if sidecar_text != sidecar_text.strip() + "\n":
        raise QualificationError("outer bundle checksum sidecar is noncanonical")
    match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9._+-]+\.tar\.gz)\n", sidecar_text)
    if match is None or match.group(2) != archive_path.name:
        raise QualificationError("outer bundle checksum sidecar does not name the exact archive")
    identity_bytes = _read_regular(release_identity_path, "protected release identity", compose_bundle.MAX_IDENTITY_BYTES)
    identity = _canonical_identity(_json_bytes(identity_bytes, "release identity", compose_bundle.MAX_IDENTITY_BYTES))
    canonical_identity = (json.dumps(identity, indent=2, ensure_ascii=False, allow_nan=False) + "\n").encode("utf-8")
    if identity_bytes != canonical_identity or identity["revision"] != source_revision or identity["image"] != image:
        raise QualificationError("protected release identity differs from the exact source revision or image")
    archive_path, descriptor, signature = _open_regular(archive_path, "outer Compose bundle archive", MAX_BUNDLE_BYTES)
    extracted_files = {}
    try:
        outer_digest = hashlib.sha256()
        offset = 0
        while offset < signature[4]:
            chunk = os.pread(descriptor, min(1024 * 1024, signature[4] - offset), offset)
            if not chunk:
                raise QualificationError("outer bundle archive was truncated while hashing")
            outer_digest.update(chunk)
            offset += len(chunk)
        archive_sha = "sha256:" + outer_digest.hexdigest()
        if outer_digest.hexdigest() != match.group(1):
            raise QualificationError("outer bundle archive differs from its exact checksum sidecar")
        _verify_deterministic_gzip(descriptor, signature[4])
        _assert_unchanged(archive_path, descriptor, signature, "outer Compose bundle archive")
        expected_package_name = archive_path.name[:-len(".tar.gz")]
        package_match = PACKAGE_NAME_RE.fullmatch(expected_package_name)
        if package_match is None or platform != "linux/" + package_match.group(1):
            raise QualificationError("outer bundle package name does not match the exact Linux platform")
        controller_member = None
        files = {}
        directories = set()
        with os.fdopen(os.dup(descriptor), "rb") as archive_stream:
            try:
                archive = tarfile.open(fileobj=archive_stream, mode="r:gz")
                members = []
                for member in archive:
                    if len(members) >= 10000:
                        raise QualificationError("outer bundle has an unsupported member count")
                    members.append(member)
                if not members:
                    raise QualificationError("outer bundle has no members")
                root_name = expected_package_name
                normalized = []
                total = 0
                seen_paths = set()
                for member in members:
                    canonical_pax_path = member.name + "/" if member.isdir() and not member.name.endswith("/") else member.name
                    if (
                        not isinstance(member.name, str) or "\\" in member.name
                        or member.pax_headers not in ({}, {"path": canonical_pax_path})
                    ):
                        raise QualificationError("outer bundle contains a noncanonical tar member")
                    if member.isdir() and member.name.rstrip("/") == root_name:
                        base = root_name
                        directory = True
                    else:
                        raw = member.name
                        directory = member.isdir()
                        if directory and raw.endswith("/"):
                            raw = raw[:-1]
                        path = PurePosixPath(raw)
                        if path.is_absolute() or str(path) != raw or any(part in ("", ".", "..") for part in path.parts):
                            raise QualificationError("outer bundle contains an unsafe tar member path")
                        if not path.parts or path.parts[0] != root_name or len(path.parts) < 2:
                            raise QualificationError("outer bundle member is outside its one package root")
                        base = raw
                    if base in seen_paths:
                        raise QualificationError("outer bundle repeats a member path")
                    seen_paths.add(base)
                    if member.uid != 0 or member.gid != 0 or member.uname != "" or member.gname != "" or member.mtime != 0:
                        raise QualificationError("outer bundle tar metadata is not deterministic")
                    if directory:
                        if not member.isdir() or member.size != 0 or member.mode != 0o755:
                            raise QualificationError("outer bundle directory metadata is unsafe or noncanonical")
                        directories.add(base)
                    else:
                        if not member.isfile():
                            raise QualificationError("outer bundle contains a symlink, hard link or special file")
                        if member.size < 0 or member.size > compose_bundle.MAX_CONTROLLER_BYTES and base.endswith("/leapviewctl"):
                            raise QualificationError("outer bundle controller exceeds its byte limit")
                        total += member.size
                        if total > MAX_BUNDLE_UNPACKED_BYTES:
                            raise QualificationError("outer bundle exceeds its unpacked byte limit")
                        stream = archive.extractfile(member)
                        if stream is None:
                            raise QualificationError("outer bundle file data is missing")
                        data = stream.read(member.size + 1)
                        if len(data) != member.size:
                            raise QualificationError("outer bundle member size is inconsistent")
                        files[base.removeprefix(root_name + "/")] = data
                        extracted_files[base.removeprefix(root_name + "/")] = (data, member.mode)
                    normalized.append((base, directory))
                    if directory and base == root_name:
                        root_mode = member.mode
                archive.close()
                if root_name not in directories or root_mode != 0o755:
                    raise QualificationError("outer bundle has no canonical package root")
                ordered = [(root_name, True)] + sorted(
                    (entry for entry in normalized if entry[0] != root_name), key=lambda entry: entry[0]
                )
                if normalized != ordered:
                    raise QualificationError("outer bundle tar members are not in deterministic path order")
            except tarfile.TarError as exc:
                raise QualificationError(f"outer Compose bundle tar is invalid: {exc}") from exc
        controller_bytes = files.get("leapviewctl")
        if not isinstance(controller_bytes, bytes) or not controller_bytes:
            raise QualificationError("outer bundle has no controller executable")
        expected_assets = _expected_package_files(source_root, identity, identity_bytes, image, controller_bytes)
        expected_file_paths = {relative.as_posix() for relative in expected_assets}
        if set(files) != expected_file_paths:
            missing = sorted(expected_file_paths - set(files))
            unexpected = sorted(set(files) - expected_file_paths)
            raise QualificationError(f"outer bundle file inventory differs from canonical package (missing={missing[:3]}, unexpected={unexpected[:3]})")
        for relative, expected_data in expected_assets.items():
            name = relative.as_posix()
            actual = files[name]
            if actual != expected_data:
                raise QualificationError(f"outer bundle file differs from canonical source bytes: {name}")
            expected_mode = compose_bundle._packaged_mode(relative)
            if extracted_files[name][1] != expected_mode:
                raise QualificationError(f"outer bundle file mode is noncanonical: {name}")
        # Every directory must be implied by a packaged file, with no extra subtree.
        expected_dirs = {expected_package_name}
        for relative in expected_assets:
            parent = PurePosixPath(expected_package_name) / relative.parent
            while parent.as_posix() != ".":
                expected_dirs.add(parent.as_posix())
                if len(parent.parts) == 1:
                    break
                parent = parent.parent
        if directories != expected_dirs:
            raise QualificationError("outer bundle directory inventory differs from the canonical package")
        # Re-serialize with the protected assembler to reject alternate tar/PAX
        # encodings, hidden padding and appended zero blocks. The temporary tree
        # contains only the bytes already validated above.
        with tempfile.TemporaryDirectory(prefix="leapview-nix-compose-canonical-") as temporary:
            temporary_root = Path(temporary)
            canonical_root = temporary_root / expected_package_name
            canonical_root.mkdir(mode=0o755)
            os.chmod(canonical_root, 0o755)
            for relative, data in expected_assets.items():
                target = canonical_root.joinpath(*relative.parts)
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
                os.chmod(target.parent, 0o755)
                target.write_bytes(data)
                target.chmod(compose_bundle._packaged_mode(relative))
            canonical_archive = temporary_root / "canonical.tar.gz"
            compose_bundle._write_deterministic_archive(canonical_root, canonical_archive)
            with canonical_archive.open("rb") as canonical_stream:
                canonical_digest = "sha256:" + hashlib.file_digest(canonical_stream, "sha256").hexdigest()
            if canonical_digest != archive_sha:
                raise QualificationError("outer bundle bytes differ from the protected deterministic assembler output")
        controller_path = None
        if extract_dir is not None:
            extract_root = Path(extract_dir)
            if extract_root.exists() or extract_root.is_symlink():
                raise QualificationError("verified bundle extraction destination must be new")
            extract_root.mkdir(parents=True, mode=0o700)
            os.chmod(extract_root, 0o700)
            package_root = extract_root / expected_package_name
            package_root.mkdir(mode=0o755)
            os.chmod(package_root, 0o755)
            for relative, data in expected_assets.items():
                target = package_root.joinpath(*relative.parts)
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
                current = target.parent
                while current != package_root.parent:
                    os.chmod(current, 0o755)
                    if current == package_root:
                        break
                    current = current.parent
                mode = compose_bundle._packaged_mode(relative)
                _write_new(target, data, mode)
            controller_path = str(package_root / "leapviewctl")
        controller_digest = _digest_bytes(controller_bytes)
        receipt_bytes = _read_regular(build_receipt_path, "Nix controller build identity receipt", compose_bundle.MAX_IDENTITY_BYTES)
        receipt = _json_bytes(receipt_bytes, "Nix controller build identity", compose_bundle.MAX_IDENTITY_BYTES)
        _validate_build_receipt(receipt, identity, platform, controller_digest)
        reader = metadata_reader or compose_bundle.read_go_build_metadata
        if controller_path is None:
            with tempfile_controller_file(controller_bytes) as temporary_controller:
                metadata = reader(temporary_controller)
        else:
            metadata = reader(Path(controller_path))
        if not isinstance(metadata, dict):
            raise QualificationError("trusted Go reader returned invalid controller metadata")
        try:
            compose_bundle._validate_controller_metadata(metadata, platform)
        except compose_bundle.BundleError as exc:
            raise QualificationError(f"controller embedded Go metadata is invalid: {exc}") from exc
        _assert_unchanged(archive_path, descriptor, signature, "outer Compose bundle archive")
        if _digest_bytes(controller_bytes) != controller_digest:
            raise QualificationError("controller bytes changed during bundle verification")
        result = {
            "schemaVersion": 1,
            "archiveSHA256": archive_sha,
            "controllerSHA256": controller_digest,
            "controllerBuildIdentitySHA256": _digest_bytes(receipt_bytes),
            "sourceRevision": source_revision,
            "platform": platform,
            "image": image,
            "releaseIdentitySHA256": _digest_bytes(identity_bytes),
            "version": identity["version"],
            "buildTime": identity["buildTime"],
            "releaseAdmission": False,
        }
        if controller_path is not None:
            result["extractedController"] = controller_path
        return result
    finally:
        os.close(descriptor)


class tempfile_controller_file:
    """Create a private temporary controller copy for the trusted Go metadata reader."""

    def __init__(self, data):
        self._temporary = tempfile.TemporaryDirectory(prefix="leapview-nix-compose-go-")
        self.path = Path(self._temporary.name) / "leapviewctl"
        self.path.write_bytes(data)
        self.path.chmod(0o700)

    def __enter__(self):
        return self.path

    def __exit__(self, *_exc):
        self._temporary.cleanup()


def _datetime_nanoseconds(value):
    delta = value - datetime(1970, 1, 1, tzinfo=timezone.utc)
    return (delta.days * 86400 + delta.seconds) * 1_000_000_000 + delta.microseconds * 1000


def _parse_timestamp(value, label):
    if not isinstance(value, str) or not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z", value):
        raise QualificationError(f"{label} must be a canonical UTC RFC3339 timestamp")
    fraction = re.search(r"\.(\d+)Z$", value)
    if fraction is not None and fraction.group(1).endswith("0"):
        raise QualificationError(f"{label} is not in canonical RFC3339Nano form")
    try:
        parsed = datetime.fromisoformat(value[:19] + "+00:00")
    except ValueError:
        raise QualificationError(f"{label} is not a valid timestamp") from None
    if parsed.tzinfo is None or parsed.utcoffset() != timedelta(0):
        raise QualificationError(f"{label} must use UTC")
    # Go emits RFC3339Nano. Python datetime only retains microseconds, so
    # preserve the fraction separately for exact elapsed and phase comparisons.
    nanoseconds = int(fraction.group(1).ljust(9, "0")) if fraction is not None else 0
    return _datetime_nanoseconds(parsed) + nanoseconds


def _runtime_identity(data, expected, product, label):
    actual = _json_bytes(data, label, compose_bundle.MAX_IDENTITY_BYTES)
    fields = {"product", "version", "revision", "buildTime", "dirty", "development"}
    if not isinstance(actual, dict) or set(actual) != fields or actual["product"] != product:
        raise QualificationError(f"{label} has an unsupported product identity schema")
    for field in ("version", "revision", "buildTime", "dirty", "development"):
        if actual[field] != expected[field] or type(actual[field]) is not type(expected[field]):
            raise QualificationError(f"{label} {field} differs from the canonical release identity")
    if actual["dirty"] is not False or actual["development"] is not False:
        raise QualificationError(f"{label} does not identify a clean release runtime")
    return actual


def _validate_installed_report(data, image, platform, now=None):
    report = _json_bytes(data, "installed qualification report")
    expected_arch = platform.removeprefix("linux/")
    top_fields = {"schemaVersion", "result", "image", "architecture", "startedAt", "completedAt", "elapsedSeconds", "phases", "assertions", "multiNode"}
    if (
        not isinstance(report, dict) or set(report) != top_fields
        or type(report["schemaVersion"]) is not int or report["schemaVersion"] != 1
        or report["result"] != "success" or report["image"] != image
        or report["architecture"] != expected_arch or type(report["elapsedSeconds"]) is not int
        or report["elapsedSeconds"] < 0 or not isinstance(report["phases"], list)
        or len(report["phases"]) != len(PHASES)
    ):
        raise QualificationError("installed qualification report is not a complete success for the exact image and native architecture")
    assertions = report["assertions"]
    if not isinstance(assertions, dict) or set(assertions) != set(ASSERTIONS) or any(value is not True for value in assertions.values()):
        raise QualificationError("installed qualification report is missing a required successful assertion")
    started = _parse_timestamp(report["startedAt"], "qualification startedAt")
    completed = _parse_timestamp(report["completedAt"], "qualification completedAt")
    now = _datetime_nanoseconds(now or datetime.now(timezone.utc))
    clock_slack = 300 * 1_000_000_000
    max_age = int(QUALIFICATION_MAX_AGE.total_seconds()) * 1_000_000_000
    if started > now + clock_slack or completed > now + clock_slack or now - started >= max_age:
        raise QualificationError("installed qualification report is future-dated or stale")
    if completed < started or (completed - started) // 1_000_000_000 != report["elapsedSeconds"]:
        raise QualificationError("installed qualification report elapsed time disagrees with its timestamps")
    previous_end = started
    for entry, (name, timeout) in zip(report["phases"], PHASES):
        fields = {"name", "result", "startedAt", "durationMillis", "timeoutSeconds", "cleanupGuaranteed"}
        if (
            not isinstance(entry, dict) or set(entry) != fields or entry["name"] != name or entry["result"] != "success"
            or entry["cleanupGuaranteed"] is not True or type(entry["durationMillis"]) is not int
            or not 0 <= entry["durationMillis"] <= timeout * 1000
            or type(entry["timeoutSeconds"]) is not int or entry["timeoutSeconds"] != timeout
        ):
            raise QualificationError("installed qualification phases are incomplete or outside their bounded contract")
        phase_started = _parse_timestamp(entry["startedAt"], f"qualification phase {name} startedAt")
        phase_ended = phase_started + entry["durationMillis"] * 1_000_000
        if phase_started < previous_end or phase_started > now + clock_slack or phase_ended > completed:
            raise QualificationError("installed qualification phases overlap or fall outside the overall report")
        if now - phase_started >= max_age:
            raise QualificationError("installed qualification phase is stale")
        previous_end = phase_ended
    multi = report["multiNode"]
    multi_fields = {"nodeCount", "generationId", "abruptNodeLoss", "recovery", "rollingRestart", "durableConvergence"}
    if (
        not isinstance(multi, dict) or set(multi) != multi_fields
        or type(multi["nodeCount"]) is not int or multi["nodeCount"] != 2
        or not isinstance(multi["generationId"], str) or not multi["generationId"] or len(multi["generationId"]) > 256
        or any(multi[field] is not True for field in ("abruptNodeLoss", "recovery", "rollingRestart", "durableConvergence"))
    ):
        raise QualificationError("installed qualification report lacks complete two-node process evidence")
    return report


def _verify_controller_evidence(archive, sidecar, build_receipt, source_root, release_identity, *,
                               platform, source_revision, image, binary_verifier, evidence_dir, now=None):
    try:
        import nix_compose_controller_evidence

        return nix_compose_controller_evidence.verify_controller_evidence(
            archive, sidecar, build_receipt, source_root, release_identity,
            platform=platform, source_revision=source_revision, image=image,
            binary_verifier=binary_verifier, evidence_dir=evidence_dir, now=now,
        )
    except (ImportError, ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError) as exc:
        raise QualificationError(f"protected controller evidence verification failed: {exc}") from exc


def record_qualification(archive, sidecar, build_receipt, source_root, release_identity, *, platform,
                         source_revision, image, release_artifact_zip, admission_evidence, controller_runtime_identity,
                         runtime_identity, qualification_report, qualification_evidence_dir,
                         controller_evidence_dir, controller_binary_verifier,
                         release_authorization, signer_revision, metadata_reader=None, now=None):
    """Create a success-only receipt from reverified bundle, image and native journey evidence."""
    authorization = _validate_release_authorization(release_authorization)
    signer_revision = _revision(signer_revision, "protected signer workflow revision")
    if signer_revision != authorization["protectedWorkflowRevision"] or source_revision != authorization["sourceRevision"]:
        raise QualificationError("signer or candidate source differs from the authorized protected release run")
    image_ref_bytes = _read_regular(Path(release_identity).parent / "image-reference.txt", "release image reference", 4096)
    identity_bytes = _read_regular(release_identity, "release identity", compose_bundle.MAX_IDENTITY_BYTES)
    identity = _canonical_identity(_json_bytes(identity_bytes, "release identity", compose_bundle.MAX_IDENTITY_BYTES))
    _check_image_reference(image_ref_bytes, identity, image)
    if identity["revision"] != source_revision:
        raise QualificationError("release identity source does not match protected release authorization")
    handoff = extract_release_handoff(
        release_artifact_zip, authorization["releaseArtifactDigest"], release_authorization=authorization,
    )
    if handoff["image"] != image or handoff["releaseIdentitySHA256"] != _digest_bytes(identity_bytes):
        raise QualificationError("Compose identity or image differs from the exact REST release artifact")
    bundle_binding = verify_bundle(
        archive, sidecar, build_receipt, source_root, release_identity,
        platform=platform, source_revision=source_revision, image=image, metadata_reader=metadata_reader,
    )
    controller_evidence = _verify_controller_evidence(
        archive, sidecar, build_receipt, source_root, release_identity,
        platform=platform, source_revision=source_revision, image=image,
        binary_verifier=controller_binary_verifier, evidence_dir=controller_evidence_dir, now=now,
    )
    controller_evidence_bytes = _read_regular(
        Path(controller_evidence_dir) / "controller-evidence.json",
        "protected controller evidence receipt", MAX_JSON_BYTES,
    )
    controller_identity_bytes = _read_regular(controller_runtime_identity, "native controller runtime identity", compose_bundle.MAX_IDENTITY_BYTES)
    image_identity_bytes = _read_regular(runtime_identity, "installed image runtime identity", compose_bundle.MAX_IDENTITY_BYTES)
    _runtime_identity(controller_identity_bytes, identity, "leapviewctl", "native controller runtime identity")
    _runtime_identity(image_identity_bytes, identity, "leapview", "installed image runtime identity")
    admission_bytes = _read_regular(admission_evidence, "OCI admission evidence", MAX_ADMISSION_BYTES)
    _validate_admission_evidence(admission_bytes, image, source_revision, platform)
    evidence_root = Path(os.path.abspath(os.fspath(qualification_evidence_dir)))
    report_path = Path(os.path.abspath(os.fspath(qualification_report)))
    if report_path != evidence_root / "qualification-report.json":
        raise QualificationError("qualification report must be the root qualification-report.json evidence file")
    evidence_inventory = _qualification_evidence_inventory(evidence_root)
    report_bytes = _read_regular(qualification_report, "installed qualification report", MAX_JSON_BYTES)
    report_entry = next(entry for entry in evidence_inventory["files"] if entry["path"] == "qualification-report.json")
    if report_entry["sha256"] != _digest_bytes(report_bytes) or report_entry["sizeBytes"] != len(report_bytes):
        raise QualificationError("qualification report changed while its evidence inventory was verified")
    _validate_installed_report(report_bytes, image, platform, now=now)
    receipt = {
        "schemaVersion": 1,
        "bundle": {key: bundle_binding[key] for key in (
            "archiveSHA256", "controllerSHA256", "controllerBuildIdentitySHA256", "sourceRevision",
            "platform", "image", "releaseIdentitySHA256",
        )},
        "runtime": {
            "controllerIdentitySHA256": _digest_bytes(controller_identity_bytes),
            "imageIdentitySHA256": _digest_bytes(image_identity_bytes),
        },
        "ociAdmission": {
            "image": image,
            "digest": image.rsplit("@", 1)[1],
            "evidenceSHA256": _digest_bytes(admission_bytes),
        },
        "qualification": {
            "reportSHA256": _digest_bytes(report_bytes),
            "result": "success",
            "platform": platform,
            "evidence": evidence_inventory,
        },
        "controllerEvidence": {
            "archiveSHA256": controller_evidence["bundle"]["archiveSHA256"],
            "controllerSHA256": controller_evidence["bundle"]["controllerSHA256"],
            "sourceRevision": controller_evidence["bundle"]["sourceRevision"],
            "platform": controller_evidence["bundle"]["platform"],
            "receiptSHA256": _digest_bytes(controller_evidence_bytes),
            "bindingDigest": controller_evidence["controllerEvidenceBindingDigest"],
            "reportInventorySHA256": controller_evidence["reports"]["inventorySHA256"],
        },
        "releaseAuthorization": authorization,
        "signer": {
            "workflow": SIGNER_WORKFLOW_IDENTITY,
            "workflowRevision": signer_revision,
            "sourceRef": "refs/heads/main",
        },
        "releaseAdmission": False,
    }
    receipt["composeQualificationBindingDigest"] = _digest_bytes(
        b"leapview/nix-compose-qualification/v1\n" + _canonical_bytes(receipt)
    )
    if len(json.dumps(receipt, indent=2, ensure_ascii=False, allow_nan=False).encode("utf-8")) + 1 > MAX_JSON_BYTES:
        raise QualificationError("Compose qualification receipt exceeds its byte limit")
    return receipt


def verify_qualification(receipt_path, *args, **kwargs):
    """Read-only verifier: recompute every binding and compare canonical JSON bytes."""
    retained = _read_regular(receipt_path, "Compose qualification receipt", MAX_JSON_BYTES)
    supplied = _json_bytes(retained, "Compose qualification receipt", MAX_JSON_BYTES)
    expected = record_qualification(*args, **kwargs)
    if _canonical_bytes(supplied) != _canonical_bytes(expected):
        raise QualificationError("Compose qualification receipt differs from freshly verified evidence")
    return expected


def _write_json_new(path, value):
    data = (json.dumps(value, indent=2, ensure_ascii=False, allow_nan=False) + "\n").encode("utf-8")
    if len(data) > MAX_JSON_BYTES:
        raise QualificationError("evidence JSON exceeds its byte limit")
    _write_new(path, data)


def _load_json_argument(path, label, limit=MAX_JSON_BYTES):
    return _read_json_file(path, label, limit)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    operations = parser.add_subparsers(dest="operation", required=True)
    authorize = operations.add_parser("authorize-release")
    authorize.add_argument("--run", type=Path, required=True, help="normalized run metadata JSON from REST API")
    authorize.add_argument("--workflow", type=Path, required=True, help="workflow metadata JSON from REST API")
    authorize.add_argument("--artifact", type=Path, required=True, help="normalized artifact metadata JSON from REST API")
    authorize.add_argument("--protected-source-root", type=Path, required=True)
    authorize.add_argument("--protected-revision", required=True)
    authorize.add_argument("--output", type=Path, required=True)

    handoff = operations.add_parser("extract-release-handoff")
    handoff.add_argument("--artifact-zip", type=Path, required=True)
    handoff.add_argument("--artifact-digest", required=True)
    handoff.add_argument("--output-dir", type=Path, required=True)
    handoff.add_argument("--release-authorization", type=Path, required=True)

    bundle = operations.add_parser("verify-bundle")
    bundle.add_argument("--archive", type=Path, required=True)
    bundle.add_argument("--sidecar", type=Path, required=True)
    bundle.add_argument("--controller-build-identity", type=Path, required=True)
    bundle.add_argument("--source-root", type=Path, required=True)
    bundle.add_argument("--release-identity", type=Path, required=True)
    bundle.add_argument("--image", required=True)
    bundle.add_argument("--source-revision", required=True)
    bundle.add_argument("--platform", choices=sorted(PLATFORMS), required=True)
    bundle.add_argument("--extract-dir", type=Path)
    bundle.add_argument("--output", type=Path, required=True)

    for operation_name in ("record-qualification", "verify-qualification"):
        qualification = operations.add_parser(operation_name)
        qualification.add_argument("--archive", type=Path, required=True)
        qualification.add_argument("--sidecar", type=Path, required=True)
        qualification.add_argument("--controller-build-identity", type=Path, required=True)
        qualification.add_argument("--source-root", type=Path, required=True)
        qualification.add_argument("--release-identity", type=Path, required=True)
        qualification.add_argument("--release-artifact-zip", type=Path, required=True)
        qualification.add_argument("--image", required=True)
        qualification.add_argument("--source-revision", required=True)
        qualification.add_argument("--platform", choices=sorted(PLATFORMS), required=True)
        qualification.add_argument("--admission-evidence", type=Path, required=True)
        qualification.add_argument("--controller-runtime-identity", type=Path, required=True)
        qualification.add_argument("--runtime-identity", type=Path, required=True, help="actual installed app image version --format json output")
        qualification.add_argument("--qualification-report", type=Path, required=True)
        qualification.add_argument("--qualification-evidence-dir", type=Path, required=True)
        qualification.add_argument("--controller-evidence-dir", type=Path, required=True)
        qualification.add_argument("--controller-binary-verifier", type=Path, required=True)
        qualification.add_argument("--release-authorization", type=Path, required=True)
        qualification.add_argument("--signer-revision", required=True)
        if operation_name == "record-qualification":
            qualification.add_argument("--output", type=Path, required=True)
        else:
            qualification.add_argument("--receipt", type=Path, required=True)

    args = parser.parse_args()
    os.umask(0o077)
    try:
        if args.operation == "authorize-release":
            result = verify_release_run(
                _load_json_argument(args.run, "release run metadata"),
                _load_json_argument(args.workflow, "release workflow metadata"),
                _load_json_argument(args.artifact, "release artifact metadata"),
                protected_revision=args.protected_revision,
                source_root=args.protected_source_root,
            )
            _write_json_new(args.output, result)
        elif args.operation == "extract-release-handoff":
            authorization = _load_json_argument(args.release_authorization, "release authorization")
            _validate_release_authorization(authorization)
            result = extract_release_handoff(
                args.artifact_zip, args.artifact_digest, args.output_dir, release_authorization=authorization,
            )
            print(json.dumps(result, sort_keys=True))
        elif args.operation == "verify-bundle":
            result = verify_bundle(
                args.archive, args.sidecar, args.controller_build_identity, args.source_root,
                args.release_identity, platform=args.platform, source_revision=args.source_revision,
                image=args.image, extract_dir=args.extract_dir,
            )
            _write_json_new(args.output, result)
        else:
            common = {
                "archive": args.archive,
                "sidecar": args.sidecar,
                "build_receipt": args.controller_build_identity,
                "source_root": args.source_root,
                "release_identity": args.release_identity,
                "platform": args.platform,
                "source_revision": args.source_revision,
                "image": args.image,
                "release_artifact_zip": args.release_artifact_zip,
                "admission_evidence": args.admission_evidence,
                "controller_runtime_identity": args.controller_runtime_identity,
                "runtime_identity": args.runtime_identity,
                "qualification_report": args.qualification_report,
                "qualification_evidence_dir": args.qualification_evidence_dir,
                "controller_evidence_dir": args.controller_evidence_dir,
                "controller_binary_verifier": args.controller_binary_verifier,
                "release_authorization": _load_json_argument(args.release_authorization, "release authorization"),
                "signer_revision": args.signer_revision,
            }
            if args.operation == "record-qualification":
                result = record_qualification(**common)
                _write_json_new(args.output, result)
            else:
                result = verify_qualification(args.receipt, **common)
            print(json.dumps({"composeQualificationBindingDigest": result["composeQualificationBindingDigest"],
                              "releaseAdmission": False}, sort_keys=True))
    except (QualificationError, compose_bundle.BundleError, KeyError, TypeError, OSError, EOFError,
            tarfile.TarError, zipfile.BadZipFile, subprocess.SubprocessError) as exc:
        raise SystemExit("Nix Compose qualification rejected: " + str(exc)) from exc


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Record hash-bound controller build inputs and assemble deterministic Compose bundles."""

from __future__ import annotations

import argparse
import datetime as _datetime
import gzip
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path, PurePosixPath
from typing import Callable, Iterable


MAX_CONTROLLER_BYTES = 512 * 1024 * 1024
MAX_IDENTITY_BYTES = 64 * 1024
MAX_ASSET_BYTES = 128 * 1024 * 1024
MAX_TOTAL_ASSET_BYTES = 512 * 1024 * 1024
MAX_GO_STDOUT_BYTES = 1024 * 1024
MAX_GO_STDERR_BYTES = 64 * 1024
GO_PACKAGE = "github.com/flidai/leapview/cmd/leapviewctl"
PLATFORMS = {
    "linux/amd64": ("linux", "amd64"),
    "linux/arm64": ("linux", "arm64"),
    "darwin/amd64": ("darwin", "amd64"),
    "darwin/arm64": ("darwin", "arm64"),
}
IDENTITY_FIELDS = {"version", "revision", "buildTime", "dirty", "development", "image"}
BUILD_RECEIPT_FIELDS = {"schemaVersion", "platform", "binarySHA256", "version", "revision", "buildTime", "dirty", "development"}
IMAGE_REFERENCE_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/:+-]*@sha256:[0-9a-f]{64}$")
SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
REVISION_RE = re.compile(r"^[0-9a-f]{40}$")
VERSION_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$")
PACKAGE_NAME_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._+-]*$")
ASSET_COMPONENT_RE = re.compile(r"^[A-Za-z0-9._+-]+$")


class BundleError(Exception):
    """An invalid candidate or an incomplete bundle input."""


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError(f"duplicate JSON field {key!r}")
        value[key] = item
    return value


def _decode_json(data: bytes, label: str) -> object:
    try:
        return json.loads(data.decode("utf-8"), object_pairs_hook=_unique_object)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise BundleError(f"{label} is not valid JSON: {exc}") from exc


def _stat_signature(info: os.stat_result) -> tuple[int, ...]:
    return (
        info.st_dev,
        info.st_ino,
        info.st_mode,
        info.st_nlink,
        info.st_size,
        info.st_mtime_ns,
        info.st_ctime_ns,
    )


def _lstat_regular(path: Path, label: str, max_bytes: int) -> os.stat_result:
    try:
        info = path.lstat()
    except OSError as exc:
        raise BundleError(f"cannot inspect {label}: {exc}") from exc
    if stat.S_ISLNK(info.st_mode):
        raise BundleError(f"{label} must not be a symlink")
    if not stat.S_ISREG(info.st_mode):
        raise BundleError(f"{label} must be a regular file")
    if info.st_nlink != 1:
        raise BundleError(f"{label} must not have additional hard links")
    if info.st_size < 0 or info.st_size > max_bytes:
        raise BundleError(f"{label} exceeds the {max_bytes}-byte size limit")
    return info


def _read_regular(path: Path, label: str, max_bytes: int, expected: os.stat_result | None = None) -> bytes:
    before = _lstat_regular(path, label, max_bytes)
    if expected is not None and _stat_signature(before) != _stat_signature(expected):
        raise BundleError(f"{label} changed while inputs were being inspected")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as exc:
        raise BundleError(f"cannot open {label}: {exc}") from exc
    try:
        opened = os.fstat(descriptor)
        if not stat.S_ISREG(opened.st_mode) or _stat_signature(opened) != _stat_signature(before):
            raise BundleError(f"{label} changed while it was being opened")
        chunks: list[bytes] = []
        total = 0
        while True:
            chunk = os.read(descriptor, min(1024 * 1024, max_bytes + 1 - total))
            if not chunk:
                break
            chunks.append(chunk)
            total += len(chunk)
            if total > max_bytes:
                raise BundleError(f"{label} exceeds the {max_bytes}-byte size limit")
        after_open = os.fstat(descriptor)
    finally:
        os.close(descriptor)
    after_path = _lstat_regular(path, label, max_bytes)
    signature = _stat_signature(before)
    if _stat_signature(after_open) != signature or _stat_signature(after_path) != signature:
        raise BundleError(f"{label} changed while it was being read")
    data = b"".join(chunks)
    if len(data) != before.st_size:
        raise BundleError(f"{label} changed size while it was being read")
    return data


def _hash_regular(path: Path, label: str, max_bytes: int, expected: os.stat_result | None = None) -> str:
    before = _lstat_regular(path, label, max_bytes)
    if expected is not None and _stat_signature(before) != _stat_signature(expected):
        raise BundleError(f"{label} changed while inputs were being inspected")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as exc:
        raise BundleError(f"cannot open {label}: {exc}") from exc
    digest = hashlib.sha256()
    total = 0
    try:
        opened = os.fstat(descriptor)
        if not stat.S_ISREG(opened.st_mode) or _stat_signature(opened) != _stat_signature(before):
            raise BundleError(f"{label} changed while it was being opened")
        while True:
            chunk = os.read(descriptor, 1024 * 1024)
            if not chunk:
                break
            total += len(chunk)
            if total > max_bytes:
                raise BundleError(f"{label} exceeds the {max_bytes}-byte size limit")
            digest.update(chunk)
        after_open = os.fstat(descriptor)
    finally:
        os.close(descriptor)
    after_path = _lstat_regular(path, label, max_bytes)
    signature = _stat_signature(before)
    if _stat_signature(after_open) != signature or _stat_signature(after_path) != signature or total != before.st_size:
        raise BundleError(f"{label} changed while it was being hashed")
    return "sha256:" + digest.hexdigest()


def _canonical_utc(value: object) -> bool:
    if not isinstance(value, str) or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", value):
        return False
    try:
        parsed = _datetime.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ")
    except ValueError:
        return False
    return parsed.strftime("%Y-%m-%dT%H:%M:%SZ") == value


def _validate_image_reference(value: object) -> str:
    if not isinstance(value, str) or not value.isascii() or IMAGE_REFERENCE_RE.fullmatch(value) is None:
        raise BundleError("image reference must be an immutable name@sha256:<64 lowercase hex> reference")
    return value


def _load_identity(path: Path, image_reference: str) -> tuple[dict[str, object], bytes]:
    data = _read_regular(path, "release identity", MAX_IDENTITY_BYTES)
    parsed = _decode_json(data, "release identity")
    if not isinstance(parsed, dict) or set(parsed) != IDENTITY_FIELDS:
        raise BundleError("release identity must contain exactly version, revision, buildTime, dirty, development, and image")
    identity = parsed
    version = identity["version"]
    if not isinstance(version, str) or VERSION_RE.fullmatch(version) is None:
        raise BundleError("release identity version must be a canonical major.minor.patch version, optionally with prerelease or build metadata")
    if not isinstance(identity["revision"], str) or REVISION_RE.fullmatch(identity["revision"]) is None:
        raise BundleError("release identity revision must be 40 lowercase hexadecimal characters")
    if not _canonical_utc(identity["buildTime"]):
        raise BundleError("release identity buildTime must be canonical UTC YYYY-MM-DDTHH:MM:SSZ")
    if identity["dirty"] is not False:
        raise BundleError("release identity dirty must be false")
    if not isinstance(identity["development"], bool):
        raise BundleError("release identity development must be a boolean")
    identity_image = _validate_image_reference(identity["image"])
    if identity_image != image_reference:
        raise BundleError("release identity image does not match --image-reference")
    return identity, data


def _load_build_receipt(
    path: Path, identity: dict[str, object], platform: str, controller_digest: str
) -> dict[str, object]:
    data = _read_regular(path, "controller build identity", MAX_IDENTITY_BYTES)
    parsed = _decode_json(data, "controller build identity")
    if not isinstance(parsed, dict) or set(parsed) != BUILD_RECEIPT_FIELDS:
        raise BundleError("controller build identity must contain exactly schemaVersion, platform, binarySHA256, version, revision, buildTime, dirty, and development")
    receipt = parsed
    if type(receipt["schemaVersion"]) is not int or receipt["schemaVersion"] != 1:
        raise BundleError("controller build identity schemaVersion must be 1")
    if receipt["platform"] != platform:
        raise BundleError("controller build identity platform does not match --platform")
    if not isinstance(receipt["binarySHA256"], str) or SHA256_RE.fullmatch(receipt["binarySHA256"]) is None:
        raise BundleError("controller build identity binarySHA256 must be sha256:<64 lowercase hex>")
    if receipt["binarySHA256"] != controller_digest:
        raise BundleError("controller build identity binarySHA256 does not match controller bytes")
    for field in ("version", "revision", "buildTime", "dirty", "development"):
        if receipt[field] != identity[field] or type(receipt[field]) is not type(identity[field]):
            raise BundleError(f"controller build identity {field} does not match release identity")
    return receipt


def read_go_build_metadata(controller: Path) -> dict[str, object]:
    """Read Go's embedded build metadata without executing the target binary."""
    go = shutil.which("go")
    if go is None:
        raise BundleError("trusted Go toolchain is not available on PATH")
    try:
        process = subprocess.Popen(
            [go, "version", "-m", "-json", str(controller)],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    except OSError as exc:
        raise BundleError(f"cannot inspect controller with go version -m -json: {exc}") from exc
    captured: dict[str, bytearray] = {"stdout": bytearray(), "stderr": bytearray()}
    overflow: set[str] = set()

    def drain(name: str, stream: object, limit: int) -> None:
        while True:
            chunk = stream.read(64 * 1024)  # type: ignore[attr-defined]
            if not chunk:
                return
            remaining = limit - len(captured[name])
            if remaining > 0:
                captured[name].extend(chunk[:remaining])
            if len(chunk) > remaining:
                overflow.add(name)

    import threading

    assert process.stdout is not None and process.stderr is not None
    stdout_thread = threading.Thread(target=drain, args=("stdout", process.stdout, MAX_GO_STDOUT_BYTES), daemon=True)
    stderr_thread = threading.Thread(target=drain, args=("stderr", process.stderr, MAX_GO_STDERR_BYTES), daemon=True)
    stdout_thread.start()
    stderr_thread.start()
    try:
        returncode = process.wait(timeout=30)
    except subprocess.TimeoutExpired as exc:
        process.kill()
        process.wait()
        stdout_thread.join()
        stderr_thread.join()
        raise BundleError("go version -m -json timed out while inspecting controller") from exc
    stdout_thread.join()
    stderr_thread.join()
    process.stdout.close()
    process.stderr.close()
    if overflow:
        raise BundleError("go version -m -json output exceeded its size limit")
    try:
        stderr_text = bytes(captured["stderr"]).decode("utf-8").strip()
        stdout_text = bytes(captured["stdout"]).decode("utf-8")
    except UnicodeDecodeError as exc:
        raise BundleError("go version -m -json returned non-UTF-8 output") from exc
    if returncode != 0:
        detail = stderr_text or f"exit status {returncode}"
        raise BundleError(f"go version -m -json rejected controller: {detail}")
    parsed = _decode_json(stdout_text.encode("utf-8"), "Go controller build metadata")
    if not isinstance(parsed, dict):
        raise BundleError("Go controller build metadata must be one JSON object")
    return parsed


def _validate_controller_metadata(metadata: dict[str, object], platform: str) -> None:
    expected_goos, expected_goarch = PLATFORMS[platform]
    if metadata.get("Path") != GO_PACKAGE:
        raise BundleError(f"controller Go package must be {GO_PACKAGE}")
    if not isinstance(metadata.get("GoVersion"), str) or not metadata["GoVersion"]:
        raise BundleError("controller Go metadata is missing GoVersion")
    settings_value = metadata.get("Settings")
    if not isinstance(settings_value, list):
        raise BundleError("controller Go build metadata is missing Settings")
    settings: dict[str, str] = {}
    for entry in settings_value:
        if not isinstance(entry, dict) or not isinstance(entry.get("Key"), str) or not isinstance(entry.get("Value"), str):
            raise BundleError("controller Go build metadata contains an invalid setting")
        key = entry["Key"]
        if key in settings:
            raise BundleError(f"controller Go build metadata repeats {key}")
        settings[key] = entry["Value"]
    expected_settings = {"GOOS": expected_goos, "GOARCH": expected_goarch, "CGO_ENABLED": "0"}
    for key, expected in expected_settings.items():
        if settings.get(key) != expected:
            raise BundleError(f"controller {key}={settings.get(key)!r}, expected {expected!r} for {platform}")


def _validate_source_relative(relative: PurePosixPath) -> None:
    if relative.is_absolute() or not relative.parts or any(part in (".", "..") for part in relative.parts):
        raise BundleError(f"unsafe source asset path: {relative}")
    for part in relative.parts:
        if ASSET_COMPONENT_RE.fullmatch(part) is None:
            raise BundleError(f"unsafe source asset path component: {part!r}")


def _tree_inventory(root: Path, label: str) -> dict[Path, os.stat_result]:
    try:
        root_info = root.lstat()
    except OSError as exc:
        raise BundleError(f"missing {label}: {exc}") from exc
    if stat.S_ISLNK(root_info.st_mode) or not stat.S_ISDIR(root_info.st_mode):
        raise BundleError(f"{label} must be a real directory")
    inventory: dict[Path, os.stat_result] = {}
    for directory, dirnames, filenames in os.walk(root, topdown=True, followlinks=False):
        current = Path(directory)
        for name in sorted(dirnames):
            child = current / name
            info = child.lstat()
            if stat.S_ISLNK(info.st_mode):
                raise BundleError(f"{label} contains a symlink: {child.relative_to(root)}")
            if not stat.S_ISDIR(info.st_mode):
                raise BundleError(f"{label} contains a non-directory path: {child.relative_to(root)}")
            _validate_source_relative(PurePosixPath(child.relative_to(root).as_posix()))
        for name in sorted(filenames):
            child = current / name
            info = _lstat_regular(child, f"{label} file {child.relative_to(root)}", MAX_ASSET_BYTES)
            relative = PurePosixPath(child.relative_to(root).as_posix())
            _validate_source_relative(relative)
            inventory[child] = info
    return inventory


def _assert_real_parent_chain(root: Path, path: Path, label: str) -> None:
    try:
        relative = path.relative_to(root)
    except ValueError as exc:
        raise BundleError(f"{label} is outside the source root") from exc
    current = root
    for part in relative.parts[:-1]:
        current = current / part
        try:
            info = current.lstat()
        except OSError as exc:
            raise BundleError(f"missing source directory for {label}: {exc}") from exc
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
            raise BundleError(f"source path for {label} traverses a symlink or non-directory: {current}")


def _asset_inventory(source_root: Path) -> dict[PurePosixPath, tuple[Path, os.stat_result]]:
    fixed = {
        "compose.yaml": "deploy/compose/compose.yaml",
        "compose.https.yaml": "deploy/compose/compose.https.yaml",
        "compose.first-install-bootstrap.yaml": "deploy/compose/compose.first-install-bootstrap.yaml",
        "Caddyfile": "deploy/compose/Caddyfile",
        "Caddyfile.first-install-bootstrap": "deploy/compose/Caddyfile.first-install-bootstrap",
        "first-install.env": "deploy/compose/first-install.env",
        "README.md": "deploy/compose/README.md",
        "QUALIFICATION.md": "deploy/compose/QUALIFICATION.md",
        "leapviewctl-wrapper": "deploy/host/files/leapviewctl-wrapper",
        "bootstrap-linux.sh": "deploy/host/bootstrap-linux.sh",
        "leapview.env.example": "deploy/compose/leapview.env.example",
        "deployment.env.example": "deploy/compose/deployment.env.example",
        "local-runtime/compose.yaml": "deploy/local/compose.yaml",
        "local-runtime/README.md": "deploy/local/README.md",
        "local-runtime/runtime-package.schema.json": "deploy/local/runtime-package.schema.json",
    }
    files: dict[PurePosixPath, tuple[Path, os.stat_result]] = {}
    for destination, source in fixed.items():
        path = source_root / source
        _assert_real_parent_chain(source_root, path, f"canonical asset {source}")
        info = _lstat_regular(path, f"canonical asset {source}", MAX_ASSET_BYTES)
        files[PurePosixPath(destination)] = (path, info)

    qualification = source_root / "deploy/compose/qualification"
    _assert_real_parent_chain(source_root, qualification / "marker", "canonical qualification tree")
    for path, info in _tree_inventory(qualification, "canonical qualification tree").items():
        relative = path.relative_to(qualification)
        destination = PurePosixPath("qualification") / PurePosixPath(relative.as_posix())
        if destination in files:
            raise BundleError(f"duplicate package path: {destination}")
        files[destination] = (path, info)

    init_path = source_root / "deploy/postgres/init.sh"
    _assert_real_parent_chain(source_root, init_path, "canonical PostgreSQL init script")
    init_info = _lstat_regular(init_path, "canonical PostgreSQL init script", MAX_ASSET_BYTES)
    for destination in (PurePosixPath("qualification/postgres-init.sh"), PurePosixPath("local-runtime/postgres-init.sh")):
        if destination in files:
            raise BundleError(f"duplicate package path: {destination}")
        files[destination] = (init_path, init_info)

    folded: dict[str, PurePosixPath] = {}
    for destination in files:
        _validate_source_relative(destination)
        key = destination.as_posix().casefold()
        previous = folded.get(key)
        if previous is not None:
            raise BundleError(f"package paths collide on case-insensitive filesystems: {previous} and {destination}")
        folded[key] = destination
    return files


def _read_assets(files: dict[PurePosixPath, tuple[Path, os.stat_result]], source_root: Path) -> dict[PurePosixPath, bytes]:
    assets: dict[PurePosixPath, bytes] = {}
    total = 0
    for destination in sorted(files, key=lambda item: item.as_posix()):
        source, signature = files[destination]
        _assert_real_parent_chain(source_root, source, f"canonical asset {source}")
        data = _read_regular(source, f"canonical asset {source}", MAX_ASSET_BYTES, signature)
        total += len(data)
        if total > MAX_TOTAL_ASSET_BYTES:
            raise BundleError(f"canonical bundle assets exceed the {MAX_TOTAL_ASSET_BYTES}-byte total size limit")
        assets[destination] = data
    # Re-inventory the copied tree and recheck standalone files. This catches
    # additions, removals, replacements, and mode changes during assembly.
    expected_sources = {source for source, _ in files.values()}
    qualification_root = source_root / "deploy/compose/qualification"
    qualification_now = _tree_inventory(qualification_root, "canonical qualification tree")
    qualification_expected = {source for source in expected_sources if source.is_relative_to(qualification_root)}
    if set(qualification_now) != qualification_expected:
        raise BundleError("canonical qualification tree changed while assets were being read")
    for source in qualification_expected:
        original = next(info for candidate, info in files.values() if candidate == source)
        if _stat_signature(qualification_now[source]) != _stat_signature(original):
            raise BundleError(f"canonical qualification asset changed while assets were being read: {source}")
    for source in expected_sources - qualification_expected:
        original = next(info for candidate, info in files.values() if candidate == source)
        current = _lstat_regular(source, f"canonical asset {source}", MAX_ASSET_BYTES)
        if _stat_signature(current) != _stat_signature(original):
            raise BundleError(f"canonical asset changed while assets were being read: {source}")
    return assets


def _postgres_image(compose: bytes, schema: bytes) -> str:
    schema_value = _decode_json(schema, "local-runtime runtime-package schema")
    try:
        postgres_schema = schema_value["$defs"]["postgres"]["properties"]
        expected_major = postgres_schema["major"]["const"]
        expected_image = postgres_schema["image"]["const"]
    except (TypeError, KeyError) as exc:
        raise BundleError("local-runtime schema is missing pinned PostgreSQL package fields") from exc
    if expected_major != 18 or not isinstance(expected_image, str):
        raise BundleError("local-runtime schema has an unsupported PostgreSQL contract")
    try:
        text = compose.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise BundleError("local-runtime Compose file is not UTF-8") from exc
    matches = re.findall(r"^[ \t]+image:[ \t]*(docker\.io/library/postgres:[^\s#]+)[ \t]*$", text, flags=re.MULTILINE)
    if len(matches) != 1:
        raise BundleError("local-runtime Compose file must contain exactly one pinned PostgreSQL image")
    if matches[0] != expected_image:
        raise BundleError("local-runtime PostgreSQL image disagrees with runtime-package schema")
    return expected_image


def _validate_package_name(name: str) -> None:
    if (
        PACKAGE_NAME_RE.fullmatch(name) is None
        or name in (".", "..")
        or name.endswith((".", " "))
        or len(os.fsencode(name)) > 240
    ):
        raise BundleError("package name must be a safe filename stem using letters, digits, dot, underscore, or hyphen")


def _validate_output_targets(output_dir: Path, package_name: str) -> tuple[Path, Path, Path]:
    _validate_package_name(package_name)
    bundle_path = output_dir / package_name
    archive_path = output_dir / f"{package_name}.tar.gz"
    checksum_path = output_dir / f"{package_name}.tar.gz.sha256"
    for path in (bundle_path, archive_path, checksum_path):
        if path.exists() or path.is_symlink():
            raise BundleError(f"refusing to replace existing output: {path}")
    return bundle_path, archive_path, checksum_path


def _packaged_mode(relative: PurePosixPath) -> int:
    if relative.as_posix() in {"leapviewctl", "leapviewctl-wrapper", "bootstrap-linux.sh"}:
        return 0o755
    if relative.name.endswith(".sh"):
        return 0o755
    return 0o644


def _write_file(path: Path, data: bytes, mode: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0)
    descriptor = os.open(path, flags, mode)
    try:
        view = memoryview(data)
        while view:
            written = os.write(descriptor, view)
            view = view[written:]
        os.fchmod(descriptor, mode)
    finally:
        os.close(descriptor)


def _checksum_manifest(bundle_root: Path) -> bytes:
    lines: list[str] = []
    files = sorted(
        (path for path in bundle_root.rglob("*") if path.is_file()),
        key=lambda path: path.relative_to(bundle_root).as_posix(),
    )
    if any(path.is_symlink() for path in files):
        raise BundleError("assembled package unexpectedly contains a symlink")
    for path in files:
        relative = path.relative_to(bundle_root).as_posix()
        digest = _sha256(path)
        lines.append(f"{digest}  ./{relative}\n")
    return "".join(lines).encode("utf-8")


def _write_deterministic_archive(bundle_root: Path, archive_path: Path) -> None:
    package_name = bundle_root.name
    entries = [bundle_root, *sorted(bundle_root.rglob("*"), key=lambda item: item.relative_to(bundle_root.parent).as_posix())]
    with archive_path.open("xb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0, compresslevel=9) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as archive:
                for path in entries:
                    relative = PurePosixPath(package_name) if path == bundle_root else PurePosixPath(package_name) / PurePosixPath(path.relative_to(bundle_root).as_posix())
                    name = relative.as_posix()
                    if path.is_dir():
                        info = tarfile.TarInfo(name + "/")
                        info.type = tarfile.DIRTYPE
                        info.mode = 0o755
                        info.size = 0
                        info.mtime = 0
                        info.uid = info.gid = 0
                        info.uname = info.gname = ""
                        archive.addfile(info)
                        continue
                    if path.is_symlink() or not path.is_file():
                        raise BundleError(f"assembled package contains an unsafe path: {path}")
                    info = tarfile.TarInfo(name)
                    info.type = tarfile.REGTYPE
                    info.mode = stat.S_IMODE(path.stat().st_mode)
                    info.size = path.stat().st_size
                    info.mtime = 0
                    info.uid = info.gid = 0
                    info.uname = info.gname = ""
                    with path.open("rb") as source:
                        archive.addfile(info, fileobj=source)


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        while True:
            chunk = source.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def _write_new_file(path: Path, data: bytes, mode: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
    temporary: Path | None = None
    try:
        descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.tmp-", dir=path.parent)
        temporary = Path(temporary_name)
        with os.fdopen(descriptor, "wb") as target:
            target.write(data)
            target.flush()
            os.fchmod(target.fileno(), mode)
            os.fsync(target.fileno())
        os.link(temporary, path)
    except FileExistsError as exc:
        raise BundleError(f"refusing to replace existing output: {path}") from exc
    except OSError as exc:
        raise BundleError(f"cannot write build identity receipt {path}: {exc}") from exc
    finally:
        if temporary is not None:
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass


def record_build_identity(
    args: argparse.Namespace,
    metadata_reader: Callable[[Path], dict[str, object]] = read_go_build_metadata,
) -> Path:
    """Write a builder receipt bound to the binary hash; this is not runtime evidence."""
    if args.platform not in PLATFORMS:
        raise BundleError(f"unsupported platform: {args.platform}")
    image_reference = _validate_image_reference(args.image_reference)
    identity, _ = _load_identity(Path(args.release_identity), image_reference)
    controller = Path(args.controller)
    controller_info = _lstat_regular(controller, "controller executable", MAX_CONTROLLER_BYTES)
    if stat.S_IMODE(controller_info.st_mode) & 0o111 == 0:
        raise BundleError("controller must be executable")
    controller_digest = _hash_regular(controller, "controller executable", MAX_CONTROLLER_BYTES, controller_info)
    metadata = metadata_reader(controller)
    if not isinstance(metadata, dict):
        raise BundleError("Go controller metadata reader returned an invalid value")
    _validate_controller_metadata(metadata, args.platform)
    if _hash_regular(controller, "controller executable", MAX_CONTROLLER_BYTES, controller_info) != controller_digest:
        raise BundleError("controller changed during Go metadata inspection")
    receipt = {
        "schemaVersion": 1,
        "platform": args.platform,
        "binarySHA256": controller_digest,
        "version": identity["version"],
        "revision": identity["revision"],
        "buildTime": identity["buildTime"],
        "dirty": identity["dirty"],
        "development": identity["development"],
    }
    output = Path(args.output)
    if output.exists() or output.is_symlink():
        raise BundleError(f"refusing to replace existing output: {output}")
    _write_new_file(output, (json.dumps(receipt, indent=2) + "\n").encode("utf-8"), 0o644)
    return output


def _validate_with_canonical_script(bundle_root: Path) -> None:
    validator = bundle_root / "qualification/validate-bundle.sh"
    try:
        result = subprocess.run([str(validator), str(bundle_root)], check=False, capture_output=True, text=True, timeout=30)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise BundleError(f"cannot run canonical Compose bundle validator: {exc}") from exc
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or f"exit status {result.returncode}"
        raise BundleError(f"canonical Compose bundle validation failed: {detail}")


def assemble(args: argparse.Namespace, metadata_reader: Callable[[Path], dict[str, object]] = read_go_build_metadata) -> tuple[Path, Path, Path]:
    if args.platform not in PLATFORMS:
        raise BundleError(f"unsupported platform: {args.platform}")
    _validate_package_name(args.package_name)
    image_reference = _validate_image_reference(args.image_reference)
    output_dir = Path(args.output_dir)
    if output_dir.exists() and (output_dir.is_symlink() or not output_dir.is_dir()):
        raise BundleError("output directory must be a real directory")
    final_bundle, final_archive, final_checksum = _validate_output_targets(output_dir, args.package_name)

    source_root = Path(args.source_root)
    try:
        root_info = source_root.lstat()
    except OSError as exc:
        raise BundleError(f"cannot inspect source root: {exc}") from exc
    if stat.S_ISLNK(root_info.st_mode) or not stat.S_ISDIR(root_info.st_mode):
        raise BundleError("source root must be a real directory")

    controller = Path(args.controller)
    controller_info = _lstat_regular(controller, "controller executable", MAX_CONTROLLER_BYTES)
    if stat.S_IMODE(controller_info.st_mode) & 0o111 == 0:
        raise BundleError("controller must be executable")
    controller_bytes = _read_regular(controller, "controller executable", MAX_CONTROLLER_BYTES, controller_info)
    controller_digest = "sha256:" + hashlib.sha256(controller_bytes).hexdigest()

    identity_path = Path(args.release_identity)
    identity, identity_bytes = _load_identity(identity_path, image_reference)
    _load_build_receipt(Path(args.controller_build_identity), identity, args.platform, controller_digest)
    metadata = metadata_reader(controller)
    if not isinstance(metadata, dict):
        raise BundleError("Go controller metadata reader returned an invalid value")
    _validate_controller_metadata(metadata, args.platform)
    if _hash_regular(controller, "controller executable", MAX_CONTROLLER_BYTES, controller_info) != controller_digest:
        raise BundleError("controller changed during Go metadata inspection")

    inventory = _asset_inventory(source_root)
    assets = _read_assets(inventory, source_root)
    deployment_template = assets.pop(PurePosixPath("deployment.env.example"), None)
    if deployment_template is None:
        raise BundleError("canonical deployment environment template is missing from the asset inventory")
    token = b"ghcr.io/flidai/leapview@sha256:<release-digest>"
    if deployment_template.count(token) != 1:
        raise BundleError("canonical deployment environment template must contain the release image marker exactly once")
    assets[PurePosixPath("deployment.env.example")] = deployment_template.replace(token, image_reference.encode("ascii"))
    if assets[PurePosixPath("deployment.env.example")].count(b"<release-digest>"):
        raise BundleError("deployment environment still contains an unsubstituted release image marker")

    image_bytes = (image_reference + "\n").encode("ascii")
    assets[PurePosixPath("image-reference.txt")] = image_bytes
    assets[PurePosixPath("release-identity.json")] = identity_bytes
    postgres_image = _postgres_image(
        assets[PurePosixPath("local-runtime/compose.yaml")],
        assets[PurePosixPath("local-runtime/runtime-package.schema.json")],
    )
    runtime_package = {
        "schemaVersion": 1,
        "persistentStateSchemaVersion": 1,
        "composeMinimumVersion": "2.17.0",
        "leapview": {
            "version": identity["version"],
            "revision": identity["revision"],
            "image": image_reference,
        },
        "postgres": {"major": 18, "image": postgres_image},
    }
    assets[PurePosixPath("local-runtime/runtime-package.json")] = (json.dumps(runtime_package, indent=2) + "\n").encode("utf-8")
    assets[PurePosixPath("leapviewctl")] = controller_bytes

    # Check collisions after generated files have been added, including
    # case-insensitive collisions that would overwrite on macOS.
    destinations: dict[str, PurePosixPath] = {}
    for relative in assets:
        _validate_source_relative(relative)
        key = relative.as_posix().casefold()
        if key in destinations:
            raise BundleError(f"duplicate package path: {destinations[key]} and {relative}")
        destinations[key] = relative

    output_created = False
    if not output_dir.exists():
        output_dir.mkdir(parents=True, mode=0o755)
        output_created = True
    stage_dir = Path(tempfile.mkdtemp(prefix=f".{args.package_name}.tmp-", dir=output_dir))
    published: list[Path] = []
    try:
        bundle_root = stage_dir / args.package_name
        bundle_root.mkdir(mode=0o755)
        os.chmod(bundle_root, 0o755)
        for relative in sorted(assets, key=lambda item: item.as_posix()):
            _write_file(bundle_root.joinpath(*relative.parts), assets[relative], _packaged_mode(relative))
        for directory in sorted((path for path in bundle_root.rglob("*") if path.is_dir()), key=lambda item: len(item.parts), reverse=True):
            os.chmod(directory, 0o755)
        os.chmod(bundle_root, 0o755)
        _validate_with_canonical_script(bundle_root)
        _write_file(bundle_root / "SHA256SUMS", _checksum_manifest(bundle_root), 0o644)
        staged_archive = stage_dir / f"{args.package_name}.tar.gz"
        _write_deterministic_archive(bundle_root, staged_archive)
        staged_checksum = stage_dir / f"{args.package_name}.tar.gz.sha256"
        _write_file(staged_checksum, f"{_sha256(staged_archive)}  {staged_archive.name}\n".encode("ascii"), 0o644)

        # Recheck immediately before publishing and roll back every visible
        # output if any rename fails. The checksum sidecar is installed last.
        _validate_output_targets(output_dir, args.package_name)
        os.rename(bundle_root, final_bundle)
        published.append(final_bundle)
        os.rename(staged_archive, final_archive)
        published.append(final_archive)
        os.rename(staged_checksum, final_checksum)
        published.append(final_checksum)
        return final_bundle, final_archive, final_checksum
    except Exception:
        for path in reversed(published):
            if path.is_dir() and not path.is_symlink():
                shutil.rmtree(path, ignore_errors=True)
            else:
                try:
                    path.unlink()
                except FileNotFoundError:
                    pass
        raise
    finally:
        shutil.rmtree(stage_dir, ignore_errors=True)
        if output_created:
            try:
                output_dir.rmdir()
            except OSError:
                pass


def _argument_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)

    record = commands.add_parser(
        "record-build-identity",
        help="write a hash-bound build-input receipt (not runtime qualification evidence)",
    )
    record.add_argument("--controller", required=True, type=Path, help="target-platform leapviewctl executable")
    record.add_argument("--platform", required=True, choices=sorted(PLATFORMS), help="controller target platform")
    record.add_argument("--image-reference", required=True, help="immutable name@sha256:<digest> application image")
    record.add_argument("--release-identity", required=True, type=Path, help="JSON build identity for this candidate")
    record.add_argument("--output", required=True, type=Path, help="new path for the controller build receipt")

    assembly = commands.add_parser("assemble", help="assemble a validated Compose bundle")
    assembly.add_argument("--controller", required=True, type=Path, help="target-platform leapviewctl executable")
    assembly.add_argument("--controller-build-identity", required=True, type=Path, help="hash-bound builder receipt; not runtime qualification")
    assembly.add_argument("--platform", required=True, choices=sorted(PLATFORMS), help="controller target platform")
    assembly.add_argument("--image-reference", required=True, help="immutable name@sha256:<digest> application image")
    assembly.add_argument("--release-identity", required=True, type=Path, help="JSON build identity for this candidate")
    assembly.add_argument("--package-name", required=True, help="safe package filename stem")
    assembly.add_argument("--output-dir", required=True, type=Path, help="directory for bundle, archive, and checksum")
    assembly.add_argument("--source-root", required=True, type=Path, help="LeapView source checkout containing canonical deploy assets")
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = _argument_parser().parse_args(argv)
    try:
        if args.command == "record-build-identity":
            output = record_build_identity(args)
            print(f"build identity receipt (builder inputs only): {output}")
            return 0
        bundle, archive, checksum = assemble(args)
    except BundleError as exc:
        print(f"Compose bundle assembly failed: {exc}", file=sys.stderr)
        return 2
    except OSError as exc:
        print(f"Compose bundle assembly failed: {exc}", file=sys.stderr)
        return 2
    print(f"bundle: {bundle}")
    print(f"archive: {archive}")
    print(f"checksum: {checksum}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

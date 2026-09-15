#!/usr/bin/env python3
"""Qualify an extracted LeapView authoring package.

The package checks in this module are deliberately independent of the source
checkout.  A normal invocation verifies an archive, its two checksum layers,
the package/runtime manifests, and the installed command identity.  The
optional lifecycle lane is opt-in because local authentication is a human
prerequisite and preview/deploy qualification is not released yet.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import platform
import posixpath
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
from pathlib import Path
from typing import Any, Optional


CONTRACT_VERSION = 1
MAX_OUTPUT = 4_000
MAX_MEMBERS = 2_000
MAX_FILE_BYTES = 128 * 1024 * 1024
DEFAULT_TIMEOUT_SECONDS = 300
SUPPORTED_HOSTS = {
    ("Linux", "x86_64"): ("linux", "amd64"),
    ("Linux", "amd64"): ("linux", "amd64"),
    ("Linux", "aarch64"): ("linux", "arm64"),
    ("Linux", "arm64"): ("linux", "arm64"),
    ("Darwin", "x86_64"): ("darwin", "amd64"),
    ("Darwin", "arm64"): ("darwin", "arm64"),
}
SCENARIOS = (
    "semantic",
    "model",
    "dashboard",
    "presentation",
    "invalid",
)
REPETITIONS = ("coldUncached", "coldCached", "warmRestart", "editToVisible")
PREVIEW_NOT_RELEASED = (
    "preview/deploy implementation and browser observation are planned in "
    "ADR-0021; this package lane does not fabricate edit-to-visible samples"
)


class QualificationError(Exception):
    """A required qualification assertion failed."""


class QualificationSkip(Exception):
    """The caller did not provide an explicitly required prerequisite."""


def now_iso() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def redact(value: str) -> str:
    """Keep command diagnostics useful without retaining credential material."""

    value = value.replace("\x00", "")
    value = re.sub(r"(?i)(authorization\s*:\s*bearer\s+)[^\s]+", r"\1<redacted>", value)
    value = re.sub(r"(?i)(\"[^\"]*(?:password|passwd|token|secret|credential|cookie|authorization|api[_-]?key)[^\"]*\"\s*:\s*)\"[^\"]*\"", r"\1\"<redacted>\"", value)
    value = re.sub(r"(?i)(\b(?:password|passwd|token|secret|credential|cookie|authorization|api[_-]?key)[A-Za-z0-9_-]*\s*:\s*)\S+", r"\1<redacted>", value)
    value = re.sub(r"(?i)(password|passwd|token|secret|credential|cookie|api[_-]?key)\s*[:=]\s*[^\s,;]+", r"\1=<redacted>", value)
    value = re.sub(r"(?i)(://)([^/@\s]+)@", r"\1<redacted>@", value)
    value = re.sub(r"(?i)(LEAPVIEW_[A-Z0-9_]*(?:PASSWORD|TOKEN|SECRET|CREDENTIAL)[A-Z0-9_]*=)[^\s]+", r"\1<redacted>", value)
    if len(value) > MAX_OUTPUT:
        return value[:MAX_OUTPUT] + "\n[output truncated]"
    return value


def command_display(command: list[str]) -> str:
    return " ".join("<redacted>" if re.search(r"(?i)(password|token|secret|credential)", arg) else arg for arg in command)


def write_json(path: Path, document: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.chmod(path, 0o600)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def safe_relative(name: str) -> str:
    if not name.startswith("./"):
        raise QualificationError(f"internal checksum path is not relative: {name!r}")
    relative = name[2:]
    normalized = posixpath.normpath(relative)
    if not relative or normalized != relative or normalized == "." or normalized.startswith("../") or "/../" in normalized or "\\" in relative:
        raise QualificationError(f"unsafe internal checksum path: {name!r}")
    return normalized


def parse_outer_checksum(checksum_path: Path, archive: Path) -> str:
    lines = [line.strip() for line in checksum_path.read_text(encoding="utf-8").splitlines() if line.strip()]
    if len(lines) != 1:
        raise QualificationError("outer checksum must contain exactly one non-empty entry")
    match = re.fullmatch(r"([0-9a-f]{64})\s+(?:\*?)([^\s]+)", lines[0])
    if not match or Path(match.group(2)).name != archive.name:
        raise QualificationError("outer checksum does not name the supplied archive")
    return match.group(1)


def extract_archive(archive: Path, destination: Path) -> tuple[Path, list[str]]:
    try:
        source = tarfile.open(archive, "r:gz")
    except (tarfile.TarError, OSError) as exc:
        raise QualificationError(f"read authoring archive: {exc}") from exc
    with source:
        members = source.getmembers()
        if not members or len(members) > MAX_MEMBERS:
            raise QualificationError("authoring archive has no members or exceeds the bounded member limit")
        roots: set[str] = set()
        seen: set[str] = set()
        for member in members:
            name = member.name
            normalized = posixpath.normpath(name)
            if name.startswith("/") or normalized != name or normalized == "." or normalized.startswith("../") or "\\" in name:
                raise QualificationError(f"unsafe archive member: {name!r}")
            root = name.split("/", 1)[0]
            roots.add(root)
            if name in seen:
                raise QualificationError(f"duplicate archive member: {name!r}")
            seen.add(name)
            if member.issym() or member.islnk() or not (member.isdir() or member.isreg()):
                raise QualificationError(f"archive member is not a regular file or directory: {name!r}")
            if member.size > MAX_FILE_BYTES:
                raise QualificationError(f"archive member exceeds bounded file size: {name!r}")
        if len(roots) != 1:
            raise QualificationError("authoring archive must contain exactly one package root")
        package_name = next(iter(roots))
        expected = re.fullmatch(r"leapview-cli-v[^/]+-(linux|darwin)-(amd64|arm64)", package_name)
        if not expected:
            raise QualificationError(f"unsupported authoring package root: {package_name!r}")
        package_root = destination / package_name
        package_root.mkdir(parents=True)
        for member in members:
            relative = member.name[len(package_name):].lstrip("/")
            if not relative:
                continue
            target = package_root / Path(relative)
            target_parent = target.parent
            target_parent.mkdir(parents=True, exist_ok=True)
            if member.isdir():
                target.mkdir(exist_ok=True)
                continue
            reader = source.extractfile(member)
            if reader is None:
                raise QualificationError(f"cannot read archive member: {member.name!r}")
            with reader, target.open("xb") as output:
                shutil.copyfileobj(reader, output, 1024 * 1024)
            os.chmod(target, member.mode & 0o777)
    return package_root, sorted(seen)


def verify_internal_checksums(package_root: Path) -> dict[str, Any]:
    checksum_path = package_root / "SHA256SUMS"
    if not checksum_path.is_file():
        raise QualificationError("authoring package is missing SHA256SUMS")
    entries: dict[str, str] = {}
    for line in checksum_path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        match = re.fullmatch(r"([0-9a-f]{64})\s+(.+)", line.strip())
        if not match:
            raise QualificationError(f"invalid internal checksum entry: {line!r}")
        relative = safe_relative(match.group(2))
        if relative == "SHA256SUMS" or relative in entries:
            raise QualificationError(f"invalid duplicate/internal checksum path: {relative!r}")
        entries[relative] = match.group(1)
    files: dict[str, str] = {}
    for path in package_root.rglob("*"):
        if path.is_symlink():
            raise QualificationError(f"extracted package contains a symlink: {path.relative_to(package_root)}")
        if path.is_file() and path.name != "SHA256SUMS":
            relative = path.relative_to(package_root).as_posix()
            files[relative] = sha256_file(path)
    if set(entries) != set(files):
        missing = sorted(set(files) - set(entries))
        extra = sorted(set(entries) - set(files))
        raise QualificationError(f"internal checksum manifest does not cover exact files (missing={missing}, extra={extra})")
    mismatches = sorted(path for path, digest in entries.items() if files[path] != digest)
    if mismatches:
        raise QualificationError(f"internal checksum mismatch: {mismatches}")
    return {"fileCount": len(files), "verified": True, "files": sorted(files)}


def load_json(path: Path, label: str) -> dict[str, Any]:
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise QualificationError(f"read {label}: {exc}") from exc
    if not isinstance(document, dict):
        raise QualificationError(f"{label} must be a JSON object")
    return document


def require_exact_keys(document: dict[str, Any], required: set[str], label: str) -> None:
    missing = sorted(required - set(document))
    extra = sorted(set(document) - required)
    if missing or extra:
        raise QualificationError(f"{label} keys differ (missing={missing}, extra={extra})")


def verify_manifests(package_root: Path, package_name: str, archive_digest: str) -> dict[str, Any]:
    match = re.fullmatch(r"leapview-cli-(v[^/]+)-(linux|darwin)-(amd64|arm64)", package_name)
    if not match:
        raise QualificationError("cannot derive release identity from package name")
    release_tag, target_os, target_arch = match.groups()
    required_files = {
        "INSTALL.md",
        "SHA256SUMS",
        "authoring-package.json",
        "authoring-package.schema.json",
        "image-reference.txt",
        "leapview",
        "local-runtime/README.md",
        "local-runtime/compose.yaml",
        "local-runtime/postgres-init.sh",
        "local-runtime/runtime-package.json",
        "local-runtime/runtime-package.schema.json",
        "release-identity.json",
    }
    missing_files = sorted(relative for relative in required_files if not (package_root / relative).is_file())
    if missing_files:
        raise QualificationError(f"authoring archive is missing required package files: {missing_files}")
    authoring = load_json(package_root / "authoring-package.json", "authoring-package.json")
    require_exact_keys(authoring, {"schemaVersion", "product", "identity", "host", "applicationImage"}, "authoring manifest")
    if authoring["schemaVersion"] != 1 or authoring["product"] != "leapview":
        raise QualificationError("authoring manifest schema/product identity is invalid")
    identity = authoring["identity"]
    host = authoring["host"]
    if not isinstance(identity, dict) or not isinstance(host, dict):
        raise QualificationError("authoring manifest identity/host must be objects")
    require_exact_keys(identity, {"version", "revision", "buildTime", "dirty", "development"}, "authoring identity")
    require_exact_keys(host, {"os", "architecture", "supportProfile"}, "authoring host")
    if identity["version"] != release_tag[1:] or not re.fullmatch(r"[0-9a-f]{40}", str(identity["revision"])):
        raise QualificationError("authoring manifest release tag/revision is invalid")
    if identity["dirty"] is not False or identity["development"] is not False:
        raise QualificationError("released authoring package must record dirty=false and development=false")
    try:
        dt.datetime.fromisoformat(str(identity["buildTime"]).replace("Z", "+00:00"))
    except ValueError as exc:
        raise QualificationError("authoring package buildTime is not an ISO-8601 timestamp") from exc
    expected_profile = "ubuntu-24.04-docker-engine" if target_os == "linux" else "macos-15-docker-desktop"
    if host != {"os": target_os, "architecture": target_arch, "supportProfile": expected_profile}:
        raise QualificationError("authoring manifest host identity does not match its archive name")
    image = authoring["applicationImage"]
    if not isinstance(image, str) or not re.fullmatch(r"[^@\s]+@sha256:[0-9a-f]{64}", image):
        raise QualificationError("authoring manifest image is not an immutable digest reference")

    release = load_json(package_root / "release-identity.json", "release-identity.json")
    require_exact_keys(release, {"version", "revision", "buildTime", "dirty", "development", "image"}, "release identity")
    for field in ("version", "revision", "buildTime", "dirty", "development"):
        if release[field] != identity[field]:
            raise QualificationError(f"release identity {field} disagrees with authoring manifest")
    if release["image"] != image:
        raise QualificationError("release identity image disagrees with authoring manifest")
    if (package_root / "image-reference.txt").read_text(encoding="utf-8").strip() != image:
        raise QualificationError("image-reference.txt disagrees with package manifest")

    runtime = load_json(package_root / "local-runtime" / "runtime-package.json", "runtime-package.json")
    require_exact_keys(runtime, {"schemaVersion", "persistentStateSchemaVersion", "composeMinimumVersion", "leapview", "postgres"}, "runtime manifest")
    if runtime["schemaVersion"] != 1 or runtime["persistentStateSchemaVersion"] != 1 or runtime["composeMinimumVersion"] != "2.17.0":
        raise QualificationError("runtime manifest schema/version is unsupported")
    runtime_identity = runtime["leapview"]
    if not isinstance(runtime_identity, dict) or runtime_identity != {"version": identity["version"], "revision": identity["revision"], "image": image}:
        raise QualificationError("runtime manifest does not identify the same release")
    postgres = runtime["postgres"]
    expected_postgres = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"
    if postgres != {"major": 18, "image": expected_postgres}:
        raise QualificationError("runtime manifest PostgreSQL identity is not the pinned release contract")
    return {
        "archiveSha256": "sha256:" + archive_digest,
        "package": package_name,
        "releaseTag": release_tag,
        "host": host,
        "identity": identity,
        "applicationImage": image,
        "runtime": runtime,
    }


def command_environment(docker_host: Optional[str]) -> dict[str, str]:
    blocked = re.compile(r"(?i)(password|token|secret|credential|cookie|authorization)")
    environment = {key: value for key, value in os.environ.items() if not blocked.search(key)}
    for key in ("DOCKER_CONTEXT", "DOCKER_HOST", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "LEAPVIEW_TARGET"):
        environment.pop(key, None)
    if docker_host:
        environment["DOCKER_HOST"] = docker_host
    return environment


def run_command(
    name: str,
    command: list[str],
    raw_results: list[dict[str, Any]],
    timeout: int,
    cwd: Optional[Path] = None,
    docker_host: Optional[str] = None,
    check: bool = True,
) -> dict[str, Any]:
    started = time.monotonic()
    result: dict[str, Any] = {"name": name, "command": command_display(command), "status": "failed"}
    try:
        completed = subprocess.run(
            command,
            cwd=str(cwd) if cwd else None,
            env=command_environment(docker_host),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            timeout=timeout,
            check=False,
        )
        result["exitCode"] = completed.returncode
        result["output"] = redact(completed.stdout)
        result["status"] = "passed" if completed.returncode == 0 else "failed"
    except FileNotFoundError as exc:
        result["exitCode"] = None
        result["output"] = redact(str(exc))
    except subprocess.TimeoutExpired as exc:
        result["exitCode"] = None
        result["output"] = redact((exc.stdout or "") if isinstance(exc.stdout, str) else "[command timed out]")
        result["status"] = "timed-out"
    result["durationMs"] = round((time.monotonic() - started) * 1000, 3)
    raw_results.append(result)
    if check and result["status"] != "passed":
        raise QualificationError(f"{name} failed; see bounded raw result")
    return result


def host_identity() -> dict[str, Any]:
    system = platform.system()
    machine = platform.machine()
    target = SUPPORTED_HOSTS.get((system, machine))
    return {
        "name": system,
        "release": platform.release(),
        "machine": machine,
        "architecture": target[1] if target else machine,
        "supportedTarget": {"os": target[0], "architecture": target[1]} if target else None,
    }


def hardware_metadata() -> dict[str, Any]:
    memory_bytes: Optional[int] = None
    try:
        if Path("/proc/meminfo").is_file():
            match = re.search(r"^MemTotal:\s+(\d+)\s+kB$", Path("/proc/meminfo").read_text(encoding="utf-8"), re.MULTILINE)
            if match:
                memory_bytes = int(match.group(1)) * 1024
        elif platform.system() == "Darwin":
            output = subprocess.check_output(["sysctl", "-n", "hw.memsize"], text=True, timeout=5)
            memory_bytes = int(output.strip())
    except (OSError, ValueError, subprocess.SubprocessError):
        pass
    return {"logicalCpuCount": os.cpu_count(), "memoryBytes": memory_bytes}


def redacted_docker_host(value: str) -> str:
    if value.startswith("unix://"):
        return "unix://<local-socket>"
    if value.startswith("/"):
        return "unix://<local-socket>"
    return "<redacted-endpoint>"


def normalize_docker_host(value: str) -> str:
    value = value.strip()
    if value.startswith("unix://"):
        socket_path = value[7:]
    elif value.startswith("/"):
        socket_path = value
        value = "unix://" + value
    else:
        raise QualificationSkip("lifecycle requires an explicit local Unix Docker endpoint; SSH and TCP endpoints are unsupported")
    try:
        info = os.stat(socket_path)
    except OSError as exc:
        raise QualificationSkip(f"explicit Docker socket is unavailable: {redacted_docker_host(value)} ({exc.strerror})") from exc
    if not stat.S_ISSOCK(info.st_mode):
        raise QualificationSkip("explicit Docker endpoint is not a Unix socket")
    return value


def docker_metadata(docker_host: str, raw_results: list[dict[str, Any]], timeout: int) -> dict[str, Any]:
    docker = shutil.which("docker")
    if docker is None:
        raise QualificationSkip("Docker CLI is not installed")
    version = run_command("docker-version", [docker, "--host", docker_host, "version", "--format", "{{json .Server}}"], raw_results, timeout, docker_host=docker_host)
    compose = run_command("docker-compose-version", [docker, "--host", docker_host, "compose", "version", "--short"], raw_results, timeout, docker_host=docker_host)
    server: dict[str, Any] = {}
    try:
        parsed = json.loads(version.get("output", ""))
        if isinstance(parsed, dict):
            server = {key: parsed.get(key) for key in ("ID", "ServerVersion", "OperatingSystem", "Architecture", "Version", "ApiVersion", "Os", "Arch") if parsed.get(key) is not None}
    except json.JSONDecodeError:
        raise QualificationError("Docker version did not return JSON server metadata")
    if not server:
        raise QualificationError("Docker version did not provide server identity metadata")
    compose_version = compose.get("output", "").strip().splitlines()[-1] if compose.get("output") else ""
    if not compose_version:
        raise QualificationError("Docker Compose did not return a version")
    match = re.search(r"(?:^|\s)v?(\d+)\.(\d+)\.(\d+)(?:[-+\s]|$)", compose_version)
    if not match:
        raise QualificationError("Docker Compose returned an unrecognized version")
    if tuple(int(part) for part in match.groups()) < (2, 17, 0):
        raise QualificationError("Docker Compose 2.17.0 or newer is required")
    return {
        "cli": Path(docker).name,
        "endpoint": redacted_docker_host(docker_host),
        "transport": "unix",
        "server": server,
        "composeVersion": compose_version,
        "endpointPinned": True,
    }


def fixture_metadata(checkout: Path) -> dict[str, Any]:
    files: list[str] = []
    total_bytes = 0
    digest = hashlib.sha256()
    for path in sorted(checkout.rglob("*")):
        if path.is_symlink() or not path.is_file():
            continue
        relative = path.relative_to(checkout).as_posix()
        if relative.startswith(".git/"):
            continue
        data = path.read_bytes()
        files.append(relative)
        total_bytes += len(data)
        digest.update(relative.encode("utf-8"))
        digest.update(b"\0")
        digest.update(data)
    return {"name": "init-sample", "checkout": "temporary", "fileCount": len(files), "bytes": total_bytes, "digest": "sha256:" + digest.hexdigest(), "files": files}


def empty_measurement(reason: str, planned: int = 0) -> dict[str, Any]:
    return {"status": "not-run", "planned": planned, "executed": 0, "samplesMs": [], "reason": reason}


def qualification_document(required: bool, started_at: str) -> dict[str, Any]:
    return {
        "schemaVersion": CONTRACT_VERSION,
        "evidenceKind": "released-authoring-package-qualification",
        "qualification": "milestone-5",
        "result": "running",
        "required": required,
        "startedAt": started_at,
        "finishedAt": None,
        "metadata": {
            "os": host_identity(),
            "toolchain": {"python": platform.python_version(), "tar": "python-tarfile", "checksum": "python-sha256"},
            "docker": None,
            "compose": None,
            "hardware": hardware_metadata(),
            "fixture": None,
            "network": {"mode": "not-measured", "endpoint": None, "conditions": None},
            "warmup": {"status": "not-run", "requested": 1, "completed": 0, "samplesMs": [], "reason": PREVIEW_NOT_RELEASED},
            "repetitions": {name: empty_measurement(PREVIEW_NOT_RELEASED, 3 if name != "editToVisible" else 5) for name in REPETITIONS},
        },
        "package": None,
        "scenarios": {name: empty_measurement(PREVIEW_NOT_RELEASED, 1) for name in SCENARIOS},
        "rawResults": [],
        "skipped": [],
        "failures": [],
    }


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Verify and optionally exercise a released LeapView authoring package")
    parser.add_argument("--archive", required=True, help="released leapview-cli .tar.gz archive")
    parser.add_argument("--checksum", help="adjacent .sha256 file (defaults to --archive.sha256)")
    parser.add_argument("--evidence-dir", default="qualification-evidence", help="bounded evidence output directory")
    parser.add_argument("--run-lifecycle", action="store_true", help="run init and one local dev lifecycle against an explicit Docker socket")
    parser.add_argument("--required", action="store_true", help="fail instead of skipping missing platform/manual prerequisites")
    parser.add_argument("--manual-prerequisites-confirmed", action="store_true", help="confirm a human can complete local device authentication")
    parser.add_argument("--docker-host", help="explicit local Unix Docker endpoint; never inferred for lifecycle")
    parser.add_argument("--timeout-seconds", type=int, default=DEFAULT_TIMEOUT_SECONDS)
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    required = args.required or os.environ.get("LEAPVIEW_QUALIFICATION_REQUIRED") == "1"
    run_lifecycle = args.run_lifecycle or os.environ.get("LEAPVIEW_QUALIFICATION_RUN_LIFECYCLE") == "1"
    manual_confirmed = args.manual_prerequisites_confirmed or os.environ.get("LEAPVIEW_QUALIFICATION_MANUAL_PREREQUISITES") == "1"
    if args.timeout_seconds < 1 or args.timeout_seconds > 1_800:
        raise SystemExit("--timeout-seconds must be between 1 and 1800")

    evidence_dir = Path(args.evidence_dir).resolve()
    evidence_dir.mkdir(parents=True, exist_ok=True)
    os.chmod(evidence_dir, 0o700)
    started_at = now_iso()
    evidence = qualification_document(required, started_at)
    raw_results: list[dict[str, Any]] = evidence["rawResults"]
    workdir: Optional[Path] = None
    try:
        archive = Path(args.archive).resolve()
        if not archive.is_file() or archive.is_symlink():
            raise QualificationError("--archive must identify a regular file")
        checksum = Path(args.checksum).resolve() if args.checksum else Path(str(archive) + ".sha256")
        if not checksum.is_file() or checksum.is_symlink():
            raise QualificationError(f"checksum file is missing: {checksum.name}")
        expected_digest = parse_outer_checksum(checksum, archive)
        actual_digest = sha256_file(archive)
        if actual_digest != expected_digest:
            raise QualificationError("outer archive checksum mismatch")
        workdir = Path(tempfile.mkdtemp(prefix="leapview-package-qualification-"))
        package_root, _ = extract_archive(archive, workdir)
        checksum_metadata = verify_internal_checksums(package_root)
        package_metadata = verify_manifests(package_root, package_root.name, actual_digest)
        package_metadata["manifest"] = checksum_metadata
        evidence["package"] = package_metadata

        host = host_identity()
        expected_host = package_metadata["host"]
        if host["supportedTarget"] != {"os": expected_host["os"], "architecture": expected_host["architecture"]}:
            reason = "archive target does not match the executing supported host"
            if required:
                raise QualificationError(reason)
            evidence["skipped"].append(reason)
            evidence["result"] = "skipped"
        else:
            binary = package_root / "leapview"
            if not binary.is_file() or not os.access(binary, os.X_OK):
                raise QualificationError("authoring package command is missing or not executable")
            version_result = run_command("cli-version", [str(binary), "version", "--json"], raw_results, args.timeout_seconds)
            try:
                actual_identity = json.loads(version_result["output"])
            except json.JSONDecodeError as exc:
                raise QualificationError("authoring CLI version --json did not return JSON") from exc
            for field in ("version", "revision", "buildTime", "dirty", "development"):
                if actual_identity.get(field) != package_metadata["identity"][field]:
                    raise QualificationError(f"authoring CLI identity mismatch in {field}")
            evidence["package"]["cliIdentity"] = {field: actual_identity.get(field) for field in ("version", "revision", "buildTime", "dirty", "development")}
            for command_name in ("init", "dev", "plan", "build", "publish", "deploy"):
                run_command(f"cli-help-{command_name}", [str(binary), command_name, "--help"], raw_results, args.timeout_seconds)

            if run_lifecycle:
                if not manual_confirmed:
                    raise QualificationSkip("local lifecycle requires --manual-prerequisites-confirmed because device authentication is interactive")
                raw_host = args.docker_host or os.environ.get("LEAPVIEW_QUALIFICATION_DOCKER_HOST", "")
                if not raw_host:
                    raise QualificationSkip("local lifecycle requires an explicit --docker-host Unix socket")
                docker_host = normalize_docker_host(raw_host)
                evidence["metadata"]["docker"] = docker_metadata(docker_host, raw_results, args.timeout_seconds)
                evidence["metadata"]["compose"] = {"version": evidence["metadata"]["docker"]["composeVersion"], "minimum": "2.17.0", "satisfied": True}
                evidence["metadata"]["network"] = {"mode": "docker-local-endpoint", "endpoint": redacted_docker_host(docker_host), "conditions": "not measured"}
                checkout_parent = Path(tempfile.mkdtemp(prefix="leapview-authoring-checkout-", dir=workdir))
                checkout = checkout_parent / "sample"
                try:
                    run_command("init", [str(binary), "init", str(checkout)], raw_results, args.timeout_seconds, cwd=checkout_parent)
                    evidence["metadata"]["fixture"] = fixture_metadata(checkout)
                    run_command("dev-once", [str(binary), "dev", "--once", "--no-browser", "--docker-host", docker_host], raw_results, args.timeout_seconds, cwd=checkout, docker_host=docker_host)
                    evidence["metadata"]["warmup"] = {"status": "not-run", "requested": 1, "completed": 0, "samplesMs": [], "reason": PREVIEW_NOT_RELEASED}
                finally:
                    # Reset is intentionally bounded and recorded. If dev
                    # fails after creating state, cleanup still uses the
                    # exact confirmation for this temporary checkout.
                    plan = run_command("cleanup-reset-plan", [str(binary), "dev", "reset", "--docker-host", docker_host], raw_results, args.timeout_seconds, cwd=checkout, docker_host=docker_host, check=False)
                    if evidence["metadata"]["fixture"] is not None and plan["status"] != "passed":
                        raise QualificationError("cleanup reset plan failed; the temporary local runtime could not be proven owned")
                    confirmation = re.search(r"Exact confirmation:\s*(sha256:[0-9a-f]{64})", plan.get("output", ""))
                    if not confirmation:
                        raise QualificationError("cleanup reset plan did not return an exact ownership confirmation; the temporary runtime was retained")
                    run_command("cleanup-reset", [str(binary), "dev", "reset", "--docker-host", docker_host, "--confirm", confirmation.group(1)], raw_results, args.timeout_seconds, cwd=checkout, docker_host=docker_host)
    except QualificationSkip as exc:
        evidence["skipped"].append(str(exc))
        evidence["result"] = "skipped"
    except QualificationError as exc:
        evidence["failures"].append(str(exc))
        evidence["result"] = "failed"
    except Exception as exc:  # Keep evidence durable for unexpected harness failures.
        evidence["failures"].append(f"unexpected harness failure: {exc}")
        evidence["result"] = "failed"
    finally:
        evidence["rawResults"] = raw_results
        evidence["finishedAt"] = now_iso()
        if evidence["result"] == "running":
            evidence["result"] = "passed"
        write_json(evidence_dir / "qualification-report.json", evidence)
        write_json(evidence_dir / "raw-results.json", {"schemaVersion": CONTRACT_VERSION, "evidenceKind": evidence["evidenceKind"], "results": raw_results})
        if workdir is not None:
            shutil.rmtree(workdir, ignore_errors=True)
        print(f"released authoring package qualification: {evidence['result']} (evidence: {evidence_dir / 'qualification-report.json'})")
        for reason in evidence["skipped"]:
            print(f"skipped: {reason}", file=sys.stderr)
        for failure in evidence["failures"]:
            print(f"failure: {failure}", file=sys.stderr)
    if evidence["result"] == "failed":
        return 1
    if evidence["result"] == "skipped" and required:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))

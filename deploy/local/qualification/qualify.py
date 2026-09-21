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
import threading
import time
from pathlib import Path
from typing import Any, Optional


CONTRACT_VERSION = 1
MAX_OUTPUT = 4_000
MAX_MEMBERS = 2_000
# Released native binaries currently include the complete application and
# authoring surfaces. Keep extraction bounded while allowing the package built
# by the release workflow (about 166 MiB uncompressed on linux-amd64).
MAX_FILE_BYTES = 256 * 1024 * 1024
DEFAULT_TIMEOUT_SECONDS = 300
SUPPORTED_HOSTS = {
    ("Linux", "x86_64"): ("linux", "amd64"),
    ("Linux", "amd64"): ("linux", "amd64"),
    ("Linux", "aarch64"): ("linux", "arm64"),
    ("Linux", "arm64"): ("linux", "arm64"),
    ("Darwin", "x86_64"): ("darwin", "amd64"),
    ("Darwin", "arm64"): ("darwin", "arm64"),
}
PACKAGE_NAME_PATTERN = re.compile(
    r"leapview-cli-(v[0-9A-Za-z][0-9A-Za-z._+-]*|candidate-[1-9][0-9]*-[1-9][0-9]*)-(linux|darwin)-(amd64|arm64)"
)
VERSION_PATTERN = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?")
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
# The qualification subprocesses are intentionally given a small, explicit
# process environment.  In particular, Docker context/configuration and
# arbitrary LEAPVIEW_* variables are not inherited from the operator.  The
# lifecycle passes its already-validated endpoint explicitly below.
COMMAND_ENV_ALLOWLIST = (
    "LANG",
    "LC_ALL",
    "PATH",
    "TEMP",
    "TMP",
    "TMPDIR",
)
SECRET_KEY_PATTERN = (
    r"(?:password|passwd|passphrase|token|secret|credential(?:s)?|cookie|"
    r"authorization|api[_-]?key|access[_-]?(?:key|token|secret)|"
    r"client[_-]?secret|private[_-]?key|connection[_-]?string|"
    r"refresh[_-]?token|id[_-]?token)"
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
    # Python and CLI diagnostics commonly render credential maps with either
    # JSON double quotes or repr-style single quotes.  Redact a complete flat
    # credentials/secrets map first, then individual nested pairs below.
    value = redact_quoted_credential_maps(value)
    quoted_pair = re.compile(
        rf'''(?ix)(?P<prefix>["']?[A-Za-z0-9_.-]*{SECRET_KEY_PATTERN}[A-Za-z0-9_.-]*["']?\s*[:=]\s*)
            (?P<quote>["'])(?P<secret>(?:\\.|(?! (?P=quote) ).)*)(?P=quote)'''
    )
    value = quoted_pair.sub(lambda match: match.group("prefix") + match.group("quote") + "<redacted>" + match.group("quote"), value)
    value = re.sub(
        rf"(?i)(\b[A-Za-z0-9_.-]*{SECRET_KEY_PATTERN}[A-Za-z0-9_.-]*\s*:\s*)(?:[^\s,;}}]+|\"[^\"]*\"|'[^']*')",
        r"\1<redacted>",
        value,
    )
    value = re.sub(rf"(?i)([A-Za-z0-9_.-]*{SECRET_KEY_PATTERN}[A-Za-z0-9_.-]*)\s*[:=]\s*(?:\"[^\"]*\"|'[^']*'|[^\s,;}}]+)", r"\1=<redacted>", value)
    # Cover URL user-info and query/fragment credentials, including quoted
    # URLs and values containing percent-encoding.
    value = re.sub(r"(?i)(://)([^/@\s]+)@", r"\1<redacted>@", value)
    value = re.sub(
        rf"(?i)([?&][A-Za-z0-9_.-]*{SECRET_KEY_PATTERN}[A-Za-z0-9_.-]*=)([^&#\s,}}'\"]+)",
        r"\1<redacted>",
        value,
    )
    value = re.sub(rf"(?i)(LEAPVIEW_[A-Z0-9_]*{SECRET_KEY_PATTERN}[A-Z0-9_]*=)(?:\"[^\"]*\"|'[^']*'|[^\s]+)", r"\1<redacted>", value)
    if len(value) > MAX_OUTPUT:
        return value[:MAX_OUTPUT] + "\n[output truncated]"
    return value


def redact_quoted_credential_maps(value: str) -> str:
    """Replace complete quoted ``credentials``/``secrets`` object values.

    A small scanner is used instead of a greedy regular expression so nested
    maps and braces inside quoted values cannot leave a credential tail in the
    retained diagnostic.
    """

    prefix = re.compile(r'''(?ix)(["']?(?:credentials?|credential[_-]?map|secrets?)["']?\s*:\s*)\{''')
    cursor = 0
    pieces: list[str] = []
    while True:
        match = prefix.search(value, cursor)
        if match is None:
            pieces.append(value[cursor:])
            break
        pieces.append(value[cursor:match.start()])
        depth = 0
        quote: Optional[str] = None
        escaped = False
        end = match.end() - 1
        for index in range(end, len(value)):
            char = value[index]
            if quote is not None:
                if escaped:
                    escaped = False
                elif char == "\\":
                    escaped = True
                elif char == quote:
                    quote = None
                continue
            if char in ("'", '"'):
                quote = char
            elif char == "{":
                depth += 1
            elif char == "}":
                depth -= 1
                if depth == 0:
                    end = index + 1
                    break
        else:
            # An unterminated diagnostic map is still secret-bearing; redact
            # the remainder rather than retaining a partial value.
            end = len(value)
        pieces.append(match.group(1) + '"<redacted>"')
        cursor = end
    return "".join(pieces)


def command_display(command: list[str]) -> str:
    return redact(" ".join(command))


def write_json(path: Path, document: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.chmod(path, 0o600)


def redact_document(value: Any) -> Any:
    """Apply output redaction to every retained report/diagnostic string."""

    if isinstance(value, str):
        return redact(value)
    if isinstance(value, list):
        return [redact_document(item) for item in value]
    if isinstance(value, dict):
        return {key: redact_document(item) for key, item in value.items()}
    return value


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
        expected = PACKAGE_NAME_PATTERN.fullmatch(package_name)
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


PINNED_POSTGRES_IMAGE = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"


def validate_authoring_schema(schema: dict[str, Any]) -> None:
    """Validate the package's copied schema without requiring source files."""

    require_exact_keys(schema, {"$schema", "$id", "title", "type", "additionalProperties", "required", "properties", "allOf"}, "authoring schema")
    if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" or schema["$id"] != "https://leapview.flid.ai/schemas/authoring-package-v1.json":
        raise QualificationError("authoring schema identity is invalid")
    if schema["type"] != "object" or schema["additionalProperties"] is not False:
        raise QualificationError("authoring schema must be a closed object")
    if schema["required"] != ["schemaVersion", "product", "identity", "host", "applicationImage"]:
        raise QualificationError("authoring schema required fields differ from the package contract")
    properties = schema["properties"]
    if not isinstance(properties, dict) or set(properties) != {"schemaVersion", "product", "identity", "host", "applicationImage"}:
        raise QualificationError("authoring schema properties differ from the package contract")
    if properties["schemaVersion"] != {"const": 1} or properties["product"] != {"const": "leapview"}:
        raise QualificationError("authoring schema schemaVersion/product constraints are invalid")
    identity = properties["identity"]
    host = properties["host"]
    if not isinstance(identity, dict) or identity.get("type") != "object" or identity.get("additionalProperties") is not False:
        raise QualificationError("authoring identity schema is not a closed object")
    if identity.get("required") != ["version", "revision", "buildTime", "dirty", "development"]:
        raise QualificationError("authoring identity schema required fields differ")
    if set(identity.get("properties", {})) != {"version", "revision", "buildTime", "dirty", "development"}:
        raise QualificationError("authoring identity schema properties differ")
    if identity["properties"].get("revision", {}).get("pattern") != r"^[0-9a-f]{40}$" or identity["properties"].get("dirty") != {"const": False}:
        raise QualificationError("authoring identity schema release constraints are invalid")
    if not isinstance(host, dict) or host.get("type") != "object" or host.get("additionalProperties") is not False:
        raise QualificationError("authoring host schema is not a closed object")
    if host.get("required") != ["os", "architecture", "supportProfile"]:
        raise QualificationError("authoring host schema required fields differ")
    host_properties = host.get("properties", {})
    if set(host_properties) != {"os", "architecture", "supportProfile"} or host_properties.get("os", {}).get("enum") != ["linux", "darwin"] or host_properties.get("architecture", {}).get("enum") != ["amd64", "arm64"]:
        raise QualificationError("authoring host schema platform constraints are invalid")
    image = properties["applicationImage"]
    if not isinstance(image, dict) or image.get("type") != "string" or image.get("pattern") != r"^[^@\s]+@sha256:[0-9a-f]{64}$":
        raise QualificationError("authoring image schema does not require an immutable digest")
    all_of = schema["allOf"]
    if not isinstance(all_of, list) or len(all_of) != 2:
        raise QualificationError("authoring schema must retain OS/profile compatibility constraints")


def validate_runtime_schema(schema: dict[str, Any]) -> None:
    """Validate the copied runtime schema's exact closed-contract shape."""

    require_exact_keys(schema, {"$schema", "$id", "title", "type", "additionalProperties", "required", "properties", "$defs"}, "runtime schema")
    if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" or schema["$id"] != "https://leapview.flid.ai/schemas/local-runtime-package-v1.json":
        raise QualificationError("runtime schema identity is invalid")
    if schema["type"] != "object" or schema["additionalProperties"] is not False:
        raise QualificationError("runtime schema must be a closed object")
    expected_required = ["schemaVersion", "persistentStateSchemaVersion", "composeMinimumVersion", "leapview", "postgres"]
    if schema["required"] != expected_required or set(schema["properties"]) != set(expected_required):
        raise QualificationError("runtime schema required fields differ from the package contract")
    properties = schema["properties"]
    if properties["schemaVersion"] != {"const": 1} or properties["persistentStateSchemaVersion"] != {"const": 1} or properties["composeMinimumVersion"] != {"const": "2.17.0"}:
        raise QualificationError("runtime schema version constraints are invalid")
    if properties["leapview"] != {"$ref": "#/$defs/leapview"} or properties["postgres"] != {"$ref": "#/$defs/postgres"}:
        raise QualificationError("runtime schema identity references are invalid")
    definitions = schema["$defs"]
    if not isinstance(definitions, dict) or set(definitions) != {"leapview", "postgres"}:
        raise QualificationError("runtime schema definitions differ from the package contract")
    leapview = definitions["leapview"]
    if not isinstance(leapview, dict) or leapview.get("type") != "object" or leapview.get("additionalProperties") is not False or leapview.get("required") != ["version", "revision", "image"]:
        raise QualificationError("runtime LeapView identity schema is invalid")
    if set(leapview.get("properties", {})) != {"version", "revision", "image"} or leapview["properties"].get("revision", {}).get("pattern") != r"^[0-9a-f]{40}$" or leapview["properties"].get("image", {}).get("pattern") != r"^[^@\s]+@sha256:[0-9a-f]{64}$":
        raise QualificationError("runtime LeapView identity constraints are invalid")
    postgres = definitions["postgres"]
    if not isinstance(postgres, dict) or postgres.get("type") != "object" or postgres.get("additionalProperties") is not False or postgres.get("required") != ["major", "image"]:
        raise QualificationError("runtime PostgreSQL schema is invalid")
    if postgres.get("properties", {}).get("major") != {"const": 18} or postgres.get("properties", {}).get("image") != {"const": PINNED_POSTGRES_IMAGE}:
        raise QualificationError("runtime PostgreSQL schema is not the pinned release contract")


def compose_section(document: str, section: str) -> str:
    """Return one top-level YAML section for the deliberately fixed payload."""

    lines = document.splitlines()
    start: Optional[int] = None
    end = len(lines)
    for index, line in enumerate(lines):
        if line and not line[0].isspace() and re.fullmatch(r"[A-Za-z0-9_-]+:", line.strip()):
            name = line.strip()[:-1]
            if name == section:
                start = index + 1
            elif start is not None:
                end = index
                break
    if start is None:
        raise QualificationError(f"runtime compose file is missing top-level {section!r} section")
    return "\n".join(lines[start:end])


def compose_service_block(services: str, service: str) -> str:
    lines = services.splitlines()
    start: Optional[int] = None
    end = len(lines)
    for index, line in enumerate(lines):
        if re.fullmatch(r"  [A-Za-z0-9_-]+:\s*", line):
            name = line.strip()[:-1]
            if name == service:
                start = index + 1
            elif start is not None:
                end = index
                break
    if start is None:
        raise QualificationError(f"runtime compose is missing service {service!r}")
    return "\n".join(lines[start:end])


def validate_compose_payload(path: Path) -> dict[str, Any]:
    """Check the extracted runtime Compose shape independently of the checkout."""

    try:
        document = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise QualificationError(f"read runtime compose file: {exc}") from exc
    # PyYAML is optional: released qualification remains runnable with the
    # Python standard library alone, while environments that provide a YAML
    # parser get exact map/key validation in addition to the bounded checks
    # below.
    try:
        import yaml  # type: ignore[import-not-found]
    except ImportError:
        yaml = None
    if yaml is not None:
        try:
            parsed = yaml.safe_load(document)
        except yaml.YAMLError as exc:
            raise QualificationError(f"runtime compose YAML is invalid: {exc}") from exc
        if not isinstance(parsed, dict) or set(parsed) != {"services", "volumes", "networks"}:
            raise QualificationError("runtime compose document keys differ from the exact package contract")
        if not isinstance(parsed.get("services"), dict) or set(parsed["services"]) != {"postgres", "leapview"}:
            raise QualificationError("runtime compose service map differs from the exact package contract")
        expected_service_keys = {
            "postgres": {"image", "restart", "command", "environment", "ports", "volumes", "labels", "healthcheck", "logging"},
            "leapview": {"image", "restart", "init", "network_mode", "depends_on", "command", "read_only", "cap_drop", "security_opt", "stop_grace_period", "environment", "env_file", "volumes", "labels"},
        }
        for service, expected_keys in expected_service_keys.items():
            if not isinstance(parsed["services"][service], dict) or set(parsed["services"][service]) != expected_keys:
                raise QualificationError(f"runtime compose {service} service keys differ from the exact package contract")
        if not isinstance(parsed.get("volumes"), dict) or set(parsed["volumes"]) != {"leapview-postgres", "leapview-runtime"}:
            raise QualificationError("runtime compose volume map differs from the exact package contract")
        if not isinstance(parsed.get("networks"), dict) or set(parsed["networks"]) != {"default"}:
            raise QualificationError("runtime compose network map differs from the exact package contract")
    sections = [line.strip()[:-1] for line in document.splitlines() if line and not line[0].isspace() and re.fullmatch(r"[A-Za-z0-9_-]+:", line.strip())]
    if sections != ["services", "volumes", "networks"]:
        raise QualificationError(f"runtime compose top-level sections differ: {sections}")
    services = compose_section(document, "services")
    service_names = [match.group(1) for match in re.finditer(r"^  ([A-Za-z0-9_-]+):\s*$", services, re.MULTILINE)]
    if service_names != ["postgres", "leapview"]:
        raise QualificationError(f"runtime compose services differ: {service_names}")
    postgres = compose_service_block(services, "postgres")
    leapview = compose_service_block(services, "leapview")
    if f"image: {PINNED_POSTGRES_IMAGE}" not in services:
        raise QualificationError("runtime compose PostgreSQL image is not pinned")
    if "command: [postgres, -c, listen_addresses=127.0.0.1]" not in postgres:
        raise QualificationError("runtime compose PostgreSQL must listen only on its private loopback")
    if '"127.0.0.1:${LEAPVIEW_LOCAL_APP_PORT:?local application port is required}:8080"' not in postgres:
        raise QualificationError("runtime compose must publish only the application port on host loopback")
    if "network_mode: service:postgres" not in leapview:
        raise QualificationError("runtime compose application must join PostgreSQL's network namespace")
    if "image: ${LEAPVIEW_IMAGE:?version-matched LeapView image digest is required}" not in leapview:
        raise QualificationError("runtime compose application image binding is not release-controlled")
    if "./postgres-init.sh:/docker-entrypoint-initdb.d/10-leapview-roles.sh:ro" not in postgres:
        raise QualificationError("runtime compose does not mount the canonical PostgreSQL initializer")
    if "LEAPVIEW_DEVELOPMENT_CREDENTIAL_ENV_FILE:?private selected development credential file is required" not in leapview:
        raise QualificationError("runtime compose does not require the selected private credential file")
    volumes = compose_section(document, "volumes")
    if [match.group(1) for match in re.finditer(r"^  ([A-Za-z0-9_-]+):\s*$", volumes, re.MULTILINE)] != ["leapview-postgres", "leapview-runtime"]:
        raise QualificationError("runtime compose volumes differ from the two-volume contract")
    networks = compose_section(document, "networks")
    if not re.search(r"^  default:\s*$", networks, re.MULTILINE):
        raise QualificationError("runtime compose default network is missing")
    if re.search(r"(?i)(sqlite|DOCKER_CONTEXT|DOCKER_HOST|LEAPVIEW_TARGET)", document):
        raise QualificationError("runtime compose contains a forbidden fallback or ambient-target setting")
    return {"sha256": "sha256:" + sha256_file(path), "services": service_names, "volumes": ["leapview-postgres", "leapview-runtime"]}


def validate_init_payload(path: Path) -> dict[str, Any]:
    """Validate mode and fixed role/database semantics of the packaged init."""

    try:
        mode = stat.S_IMODE(path.stat().st_mode)
        document = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise QualificationError(f"read packaged PostgreSQL initializer: {exc}") from exc
    if mode != 0o755:
        raise QualificationError(f"packaged PostgreSQL initializer mode is {mode:o}, want 755")
    required = (
        "#!/usr/bin/env bash",
        "set -euo pipefail",
        "CREATE ROLE leapview_control_owner",
        "CREATE ROLE leapview_ducklake_owner",
        "ensure_database leapview_control leapview_control_owner",
        "ensure_database leapview_ducklake leapview_ducklake_owner",
        "ALTER ROLE leapview_control_runtime PASSWORD",
        "ALTER ROLE leapview_ducklake_runtime PASSWORD",
        "REVOKE ALL ON DATABASE postgres FROM PUBLIC",
    )
    missing = [fragment for fragment in required if fragment not in document]
    if missing:
        raise QualificationError(f"packaged PostgreSQL initializer is missing required semantics: {missing}")
    if re.search(r"(?im)^\s*(?:echo|printf)\b.*(?:password|token|secret|credential)", document):
        raise QualificationError("packaged PostgreSQL initializer may not print credential material")
    return {"sha256": "sha256:" + sha256_file(path), "mode": mode}


def validate_packaged_readme(path: Path) -> dict[str, Any]:
    """Ensure docs copied into an archive remain useful outside the checkout."""

    try:
        document = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise QualificationError(f"read packaged README: {exc}") from exc
    # Only package-local links to files that are actually shipped are allowed;
    # repository-relative docs links break after extraction.
    for target in re.findall(r"\[[^]]+\]\(([^)]+)\)", document):
        if target.startswith(("../", "/docs/", "../../")):
            raise QualificationError(f"packaged README contains a checkout-relative link: {target}")
        if not re.match(r"^[a-z][a-z0-9+.-]*://", target) and not (path.parent / target).exists():
            raise QualificationError(f"packaged README link is not usable from the extracted archive: {target}")
    if "https://" not in document:
        raise QualificationError("packaged README must retain at least one absolute documentation URL")
    return {"sha256": "sha256:" + sha256_file(path)}


def verify_manifests(package_root: Path, package_name: str, archive_digest: str) -> dict[str, Any]:
    match = PACKAGE_NAME_PATTERN.fullmatch(package_name)
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
    version = str(identity["version"])
    tag_matches_version = release_tag.startswith("candidate-") or version == release_tag[1:]
    if not tag_matches_version or not VERSION_PATTERN.fullmatch(version) or not re.fullmatch(r"[0-9a-f]{40}", str(identity["revision"])):
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
    if postgres != {"major": 18, "image": PINNED_POSTGRES_IMAGE}:
        raise QualificationError("runtime manifest PostgreSQL identity is not the pinned release contract")
    validate_authoring_schema(load_json(package_root / "authoring-package.schema.json", "authoring-package.schema.json"))
    validate_runtime_schema(load_json(package_root / "local-runtime" / "runtime-package.schema.json", "runtime-package.schema.json"))
    validate_compose_payload(package_root / "local-runtime" / "compose.yaml")
    validate_init_payload(package_root / "local-runtime" / "postgres-init.sh")
    validate_packaged_readme(package_root / "local-runtime" / "README.md")
    return {
        "archiveSha256": "sha256:" + archive_digest,
        "package": package_name,
        "releaseTag": release_tag,
        "host": host,
        "identity": identity,
        "applicationImage": image,
        "runtime": runtime,
    }


def command_environment(docker_host: Optional[str], command_home: Path) -> dict[str, str]:
    environment = {
        key: os.environ[key]
        for key in COMMAND_ENV_ALLOWLIST
        if key in os.environ
    }
    # Released-artifact checks must not consult the operator's LeapView,
    # Docker, cloud, or desktop credential/configuration directories.  Give
    # every child a private empty home and XDG tree owned by this run.
    command_home.mkdir(mode=0o700, parents=True, exist_ok=True)
    xdg_config = command_home / ".config"
    xdg_cache = command_home / ".cache"
    xdg_data = command_home / ".local" / "share"
    for directory in (xdg_config, xdg_cache, xdg_data):
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    environment.update({
        "HOME": str(command_home),
        "XDG_CONFIG_HOME": str(xdg_config),
        "XDG_CACHE_HOME": str(xdg_cache),
        "XDG_DATA_HOME": str(xdg_data),
    })
    # An explicit endpoint is the only Docker targeting input allowed into a
    # child process.  DOCKER_CONTEXT, DOCKER_CONFIG, TLS credential paths, and
    # all other ambient Docker variables remain absent.
    if docker_host:
        environment["DOCKER_HOST"] = docker_host
    return environment


def run_command(
    name: str,
    command: list[str],
    raw_results: list[dict[str, Any]],
    timeout: int,
    command_home: Path,
    cwd: Optional[Path] = None,
    docker_host: Optional[str] = None,
    check: bool = True,
    live_output: bool = False,
) -> dict[str, Any]:
    started = time.monotonic()
    result: dict[str, Any] = {"name": name, "command": command_display(command), "status": "failed"}
    try:
        if live_output:
            process = subprocess.Popen(
                command,
                cwd=str(cwd) if cwd else None,
                env=command_environment(docker_host, command_home),
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                bufsize=1,
            )
            captured: list[str] = []

            def copy_output() -> None:
                assert process.stdout is not None
                for line in process.stdout:
                    captured.append(line)
                    print(redact(line), file=sys.stderr, end="", flush=True)

            reader = threading.Thread(target=copy_output, daemon=True)
            reader.start()
            try:
                return_code = process.wait(timeout=timeout)
                result["exitCode"] = return_code
                result["status"] = "passed" if return_code == 0 else "failed"
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                result["exitCode"] = None
                result["status"] = "timed-out"
            reader.join()
            result["output"] = redact("".join(captured))
        else:
            completed = subprocess.run(
                command,
                cwd=str(cwd) if cwd else None,
                env=command_environment(docker_host, command_home),
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


def require_command_help(command_name: str, result: dict[str, Any]) -> None:
    """Require recognizable help for this command, not merely exit status 0."""

    output = str(result.get("output", ""))
    lines = output.splitlines()
    usage_lines: list[str] = []
    for index, line in enumerate(lines):
        if not re.match(r"^\s*usage\s*:", line, re.IGNORECASE):
            continue
        usage_lines.append(line)
        if re.fullmatch(r"\s*usage\s*:\s*", line, re.IGNORECASE):
            for candidate in lines[index + 1:]:
                if not candidate.strip():
                    break
                usage_lines.append(candidate)
    command_pattern = re.compile(rf"\b(?:leapview(?:ctl)?|\S*leapview\S*)\s+{re.escape(command_name)}(?:\s|$|[\[<])", re.IGNORECASE)
    if not usage_lines or not any(command_pattern.search(line) for line in usage_lines):
        raise QualificationError(f"cli-help-{command_name} did not return recognizable command-specific help")


def require_happy_path_dev_output(name: str, result: dict[str, Any]) -> None:
    """Require the observable init -> dev contract, not only exit status 0."""

    output = str(result.get("output", ""))
    required = (
        "staging declared development input sample (sha256:",
        "staged sha256:",
        "synchronized sha256:",
        "session-preview http",
    )
    missing = [fragment for fragment in required if fragment not in output]
    if missing:
        raise QualificationError(f"{name} did not demonstrate the local authoring happy path; missing output {missing}")


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


def recognized_local_socket_paths() -> set[str]:
    """Return absolute Engine/Desktop socket paths accepted by this lane."""

    paths = {os.path.realpath("/var/run/docker.sock"), os.path.realpath("/run/docker.sock")}
    home = Path.home()
    if platform.system() == "Darwin":
        paths.update({os.path.realpath(str(home / ".docker/run/docker.sock")), os.path.realpath(str(home / ".docker/desktop/docker.sock"))})
    elif platform.system() == "Linux":
        paths.update({os.path.realpath(str(home / ".docker/run/docker.sock")), os.path.realpath(str(home / ".docker/desktop/docker.sock"))})
        runtime = os.environ.get("XDG_RUNTIME_DIR", "")
        if runtime == f"/run/user/{os.getuid()}":
            paths.add(os.path.realpath(str(Path(runtime) / "docker.sock")))
        paths.add(os.path.realpath(f"/run/user/{os.getuid()}/docker.sock"))
    return paths


def normalize_docker_host(value: str) -> str:
    value = value.strip()
    if value.startswith("unix://"):
        socket_path = value[7:]
    elif value.startswith("/"):
        socket_path = value
        value = "unix://" + value
    else:
        raise QualificationSkip("lifecycle requires an explicit local Unix Docker endpoint; SSH and TCP endpoints are unsupported")
    if not Path(socket_path).is_absolute():
        raise QualificationSkip("lifecycle requires an absolute local Docker socket path")
    if os.path.realpath(socket_path) not in recognized_local_socket_paths():
        raise QualificationSkip("Docker socket is not a recognized local Engine/Desktop endpoint")
    try:
        info = os.stat(socket_path)
    except OSError as exc:
        raise QualificationSkip(f"explicit Docker socket is unavailable: {redacted_docker_host(value)} ({exc.strerror})") from exc
    if not stat.S_ISSOCK(info.st_mode):
        raise QualificationSkip("explicit Docker endpoint is not a Unix socket")
    return "unix://" + os.path.realpath(socket_path)


def docker_server_identity(docker: str, docker_host: str, raw_results: list[dict[str, Any]], timeout: int, command_home: Path, name: str) -> dict[str, str]:
    version = run_command(name, [docker, "--host", docker_host, "version", "--format", "{{json .Server}}"], raw_results, timeout, command_home, docker_host=docker_host)
    try:
        parsed = json.loads(version.get("output", ""))
    except json.JSONDecodeError as exc:
        raise QualificationError(f"{name} did not return JSON server metadata") from exc
    if not isinstance(parsed, dict):
        raise QualificationError(f"{name} did not provide server identity metadata")
    server = {key: str(parsed[key]) for key in ("ID", "ServerVersion", "OperatingSystem", "Architecture", "Version", "ApiVersion", "Os", "Arch") if parsed.get(key) is not None}
    if not server:
        raise QualificationError(f"{name} did not provide server identity metadata")
    return server


def docker_metadata(docker_host: str, raw_results: list[dict[str, Any]], timeout: int, command_home: Path) -> dict[str, Any]:
    docker = shutil.which("docker")
    if docker is None:
        raise QualificationSkip("Docker CLI is not installed")
    server = docker_server_identity(docker, docker_host, raw_results, timeout, command_home, "docker-version-pre")
    compose = run_command("docker-compose-version", [docker, "--host", docker_host, "compose", "version", "--short"], raw_results, timeout, command_home, docker_host=docker_host)
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
        # One probe establishes only a candidate identity.  The lifecycle
        # upgrades this to verified-pre-post after init and before dev.
        "endpointPinned": False,
        "pinState": "not-proven",
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
        "qualification": "milestone-5-static",
        "scope": "static",
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
        command_home = workdir / "command-home"
        package_root, archive_members = extract_archive(archive, workdir)
        checksum_metadata = verify_internal_checksums(package_root)
        expected_files = {
            "INSTALL.md",
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
        if set(checksum_metadata["files"]) != expected_files:
            missing = sorted(expected_files - set(checksum_metadata["files"]))
            extra = sorted(set(checksum_metadata["files"]) - expected_files)
            raise QualificationError(f"authoring archive files differ from the exact package contract (missing={missing}, extra={extra})")
        expected_members = {package_root.name, package_root.name + "/local-runtime", package_root.name + "/SHA256SUMS"}
        expected_members.update(package_root.name + "/" + relative for relative in expected_files)
        if set(archive_members) != expected_members:
            missing = sorted(expected_members - set(archive_members))
            extra = sorted(set(archive_members) - expected_members)
            raise QualificationError(f"authoring archive members differ from the exact package contract (missing={missing}, extra={extra})")
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
            version_result = run_command("cli-version", [str(binary), "version", "--json"], raw_results, args.timeout_seconds, command_home)
            try:
                actual_identity = json.loads(version_result["output"])
            except json.JSONDecodeError as exc:
                raise QualificationError("authoring CLI version --json did not return JSON") from exc
            for field in ("version", "revision", "buildTime", "dirty", "development"):
                if actual_identity.get(field) != package_metadata["identity"][field]:
                    raise QualificationError(f"authoring CLI identity mismatch in {field}")
            evidence["package"]["cliIdentity"] = {field: actual_identity.get(field) for field in ("version", "revision", "buildTime", "dirty", "development")}
            for command_name in ("init", "dev", "plan", "build", "publish", "deploy"):
                help_result = run_command(f"cli-help-{command_name}", [str(binary), command_name, "--help"], raw_results, args.timeout_seconds, command_home)
                require_command_help(command_name, help_result)

            if run_lifecycle:
                if not manual_confirmed:
                    raise QualificationSkip("local lifecycle requires --manual-prerequisites-confirmed because device authentication is interactive")
                raw_host = args.docker_host or ""
                if not raw_host:
                    raise QualificationSkip("local lifecycle requires an explicit --docker-host Unix socket")
                docker_host = normalize_docker_host(raw_host)
                docker = docker_metadata(docker_host, raw_results, args.timeout_seconds, command_home)
                evidence["metadata"]["docker"] = docker
                evidence["metadata"]["compose"] = {"version": docker["composeVersion"], "minimum": "2.17.0", "satisfied": True}
                evidence["metadata"]["network"] = {"mode": "docker-local-endpoint", "endpoint": redacted_docker_host(docker_host), "conditions": "not measured"}
                checkout_parent = Path(tempfile.mkdtemp(prefix="leapview-authoring-checkout-", dir=workdir))
                checkout = checkout_parent / "sample"
                try:
                    run_command("init", [str(binary), "init", str(checkout)], raw_results, args.timeout_seconds, command_home, cwd=checkout_parent)
                    docker_path = shutil.which("docker")
                    if docker_path is None:
                        raise QualificationError("Docker CLI disappeared before lifecycle endpoint revalidation")
                    post_identity = docker_server_identity(docker_path, docker_host, raw_results, args.timeout_seconds, command_home, "docker-version-post-init")
                    if docker["server"] != post_identity:
                        docker["serverPost"] = post_identity
                        docker["endpointPinned"] = False
                        docker["pinState"] = "not-proven"
                        raise QualificationError("Docker effective server identity changed during init; lifecycle endpoint was not proven stable")
                    docker["serverPost"] = post_identity
                    docker["pinState"] = "verified-pre-post"
                    evidence["metadata"]["fixture"] = fixture_metadata(checkout)
                    first_dev = run_command("dev-once", [str(binary), "dev", "--once", "--no-browser", "--docker-host", docker_host], raw_results, args.timeout_seconds, command_home, cwd=checkout, docker_host=docker_host, live_output=True)
                    require_happy_path_dev_output("dev-once", first_dev)
                    post_dev_identity = docker_server_identity(docker_path, docker_host, raw_results, args.timeout_seconds, command_home, "docker-version-post-dev")
                    if post_identity != post_dev_identity:
                        docker["serverPost"] = post_dev_identity
                        docker["endpointPinned"] = False
                        docker["pinState"] = "not-proven"
                        raise QualificationError("Docker effective server identity changed during dev; lifecycle endpoint was not proven stable")
                    docker["serverPost"] = post_dev_identity
                    restarted_dev = run_command("dev-once-restart", [str(binary), "dev", "--once", "--no-browser", "--docker-host", docker_host], raw_results, args.timeout_seconds, command_home, cwd=checkout, docker_host=docker_host, live_output=True)
                    require_happy_path_dev_output("dev-once-restart", restarted_dev)
                    post_restart_identity = docker_server_identity(docker_path, docker_host, raw_results, args.timeout_seconds, command_home, "docker-version-post-restart")
                    if post_dev_identity != post_restart_identity:
                        docker["serverPost"] = post_restart_identity
                        docker["endpointPinned"] = False
                        docker["pinState"] = "not-proven"
                        raise QualificationError("Docker effective server identity changed during retained-data restart; lifecycle endpoint was not proven stable")
                    docker["serverPost"] = post_restart_identity
                    docker["endpointPinned"] = True
                    evidence["metadata"]["warmup"] = {"status": "not-run", "requested": 1, "completed": 0, "samplesMs": [], "reason": PREVIEW_NOT_RELEASED}
                finally:
                    # Reset is intentionally bounded and recorded. If dev
                    # fails after creating state, cleanup still uses the
                    # exact confirmation for this temporary checkout.
                    plan = run_command("cleanup-reset-plan", [str(binary), "dev", "reset", "--docker-host", docker_host], raw_results, args.timeout_seconds, command_home, cwd=checkout, docker_host=docker_host, check=False)
                    if evidence["metadata"]["fixture"] is not None and plan.get("exitCode") not in (0, 1):
                        raise QualificationError("cleanup reset plan failed; the temporary local runtime could not be proven owned")
                    confirmation = re.search(r"Exact confirmation:\s*(sha256:[0-9a-f]{64})", plan.get("output", ""))
                    if not confirmation:
                        raise QualificationError("cleanup reset plan did not return an exact ownership confirmation; the temporary runtime was retained")
                    run_command("cleanup-reset", [str(binary), "dev", "reset", "--docker-host", docker_host, "--confirm", confirmation.group(1)], raw_results, args.timeout_seconds, command_home, cwd=checkout, docker_host=docker_host)
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
            # The archive/lifecycle lane does not execute preview scenarios or
            # browser measurements.  A static result must never be mistaken
            # for complete Milestone 5 qualification.
            evidence["result"] = "partial"
        evidence = redact_document(evidence)
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

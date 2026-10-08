#!/usr/bin/env python3
"""Collect bounded evidence from a fresh, loopback-only Linux Compose guest."""

from __future__ import annotations

import argparse
from contextlib import ExitStack
import fcntl
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform as host_platform
import re
import selectors
import secrets
import shlex
import stat
import subprocess
import tempfile
import time
import urllib.parse

import nix_compose_controller_evidence as controller_evidence
import nix_compose_qualification as qualification


SCHEMA_VERSION = 1
SCOPE = "nix-compose-host-guest"
MAX_RECEIPT_BYTES = 2 * 1024**2
MAX_GUEST_OUTPUT_BYTES = 2 * 1024**2
# Cold candidate images exceed 1 GiB compressed; unpacking them under the
# supported native-ISA TCG profile takes longer than a five-minute pull.
CANDIDATE_IMAGE_PULL_TIMEOUT = 1200
SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
REVISION_RE = re.compile(r"^[0-9a-f]{40}$")
NONCE_RE = re.compile(r"^[0-9a-f]{32,128}$")
ARCHITECTURES = {"amd64": "x86_64", "arm64": "aarch64"}
GUEST_OS = {"ubuntu2404": ("ubuntu", "24.04"), "debian13": ("debian", "13")}
ASSERTIONS = (
    "launcherManifestMatched",
    "freshInstallTarget",
    "supportedGuestOS",
    "hostAndGuestArchitectureMatch",
    "composeArchiveControllerIdentityMatched",
    "selectedInstallDriverMatched",
    "automaticRestartChangedBootID",
    "dockerEnabledAndActive",
    "containerRestartedWithoutManualStart",
    "containerHealthyOnExpectedImage",
    "installMarkerAndGenerationMatched",
    "payloadLinksMatched",
    "installedControllerMatchesOCIPayload",
    "pinnedPostgresFixtureMatched",
    "tlsPostgresRolesAuthenticated",
    "canonicalPoolArtifactsPreparedBeforeInstall",
    "migratorURLsExcludedFromServingEnvironment",
    "privateOperatorInputRemoved",
    "postgresAutomaticallyRestarted",
    "installedFirstPublicationCommitted",
    "independentReviewerApprovedFirstPublication",
    "firstPublicationReadinessTransitionObserved",
    "protectedFirstPublicationVerifierMatched",
    "protectedQualificationAssetsMatched",
    "privateBootstrapPhaseSurvivedReboot",
    "privateBootstrapProxyWasLoopbackOnly",
    "firstPublicationRequiredExplicitActivation",
    "publicActivationPhaseSurvivedReboot",
    "publicActivationUsedConfiguredProxyBindings",
    "applicationListenerStayedLoopbackOnly",
)
CONTAINER_RE = re.compile(r"^[0-9a-f]{64}$")
BOOT_ID_RE = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
IDENTIFIER_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9:_-]{0,255}$")
PROTECTED_QUALIFICATION_ASSETS = (
    "Dockerfile.authoring-client",
    "package.json",
    "authoring-worker.mjs",
)
FIRST_PUBLICATION_SCOPE = "managed-first-publication"
FIRST_PUBLICATION_PROJECT_ID = "project:leapview-evaluation"
FIRST_PUBLICATION_ENVIRONMENT = "prod"
FIRST_INSTALL_PRIVATE_PHASE = "private-bootstrap"
FIRST_INSTALL_PUBLIC_PHASE = "public"
CADDY_PORT_BINDING_KEYS = {"80/tcp", "443/tcp", "443/udp"}
APP_PORT_BINDING_KEYS = {"8080/tcp"}
CADDY_PUBLIC_BIND_CONFIG = {
    "80/tcp": "CADDY_HTTP_BIND",
    "443/tcp": "CADDY_HTTPS_BIND",
    "443/udp": "CADDY_HTTPS_UDP_BIND",
}


class HostGuestError(ValueError):
    """Guest qualification input, execution or retained evidence is invalid."""


_read = qualification._read_regular
_json = qualification._json_bytes
_canonical = qualification._canonical_bytes
_write_new = qualification._write_new


def _digest(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def _one_line(data: bytes, label: str) -> str:
    try:
        value = data.decode("utf-8").strip()
    except UnicodeDecodeError as exc:
        raise HostGuestError(f"{label} is not UTF-8") from exc
    if not value or "\n" in value or "\r" in value:
        raise HostGuestError(f"{label} must contain exactly one non-empty line")
    return value


def _os_release(data: bytes) -> tuple[str, str]:
    values = {}
    for line in data.decode("utf-8", errors="strict").splitlines():
        if "=" not in line or line.startswith("#"):
            continue
        key, value = line.split("=", 1)
        if key in {"ID", "VERSION_ID"}:
            values[key] = value.strip().strip('"').strip("'")
    if set(values) != {"ID", "VERSION_ID"}:
        raise HostGuestError("guest /etc/os-release lacks ID or VERSION_ID")
    return values["ID"], values["VERSION_ID"]


def _runtime_identity(data: bytes, expected: dict, product: str, label: str) -> dict:
    value = _json(data, label, 1024**2)
    if not isinstance(value, dict) or set(value) != {
        "product", "version", "revision", "buildTime", "dirty", "development",
    }:
        raise HostGuestError(f"{label} has an unsupported runtime identity schema")
    if value != {
        "product": product,
        "version": expected["version"],
        "revision": expected["revision"],
        "buildTime": expected["buildTime"],
        "dirty": False,
        "development": False,
    }:
        raise HostGuestError(f"{label} differs from the exact release identity")
    return value


def _local_architecture() -> str:
    machine = host_platform.machine().lower()
    aliases = {"x86_64": "x86_64", "amd64": "x86_64", "aarch64": "aarch64", "arm64": "aarch64"}
    try:
        return aliases[machine]
    except KeyError as exc:
        raise HostGuestError(f"unsupported qualification runner architecture {machine!r}") from exc


def _kvm_probe() -> dict:
    available = False
    api_version = None
    try:
        descriptor = os.open("/dev/kvm", os.O_RDWR | getattr(os, "O_CLOEXEC", 0))
    except OSError:
        pass
    else:
        try:
            api_version = fcntl.ioctl(descriptor, 0xAE00, 0)
            available = api_version == 12
        except OSError:
            api_version = None
        finally:
            os.close(descriptor)
    return {"available": available, "apiVersion": api_version}


def _validate_manifest(data: bytes, *, nonce: str, guest_os: str, platform: str, mode: str) -> dict:
    manifest = _json(data, "launcher guest manifest", 64 * 1024)
    expected_keys = {
        "schemaVersion", "nonce", "sourceCloudImageSHA256", "guestOS", "architecture", "virtualizationMode",
    }
    if (not isinstance(manifest, dict) or set(manifest) != expected_keys
            or type(manifest.get("schemaVersion")) is not int or manifest["schemaVersion"] != 1):
        raise HostGuestError("launcher guest manifest has an unsupported schema")
    if NONCE_RE.fullmatch(nonce) is None or not isinstance(manifest["sourceCloudImageSHA256"], str) or SHA256_RE.fullmatch(manifest["sourceCloudImageSHA256"]) is None:
        raise HostGuestError("launcher manifest must bind a SHA-256 cloud image digest")
    expected_arch = platform.removeprefix("linux/")
    if manifest != {
        "schemaVersion": 1,
        "nonce": nonce,
        "sourceCloudImageSHA256": manifest["sourceCloudImageSHA256"],
        "guestOS": guest_os,
        "architecture": expected_arch,
        "virtualizationMode": mode,
    }:
        raise HostGuestError("launcher manifest differs from the independently selected guest inputs")
    return manifest


def _validate_launcher_receipt(data: bytes, *, manifest: dict, manifest_bytes: bytes, known_hosts_sha256: str) -> dict:
    receipt = _json(data, "immutable launcher receipt", 64 * 1024)
    keys = {
        "schemaVersion", "scope", "result", "nonce", "sourceCloudImageSHA256", "guestOS", "architecture",
        "virtualizationMode", "manifestSHA256", "inputs", "runner",
    }
    if (not isinstance(receipt, dict) or set(receipt) != keys or type(receipt.get("schemaVersion")) is not int
            or receipt["schemaVersion"] != 1 or receipt["scope"] != "nix-compose-guest-launcher"
            or receipt["result"] != "ready"):
        raise HostGuestError("immutable launcher receipt has an unsupported schema or lifecycle state")
    if any(receipt[key] != manifest[field] for key, field in (
        ("nonce", "nonce"), ("sourceCloudImageSHA256", "sourceCloudImageSHA256"),
        ("guestOS", "guestOS"), ("architecture", "architecture"), ("virtualizationMode", "virtualizationMode"),
    )) or receipt["manifestSHA256"] != _digest(manifest_bytes):
        raise HostGuestError("immutable launcher receipt differs from its exact manifest")
    input_keys = {
        "cloudImageSHA256", "firmwareSHA256", "userDataSHA256", "metaDataSHA256", "seedISOSHA256",
        "sshClientPublicKeySHA256", "sshHostPublicKeySHA256", "knownHostsSHA256",
    }
    inputs = receipt["inputs"]
    if not isinstance(inputs, dict) or set(inputs) != input_keys:
        raise HostGuestError("immutable launcher receipt has incomplete input hashes")
    for name, digest in inputs.items():
        if name == "firmwareSHA256" and digest is None and manifest["architecture"] == "amd64":
            continue
        if not isinstance(digest, str) or SHA256_RE.fullmatch(digest) is None:
            raise HostGuestError(f"immutable launcher receipt has an invalid {name}")
    if (manifest["architecture"] == "amd64" and inputs["firmwareSHA256"] is not None
            or manifest["architecture"] == "arm64" and inputs["firmwareSHA256"] is None):
        raise HostGuestError("launcher firmware hash does not match the guest architecture")
    if inputs["cloudImageSHA256"] != manifest["sourceCloudImageSHA256"] or inputs["knownHostsSHA256"] != known_hosts_sha256:
        raise HostGuestError("launcher cloud image or SSH host key hash differs from independently supplied inputs")
    runner = receipt["runner"]
    if not isinstance(runner, dict) or set(runner) != {
        "hostArchitecture", "qemuSystemBinarySHA256", "qemuVersionSHA256", "accelerator",
    }:
        raise HostGuestError("immutable launcher receipt has incomplete QEMU identity")
    if runner["hostArchitecture"] != ARCHITECTURES[manifest["architecture"]] or runner["accelerator"] != manifest["virtualizationMode"]:
        raise HostGuestError("launcher receipt QEMU architecture or accelerator differs from the selected guest")
    for name in ("qemuSystemBinarySHA256", "qemuVersionSHA256"):
        if not isinstance(runner[name], str) or SHA256_RE.fullmatch(runner[name]) is None:
            raise HostGuestError(f"immutable launcher receipt has an invalid {name}")
    return receipt


def _validate_config(data: bytes, image: str) -> dict:
    config = _json(data, "private host configuration", 64 * 1024)
    required = {"schemaVersion", "domain", "adminEmail", "environment", "image", "https"}
    allowed = required | {"targetId"}
    if not isinstance(config, dict) or not required.issubset(config) or set(config) - allowed:
        raise HostGuestError("host configuration has unsupported or missing fields")
    if (type(config["schemaVersion"]) is not int or config["schemaVersion"] != 1
            or config["image"] != image or not isinstance(config["https"], bool)
            or any(not isinstance(config[key], str) or not config[key].strip()
                   for key in ("domain", "adminEmail", "environment"))
            or ("targetId" in config and (not isinstance(config["targetId"], str) or not config["targetId"].strip()))):
        raise HostGuestError("host configuration is invalid or selects a different image")
    return config


def _validate_first_publication_host_config(config: dict) -> None:
    domain = config.get("domain")
    if (not isinstance(domain, str) or domain.strip().lower().removesuffix(".") != "localhost"
            or config.get("https") is not True or config.get("environment") != FIRST_PUBLICATION_ENVIRONMENT):
        raise HostGuestError("first-publication qualification requires the installed localhost HTTPS prod profile")


def _first_install_generation(image: str) -> str:
    if not isinstance(image, str) or qualification.IMAGE_RE.fullmatch(image) is None:
        raise HostGuestError("first-install state cannot bind an invalid immutable image")
    return "sha256-" + image.rsplit("sha256:", 1)[1]


def _validate_host_install_marker(marker: dict, *, phase: str, image: str, target_id: str) -> dict:
    expected_keys = {
        "schemaVersion", "domain", "adminEmail", "environment", "https", "image", "targetId",
        "bootstrapPhase", "generation",
    }
    if (not isinstance(marker, dict) or set(marker) != expected_keys
            or type(marker.get("schemaVersion")) is not int or marker["schemaVersion"] != 1
            or marker.get("domain") != "localhost" or marker.get("environment") != FIRST_PUBLICATION_ENVIRONMENT
            or marker.get("https") is not True or marker.get("image") != image
            or marker.get("targetId") != target_id
            or marker.get("bootstrapPhase") != phase
            or phase not in {FIRST_INSTALL_PRIVATE_PHASE, FIRST_INSTALL_PUBLIC_PHASE}
            or marker.get("generation") != _first_install_generation(image)
            or not isinstance(marker.get("adminEmail"), str) or not marker["adminEmail"]):
        raise HostGuestError("host install marker does not bind the expected phase, localhost profile, image, and generation")
    return marker


def _validate_caddy_inspection(inspection: dict, *, image: str | None = None) -> dict:
    if (not isinstance(inspection, dict) or set(inspection) != {
        "id", "image", "status", "project", "service", "startedAt",
    } or CONTAINER_RE.fullmatch(inspection.get("id", "")) is None
            or not isinstance(inspection.get("image"), str) or not inspection["image"]
            or (image is not None and inspection["image"] != image)
            or inspection.get("status") != "running" or inspection.get("project") != "leapview"
            or inspection.get("service") != "caddy" or not isinstance(inspection.get("startedAt"), str)
            or not inspection["startedAt"]):
        raise HostGuestError("private HTTPS proxy inspection does not identify one running Compose Caddy service")
    return inspection


def _parse_caddy_bind_config(data: bytes) -> dict:
    expected_keys = set(CADDY_PUBLIC_BIND_CONFIG.values())
    values = {}
    try:
        lines = data.decode("utf-8").splitlines()
    except UnicodeDecodeError as exc:
        raise HostGuestError("installed Caddy bind configuration is not UTF-8") from exc
    for line in lines:
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        if key in expected_keys:
            if key in values or not value or any(character.isspace() for character in value):
                raise HostGuestError("installed Caddy bind configuration has duplicate or invalid entries")
            values[key] = value
    if set(values) != expected_keys:
        raise HostGuestError("installed Caddy bind configuration is incomplete")
    for value in values.values():
        _parse_host_bind_value(value)
    return values


def _parse_host_bind_value(value: str) -> tuple[str | None, str]:
    address = None
    port = value
    if value.startswith("["):
        close = value.find("]")
        if close < 0 or value[close + 1:close + 2] != ":":
            raise HostGuestError("installed Caddy bind configuration has an invalid IPv6 listener")
        address, port = value[1:close], value[close + 2:]
    elif ":" in value:
        address, port = value.rsplit(":", 1)
    if not port.isdigit() or not 1 <= int(port) <= 65535:
        raise HostGuestError("installed Caddy bind configuration has an invalid port")
    if address is not None:
        try:
            address = str(ipaddress.ip_address(address))
        except ValueError as exc:
            raise HostGuestError("installed Caddy bind configuration has an invalid address") from exc
    return address, port


def _validate_caddy_port_bindings(bindings: dict, *, private: bool, public_config: dict | None = None) -> dict:
    if not isinstance(bindings, dict) or set(bindings) != CADDY_PORT_BINDING_KEYS:
        raise HostGuestError("HTTPS proxy port bindings differ from the exact Compose listener set")
    if not private and public_config is not None and set(public_config) != set(CADDY_PUBLIC_BIND_CONFIG.values()):
        raise HostGuestError("public Caddy bind configuration does not define the exact listener set")
    normalized = {}
    for transport in sorted(CADDY_PORT_BINDING_KEYS):
        entries = bindings[transport]
        expected_address, expected_port = (None, "80" if transport == "80/tcp" else "443")
        if not private and public_config is not None:
            expected_address, expected_port = _parse_host_bind_value(public_config[CADDY_PUBLIC_BIND_CONFIG[transport]])
        if (not isinstance(entries, list) or len(entries) != 1 or not isinstance(entries[0], dict)
                or set(entries[0]) != {"HostIp", "HostPort"} or entries[0].get("HostPort") != expected_port
                or not isinstance(entries[0].get("HostIp"), str)):
            raise HostGuestError("HTTPS proxy port binding is missing, duplicated, or uses an unexpected host port")
        host_ip = entries[0]["HostIp"]
        if host_ip == "":
            host_ip = "0.0.0.0"
        try:
            address = ipaddress.ip_address(host_ip)
        except ValueError as exc:
            raise HostGuestError("HTTPS proxy port binding has an invalid host address") from exc
        if private and str(address) != "127.0.0.1":
            raise HostGuestError("first-install HTTPS proxy is exposed beyond IPv4 loopback")
        if not private and address.is_loopback:
            raise HostGuestError("activated HTTPS proxy is still bound only to loopback")
        if not private and expected_address is not None and str(address) != expected_address:
            raise HostGuestError("activated HTTPS proxy address differs from installed Caddy bind configuration")
        normalized[transport] = {"hostIP": str(address), "hostPort": expected_port}
    return normalized


def _validate_application_port_bindings(bindings: dict) -> dict:
    if (not isinstance(bindings, dict) or set(bindings) != APP_PORT_BINDING_KEYS
            or not isinstance(bindings["8080/tcp"], list) or len(bindings["8080/tcp"]) != 1
            or not isinstance(bindings["8080/tcp"][0], dict)
            or set(bindings["8080/tcp"][0]) != {"HostIp", "HostPort"}
            or bindings["8080/tcp"][0].get("HostIp") != "127.0.0.1"
            or bindings["8080/tcp"][0].get("HostPort") != "8080"):
        raise HostGuestError("application listener is not bound only to host IPv4 loopback")
    return {"8080/tcp": {"hostIP": "127.0.0.1", "hostPort": "8080"}}


def _validate_active_caddyfile(contents: bytes, *, private: bool) -> str:
    try:
        text = contents.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise HostGuestError("active Caddy configuration is not UTF-8") from exc
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    if ("{$CADDY_DOMAIN} {" not in lines or "reverse_proxy leapview:8080" not in lines
            or ("tls internal" in lines) is not private):
        raise HostGuestError("active Caddy configuration does not match the requested first-install TLS phase")
    return text


def _protected_checkout_revision(root: Path, expected_revision: str) -> str:
    if REVISION_RE.fullmatch(expected_revision) is None:
        raise HostGuestError("protected verifier revision must be a full lowercase commit SHA")
    root_info = root.lstat()
    if not stat.S_ISDIR(root_info.st_mode):
        raise HostGuestError("protected checkout root must be a real directory")
    try:
        head = subprocess.run(
            ["git", "-C", str(root), "rev-parse", "HEAD"],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=True, timeout=10,
        ).stdout.decode("ascii").strip()
        dirty = subprocess.run(
            ["git", "-C", str(root), "status", "--porcelain", "--untracked-files=no"],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=True, timeout=10,
        ).stdout
    except (OSError, subprocess.SubprocessError, UnicodeDecodeError) as exc:
        raise HostGuestError("cannot verify protected verifier checkout identity") from exc
    if head != expected_revision or dirty:
        raise HostGuestError("protected verifier checkout differs from the expected clean commit")
    return head


def _protected_qualification_assets(root: Path) -> tuple[dict[str, bytes], dict, str]:
    asset_root = root / "deploy/compose/qualification"
    try:
        root_info = asset_root.lstat()
    except OSError as exc:
        raise HostGuestError("protected qualification assets are unavailable") from exc
    if not stat.S_ISDIR(root_info.st_mode):
        raise HostGuestError("protected qualification assets must be a real directory")
    assets = {}
    for name in PROTECTED_QUALIFICATION_ASSETS:
        try:
            info = (asset_root / name).lstat()
        except OSError as exc:
            raise HostGuestError(f"protected qualification asset {name} is unavailable") from exc
        if not stat.S_ISREG(info.st_mode) or info.st_size > 8 * 1024**2:
            raise HostGuestError(f"protected qualification asset {name} is not a bounded regular file")
        assets[name] = _read(asset_root / name, "protected qualification asset " + name, 8 * 1024**2)
    manifest = {
        "schemaVersion": 1,
        "assets": {name: _digest(data) for name, data in sorted(assets.items())},
    }
    return assets, manifest, _digest(_canonical(manifest))


def _protected_verifier_identity(path: Path) -> tuple[str, int]:
    try:
        info = path.lstat()
    except OSError as exc:
        raise HostGuestError("protected first-publication verifier is unavailable") from exc
    if not stat.S_ISREG(info.st_mode) or not info.st_mode & 0o111 or info.st_size == 0 or info.st_size > 512 * 1024**2:
        raise HostGuestError("protected first-publication verifier must be a bounded executable file")
    digest = hashlib.sha256()
    size = 0
    try:
        with path.open("rb") as source:
            while chunk := source.read(1024**2):
                size += len(chunk)
                if size > 512 * 1024**2:
                    raise HostGuestError("protected first-publication verifier exceeds its byte limit")
                digest.update(chunk)
    except OSError as exc:
        raise HostGuestError("cannot read protected first-publication verifier") from exc
    if size != info.st_size:
        raise HostGuestError("protected first-publication verifier changed while it was read")
    return "sha256:" + digest.hexdigest(), size


def _validate_first_publication_report(report: dict, *, image: str, image_revision: str,
                                       environment: str) -> dict:
    report_keys = {
        "schemaVersion", "scope", "result", "request", "publication", "approval",
        "publisherPrincipalID", "reviewerPrincipalID", "readinessBefore", "readinessAfter",
        "phases", "assertions",
    }
    if (not isinstance(report, dict) or set(report) != report_keys
            or type(report.get("schemaVersion")) is not int or report["schemaVersion"] != 1
            or report.get("scope") != FIRST_PUBLICATION_SCOPE or report.get("result") != "passed"):
        raise HostGuestError("installed first-publication report has an unsupported schema or result")
    request, publication, approval = report["request"], report["publication"], report["approval"]
    request_keys = {
        "targetURL", "projectID", "environment", "image", "imageSourceRevision", "sourceRevision",
        "candidateID", "candidateRevision", "targetID", "principalID", "artifactDigest", "releaseDigest", "planID", "planDigest",
    }
    publication_keys = {
        "candidateID", "candidateRevision", "targetID", "publicationID", "publicationStatus", "generationID",
        "principalID", "sourceArtifactDigest", "servingArtifactDigest", "releaseDigest", "sourceRevision", "planID", "planDigest",
    }
    approval_keys = {"id", "status", "approvedBy", "deploymentId", "projectId", "environment", "requestDigest"}
    if (not isinstance(request, dict) or set(request) != request_keys
            or not isinstance(publication, dict) or set(publication) != publication_keys
            or not isinstance(approval, dict) or set(approval) != approval_keys):
        raise HostGuestError("first-publication report has incomplete or unexpected request, publication, or approval fields")
    for field in ("targetURL", "projectID", "environment", "image", "imageSourceRevision", "sourceRevision",
                  "candidateID", "targetID", "principalID", "planID"):
        value = request[field]
        if not isinstance(value, str) or not value or len(value) > 512 or any(ord(character) < 0x20 for character in value):
            raise HostGuestError(f"first-publication request {field} is not a bounded single-line value")
    if (request["targetURL"] != "https://localhost" or request["projectID"] != FIRST_PUBLICATION_PROJECT_ID
            or request["environment"] != environment or environment != FIRST_PUBLICATION_ENVIRONMENT
            or request["image"] != image or request["imageSourceRevision"] != image_revision
            or REVISION_RE.fullmatch(image_revision) is None
            or SHA256_RE.fullmatch(request["sourceRevision"]) is None
            or request["sourceRevision"] == image_revision
            or not _safe_qualification_identifier(request["candidateID"])
            or not _safe_qualification_identifier(request["targetID"])
            or not _safe_qualification_identifier(request["principalID"])
            or not _safe_qualification_identifier(request["planID"])
            or type(request["candidateRevision"]) is not int or request["candidateRevision"] < 1
            or not isinstance(request["artifactDigest"], str) or SHA256_RE.fullmatch(request["artifactDigest"]) is None
            or not isinstance(request["releaseDigest"], str) or SHA256_RE.fullmatch(request["releaseDigest"]) is None
            or not isinstance(request["planDigest"], str) or SHA256_RE.fullmatch(request["planDigest"]) is None):
        raise HostGuestError("first-publication request is not bound to localhost, the installed image, and managed source revision")
    for field in ("candidateID", "targetID", "publicationID", "generationID", "principalID", "planID"):
        value = publication[field]
        if not _safe_qualification_identifier(value):
            raise HostGuestError(f"first-publication result {field} is not a safe identifier")
    if (publication["candidateID"] != request["candidateID"]
            or type(publication["candidateRevision"]) is not int
            or publication["candidateRevision"] != request["candidateRevision"]
            or publication["targetID"] != request["targetID"]
            or publication["principalID"] != request["principalID"]
            or publication["sourceArtifactDigest"] != request["artifactDigest"]
            or publication["releaseDigest"] != request["releaseDigest"]
            or publication["sourceRevision"] != request["sourceRevision"]
            or publication["planID"] != request["planID"]
            or publication["planDigest"] != request["planDigest"]
            or publication["publicationStatus"] != "committed"
            or not isinstance(publication["publicationID"], str)
            or not _safe_qualification_identifier(publication["publicationID"])
            or publication["publicationID"] == ""
            or not isinstance(publication["sourceArtifactDigest"], str)
            or SHA256_RE.fullmatch(publication["sourceArtifactDigest"]) is None
            or not isinstance(publication["servingArtifactDigest"], str)
            or SHA256_RE.fullmatch(publication["servingArtifactDigest"]) is None
            or publication["servingArtifactDigest"] == publication["sourceArtifactDigest"]
            or not isinstance(publication["releaseDigest"], str)
            or SHA256_RE.fullmatch(publication["releaseDigest"]) is None
            or not isinstance(publication["sourceRevision"], str)
            or SHA256_RE.fullmatch(publication["sourceRevision"]) is None
            or not isinstance(publication["planDigest"], str)
            or SHA256_RE.fullmatch(publication["planDigest"]) is None):
        raise HostGuestError("first-publication result does not bind the exact requested candidate tuple")
    if (not _safe_qualification_identifier(approval["id"])
            or approval["status"] != "approved" or approval["approvedBy"] != report.get("reviewerPrincipalID")
            or approval["deploymentId"] != publication["publicationID"]
            or approval["projectId"] != request["projectID"] or approval["environment"] != environment
            or not isinstance(approval["requestDigest"], str) or SHA256_RE.fullmatch(approval["requestDigest"]) is None
            or report.get("publisherPrincipalID") != request["principalID"]
            or not isinstance(report.get("reviewerPrincipalID"), str)
            or not _safe_qualification_identifier(report["reviewerPrincipalID"])
            or report["reviewerPrincipalID"] == report["publisherPrincipalID"]):
        raise HostGuestError("first-publication approval does not bind an independent reviewer to the committed publication")
    if (type(report.get("readinessBefore")) is not int or report["readinessBefore"] != 503
            or type(report.get("readinessAfter")) is not int or report["readinessAfter"] != 200):
        raise HostGuestError("first-publication report lacks the 503-to-200 readiness transition")
    assertions = report["assertions"]
    expected_assertions = {
        "firstLoginConsumedOnce", "readinessTransitionObserved", "temporaryCredentialsRemoved", "secretsExcludedFromEvidence",
    }
    if (not isinstance(assertions, dict) or set(assertions) != expected_assertions
            or any(value is not True for value in assertions.values())):
        raise HostGuestError("first-publication report does not prove the credential and readiness boundaries")
    phases = report["phases"]
    if not isinstance(phases, list) or not phases or len(phases) > 64:
        raise HostGuestError("first-publication phase evidence is absent or oversized")
    phase_indices = {}
    phase_keys = {"name", "result", "startedAt", "durationMillis", "timeoutSeconds", "cleanupGuaranteed"}
    expected_phase_names = (
        "browser and client setup", "reviewer provisioning", "native keyring login",
        "private candidate preview", "protected publish",
    )
    for index, phase in enumerate(phases):
        if not isinstance(phase, dict) or set(phase) not in (phase_keys, phase_keys | {"failureCode"}):
            raise HostGuestError("first-publication phase evidence has an unsupported schema")
        name, started_at = phase.get("name"), phase.get("startedAt")
        if (not isinstance(name, str) or name not in expected_phase_names or name in phase_indices
                or phase.get("result") != "success" or not isinstance(started_at, str) or not started_at
                or re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?Z", started_at) is None
                or type(phase.get("durationMillis")) is not int or phase["durationMillis"] < 0
                or type(phase.get("timeoutSeconds")) is not int or phase["timeoutSeconds"] <= 0
                or phase.get("cleanupGuaranteed") is not True or phase.get("failureCode", "") != ""):
            raise HostGuestError("first-publication phase evidence is incomplete or failed")
        phase_indices[name] = index
    if tuple(phase["name"] for phase in phases) != expected_phase_names:
        raise HostGuestError("first-publication phase evidence does not show the complete ordered lifecycle")
    reviewer_phase = phase_indices.get("reviewer provisioning")
    preview_phase = phase_indices.get("private candidate preview")
    publish_phase = phase_indices.get("protected publish")
    if (reviewer_phase is None or preview_phase is None or publish_phase is None
            or not reviewer_phase < preview_phase < publish_phase):
        raise HostGuestError("first-publication phase order does not bind independent review before publish")
    return report


def _safe_qualification_identifier(value) -> bool:
    return (isinstance(value, str) and IDENTIFIER_RE.fullmatch(value) is not None
            and re.fullmatch(r"[A-Fa-f0-9]{32,128}", value) is None)


class SSHGuest:
    def __init__(self, *, port: int, identity: Path, known_hosts: Path, timeout: int):
        self.destination = "root@127.0.0.1"
        self.port = port
        self.timeout = timeout
        self.base = [
            "ssh", "-F", "/dev/null", "-p", str(port), "-i", str(identity),
            "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
            "-o", "UserKnownHostsFile=" + str(known_hosts), "-o", "GlobalKnownHostsFile=/dev/null",
            "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "PermitLocalCommand=no",
            "-o", "ConnectTimeout=10", self.destination,
        ]

    def run(self, command: str, *, input_bytes: bytes | None = None, input_file: Path | None = None,
            timeout: int | None = None, allow_disconnect: bool = False) -> bytes:
        if input_bytes is not None and input_file is not None:
            raise HostGuestError("SSH command accepts one input source")
        limit = timeout or self.timeout
        with ExitStack() as stack:
            if input_file is not None:
                stdin = stack.enter_context(input_file.open("rb"))
            elif input_bytes is not None:
                stdin = stack.enter_context(tempfile.TemporaryFile())
                stdin.write(input_bytes)
                stdin.seek(0)
            else:
                stdin = subprocess.DEVNULL
            process = subprocess.Popen(
                [*self.base, command], stdin=stdin, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            )
            selector = selectors.DefaultSelector()
            selector.register(process.stdout, selectors.EVENT_READ)
            deadline = time.monotonic() + limit
            output = bytearray()
            try:
                while True:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0 or not selector.select(remaining):
                        process.kill()
                        process.wait()
                        if allow_disconnect:
                            return b""
                        raise HostGuestError("SSH command timed out")
                    chunk = os.read(process.stdout.fileno(), min(64 * 1024, MAX_GUEST_OUTPUT_BYTES + 1 - len(output)))
                    if not chunk:
                        break
                    output.extend(chunk)
                    if len(output) > MAX_GUEST_OUTPUT_BYTES:
                        process.kill()
                        process.wait()
                        raise HostGuestError("SSH guest command exceeded its output limit")
                return_code = process.wait(timeout=max(0.1, deadline - time.monotonic()))
            except subprocess.TimeoutExpired as exc:
                process.kill()
                process.wait()
                if allow_disconnect:
                    return b""
                raise HostGuestError("SSH command timed out") from exc
            finally:
                selector.close()
                process.stdout.close()
        if return_code != 0 and not (allow_disconnect and return_code == 255):
            raise HostGuestError(f"SSH guest command failed ({return_code})")
        return bytes(output)


def _https_readiness_command() -> str:
    request = (
        "GET /readyz HTTP/1.1\\r\\nHost: localhost\\r\\nConnection: close\\r\\n\\r\\n"
    )
    return (
        "status=$(printf '" + request + "' | timeout 15 openssl s_client -quiet "
        "-connect 127.0.0.1:443 -servername localhost 2>/dev/null | "
        "sed -n '1s#^HTTP/[0-9.][0-9.]* \\([0-9][0-9][0-9]\\).*#\\1#p'); "
        "case \"$status\" in 200|503) printf '%s\\n' \"$status\";; *) printf 'unavailable\\n';; esac"
    )


def _http_status_command(path: str) -> str:
    if path not in {"/healthz", "/readyz"}:
        raise HostGuestError("private application probe path is not allowed")
    request = f"GET {path} HTTP/1.1\\r\\nHost: localhost\\r\\nConnection: close\\r\\n\\r\\n"
    script = (
        "exec 3<>/dev/tcp/127.0.0.1/8080 2>/dev/null || { printf 'unavailable\\n'; exit 0; }; "
        f"printf '{request}' >&3; IFS= read -r line <&3 || line=; "
        "set -- $line; case \"${2:-}\" in [1-5][0-9][0-9]) printf '%s\\n' \"$2\";; *) printf 'unavailable\\n';; esac"
    )
    return "bash -c " + shlex.quote(script)


def _caddy_inspect_command(docker_env: str) -> str:
    return (
        "set -eu; ids=$(env " + docker_env +
        " docker ps -q --no-trunc --filter label=com.docker.compose.project=leapview"
        " --filter label=com.docker.compose.service=caddy); "
        "test -n \"$ids\"; test \"$(printf '%s\\n' \"$ids\" | wc -l)\" -eq 1; "
        "env " + docker_env + " docker inspect --format '{{.Id}}|{{.Config.Image}}|"
        "{{.State.Status}}|{{index .Config.Labels \"com.docker.compose.project\"}}|"
        "{{index .Config.Labels \"com.docker.compose.service\"}}|{{.State.StartedAt}}|"
        "{{json .HostConfig.PortBindings}}' \"$ids\""
    )


def _application_port_bindings_command(docker_env: str) -> str:
    return (
        "set -eu; ids=$(env " + docker_env +
        " docker ps -q --no-trunc --filter label=com.docker.compose.project=leapview"
        " --filter label=com.docker.compose.service=leapview); "
        "test -n \"$ids\"; test \"$(printf '%s\\n' \"$ids\" | wc -l)\" -eq 1; "
        "env " + docker_env + " docker inspect --format '{{json .HostConfig.PortBindings}}' \"$ids\""
    )


def _deployment_caddy_bind_config_command() -> str:
    return (
        "awk -F= '$1 == \"CADDY_HTTP_BIND\" || $1 == \"CADDY_HTTPS_BIND\" || "
        "$1 == \"CADDY_HTTPS_UDP_BIND\" { print $1 \"=\" $2 }' /opt/leapview/deployment.env"
    )


def _parse_caddy_observation(data: bytes, *, private: bool, image: str | None = None,
                            public_config: dict | None = None) -> dict:
    line = _one_line(data, "Docker Caddy inspection")
    fields = line.split("|", 6)
    if len(fields) != 7:
        raise HostGuestError("Docker Caddy inspection returned an unsupported field set")
    inspection = _validate_caddy_inspection({
        "id": fields[0], "image": fields[1], "status": fields[2], "project": fields[3],
        "service": fields[4], "startedAt": fields[5],
    }, image=image)
    try:
        bindings = _json((fields[6] + "\n").encode("utf-8"), "Docker Caddy port bindings", 64 * 1024)
    except qualification.QualificationError as exc:
        raise HostGuestError(f"Docker Caddy port bindings are invalid: {exc}") from exc
    return {
        "inspection": inspection,
        "portBindings": _validate_caddy_port_bindings(bindings, private=private, public_config=public_config),
    }


def _active_caddyfile_command(docker_env: str, container_id: str) -> str:
    if CONTAINER_RE.fullmatch(container_id) is None:
        raise HostGuestError("active Caddy container identity is invalid")
    return "env " + docker_env + " docker exec " + shlex.quote(container_id) + " cat /etc/caddy/Caddyfile"


def _active_caddy_domain_command(docker_env: str, container_id: str) -> str:
    if CONTAINER_RE.fullmatch(container_id) is None:
        raise HostGuestError("active Caddy container identity is invalid")
    return "env " + docker_env + " docker exec " + shlex.quote(container_id) + " printenv CADDY_DOMAIN"


def _wait_for_http_status(guest: SSHGuest, *, path: str, expected: str, timeout: int,
                          evidence: Path, filename: str) -> str:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        status = _one_line(guest.run(_http_status_command(path), timeout=15), "private application readiness probe")
        if status == expected:
            _record(evidence, filename, (status + "\n").encode("ascii"))
            return status
        if status not in {"unavailable", "503"}:
            _record(evidence, filename, (status + "\n").encode("ascii"))
            raise HostGuestError(f"private application {path} returned unexpected status {status}")
        time.sleep(2)
    _record(evidence, filename, b"unavailable\n")
    raise HostGuestError(f"private application {path} did not reach {expected} before timeout")


def _capture_host_install_marker(guest: SSHGuest, evidence: Path, *, filename: str, phase: str,
                                image: str, target_id: str) -> tuple[dict, bytes]:
    raw = guest.run("cat /opt/leapview/.host-install.json")
    try:
        marker = _json(raw, "host installation marker", 64 * 1024)
    except qualification.QualificationError as exc:
        raise HostGuestError(f"host installation marker is invalid: {exc}") from exc
    _validate_host_install_marker(marker, phase=phase, image=image, target_id=target_id)
    generation_link = _one_line(guest.run("readlink /opt/leapview/current"), "active installation generation link")
    expected_link = "releases/" + marker["generation"]
    if generation_link != expected_link:
        raise HostGuestError("host installation marker generation differs from the real current symlink")
    _record(evidence, filename, raw)
    _record(evidence, filename.removesuffix(".json") + "-current-link.txt", (generation_link + "\n").encode())
    owner_mode = _one_line(guest.run("stat -c '%u:%g:%a' /opt/leapview/.host-install.json"), "host marker owner and mode")
    if owner_mode != "0:0:600":
        raise HostGuestError("host installation marker is not root-owned mode 0600")
    _record(evidence, filename.removesuffix(".json") + "-owner-mode.txt", (owner_mode + "\n").encode("ascii"))
    return marker, raw


def _capture_caddy_observation(guest: SSHGuest, evidence: Path, *, docker_env: str, prefix: str,
                               private: bool, expected_image: str | None = None,
                               public_config: dict | None = None) -> dict:
    raw_inspection = guest.run(_caddy_inspect_command(docker_env), timeout=20)
    observation = _parse_caddy_observation(
        raw_inspection, private=private, image=expected_image, public_config=public_config,
    )
    active_config = guest.run(_active_caddyfile_command(docker_env, observation["inspection"]["id"]))
    _validate_active_caddyfile(active_config, private=private)
    domain = _one_line(guest.run(
        _active_caddy_domain_command(docker_env, observation["inspection"]["id"]),
    ), "active Caddy domain")
    if domain != "localhost":
        raise HostGuestError("active first-install Caddy service is not configured for localhost")
    # Retryable probes must finish before immutable evidence is retained.
    _record(evidence, prefix + "-docker-inspect.txt", raw_inspection)
    _record(evidence, prefix + "-active-caddyfile.txt", active_config)
    _record(evidence, prefix + "-active-domain.txt", (domain + "\n").encode("ascii"))
    observation["caddyfileSHA256"] = _digest(active_config)
    observation["domain"] = domain
    return observation


def _wait_for_caddy_observation(guest: SSHGuest, evidence: Path, *, docker_env: str, prefix: str,
                                private: bool, timeout: int, expected_image: str | None = None,
                                public_config: dict | None = None) -> dict:
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            return _capture_caddy_observation(
                guest, evidence, docker_env=docker_env, prefix=prefix, private=private,
                expected_image=expected_image, public_config=public_config,
            )
        except HostGuestError as exc:
            last_error = exc
            time.sleep(2)
    raise HostGuestError(f"Caddy did not reach the expected {'private' if private else 'configured'} phase: {last_error}")


def _capture_application_ports(guest: SSHGuest, evidence: Path, *, docker_env: str,
                               filename: str) -> dict:
    raw = guest.run(_application_port_bindings_command(docker_env), timeout=20)
    try:
        bindings = _json(raw, "application Docker port bindings", 64 * 1024)
    except qualification.QualificationError as exc:
        raise HostGuestError(f"application Docker port bindings are invalid: {exc}") from exc
    normalized = _validate_application_port_bindings(bindings)
    _record(evidence, filename, _canonical(bindings) + b"\n")
    return normalized


def _wait_for_https_readiness(guest: SSHGuest, *, expected: str, timeout: int, evidence: Path,
                              filename: str) -> str:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        status = _one_line(guest.run(_https_readiness_command(), timeout=20), "private HTTPS readiness probe")
        if status == expected:
            _record(evidence, filename, (status + "\n").encode("ascii"))
            return status
        if status == "200" and expected == "503":
            _record(evidence, filename, (status + "\n").encode("ascii"))
            raise HostGuestError("fresh localhost HTTPS target was ready before its first publication")
        if status not in {"unavailable", "503"}:
            _record(evidence, filename, (status + "\n").encode("ascii"))
            raise HostGuestError(f"private localhost HTTPS readiness returned unexpected status {status}")
        time.sleep(2)
    _record(evidence, filename, b"unavailable\n")
    raise HostGuestError(f"private localhost HTTPS readiness did not reach {expected} before timeout")


def _run_first_publication_qualification(guest: SSHGuest, evidence: Path, *, args, paths: dict,
                                         fixture_secrets: list[str], image_runtime: dict) -> tuple[dict, dict]:
    verifier_sha, _ = _protected_verifier_identity(args.first_publication_verifier)
    assets, assets_manifest, assets_sha = _protected_qualification_assets(args.protected_root)
    protected_revision = _protected_checkout_revision(args.protected_root, args.protected_revision)
    verifier_path = _remote_path(paths["root"], "protected-first-publication-verifier")
    assets_path = _remote_path(paths["root"], "protected-qualification-assets")
    remote_evidence = _remote_path(paths["root"], "first-publication-evidence")
    _record(evidence, "protected-first-publication-verifier-sha256.txt", (verifier_sha + "\n").encode("ascii"))
    _record(evidence, "protected-first-publication-source-revision.txt", (protected_revision + "\n").encode("ascii"))
    _record(evidence, "protected-qualification-assets-sha256.txt", (assets_sha + "\n").encode("ascii"))
    _record(evidence, "protected-qualification-assets-manifest.json", (_canonical(assets_manifest) + b"\n"))

    before = _wait_for_https_readiness(
        guest, expected="503", timeout=args.startup_timeout, evidence=evidence,
        filename="first-publication-https-readiness-before.txt",
    )
    report = None
    try:
        guest.run("install -d -m 700 -- " + shlex.quote(assets_path))
        for name, data in assets.items():
            remote_file = _remote_path(assets_path, name)
            guest.run("umask 077; cat > " + shlex.quote(remote_file), input_bytes=data)
            actual_sha = _one_line(guest.run("sha256sum -- " + shlex.quote(remote_file)), "protected qualification asset hash").split()[0]
            if "sha256:" + actual_sha != assets_manifest["assets"][name]:
                raise HostGuestError("protected qualification asset changed during transfer: " + name)
        guest.run("cat > " + shlex.quote(verifier_path), input_file=args.first_publication_verifier, timeout=180)
        guest.run("chmod 700 " + shlex.quote(verifier_path))
        actual_verifier_line = _one_line(
            guest.run("sha256sum -- " + shlex.quote(verifier_path)), "transferred protected first-publication verifier hash",
        )
        if "sha256:" + actual_verifier_line.split()[0] != verifier_sha:
            raise HostGuestError("transferred first-publication verifier differs from its protected build")
        verifier_command = (
            "LEAPVIEWCTL_ROOT=/opt/leapview " + shlex.quote(verifier_path) +
            " qualify first-publication --evidence-dir " + shlex.quote(remote_evidence) +
            " --assets-root " + shlex.quote(assets_path)
        )
        guest.run(verifier_command, timeout=3600)
        report_file = _remote_path(remote_evidence, "first-publication-report.json")
        guest.run("test -f " + shlex.quote(report_file) + " && test ! -L " + shlex.quote(report_file))
        raw_report = guest.run("cat " + shlex.quote(report_file), timeout=30)
        try:
            decoded_report = _json(raw_report, "protected first-publication report", 1024**2)
        except qualification.QualificationError as exc:
            raise HostGuestError(f"protected first-publication report is invalid: {exc}") from exc
        report = _validate_first_publication_report(
            decoded_report, image=args.image, image_revision=image_runtime["revision"],
            environment=FIRST_PUBLICATION_ENVIRONMENT,
        )
        sanitized_report = _canonical(report) + b"\n"
        _assert_no_secrets_in_bytes(sanitized_report, fixture_secrets, "first-publication report")
    finally:
        guest.run(
            "rm -rf -- " + shlex.quote(remote_evidence) + " " + shlex.quote(assets_path) +
            " && rm -f -- " + shlex.quote(verifier_path) +
            " && test ! -e " + shlex.quote(remote_evidence) +
            " && test ! -e " + shlex.quote(assets_path) +
            " && test ! -e " + shlex.quote(verifier_path),
            timeout=60,
        )
    if report is None:
        raise HostGuestError("protected first-publication verifier produced no report")
    after = _wait_for_https_readiness(
        guest, expected="200", timeout=args.startup_timeout, evidence=evidence,
        filename="first-publication-https-readiness-after.txt",
    )
    if report["readinessBefore"] != int(before) or report["readinessAfter"] != int(after):
        raise HostGuestError("independent localhost HTTPS readiness probes differ from the protected report")
    _record(evidence, "first-publication-report.json", sanitized_report)
    return report, {
        "protectedRevision": protected_revision,
        "verifierSHA256": verifier_sha,
        "qualificationAssetsSHA256": assets_sha,
    }


def _record(evidence: Path, name: str, data: bytes) -> bytes:
    if len(data) > MAX_GUEST_OUTPUT_BYTES:
        raise HostGuestError(f"guest evidence {name} exceeded its byte limit")
    _write_new(evidence / name, data, 0o600)
    return data


def _remote_path(directory: str, name: str) -> str:
    return directory + "/" + name


def _guest_prerequisite(guest: SSHGuest, evidence: Path, stage: str, command: str, *, timeout: int) -> None:
    # Use only for credential-free host prerequisites, before the PostgreSQL
    # fixture exists. Keep general SSH/installer output private by default.
    probe = (
        "set +e; umask 077; log=$(mktemp) || exit 1; trap 'rm -f -- \"$log\"' EXIT; "
        "timeout --kill-after=10s " + str(timeout) + "s bash -c " + shlex.quote(command) +
        " >\"$log\" 2>&1; result=$?; "
        "printf '%s\\n' \"$result\"; tail -c 65536 \"$log\"; exit 0"
    )
    try:
        # Let the guest stop its child and return the bounded log before the
        # transport deadline; killing SSH first loses the failing command's output.
        result = guest.run(probe, timeout=timeout + 30)
    except HostGuestError as exc:
        raise HostGuestError(f"guest prerequisite {stage}: {exc}") from exc
    status, separator, diagnostic = result.partition(b"\n")
    if not separator or re.fullmatch(rb"[0-9]{1,3}", status) is None or int(status) > 255:
        raise HostGuestError(f"guest prerequisite {stage} returned an invalid exit status")
    _record(evidence, f"prerequisite-{stage}-exit-code.txt", status + b"\n")
    _record(evidence, f"prerequisite-{stage}.log", diagnostic)
    if int(status) != 0:
        raise HostGuestError(f"guest prerequisite {stage} failed ({int(status)}); see retained prerequisite-{stage}.log")


def _guest_package_setup(guest: SSHGuest, guest_os: str, *, evidence: Path) -> None:
    compose_package, extra_packages = {
        "ubuntu2404": ("docker-compose-v2", []),
        "debian13": ("docker-compose", ["docker-cli"]),
    }[guest_os]
    packages = ["ca-certificates", "docker.io", compose_package, *extra_packages, "openssl", "unattended-upgrades"]
    package_list = " ".join(shlex.quote(package) for package in packages)
    steps = (
        ("apt-update", "DEBIAN_FRONTEND=noninteractive apt-get update"),
        ("apt-install", "DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends " + package_list),
        ("docker-enable", "systemctl enable --now docker || { result=$?; journalctl -u docker --no-pager -n 50; exit \"$result\"; }"),
        ("docker-version", "docker version"),
        ("compose-version", "docker compose version"),
    )
    deadline = time.monotonic() + 600
    for stage, command in steps:
        remaining = int(deadline - time.monotonic())
        if remaining <= 0:
            raise HostGuestError(f"guest prerequisite {stage}: package setup timed out")
        _guest_prerequisite(guest, evidence, stage, command, timeout=remaining)


POSTGRES_PASSWORD_KEYS = {
    "bootstrap": "POSTGRES_PASSWORD",
    "controlRuntime": "LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD",
    "controlReadonly": "LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD",
    "duckLakeRuntime": "LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD",
    "controlMigrator": "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD",
    "controlUpgrade": "LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD",
    "controlMaintenance": "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD",
    "duckLakeMigrator": "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD",
    "duckLakeMaintenance": "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD",
}
POSTGRES_URLS = (
    ("controlUrl", "controlRuntime", "leapview_control", "leapview_control_runtime"),
    ("controlMigratorUrl", "controlMigrator", "leapview_control", "leapview_control_migrator"),
    ("controlMaintenanceUrl", "controlMaintenance", "leapview_control", "leapview_control_maintenance"),
    ("duckLakeUrl", "duckLakeRuntime", "leapview_ducklake", "leapview_ducklake_runtime"),
    ("duckLakeMaintenanceUrl", "duckLakeMaintenance", "leapview_ducklake", "leapview_ducklake_maintenance"),
    ("duckLakeMigratorUrl", "duckLakeMigrator", "leapview_ducklake", "leapview_ducklake_migrator"),
)
TLS_ROLE_EXPECTATIONS = {
    "controlRuntime": "leapview_control_runtime|leapview_control|true",
    "duckLakeRuntime": "leapview_ducklake_runtime|leapview_ducklake|true",
}


def _locked_postgres_image(source_root: Path) -> str:
    source = source_root / "internal/app/cli/composectl/qualification_native_postgres.go"
    data = _read(source, "locked PostgreSQL qualification image source", 2 * 1024**2)
    match = re.search(rb'const\s+qualificationPostgreSQL18Image\s*=\s*"([^"\r\n]+)"', data)
    if match is None:
        raise HostGuestError("source tree lacks the locked PostgreSQL qualification image")
    image = match.group(1).decode("ascii")
    if re.fullmatch(r"docker\.io/library/postgres:18-alpine@sha256:[0-9a-f]{64}", image) is None:
        raise HostGuestError("source tree PostgreSQL qualification image is not the locked PostgreSQL 18 Alpine digest")
    return image


def _postgres_repo_digest(image: str) -> str:
    match = re.fullmatch(r"(docker\.io/library/postgres):18-alpine@(sha256:[0-9a-f]{64})", image)
    if match is None:
        raise HostGuestError("source-locked PostgreSQL image reference is invalid")
    # Docker normalizes the default registry and library namespace in
    # RepoDigests for the canonical Hub `postgres` image.
    return "postgres@" + match.group(2)


def _postgres_fixture_credentials() -> dict[str, str]:
    values = {name: secrets.token_hex(24) for name in POSTGRES_PASSWORD_KEYS}
    if len(set(values.values())) != len(values):
        raise HostGuestError("generated PostgreSQL fixture credentials are not unique")
    return values


def _postgres_fixture_environment(credentials: dict[str, str]) -> bytes:
    if set(credentials) != set(POSTGRES_PASSWORD_KEYS) or len(set(credentials.values())) != len(credentials):
        raise HostGuestError("PostgreSQL fixture credentials have an unsupported schema")
    lines = ["POSTGRES_DB=postgres", "POSTGRES_USER=leapview_bootstrap"]
    for name, key in POSTGRES_PASSWORD_KEYS.items():
        value = credentials[name]
        if re.fullmatch(r"[0-9a-f]{48}", value) is None:
            raise HostGuestError("PostgreSQL fixture credential is not canonical hexadecimal")
        lines.append(f"{key}={value}")
    return ("\n".join(lines) + "\n").encode("ascii")


def _postgres_connection_urls(credentials: dict[str, str]) -> dict[str, str]:
    urls = {}
    for field, credential, database, role in POSTGRES_URLS:
        userinfo = urllib.parse.quote(role, safe="") + ":" + urllib.parse.quote(credentials[credential], safe="")
        query = urllib.parse.urlencode({
            "sslmode": "verify-full",
            "sslrootcert": "/var/lib/leapview/home/postgres-root.crt",
        })
        urls[field] = f"postgres://{userinfo}@postgres:5432/{database}?{query}"
    return urls


def _replace_env_values(data: bytes, replacements: dict[str, str]) -> bytes:
    try:
        lines = data.decode("utf-8").splitlines()
    except UnicodeDecodeError as exc:
        raise HostGuestError("private pool-probe environment template is not UTF-8") from exc
    found: dict[str, int] = {}
    output = []
    for line in lines:
        key, separator, _ = line.partition("=")
        if separator and key in replacements:
            found[key] = found.get(key, 0) + 1
            value = replacements[key]
            if "\n" in value or "\r" in value or "\x00" in value:
                raise HostGuestError("pool-probe environment value contains a line break or NUL")
            output.append(key + "=" + value)
        else:
            output.append(line)
    if set(found) != set(replacements) or any(count != 1 for count in found.values()):
        raise HostGuestError("pool-probe environment template has missing or duplicate required keys")
    return ("\n".join(output) + "\n").encode("utf-8")


def _pool_probe_environment(template: bytes, urls: dict[str, str], config: dict) -> bytes:
    replacements = {
        "LEAPVIEW_ENVIRONMENT": "prod",
        "LEAPVIEW_PUBLIC_URL": "https://leapview-qualification.invalid",
        "LEAPVIEW_ALLOWED_HOSTS": "leapview-qualification.invalid",
        "LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL": config["adminEmail"],
        "LEAPVIEW_CSRF_KEY": secrets.token_hex(32),
        "LEAPVIEW_METRICS_BEARER_TOKEN": secrets.token_hex(32),
        "LEAPVIEW_AGENT_CREDENTIAL_KEY": secrets.token_hex(32),
        "LEAPVIEW_POSTGRES_CONTROL_URL": urls["controlUrl"],
        # The pool probe performs no migration. Migrator credentials remain
        # outside every Compose serving environment, including this one.
        "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL": "",
        "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL": urls["controlMaintenanceUrl"],
        "LEAPVIEW_POSTGRES_DUCKLAKE_URL": urls["duckLakeUrl"],
        "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL": urls["duckLakeMaintenanceUrl"],
        "LEAPVIEW_POSTGRES_REQUIRE_TLS": "true",
    }
    return _replace_env_values(template, replacements)


def _qualification_operator_config(pool_output: bytes, urls: dict[str, str]) -> bytes:
    if len(pool_output) > 1024**2:
        raise HostGuestError("canonical pool qualification output exceeds the operator-input size limit")
    try:
        artifacts = _json(pool_output, "image-generated physical-pool qualification artifacts", 1024**2)
    except qualification.QualificationError as exc:
        raise HostGuestError(f"image-generated physical-pool qualification output is invalid: {exc}") from exc
    if (not isinstance(artifacts, dict) or set(artifacts) != {"schema_version", "pool", "evidence"}
            or type(artifacts["schema_version"]) is not int or artifacts["schema_version"] != 1
            or not isinstance(artifacts["pool"], dict) or not isinstance(artifacts["evidence"], dict)
            or set(urls) != {field for field, _, _, _ in POSTGRES_URLS}):
        raise HostGuestError("image-generated physical-pool qualification output has an unsupported schema")
    operator = {
        "schemaVersion": 1,
        "postgresProfile": "external",
        "postgres": urls,
        "physicalPool": {"pool": artifacts["pool"], "evidence": artifacts["evidence"]},
    }
    encoded = _canonical(operator) + b"\n"
    if len(encoded) > 1024**2:
        raise HostGuestError("private operator bootstrap input exceeds the host installer size limit")
    return encoded


def _postgres_tls_entrypoint_script() -> str:
    return "\n".join((
        "set -eu",
        "mkdir -p /tmp/leapview-postgres-tls /var/lib/leapview/home",
        "cp /run/secrets/leapview-postgres-ca.pem /tmp/leapview-postgres-tls/ca.pem",
        "cp /run/secrets/leapview-postgres-ca.pem /var/lib/leapview/home/postgres-root.crt",
        "chmod 0644 /var/lib/leapview/home/postgres-root.crt",
        "cp /run/secrets/leapview-postgres-server.pem /tmp/leapview-postgres-tls/server.pem",
        "cp /run/secrets/leapview-postgres-server.key /tmp/leapview-postgres-tls/server.key",
        "chown -R postgres:postgres /tmp/leapview-postgres-tls",
        "chmod 0644 /tmp/leapview-postgres-tls/ca.pem /tmp/leapview-postgres-tls/server.pem",
        "chmod 0600 /tmp/leapview-postgres-tls/server.key",
        "exec /usr/local/bin/docker-entrypoint.sh postgres -c ssl=on -c ssl_ca_file=/tmp/leapview-postgres-tls/ca.pem -c ssl_cert_file=/tmp/leapview-postgres-tls/server.pem -c ssl_key_file=/tmp/leapview-postgres-tls/server.key",
    ))


def _postgres_readiness_command(container_name: str) -> str:
    sql = "SELECT current_user::text || '|' || current_database()::text || '|' || (SELECT ssl::text FROM pg_stat_ssl WHERE pid=pg_backend_pid())"
    checks = []
    for name, expected in TLS_ROLE_EXPECTATIONS.items():
        database = "leapview_control" if name == "controlRuntime" else "leapview_ducklake"
        role = "leapview_control_runtime" if name == "controlRuntime" else "leapview_ducklake_runtime"
        checks.append(
            "actual=$(docker exec " + shlex.quote(container_name) + " sh -ec " + shlex.quote(
                "export PGPASSWORD=\"$" + POSTGRES_PASSWORD_KEYS[name] + "\" PGSSLMODE=verify-full PGSSLROOTCERT=/tmp/leapview-postgres-tls/ca.pem; "
                "psql --host=postgres --username=" + role + " --dbname=" + database + " --tuples-only --no-align --command=" + shlex.quote(sql)
            ) + "); test \"$actual\" = " + shlex.quote(expected) + "; printf '%s\\n' \"$actual\""
        )
    return "set -eu; " + "; ".join(checks)


def _postgres_readiness_wait_command(container_name: str, docker_env: str) -> str:
    # A separate shell keeps the probe's errexit active when its exit status is
    # tested by `if`. Retain only one complete, successful two-role TLS probe.
    probe = "sh -ec " + shlex.quote(_postgres_readiness_command(container_name))
    return (
        "set -eu; i=0; while [ \"$i\" -lt 60 ]; do i=$((i+1)); "
        "if state=$(env " + docker_env + " docker inspect --format '{{.State.Status}}' " + shlex.quote(container_name) +
        " 2>/dev/null) && [ \"$state\" = running ]; then if readiness=$(" + probe +
        " 2>/dev/null); then printf '%s\\n' \"$readiness\"; exit 0; fi; fi; sleep 2; done; exit 1"
    )


def _serving_credential_boundary_command(root: str = "/opt/leapview") -> str:
    # The current generation owns immutable payload templates. The installer
    # writes the private mutable serving environment in the installation root.
    return (
        "set -eu; file=" + shlex.quote(root + "/leapview.env") + "; test -s \"$file\"; "
        "if grep -Eq '^(LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL|LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL)=' \"$file\" 2>/dev/null; then exit 1; "
        "else status=$?; test \"$status\" -eq 1; fi; "
        "printf '{\"controlMigratorURLAbsentFromServingEnvironment\":true,\"duckLakeMigratorURLAbsentFromServingEnvironment\":true}\\n'"
    )


def _compose_command(project_dir: str, docker_env: str) -> str:
    return (
        "env " + docker_env + " docker compose --project-name leapview --project-directory " + shlex.quote(project_dir) +
        " --env-file " + shlex.quote(project_dir + "/deployment.env") +
        " --file " + shlex.quote(project_dir + "/compose.yaml")
    )


def _pool_qualification_command(project_dir: str, docker_env: str) -> str:
    command = "test \"$(id -u)\" = 999; test -w /var/lib/leapview/home; exec /usr/local/bin/leapview admin delivery pool qualify"
    return (
        _compose_command(project_dir, docker_env) +
        " run --rm --no-deps --no-TTY --entrypoint sh leapview -ec " + shlex.quote(command)
    )


def _pool_probe_state_command(project_dir: str, docker_env: str) -> str:
    command = "printf '%s\\n' \"$(id -u):$(id -g):$(stat -c '%u:%g:%a' /var/lib/leapview/home)\"; test \"$(id -u)\" = 999; test -w /var/lib/leapview/home"
    return (
        _compose_command(project_dir, docker_env) +
        " run --rm --no-deps --no-TTY --entrypoint sh leapview -ec " + shlex.quote(command)
    )


def _host_install_command(*, mode: str, docker_env: str, controller_path: str, config_path: str,
                          payload_path: str, image: str) -> str:
    if mode == "bootstrap":
        return (
            "set -eu; test -s /run/leapview/operator-bootstrap.json; test ! -e /opt/leapview; "
            "env " + docker_env + " bash " + shlex.quote(controller_path) + " install"
        )
    if mode == "nix-controller":
        return (
            "env " + docker_env + " LEAPVIEWCTL_ROOT=/opt/leapview " + shlex.quote(controller_path) +
            " host install --config " + shlex.quote(config_path) + " --payload " + shlex.quote(payload_path) +
            " --source-image " + shlex.quote(image) + " --operator-config /run/leapview/operator-bootstrap.json"
        )
    raise HostGuestError("host install mode is unsupported")


def _run_host_installer(guest: SSHGuest, evidence: Path, command: str, *, timeout: int,
                        fixture_secrets: list[str]) -> None:
    # Drain both streams into a bounded tail, in a private, guest-local temporary
    # file. Never retain raw installer output: it may include generated bootstrap
    # credentials unknown to the collector. Only fixed vocabulary leaves memory.
    script = (
        "set +e; set -o pipefail; umask 077; log=$(mktemp) || exit 1; "
        "trap 'rm -f -- \"$log\"' EXIT; timeout --kill-after=10s " + str(timeout) +
        "s bash -c " + shlex.quote(command) + " 2>&1 | tail -c 65536 >\"$log\"; "
        "result=$?; printf '%s\\n' \"$result\"; cat \"$log\"; exit 0"
    )
    output = guest.run("bash -c " + shlex.quote(script), timeout=timeout + 30)
    status, separator, tail = output.partition(b"\n")
    if (not separator or re.fullmatch(rb"[0-9]{1,3}", status) is None
            or int(status) > 255 or len(tail) > 65536):
        raise HostGuestError("guest installer returned an invalid exit status or output bound")
    code = int(status)
    text = tail.decode("utf-8", errors="replace").lower() if code else ""
    boundaries = {
        "host-config": "validate host installation configuration",
        "payload": "validate host installation payload",
        "operator-config": "operator bootstrap configuration",
        "stage-generation": "stage deployment generation",
        "install-links": "install deployment links",
        "activate-generation": "activate deployment generation",
        "deployment-environment": "install deployment environment",
        "prepare-postgres-pool": "prepare production postgresql and delivery-pool bootstrap",
        "postgres-connections": "validate first-install postgresql connections",
        "pool-identity": "validate first-install physical-pool identity",
        "pool-evidence": "validate first-install physical-pool evidence",
        "pool-artifacts": "prepare first-install physical-pool artifacts",
        "pool-dry-run": "dry-run first-install physical-pool bootstrap",
        "initialize": "initialize leapview",
        "pool-apply": "apply production delivery-pool bootstrap",
        "private-marker": "write private-bootstrap installation marker",
        "private-start": "start leapview in private first-install bootstrap",
    }
    causes = {
        "permission-denied": "permission denied", "connection-refused": "connection refused",
        "postgres-authentication": "password authentication failed", "dns": "no such host",
        "tls-certificate": "certificate", "missing-file": "no such file or directory",
        "invalid-json": "not strict json", "invalid-schema": "required schema",
        "runtime-role": "invalid role identity", "tls-mode": "must set sslmode=verify-full",
        "credential-alias": "credential aliases", "database-identity": "unexpected database",
        "pool-output": "physical-pool bootstrap returned", "pool-compatibility": "compatibility differ",
        "missing-bootstrap-input": "bootstrap input is missing",
        "missing-prerequisites": "host prerequisites are missing",
        "controller-not-executable": "leapview deployment controller is not executable on the payload filesystem",
        "docker-unavailable": "cannot connect to the docker daemon", "image-pull": "pull access denied",
        "csrf-key": "leapview_csrf_key", "agent-key": "leapview_agent_credential_key",
        "disk-full": "no space left on device", "deadline": "deadline exceeded",
    }
    matched_causes = [key for key, phrase in causes.items() if phrase in text]
    if code in (124, 137):
        matched_causes.append("timeout" if code == 124 else "killed")
    diagnostic = _canonical({
        "schemaVersion": 1, "scope": "nix-compose-host-installer-diagnostic", "exitCode": code,
        "outputTailBytes": len(tail), "outputTailLimit": 65536,
        "boundaries": [key for key, phrase in boundaries.items() if phrase in text],
        "causes": matched_causes or (["unclassified"] if code else []),
    }) + b"\n"
    _assert_no_secrets_in_bytes(diagnostic, fixture_secrets, "installer diagnostic")
    _record(evidence, "host-install-exit-code.txt", status + b"\n")
    _record(evidence, "host-install-diagnostic.json", diagnostic)
    if code:
        raise HostGuestError(f"selected fresh-host installer failed ({code}); see retained host-install-diagnostic.json")


def _bootstrap_prepare_command(bootstrap_path: str, docker_env: str) -> str:
    return "env " + docker_env + " bash " + shlex.quote(bootstrap_path) + " prepare-host"


def _openssl_setup_command() -> str:
    return (
        "if ! command -v openssl >/dev/null 2>&1; then "
        "DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends openssl || exit $?; "
        "fi; openssl version"
    )


def _assert_no_secrets_in_evidence(evidence: Path, secrets_to_check: list[str]) -> None:
    encoded = [value.encode("utf-8") for value in secrets_to_check if value]
    for path in evidence.iterdir():
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode):
            raise HostGuestError("host guest evidence contains a non-regular file")
        contents = _read(path, "host guest evidence " + path.name, MAX_GUEST_OUTPUT_BYTES)
        if any(secret in contents for secret in encoded):
            raise HostGuestError("private PostgreSQL fixture credential appeared in retained guest evidence")


def _assert_no_secrets_in_bytes(data: bytes, secrets_to_check: list[str], label: str) -> None:
    unsafe_patterns = (
        rb"(?i)authorization\s*:\s*bearer\s+[^\s\"']+",
        rb"(?i)\"(?:accessToken|refreshToken|publisherToken|workloadToken|deliveryEvidenceToken|"
        rb"connectionEvidenceToken|recoveryUploadToken|recoveryControlToken|auditToken|temporaryPassword|"
        rb"qualificationPassword|password|clientSecret|apiKey|token)\"\s*:\s*\"[^\"]+\"",
        rb"(?<![A-Za-z0-9_-])eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]+)?",
    )
    encoded = [value.encode("utf-8") for value in secrets_to_check if value]
    if any(secret in data for secret in encoded) or any(re.search(pattern, data) for pattern in unsafe_patterns):
        raise HostGuestError(f"secret material appeared in sanitized {label}")


def _readiness_after_reboot_command(container_name: str, expected: str) -> str:
    return (
        "set -eu; actual=$(docker exec " + shlex.quote(container_name) + " sh -ec " + shlex.quote(
            "export PGPASSWORD=\"$" + POSTGRES_PASSWORD_KEYS["controlRuntime"] + "\" PGSSLMODE=verify-full PGSSLROOTCERT=/tmp/leapview-postgres-tls/ca.pem; "
            "psql --host=postgres --username=leapview_control_runtime --dbname=leapview_control --tuples-only --no-align --command="
            + shlex.quote("SELECT current_user::text || '|' || current_database()::text || '|' || (SELECT ssl::text FROM pg_stat_ssl WHERE pid=pg_backend_pid())")
        ) + "); test \"$actual\" = " + shlex.quote(expected) + "; printf '%s\\n' \"$actual\""
    )


def _transfer_postgres_init(guest: SSHGuest, evidence: Path, source: Path, destination: str) -> str:
    # verify_bundle authenticated this package asset against the clean release
    # source. The OCI deployment payload does not contain packaging-only fixtures.
    data = _read(source, "verified bundle PostgreSQL init script", qualification.compose_bundle.MAX_ASSET_BYTES)
    expected = _digest(data)
    _record(evidence, "postgres-init-bundle-sha256.txt", (expected + "\n").encode())
    guest.run("umask 077; cat > " + shlex.quote(destination), input_bytes=data)
    guest.run("chmod 644 " + shlex.quote(destination))
    measured = "sha256:" + _one_line(guest.run(
        "sha256sum " + shlex.quote(destination),
    ), "transferred PostgreSQL init script hash").split()[0]
    if measured != expected:
        raise HostGuestError("transferred PostgreSQL init script differs from the verified Compose archive")
    return expected


def _prepare_pool_fixture(guest: SSHGuest, evidence: Path, *, args, paths: dict, docker_env: str,
                          host_config: dict, nonce: str, candidate_image_id: str,
                          postgres_init: Path) -> tuple[dict, dict, list[str]]:
    postgres_image = _locked_postgres_image(Path(args.source_root))
    credentials = _postgres_fixture_credentials()
    urls = _postgres_connection_urls(credentials)
    project_dir = _remote_path(paths["root"], "pool-probe")
    fixture_dir = _remote_path(paths["root"], "postgres-fixture")
    tls_dir = _remote_path(fixture_dir, "tls")
    data_volume = "leapview-qualification-pg-" + nonce[:16]
    postgres_name = "leapview-qualification-pg-" + nonce[:16]
    network_name = "leapview_default"
    state_volume = "leapview_leapview-state"
    compose = _compose_command(project_dir, docker_env)

    guest.run("install -d -m 700 -- " + " ".join(shlex.quote(path) for path in (project_dir, fixture_dir, tls_dir)))
    guest.run("cp -- " + shlex.quote(paths["payload"] + "/compose.yaml") + " " + shlex.quote(project_dir + "/compose.yaml"))
    init_script_sha = _transfer_postgres_init(guest, evidence, postgres_init, fixture_dir + "/postgres-init.sh")
    guest.run("chmod 600 " + shlex.quote(project_dir + "/compose.yaml"))
    env_template = guest.run("cat " + shlex.quote(paths["payload"] + "/leapview.env.example"))
    probe_env = _pool_probe_environment(env_template, urls, host_config)
    deployment_env = (
        "COMPOSE_PROJECT_NAME=leapview\nLEAPVIEW_IMAGE=" + args.image + "\nCOMPOSE_APP_BIND=127.0.0.1:8080\n"
    ).encode("ascii")
    postgres_env = _postgres_fixture_environment(credentials)
    guest.run("umask 077; cat > " + shlex.quote(project_dir + "/leapview.env"), input_bytes=probe_env)
    guest.run("chmod 600 " + shlex.quote(project_dir + "/leapview.env"))
    guest.run("umask 077; cat > " + shlex.quote(project_dir + "/deployment.env"), input_bytes=deployment_env)
    guest.run("chmod 600 " + shlex.quote(project_dir + "/deployment.env"))
    guest.run("umask 077; cat > " + shlex.quote(fixture_dir + "/postgres.env"), input_bytes=postgres_env)
    guest.run("chmod 600 " + shlex.quote(fixture_dir + "/postgres.env"))

    ca_key = tls_dir + "/ca.key"
    ca_cert = tls_dir + "/ca.pem"
    server_key = tls_dir + "/server.key"
    server_csr = tls_dir + "/server.csr"
    server_cert = tls_dir + "/server.pem"
    guest.run(
        "set -eu; openssl req -x509 -newkey rsa:2048 -nodes -keyout " + shlex.quote(ca_key) +
        " -out " + shlex.quote(ca_cert) + " -subj /CN=leapview-qualification-ca -days 30 "
        "-addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign,cRLSign'; "
        "openssl req -newkey rsa:2048 -nodes -keyout " + shlex.quote(server_key) + " -out " + shlex.quote(server_csr) +
        " -subj /CN=postgres -addext 'subjectAltName=DNS:postgres'; "
        "printf 'subjectAltName=DNS:postgres\\nextendedKeyUsage=serverAuth\\n' > " + shlex.quote(tls_dir + "/server.ext") + "; "
        "openssl x509 -req -in " + shlex.quote(server_csr) + " -CA " + shlex.quote(ca_cert) + " -CAkey " +
        shlex.quote(ca_key) + " -CAcreateserial -out " + shlex.quote(server_cert) + " -days 30 -extfile " +
        shlex.quote(tls_dir + "/server.ext") + " >/dev/null 2>&1; "
        "rm -f -- " + shlex.quote(ca_key) + " " + shlex.quote(server_csr) + " " + shlex.quote(tls_dir + "/ca.srl") + "; "
        "chmod 600 " + shlex.quote(server_key) + "; chmod 644 " + shlex.quote(ca_cert) + " " +
        shlex.quote(server_cert) + " " + shlex.quote(tls_dir + "/server.ext"), timeout=120,
    )

    guest.run("env " + docker_env + " docker pull " + shlex.quote(postgres_image), timeout=300)
    postgres_repo_digest = _postgres_repo_digest(postgres_image)
    pg_repo_digests = _json(_record(evidence, "postgres-image-repo-digests.json", guest.run(
        "env " + docker_env + " docker image inspect --format '{{json .RepoDigests}}' " + shlex.quote(postgres_image),
    )), "pinned PostgreSQL RepoDigests")
    if not isinstance(pg_repo_digests, list) or postgres_repo_digest not in pg_repo_digests:
        raise HostGuestError("PostgreSQL fixture pull did not resolve to the source-locked immutable digest")
    pg_image_id = _one_line(_record(evidence, "postgres-image-id.txt", guest.run(
        "env " + docker_env + " docker image inspect --format '{{.Id}}' " + shlex.quote(postgres_image),
    )), "PostgreSQL image ID")
    pg_platform = _one_line(_record(evidence, "postgres-image-platform.txt", guest.run(
        "env " + docker_env + " docker image inspect --format '{{.Os}}/{{.Architecture}}' " + shlex.quote(postgres_image),
    )), "PostgreSQL image platform")
    if (re.fullmatch(r"sha256:[0-9a-f]{64}", pg_image_id) is None
            or pg_platform != args.platform):
        raise HostGuestError("pinned PostgreSQL image identity or native platform differs from the guest")

    # Compose materializes the canonical project network and state volume but
    # does not start the serving service. The one-shot qualification command
    # below is the first app binary invocation in this guest.
    guest.run(compose + " create --no-build leapview", timeout=300)
    guest.run(compose + " rm --force --stop leapview", timeout=120)
    network_labels = _one_line(_record(evidence, "postgres-compose-network-labels.txt", guest.run(
        "env " + docker_env + " docker network inspect --format '{{index .Labels \"com.docker.compose.project\"}} {{index .Labels \"com.docker.compose.network\"}}' " +
        shlex.quote(network_name),
    )), "Compose qualification network labels")
    volume_labels = _one_line(_record(evidence, "postgres-compose-state-volume-labels.txt", guest.run(
        "env " + docker_env + " docker volume inspect --format '{{index .Labels \"com.docker.compose.project\"}} {{index .Labels \"com.docker.compose.volume\"}}' " +
        shlex.quote(state_volume),
    )), "Compose qualification volume labels")
    if network_labels != "leapview default" or volume_labels != "leapview leapview-state":
        raise HostGuestError("pool fixture is not attached to the canonical Compose project network and state volume")

    guest.run("env " + docker_env + " docker volume create " + shlex.quote(data_volume) + " >/dev/null")
    postgres_start = " ".join((
        "env", docker_env, "docker run --detach --name", shlex.quote(postgres_name),
        "--restart unless-stopped --network", shlex.quote(network_name), "--network-alias postgres",
        "--env-file", shlex.quote(fixture_dir + "/postgres.env"),
        "--volume", shlex.quote(fixture_dir + "/postgres-init.sh:/docker-entrypoint-initdb.d/10-leapview-roles.sh:ro"),
        "--volume", shlex.quote(ca_cert + ":/run/secrets/leapview-postgres-ca.pem:ro"),
        "--volume", shlex.quote(server_cert + ":/run/secrets/leapview-postgres-server.pem:ro"),
        "--volume", shlex.quote(server_key + ":/run/secrets/leapview-postgres-server.key:ro"),
        "--volume", shlex.quote(state_volume + ":/var/lib/leapview"),
        "--volume", shlex.quote(data_volume + ":/var/lib/postgresql"),
        "--tmpfs /tmp:rw,nosuid,nodev,mode=1777,size=64m --entrypoint sh", shlex.quote(postgres_image),
        "-ec", shlex.quote(_postgres_tls_entrypoint_script()),
    ))
    guest.run(postgres_start, timeout=120)
    guest.run("rm -f -- " + shlex.quote(fixture_dir + "/postgres.env"))
    readiness = _postgres_readiness_wait_command(postgres_name, docker_env)
    role_probe = _record(evidence, "postgres-tls-role-probes-before-install.txt", guest.run(readiness, timeout=150))
    expected_probe = "\n".join(TLS_ROLE_EXPECTATIONS.values())
    if role_probe.decode("utf-8").strip() != expected_probe:
        raise HostGuestError("PostgreSQL fixture did not authenticate both native runtime roles over verified TLS")

    volume_owner = _one_line(_record(evidence, "pool-probe-state-volume-owner.txt", guest.run(
        _pool_probe_state_command(project_dir, docker_env), timeout=120,
    )), "pool-probe application volume ownership")
    if re.fullmatch(r"999:999:999:999:[0-7]{3}", volume_owner) is None:
        raise HostGuestError("fresh Compose state volume is not writable by the candidate's non-root runtime user")

    # `run` overrides ENTRYPOINT's default serve command with the exact pool
    # qualification CLI; no service container is started. Its output is the
    # canonical pool identity and conformance evidence consumed by host install.
    preconditions = guest.run(
        "set -eu; test ! -e /opt/leapview; test ! -e /opt/leapview/leapview.env; "
        "test -z \"$(" + compose + " ps --status running --quiet)\"; printf 'fresh-root-absent\\nserving-service-not-running\\n'",
    )
    _record(evidence, "pool-probe-preconditions.txt", preconditions)
    if preconditions.decode("utf-8").strip().splitlines() != ["fresh-root-absent", "serving-service-not-running"]:
        raise HostGuestError("pool qualification did not start from a fresh, stopped serving target")
    pool_command = _pool_qualification_command(project_dir, docker_env)
    pool_output = guest.run(pool_command, timeout=900)
    pool_artifacts = _qualification_operator_config(pool_output, urls)
    _record(evidence, "physical-pool-qualification-artifacts.json", pool_output)
    _record(evidence, "physical-pool-qualification-sha256.txt", (_digest(pool_output) + "\n").encode())
    operator_config_path = "/run/leapview/operator-bootstrap.json"
    guest.run("install -d -m 700 /run/leapview")
    guest.run("umask 077; cat > " + shlex.quote(operator_config_path), input_bytes=pool_artifacts)
    guest.run("chmod 600 " + shlex.quote(operator_config_path))
    guest.run("test -f " + shlex.quote(operator_config_path) + " && test \"$(stat -c '%a' " +
              shlex.quote(operator_config_path) + ")\" = 600")

    # Compose writes the command container's stdout only. Require no persistent
    # service container to be running before handing control to the installer.
    guest.run("test -z \"$(" + compose + " ps --status running --quiet)\"")
    guest.run("test ! -e /opt/leapview && test ! -e /opt/leapview/leapview.env")
    guest.run("rm -rf -- " + shlex.quote(project_dir))
    _record(evidence, "pool-probe-completed-before-install.txt", b"completed-before-install\n")
    _record(evidence, "pool-probe-order.json", (json.dumps({
        "schemaVersion": 1,
        "freshInstallRootAbsentBeforeProbe": True,
        "servingComposeServiceStartedBeforeInstall": False,
        "tlsRuntimeRolesProbedBeforeInstall": True,
        "canonicalPoolCommand": "admin delivery pool qualify",
        "candidateImage": args.image,
        "candidateImageID": candidate_image_id,
        "candidatePlatform": args.platform,
        "sourceRevision": args.source_revision,
        "operatorBootstrapWrittenAfterPoolProbe": True,
    }, sort_keys=True) + "\n").encode())
    final_init_sha = "sha256:" + _one_line(guest.run(
        "sha256sum " + shlex.quote(fixture_dir + "/postgres-init.sh"),
    ), "qualification PostgreSQL init script hash").split()[0]
    if final_init_sha != init_script_sha:
        raise HostGuestError("PostgreSQL fixture init script changed after verified bundle transfer")
    fixture = {
        "image": postgres_image,
        "repoDigest": postgres_repo_digest,
        "imageID": pg_image_id,
        "repoDigests": pg_repo_digests,
        "platform": pg_platform,
        "containerName": postgres_name,
        "network": network_name,
        "stateVolume": state_volume,
        "dataVolume": data_volume,
        "restartPolicy": "unless-stopped",
        "initScriptSHA256": init_script_sha,
        "tlsRoleProbesBeforeInstall": TLS_ROLE_EXPECTATIONS,
    }
    _record(evidence, "postgres-fixture.json", (json.dumps(fixture, sort_keys=True) + "\n").encode())
    return fixture, {"urls": urls, "poolArtifactsSHA256": _digest(pool_output)}, list(credentials.values()) + list(urls.values())


def _pull_payload(guest: SSHGuest, evidence: Path, *, image_reference: str, docker_env: str,
                 payload_path: str, nonce: str) -> tuple[list, str, str]:
    image = shlex.quote(image_reference)
    _guest_prerequisite(
        guest, evidence, "candidate-image-pull", "env " + docker_env + " docker pull " + image,
        timeout=CANDIDATE_IMAGE_PULL_TIMEOUT,
    )
    repo_digest_bytes = _record(evidence, "oci-repo-digests.json", guest.run(
        "env " + docker_env + " docker image inspect --format '{{json .RepoDigests}}' " + image,
    ))
    repo_digests = _json(repo_digest_bytes, "pulled OCI RepoDigests", 64 * 1024)
    if not isinstance(repo_digests, list) or image_reference not in repo_digests:
        raise HostGuestError("anonymous OCI pull did not resolve to the selected immutable image digest")
    pulled_image_id = _one_line(_record(evidence, "oci-image-id.txt", guest.run(
        "env " + docker_env + " docker image inspect --format '{{.Id}}' " + image,
    )), "pulled OCI image ID")
    if re.fullmatch(r"sha256:[0-9a-f]{64}", pulled_image_id) is None:
        raise HostGuestError("anonymous OCI pull returned an invalid image ID")
    container = "leapview-qual-" + nonce[:16]
    extraction = (
        "set -eu; " + docker_env + " docker create --name " + shlex.quote(container) + " " + image +
        " >/dev/null; c=" + shlex.quote(container) + "; " + docker_env +
        " docker cp \"$c:/usr/local/share/leapview/deployment/.\" " + shlex.quote(payload_path) +
        "; " + docker_env + " docker rm \"$c\" >/dev/null; test -x " +
        shlex.quote(payload_path + "/leapviewctl")
    )
    guest.run(extraction, timeout=120)
    payload_sha_line = _one_line(guest.run(
        "sha256sum " + shlex.quote(payload_path + "/leapviewctl"),
    ), "OCI payload controller hash")
    payload_sha = "sha256:" + payload_sha_line.split()[0]
    _record(evidence, "oci-payload-controller-sha256.txt", (payload_sha + "\n").encode())
    return repo_digests, pulled_image_id, payload_sha


def _install_and_collect(args) -> dict:
    if ipaddress.ip_address(args.host).is_loopback is False or args.host != "127.0.0.1":
        raise HostGuestError("guest SSH must target the IPv4 loopback address 127.0.0.1")
    if not 1024 <= args.port <= 65535:
        raise HostGuestError("guest SSH port must be an unprivileged ephemeral port")
    expected_arch = ARCHITECTURES[args.platform.removeprefix("linux/")]
    host_arch = _local_architecture()
    if host_arch != expected_arch:
        raise HostGuestError("qualification runner ISA must match the guest ISA")
    kvm = _kvm_probe()
    if args.virtualization_mode == "kvm" and not kvm["available"]:
        raise HostGuestError("KVM mode selected but /dev/kvm did not pass the API-version probe")
    _read(args.ssh_identity, "SSH identity", 1024 * 1024)
    known_hosts_bytes = _read(args.known_hosts, "SSH known-hosts input", 1024 * 1024)
    identity_info = args.ssh_identity.lstat()
    known_hosts_info = args.known_hosts.lstat()
    if not stat.S_ISREG(identity_info.st_mode) or stat.S_IMODE(identity_info.st_mode) & 0o077:
        raise HostGuestError("SSH identity must be a private regular file with mode 0600 or stricter")
    if not stat.S_ISREG(known_hosts_info.st_mode) or known_hosts_info.st_size == 0:
        raise HostGuestError("SSH known-hosts input must be a non-empty regular file")

    manifest_bytes = _read(args.guest_manifest, "launcher guest manifest", 64 * 1024)
    manifest = _validate_manifest(
        manifest_bytes, nonce=args.nonce, guest_os=args.guest_os, platform=args.platform,
        mode=args.virtualization_mode,
    )
    launcher_receipt_bytes = _read(args.launcher_receipt, "immutable launcher receipt", 64 * 1024)
    launcher_receipt = _validate_launcher_receipt(
        launcher_receipt_bytes, manifest=manifest, manifest_bytes=manifest_bytes,
        known_hosts_sha256=_digest(known_hosts_bytes),
    )
    config_bytes = _read(args.config, "private host configuration", 64 * 1024)
    host_config = _validate_config(config_bytes, args.image)
    _validate_first_publication_host_config(host_config)
    _protected_checkout_revision(Path(args.protected_root), args.protected_revision)
    _protected_qualification_assets(Path(args.protected_root))
    _protected_verifier_identity(args.first_publication_verifier)

    source_root = Path(args.source_root)
    with tempfile.TemporaryDirectory(prefix="nix-compose-host-guest-") as temporary:
        binding = qualification.verify_bundle(
            args.archive, args.sidecar, args.controller_build_identity, source_root, args.release_identity,
            platform=args.platform, source_revision=args.source_revision, image=args.image,
            extract_dir=Path(temporary) / "bundle",
        )
        controller_evidence.compare_controller_evidence(
            args.archive, args.sidecar, args.controller_build_identity, source_root, args.release_identity,
            platform=args.platform, source_revision=args.source_revision, image=args.image,
            binary_verifier=args.controller_binary_verifier,
            left_evidence_dir=args.original_controller_evidence,
            right_evidence_dir=args.retained_controller_evidence,
        )
        nix_controller = Path(binding["extractedController"])
        nix_controller_sha = binding["controllerSHA256"]
        identity_data = _read(args.release_identity, "release identity", qualification.compose_bundle.MAX_IDENTITY_BYTES)
        release_identity = _json(identity_data, "release identity", qualification.compose_bundle.MAX_IDENTITY_BYTES)

        output = args.output_dir
        if output.exists() or output.is_symlink():
            raise HostGuestError("guest qualification output directory must be new")
        output.mkdir(mode=0o700, parents=True)
        os.chmod(output, 0o700)
        evidence = output / "evidence"
        evidence.mkdir(mode=0o700)
        _record(evidence, "launcher-receipt.json", launcher_receipt_bytes)
        _record(evidence, "host-kvm-probe.json", (json.dumps(kvm, sort_keys=True) + "\n").encode())
        _record(evidence, "host-runner-architecture.txt", (host_arch + "\n").encode())
        guest = SSHGuest(port=args.port, identity=args.ssh_identity, known_hosts=args.known_hosts, timeout=30)
        root_check = guest.run("test \"$(id -u)\" = 0 && printf 'root\\n'")
        _record(evidence, "ssh-root.txt", root_check)
        actual_manifest = guest.run("cat /etc/leapview-qualification-guest.json")
        if _digest(actual_manifest) != _digest(manifest_bytes) or actual_manifest != manifest_bytes:
            raise HostGuestError("guest launcher manifest differs from the externally supplied manifest")
        _record(evidence, "launcher-manifest.json", actual_manifest)

        nonce = args.nonce
        remote_root = "/var/tmp/leapview-qualification-" + nonce
        guest.run("umask 077; mkdir -- " + shlex.quote(remote_root))
        _record(evidence, "fresh-install-state.txt", guest.run(
            "test ! -e /opt/leapview && test ! -e /etc/leapview && test ! -e /run/leapview && printf 'absent\\n'",
        ))
        os_before = _record(evidence, "preinstall-os-release.txt", guest.run("cat /etc/os-release"))
        arch_before = _one_line(_record(evidence, "preinstall-architecture.txt", guest.run("uname -m")), "guest architecture")
        boot_before = _one_line(_record(evidence, "preinstall-boot-id.txt", guest.run("cat /proc/sys/kernel/random/boot_id")), "preinstall boot ID")
        kernel_before = _one_line(_record(evidence, "preinstall-kernel.txt", guest.run("uname -r")), "preinstall kernel")
        os_id, os_version = _os_release(os_before)
        if (os_id, os_version) != GUEST_OS[args.guest_os]:
            raise HostGuestError("fresh guest OS does not match the selected supported guest image")
        if arch_before != expected_arch or BOOT_ID_RE.fullmatch(boot_before) is None:
            raise HostGuestError("fresh guest architecture or boot ID is invalid")

        paths = {
            "root": remote_root,
            "controller": _remote_path(remote_root, "leapviewctl-nix"),
            "config": _remote_path(remote_root, "host-config.json"),
            "bootstrap": _remote_path(remote_root, "bootstrap-linux.sh"),
            "payload": _remote_path(remote_root, "deployment"),
            "docker": _remote_path(remote_root, "docker-config"),
        }
        guest.run("install -d -m 700 -- " + shlex.quote(paths["docker"]))
        guest.run("printf '{}\\n' > " + shlex.quote(paths["docker"] + "/config.json") +
                  " && chmod 600 " + shlex.quote(paths["docker"] + "/config.json"))
        guest.run("cat > " + shlex.quote(paths["controller"]), input_file=nix_controller)
        guest.run("chmod 700 " + shlex.quote(paths["controller"]))
        if args.install_mode == "nix-controller":
            guest.run("cat > " + shlex.quote(paths["config"]), input_bytes=config_bytes)
            guest.run("chmod 600 " + shlex.quote(paths["config"]))
        remote_controller_sha = _one_line(guest.run("sha256sum " + shlex.quote(paths["controller"])), "transferred Nix controller hash").split()[0]
        if "sha256:" + remote_controller_sha != nix_controller_sha:
            raise HostGuestError("transferred Nix controller differs from the verified Compose archive")
        _record(evidence, "nix-controller-sha256.txt", (nix_controller_sha + "\n").encode())
        nix_runtime_data = _record(evidence, "nix-controller-runtime.json", guest.run(
            shlex.quote(paths["controller"]) + " version --format json",
        ))
        nix_runtime = _runtime_identity(nix_runtime_data, release_identity, "leapviewctl", "Nix controller runtime identity")
        _record(evidence, "nix-controller-help.txt", guest.run(shlex.quote(paths["controller"]) + " host --help"))

        docker_env = "DOCKER_CONFIG=" + shlex.quote(paths["docker"])
        image = shlex.quote(args.image)
        driver = "nix-archive" if args.install_mode == "nix-controller" else "source-bootstrap-linux.sh"
        bootstrap_sha = None
        if args.install_mode == "nix-controller":
            _guest_package_setup(guest, args.guest_os, evidence=evidence)
        else:
            bootstrap_path = source_root / "deploy/host/bootstrap-linux.sh"
            bootstrap_info = bootstrap_path.lstat()
            if not stat.S_ISREG(bootstrap_info.st_mode) or not bootstrap_info.st_mode & 0o111:
                raise HostGuestError("source host bootstrap must be a regular executable file")
            bootstrap_bytes = _read(bootstrap_path, "source host bootstrap script", 4 * 1024**2)
            bootstrap_sha = _digest(bootstrap_bytes)
            _record(evidence, "source-bootstrap-sha256.txt", (bootstrap_sha + "\n").encode())
            guest.run("cat > " + shlex.quote(paths["bootstrap"]), input_bytes=bootstrap_bytes)
            guest.run("chmod 700 " + shlex.quote(paths["bootstrap"]))
            transferred_bootstrap_sha = _one_line(guest.run(
                "sha256sum " + shlex.quote(paths["bootstrap"]),
            ), "transferred source bootstrap hash").split()[0]
            if "sha256:" + transferred_bootstrap_sha != bootstrap_sha:
                raise HostGuestError("transferred bootstrap differs from the exact source-tree script")
            _record(evidence, "guest-bootstrap-sha256.txt", (bootstrap_sha + "\n").encode())
            guest.run("install -d -m 700 /run/leapview")
            guest.run("cat > /run/leapview/bootstrap.json", input_bytes=config_bytes)
            guest.run("chmod 600 /run/leapview/bootstrap.json")
            guest.run("cat > /run/leapview/image-reference", input_bytes=(args.image + "\n").encode())
            guest.run("chmod 600 /run/leapview/image-reference")
            _guest_prerequisite(
                guest, evidence, "bootstrap-prepare", _bootstrap_prepare_command(paths["bootstrap"], docker_env),
                timeout=900,
            )
            _guest_prerequisite(
                guest, evidence, "openssl-install",
                _openssl_setup_command(), timeout=600,
            )
        guest.run("mkdir -m 700 -- " + shlex.quote(paths["payload"]))
        repo_digests, pulled_image_id, payload_sha = _pull_payload(
            guest, evidence, image_reference=args.image, docker_env=docker_env,
            payload_path=paths["payload"], nonce=nonce,
        )
        postgres_fixture, pool_info, fixture_secrets = _prepare_pool_fixture(
            guest, evidence, args=args, paths=paths, docker_env=docker_env,
            host_config=host_config, nonce=nonce, candidate_image_id=pulled_image_id,
            postgres_init=nix_controller.parent / "qualification/postgres-init.sh",
        )

        install = _host_install_command(
            mode=args.install_mode, docker_env=docker_env,
            controller_path=paths["bootstrap"] if args.install_mode == "bootstrap" else paths["controller"],
            config_path=paths["config"], payload_path=paths["payload"], image=args.image,
        )
        _run_host_installer(guest, evidence, install,
                            timeout=1800 if args.install_mode == "bootstrap" else 900,
                            fixture_secrets=fixture_secrets)
        _record(evidence, "installer-driver.txt", (driver + "\n").encode())
        boundary_result = guest.run(_serving_credential_boundary_command())
        boundary = _json(_record(evidence, "serving-credential-boundary.json", boundary_result), "serving credential boundary")
        if boundary != {
            "controlMigratorURLAbsentFromServingEnvironment": True,
            "duckLakeMigratorURLAbsentFromServingEnvironment": True,
        }:
            raise HostGuestError("first-install operation-only migrator URLs were persisted to serving environment")
        operator_cleanup = guest.run(
            "rm -f -- /run/leapview/operator-bootstrap.json; test ! -e /run/leapview/operator-bootstrap.json && printf 'absent\\n'",
        )
        _record(evidence, "operator-bootstrap-cleanup.txt", operator_cleanup)
        if _one_line(operator_cleanup, "operator bootstrap cleanup") != "absent":
            raise HostGuestError("guest-owned private operator bootstrap input was not removed after install")

        target_id = host_config.get("targetId", "")
        marker, marker_data = _capture_host_install_marker(
            guest, evidence, filename="host-marker-private-after-install.json",
            phase=FIRST_INSTALL_PRIVATE_PHASE, image=args.image, target_id=target_id,
        )
        if marker["adminEmail"] != host_config["adminEmail"].strip():
            raise HostGuestError("host install marker does not bind the selected administrator")
        marker_sha = _digest(marker_data)
        _record(evidence, "host-marker-sha256.txt", (marker_sha + "\n").encode())
        _record(evidence, "host-marker-projection.json", (json.dumps({
            "schemaVersion": marker["schemaVersion"], "image": marker["image"],
            "domain": marker["domain"], "environment": marker["environment"],
            "https": marker["https"], "targetId": marker["targetId"],
            "bootstrapPhase": marker["bootstrapPhase"], "generation": marker["generation"],
            "markerSHA256": marker_sha,
        }, sort_keys=True) + "\n").encode())
        cleanup = (
            "rm -f -- " + " ".join(shlex.quote(paths[key]) for key in ("controller", "config", "bootstrap")) +
            " && rm -rf -- " + shlex.quote(paths["payload"])
        )
        if args.install_mode == "bootstrap":
            cleanup += " && rm -f -- /run/leapview/bootstrap.json /run/leapview/image-reference && rmdir /run/leapview"
        else:
            cleanup += " && rmdir /run/leapview"
        guest.run(cleanup)
        boot_after_install = _one_line(guest.run("cat /proc/sys/kernel/random/boot_id"), "post-install boot ID")
        if boot_after_install != boot_before:
            raise HostGuestError("guest rebooted unexpectedly during installation")
        inspect_command = (
            "id=$(env " + docker_env + " docker ps -q --no-trunc --filter label=com.docker.compose.service=leapview); "
            "test -n \"$id\"; test \"$(printf '%s\\n' \"$id\" | wc -l)\" -eq 1; "
            "env " + docker_env + " docker inspect --format '{{.Id}} {{.Config.Image}} {{.Image}} {{.State.Status}} "
            "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} "
            "{{index .Config.Labels \"com.docker.compose.project\"}} "
            "{{index .Config.Labels \"com.docker.compose.service\"}} {{.State.StartedAt}}' \"$id\""
        )
        public_bind_config_raw = guest.run(_deployment_caddy_bind_config_command())
        public_bind_config = _parse_caddy_bind_config(public_bind_config_raw)
        _record(evidence, "public-caddy-bind-config.txt", public_bind_config_raw)
        pre_inspect = _one_line(_record(evidence, "prereboot-container-inspect.txt", guest.run(inspect_command)), "pre-reboot container inspection")
        pre_parts = pre_inspect.split()
        if len(pre_parts) != 8:
            raise HostGuestError("pre-reboot container inspection returned an unsupported field set")
        pre_container_id, pre_configured_image, pre_image_id, pre_state, pre_health, pre_project, pre_service, pre_started_at = pre_parts
        if (CONTAINER_RE.fullmatch(pre_container_id) is None or pre_configured_image != args.image
                or pre_image_id != pulled_image_id or pre_state != "running" or pre_health not in {"starting", "unhealthy"}
                or not pre_project or pre_service != "leapview" or not pre_started_at):
            raise HostGuestError("initial host install did not produce the selected running image before first publication")
        private_caddy_before_reboot = _capture_caddy_observation(
            guest, evidence, docker_env=docker_env, prefix="private-caddy-before-reboot",
            private=True,
        )
        private_app_ports_before_reboot = _capture_application_ports(
            guest, evidence, docker_env=docker_env, filename="application-port-bindings-before-reboot.json",
        )
        _wait_for_http_status(
            guest, path="/healthz", expected="200", timeout=args.startup_timeout,
            evidence=evidence, filename="private-bootstrap-healthz-before-reboot.txt",
        )
        _wait_for_http_status(
            guest, path="/readyz", expected="503", timeout=args.startup_timeout,
            evidence=evidence, filename="private-bootstrap-readyz-before-reboot.txt",
        )
        postgres_name = postgres_fixture["containerName"]
        postgres_inspect_command = (
            "env " + docker_env + " docker inspect --format '{{.Id}} {{.Image}} {{.State.Status}} "
            "{{.HostConfig.RestartPolicy.Name}} {{.State.StartedAt}}' " + shlex.quote(postgres_name)
        )
        postgres_before_line = _one_line(_record(evidence, "postgres-container-before-reboot.txt", guest.run(
            postgres_inspect_command,
        )), "pre-reboot PostgreSQL container inspection")
        postgres_before_parts = postgres_before_line.split()
        if (len(postgres_before_parts) != 5 or postgres_before_parts[1] != postgres_fixture["imageID"]
                or postgres_before_parts[2:4] != ["running", "unless-stopped"]
                or not postgres_before_parts[4]):
            raise HostGuestError("pinned PostgreSQL fixture is not running with persistent automatic restart")
        guest.run("systemctl reboot", timeout=15, allow_disconnect=True)

        boot_after = None
        deadline = time.monotonic() + args.reboot_timeout
        while time.monotonic() < deadline:
            try:
                candidate = _one_line(guest.run("cat /proc/sys/kernel/random/boot_id", timeout=12), "post-reboot boot ID")
            except HostGuestError:
                candidate = None
            if candidate and candidate != boot_before:
                boot_after = candidate
                break
            time.sleep(5)
        if boot_after is None or BOOT_ID_RE.fullmatch(boot_after) is None:
            raise HostGuestError("fresh guest did not return with a new boot ID")
        _record(evidence, "postreboot-boot-id.txt", (boot_after + "\n").encode())
        os_after = _record(evidence, "postreboot-os-release.txt", guest.run("cat /etc/os-release"))
        arch_after = _one_line(_record(evidence, "postreboot-architecture.txt", guest.run("uname -m")), "post-reboot guest architecture")
        kernel_after = _one_line(_record(evidence, "postreboot-kernel.txt", guest.run("uname -r")), "post-reboot kernel")
        if _os_release(os_after) != GUEST_OS[args.guest_os] or arch_after != expected_arch:
            raise HostGuestError("guest OS or architecture changed across reboot")

        pid1 = _one_line(_record(evidence, "postreboot-pid1.txt", guest.run("readlink /proc/1/exe")), "PID 1")
        docker_enabled = _one_line(_record(evidence, "docker-enabled.txt", guest.run("systemctl is-enabled docker")), "Docker enabled state")
        docker_active = _one_line(_record(evidence, "docker-active.txt", guest.run("systemctl is-active docker")), "Docker active state")
        if docker_enabled != "enabled" or docker_active != "active" or "systemd" not in pid1:
            raise HostGuestError("systemd or Docker did not remain enabled and active after reboot")

        postgres_after_line = None
        postgres_deadline = time.monotonic() + args.startup_timeout
        while time.monotonic() < postgres_deadline:
            try:
                candidate = _one_line(guest.run(postgres_inspect_command, timeout=20), "post-reboot PostgreSQL inspection")
                fields = candidate.split()
                if (len(fields) == 5 and fields[0] == postgres_before_parts[0]
                        and fields[1:4] == [postgres_fixture["imageID"], "running", "unless-stopped"]
                        and fields[4] != postgres_before_parts[4]):
                    probe = guest.run(_readiness_after_reboot_command(postgres_name, TLS_ROLE_EXPECTATIONS["controlRuntime"]), timeout=20)
                    if _one_line(probe, "post-reboot PostgreSQL TLS role probe") == TLS_ROLE_EXPECTATIONS["controlRuntime"]:
                        postgres_after_line = candidate
                        _record(evidence, "postgres-tls-role-probe-after-reboot.txt", probe)
                        break
            except HostGuestError:
                pass
            time.sleep(5)
        if postgres_after_line is None:
            raise HostGuestError("pinned TLS PostgreSQL fixture did not automatically restart with persisted role data")
        _record(evidence, "postgres-container-after-reboot.txt", (postgres_after_line + "\n").encode())

        inspect_line = None
        probe_history = []
        startup_deadline = time.monotonic() + args.startup_timeout
        while time.monotonic() < startup_deadline:
            try:
                candidate = _one_line(guest.run(inspect_command, timeout=20), "post-reboot container inspection")
                fields = candidate.split()
                probe_history.append({"elapsedSeconds": round(args.startup_timeout - max(0, startup_deadline - time.monotonic()), 3),
                                      "inspection": candidate})
                if (len(fields) == 8 and fields[0] == pre_container_id and fields[1] == args.image
                        and fields[2] == pulled_image_id and fields[3] == "running" and fields[4] in {"starting", "unhealthy"}
                        and fields[5] == pre_project and fields[6] == "leapview" and fields[7] != pre_started_at):
                    inspect_line = candidate
                    break
            except HostGuestError:
                probe_history.append({"elapsedSeconds": round(args.startup_timeout - max(0, startup_deadline - time.monotonic()), 3),
                                      "inspection": "unavailable"})
            time.sleep(5)
        if inspect_line is None:
            raise HostGuestError("application container did not automatically restart before first publication")
        _record(evidence, "postreboot-before-publication-container-inspect.txt", (inspect_line + "\n").encode())
        prepublication_parts = inspect_line.split()
        _, _, _, _, _, _, _, private_started_at = prepublication_parts
        private_marker_after_reboot, private_marker_after_reboot_raw = _capture_host_install_marker(
            guest, evidence, filename="host-marker-private-after-reboot.json",
            phase=FIRST_INSTALL_PRIVATE_PHASE, image=args.image, target_id=target_id,
        )
        if private_marker_after_reboot != marker:
            raise HostGuestError("pending private-bootstrap marker changed across reboot")
        private_caddy_after_reboot = _capture_caddy_observation(
            guest, evidence, docker_env=docker_env, prefix="private-caddy-after-reboot",
            private=True, expected_image=private_caddy_before_reboot["inspection"]["image"],
        )
        private_app_ports_after_reboot = _capture_application_ports(
            guest, evidence, docker_env=docker_env, filename="application-port-bindings-after-private-reboot.json",
        )
        if (private_caddy_after_reboot["portBindings"] != private_caddy_before_reboot["portBindings"]
                or private_caddy_after_reboot["caddyfileSHA256"] != private_caddy_before_reboot["caddyfileSHA256"]
                or private_caddy_after_reboot["inspection"]["startedAt"] == private_caddy_before_reboot["inspection"]["startedAt"]
                or private_app_ports_after_reboot != private_app_ports_before_reboot):
            raise HostGuestError("private Compose exposure did not survive the pending first-install reboot")
        _wait_for_http_status(
            guest, path="/healthz", expected="200", timeout=args.startup_timeout,
            evidence=evidence, filename="private-bootstrap-healthz-after-reboot.txt",
        )
        _wait_for_http_status(
            guest, path="/readyz", expected="503", timeout=args.startup_timeout,
            evidence=evidence, filename="private-bootstrap-readyz-after-reboot.txt",
        )
        status_result = guest.run(
            "set +e; env " + docker_env + " LEAPVIEWCTL_ROOT=/opt/leapview "
            "/opt/leapview/current/leapviewctl status >/dev/null 2>&1; "
            "result=$?; printf '%s\\n' \"$result\"; exit \"$result\"",
            timeout=120,
        )
        if _one_line(status_result, "installed controller status result") != "0":
            raise HostGuestError("installed controller status failed after automatic restart")
        _record(evidence, "controller-status.txt", b"0\n")
        current_target = _one_line(_record(evidence, "current-generation-link.txt", guest.run("readlink /opt/leapview/current")), "active generation link")
        wrapper_target = _one_line(_record(evidence, "controller-wrapper-link.txt", guest.run("readlink /usr/local/sbin/leapviewctl")), "host controller wrapper link")
        expected_generation = "sha256-" + args.image.rsplit("sha256:", 1)[1]
        if current_target != "releases/" + expected_generation or wrapper_target != "../../../opt/leapview/current/leapviewctl":
            raise HostGuestError("installed generation or controller wrapper link differs from the image")
        payload_installed_line = _one_line(_record(evidence, "installed-controller-sha256.txt", guest.run(
            "sha256sum /opt/leapview/current/leapviewctl",
        )), "installed OCI controller hash")
        installed_sha = "sha256:" + payload_installed_line.split()[0]
        if installed_sha != payload_sha:
            raise HostGuestError("installed controller bytes differ from the OCI deployment payload")
        app_runtime_data = _record(evidence, "installed-controller-runtime.json", guest.run(
            "LEAPVIEWCTL_ROOT=/opt/leapview /opt/leapview/current/leapviewctl version --format json",
        ))
        app_runtime = _runtime_identity(app_runtime_data, release_identity, "leapviewctl", "installed OCI controller runtime identity")
        first_publication_report, protected_verifier = _run_first_publication_qualification(
            guest, evidence, args=args, paths=paths, fixture_secrets=fixture_secrets, image_runtime=app_runtime,
        )
        direct_ready = _wait_for_http_status(
            guest, path="/readyz", expected="200", timeout=args.startup_timeout,
            evidence=evidence, filename="first-publication-direct-readyz-after.txt",
        )
        if first_publication_report["readinessAfter"] != int(direct_ready):
            raise HostGuestError("protected first-publication readiness differs from the independent loopback probe")
        private_marker_after_publication, private_marker_after_publication_raw = _capture_host_install_marker(
            guest, evidence, filename="host-marker-private-after-publication.json",
            phase=FIRST_INSTALL_PRIVATE_PHASE, image=args.image, target_id=target_id,
        )
        if private_marker_after_publication != marker:
            raise HostGuestError("first publication changed the pending host marker before explicit activation")
        private_caddy_after_publication = _capture_caddy_observation(
            guest, evidence, docker_env=docker_env, prefix="private-caddy-after-publication",
            private=True, expected_image=private_caddy_before_reboot["inspection"]["image"],
        )
        private_app_ports_after_publication = _capture_application_ports(
            guest, evidence, docker_env=docker_env,
            filename="application-port-bindings-after-publication.json",
        )
        if (private_caddy_after_publication["portBindings"] != private_caddy_before_reboot["portBindings"]
                or private_caddy_after_publication["caddyfileSHA256"] != private_caddy_before_reboot["caddyfileSHA256"]
                or private_app_ports_after_publication != private_app_ports_before_reboot):
            raise HostGuestError("first publication changed the private Compose exposure before explicit activation")

        activation_result = guest.run(
            "set +e; env " + docker_env + " LEAPVIEWCTL_ROOT=/opt/leapview "
            "/opt/leapview/current/leapviewctl activate-first-install >/dev/null 2>&1; "
            "result=$?; printf '%s\\n' \"$result\"; exit \"$result\"",
            timeout=900,
        )
        if _one_line(activation_result, "explicit first-install activation result") != "0":
            raise HostGuestError("installed controller did not explicitly activate the first publication")
        _record(evidence, "first-install-activation-exit-code.txt", b"0\n")
        public_marker, public_marker_raw = _capture_host_install_marker(
            guest, evidence, filename="host-marker-public-after-activation.json",
            phase=FIRST_INSTALL_PUBLIC_PHASE, image=args.image, target_id=target_id,
        )
        expected_public_marker = {**marker, "bootstrapPhase": FIRST_INSTALL_PUBLIC_PHASE}
        if public_marker != expected_public_marker:
            raise HostGuestError("public activation changed the immutable host installation configuration")
        public_caddy_after_activation = _wait_for_caddy_observation(
            guest, evidence, docker_env=docker_env, prefix="public-caddy-after-activation",
            private=False, timeout=args.startup_timeout,
            expected_image=private_caddy_before_reboot["inspection"]["image"],
            public_config=public_bind_config,
        )
        public_app_ports_after_activation = _capture_application_ports(
            guest, evidence, docker_env=docker_env,
            filename="application-port-bindings-after-activation.json",
        )
        if (public_caddy_after_activation["portBindings"] == private_caddy_after_publication["portBindings"]
                or public_caddy_after_activation["caddyfileSHA256"] == private_caddy_after_publication["caddyfileSHA256"]
                or public_app_ports_after_activation != private_app_ports_before_reboot):
            raise HostGuestError("first-install activation did not switch Caddy while preserving the loopback app listener")
        _wait_for_http_status(
            guest, path="/healthz", expected="200", timeout=args.startup_timeout,
            evidence=evidence, filename="public-activation-healthz.txt",
        )
        _wait_for_http_status(
            guest, path="/readyz", expected="200", timeout=args.startup_timeout,
            evidence=evidence, filename="public-activation-readyz.txt",
        )
        _wait_for_https_readiness(
            guest, expected="200", timeout=args.startup_timeout, evidence=evidence,
            filename="public-activation-https-readiness.txt",
        )

        guest.run("systemctl reboot", timeout=15, allow_disconnect=True)
        public_boot_after = None
        public_reboot_deadline = time.monotonic() + args.reboot_timeout
        while time.monotonic() < public_reboot_deadline:
            try:
                candidate = _one_line(guest.run("cat /proc/sys/kernel/random/boot_id", timeout=12), "public-phase reboot ID")
            except HostGuestError:
                candidate = None
            if candidate and candidate != boot_after:
                public_boot_after = candidate
                break
            time.sleep(5)
        if public_boot_after is None or BOOT_ID_RE.fullmatch(public_boot_after) is None:
            raise HostGuestError("guest did not return from the public-phase reboot with a new boot ID")
        _record(evidence, "post-public-reboot-boot-id.txt", (public_boot_after + "\n").encode())
        public_marker_after_reboot, public_marker_after_reboot_raw = _capture_host_install_marker(
            guest, evidence, filename="host-marker-public-after-reboot.json",
            phase=FIRST_INSTALL_PUBLIC_PHASE, image=args.image, target_id=target_id,
        )
        if public_marker_after_reboot != public_marker:
            raise HostGuestError("public host marker phase did not survive the post-activation reboot")
        public_caddy_after_reboot = _wait_for_caddy_observation(
            guest, evidence, docker_env=docker_env, prefix="public-caddy-after-reboot",
            private=False, timeout=args.startup_timeout,
            expected_image=public_caddy_after_activation["inspection"]["image"],
            public_config=public_bind_config,
        )
        public_app_ports_after_reboot = _capture_application_ports(
            guest, evidence, docker_env=docker_env,
            filename="application-port-bindings-after-public-reboot.json",
        )
        if (public_caddy_after_reboot["portBindings"] != public_caddy_after_activation["portBindings"]
                or public_caddy_after_reboot["caddyfileSHA256"] != public_caddy_after_activation["caddyfileSHA256"]
                or public_caddy_after_reboot["inspection"]["startedAt"] == public_caddy_after_activation["inspection"]["startedAt"]
                or public_app_ports_after_reboot != private_app_ports_before_reboot):
            raise HostGuestError("configured HTTPS proxy phase did not survive the public-phase reboot")
        _wait_for_http_status(
            guest, path="/healthz", expected="200", timeout=args.startup_timeout,
            evidence=evidence, filename="public-reboot-healthz.txt",
        )
        _wait_for_http_status(
            guest, path="/readyz", expected="200", timeout=args.startup_timeout,
            evidence=evidence, filename="public-reboot-readyz.txt",
        )
        _wait_for_https_readiness(
            guest, expected="200", timeout=args.startup_timeout, evidence=evidence,
            filename="public-reboot-https-readiness.txt",
        )

        postpublication_inspect = None
        postpublication_deadline = time.monotonic() + args.startup_timeout
        while time.monotonic() < postpublication_deadline:
            try:
                candidate = _one_line(guest.run(inspect_command, timeout=20), "post-publication container inspection")
                fields = candidate.split()
                probe_history.append({
                    "elapsedSeconds": round(args.startup_timeout - max(0, postpublication_deadline - time.monotonic()), 3),
                    "inspection": candidate,
                })
                if (len(fields) == 8 and fields[0] == pre_container_id and fields[1] == args.image
                        and fields[2] == pulled_image_id and fields[3] == "running" and fields[4] == "healthy"
                        and fields[5] == pre_project and fields[6] == "leapview" and fields[7] != private_started_at):
                    postpublication_inspect = candidate
                    break
            except HostGuestError:
                probe_history.append({
                    "elapsedSeconds": round(args.startup_timeout - max(0, postpublication_deadline - time.monotonic()), 3),
                    "inspection": "unavailable",
                })
            time.sleep(2)
        if postpublication_inspect is None:
            raise HostGuestError("application container did not remain healthy after public activation and reboot")
        _record(evidence, "automatic-restart-probes.json", (json.dumps(probe_history, sort_keys=True) + "\n").encode())
        _record(evidence, "container-inspect.txt", (postpublication_inspect + "\n").encode())
        _record(evidence, "post-public-reboot-container-inspect.txt", (postpublication_inspect + "\n").encode())
        parts = postpublication_inspect.split()
        container_id, configured_image, image_id, state, health, project, service, started_at = parts
        _record(evidence, "database-secret-boundary.json", (json.dumps({
            "poolProbeUsedSeparatePrivateEnvironment": True,
            "operationOnlyMigratorURLsAbsentFromServingEnvironment": True,
            "operatorBootstrapRemovedAfterSuccessfulInstall": True,
            "candidateSecretsExcludedFromRetainedEvidence": True,
        }, sort_keys=True) + "\n").encode())
        _assert_no_secrets_in_evidence(evidence, fixture_secrets)
    evidence_report = {
        "schemaVersion": SCHEMA_VERSION,
        "scope": SCOPE,
        "result": "passed",
        "guestOS": args.guest_os,
        "platform": args.platform,
        "installMode": args.install_mode,
        "installerDriver": driver,
        "image": args.image,
        "sourceRevision": args.source_revision,
        "releaseAdmission": False,
        "postgresFixtureImage": postgres_fixture["image"],
        "postgresFixturePlatform": postgres_fixture["platform"],
        "physicalPoolArtifactsSHA256": pool_info["poolArtifactsSHA256"],
    }
    _write_new(evidence / "qualification-report.json", (json.dumps(evidence_report, indent=2) + "\n").encode(), 0o600)
    try:
        inventory = qualification._qualification_evidence_inventory(evidence)
    except qualification.QualificationError as exc:
        raise HostGuestError(f"cannot inventory fresh-guest evidence: {exc}") from exc

    first_install_lifecycle = {
        "targetID": target_id,
        "privateMarkers": {
            "afterInstall": marker,
            "afterPendingReboot": private_marker_after_reboot,
            "afterPublication": private_marker_after_publication,
        },
        "publicMarkers": {
            "afterActivation": public_marker,
            "afterPublicReboot": public_marker_after_reboot,
        },
        "markerSHA256": {
            "privateAfterInstall": marker_sha,
            "privateAfterPendingReboot": _digest(private_marker_after_reboot_raw),
            "privateAfterPublication": _digest(private_marker_after_publication_raw),
            "publicAfterActivation": _digest(public_marker_raw),
            "publicAfterReboot": _digest(public_marker_after_reboot_raw),
        },
        "privateCaddy": {
            "beforePendingReboot": private_caddy_before_reboot,
            "afterPendingReboot": private_caddy_after_reboot,
            "afterPublication": private_caddy_after_publication,
        },
        "publicCaddy": {
            "afterActivation": public_caddy_after_activation,
            "afterPublicReboot": public_caddy_after_reboot,
        },
        "publicBindConfig": public_bind_config,
        "applicationPortBindings": {
            "beforePendingReboot": private_app_ports_before_reboot,
            "afterPendingReboot": private_app_ports_after_reboot,
            "afterPublication": private_app_ports_after_publication,
            "afterActivation": public_app_ports_after_activation,
            "afterPublicReboot": public_app_ports_after_reboot,
        },
        "bootIDAfterPendingReboot": boot_after,
        "bootIDAfterPublicReboot": public_boot_after,
        "activationExitCode": 0,
    }

    receipt = {
        "schemaVersion": SCHEMA_VERSION,
        "scope": SCOPE,
        "result": "passed",
        "releaseAdmission": False,
        "identity": {
            "archiveSHA256": binding["archiveSHA256"],
            "controllerSHA256": nix_controller_sha,
            "controllerBuildIdentitySHA256": binding["controllerBuildIdentitySHA256"],
            "image": args.image,
            "sourceRevision": args.source_revision,
            "platform": args.platform,
            "installMode": args.install_mode,
            "installerDriver": driver,
            "sourceBootstrapSHA256": bootstrap_sha,
            "releaseIdentitySHA256": binding["releaseIdentitySHA256"],
        },
        "runner": {
            "hostArchitecture": host_arch,
            "guestArchitecture": arch_after,
            "sshEndpoint": f"127.0.0.1:{args.port}",
            "virtualizationMode": args.virtualization_mode,
            "tcgEnabled": args.virtualization_mode == "tcg",
            "kvm": kvm,
            "sshKnownHostsSHA256": _digest(known_hosts_bytes),
            "launcherReceiptSHA256": _digest(launcher_receipt_bytes),
            "launcherInputs": launcher_receipt["inputs"],
            "qemuSystemBinarySHA256": launcher_receipt["runner"]["qemuSystemBinarySHA256"],
            "qemuVersionSHA256": launcher_receipt["runner"]["qemuVersionSHA256"],
        },
        "guest": {
            "nonceSHA256": _digest(args.nonce.encode("ascii")),
            "manifestSHA256": _digest(manifest_bytes),
            "sourceCloudImageSHA256": manifest["sourceCloudImageSHA256"],
            "os": {"id": os_id, "versionID": os_version},
            "architecture": arch_after,
            "kernelBefore": kernel_before,
            "kernelAfter": kernel_after,
            "bootIDBefore": boot_before,
            "bootIDAfter": boot_after,
            "systemdPID1": pid1,
            "docker": {"enabled": docker_enabled, "active": docker_active},
            "container": {
                "id": container_id,
                "configuredImage": configured_image,
                "imageID": image_id,
                "state": state,
                "health": health,
                "project": project,
                "service": service,
                "repoDigests": repo_digests,
                "startedAtBeforeReboot": pre_started_at,
                "startedAtAfterPrivateReboot": private_started_at,
                "startedAtAfterReboot": started_at,
            },
            "markerSHA256": marker_sha,
            "generation": expected_generation,
            "links": {"current": current_target, "controllerWrapper": wrapper_target},
            "controllers": {
                "nixArchiveSHA256": nix_controller_sha,
                "ociPayloadSHA256": payload_sha,
                "installedSHA256": installed_sha,
                "nixRuntimeIdentity": nix_runtime,
                "installedRuntimeIdentity": app_runtime,
            },
            "postgresFixture": postgres_fixture,
            "physicalPoolArtifactsSHA256": pool_info["poolArtifactsSHA256"],
        },
        "firstPublication": {
            "report": first_publication_report,
            "httpsReadiness": {
                "url": "https://localhost/readyz",
                "before": 503,
                "after": 200,
            },
        },
        "firstInstallLifecycle": first_install_lifecycle,
        "protectedVerifier": protected_verifier,
        "assertions": {assertion: True for assertion in ASSERTIONS},
        "excludedGates": ["two-image-upgrade-and-rollback", "full-enterprise-publication-journey"],
        "evidenceInventory": inventory,
    }
    _validate_receipt(receipt, evidence, expected_image=args.image, expected_platform=args.platform)
    _write_new(output / "host-guest-receipt.json", (json.dumps(receipt, indent=2, sort_keys=True) + "\n").encode(), 0o600)
    return receipt


def _evidence_bytes(root: Path, name: str) -> bytes:
    try:
        return qualification._read_regular(root / name, "host guest evidence " + name, MAX_GUEST_OUTPUT_BYTES)
    except qualification.QualificationError as exc:
        raise HostGuestError(str(exc)) from exc


def _validate_first_install_lifecycle(lifecycle: dict, evidence: Path, *, image: str,
                                      boot_id_before: str, boot_id_after_pending_reboot: str) -> dict:
    expected_keys = {
        "targetID", "privateMarkers", "publicMarkers", "markerSHA256", "privateCaddy", "publicCaddy",
        "publicBindConfig", "applicationPortBindings", "bootIDAfterPendingReboot", "bootIDAfterPublicReboot",
        "activationExitCode",
    }
    if not isinstance(lifecycle, dict) or set(lifecycle) != expected_keys:
        raise HostGuestError("receipt lacks the exact private-to-public first-install lifecycle record")
    target_id = lifecycle["targetID"]
    if not _safe_qualification_identifier(target_id):
        raise HostGuestError("first-install lifecycle target identity is invalid")
    private_names = ("afterInstall", "afterPendingReboot", "afterPublication")
    public_names = ("afterActivation", "afterPublicReboot")
    private_markers = lifecycle["privateMarkers"]
    public_markers = lifecycle["publicMarkers"]
    if (not isinstance(private_markers, dict) or set(private_markers) != set(private_names)
            or not isinstance(public_markers, dict) or set(public_markers) != set(public_names)):
        raise HostGuestError("first-install lifecycle marker phases are incomplete")
    for name in private_names:
        _validate_host_install_marker(private_markers[name], phase=FIRST_INSTALL_PRIVATE_PHASE,
                                      image=image, target_id=target_id)
    for name in public_names:
        _validate_host_install_marker(public_markers[name], phase=FIRST_INSTALL_PUBLIC_PHASE,
                                      image=image, target_id=target_id)
    private_base = {key: value for key, value in private_markers["afterInstall"].items() if key != "bootstrapPhase"}
    if any({key: value for key, value in marker.items() if key != "bootstrapPhase"} != private_base
           for marker in private_markers.values()):
        raise HostGuestError("private host installation configuration changed before activation")
    public_base = {key: value for key, value in public_markers["afterActivation"].items() if key != "bootstrapPhase"}
    if public_base != private_base or any(
        {key: value for key, value in marker.items() if key != "bootstrapPhase"} != public_base
        for marker in public_markers.values()
    ):
        raise HostGuestError("public activation changed the installed image or generation marker")
    if public_markers["afterActivation"]["bootstrapPhase"] != FIRST_INSTALL_PUBLIC_PHASE:
        raise HostGuestError("host marker did not change to public only after explicit activation")

    marker_hashes = lifecycle["markerSHA256"]
    marker_hash_names = {
        "privateAfterInstall": ("afterInstall", "host-marker-private-after-install.json"),
        "privateAfterPendingReboot": ("afterPendingReboot", "host-marker-private-after-reboot.json"),
        "privateAfterPublication": ("afterPublication", "host-marker-private-after-publication.json"),
        "publicAfterActivation": ("afterActivation", "host-marker-public-after-activation.json"),
        "publicAfterReboot": ("afterPublicReboot", "host-marker-public-after-reboot.json"),
    }
    if not isinstance(marker_hashes, dict) or set(marker_hashes) != set(marker_hash_names):
        raise HostGuestError("first-install lifecycle marker digests are incomplete")
    marker_collections = {**private_markers, **public_markers}
    generation = _first_install_generation(image)
    for digest_name, (marker_name, filename) in marker_hash_names.items():
        raw_marker = _evidence_bytes(evidence, filename)
        marker = _json(raw_marker, filename)
        if (not isinstance(marker_hashes[digest_name], str) or SHA256_RE.fullmatch(marker_hashes[digest_name]) is None
                or _digest(raw_marker) != marker_hashes[digest_name]
                or _canonical(marker) != _canonical(marker_collections[marker_name])):
            raise HostGuestError(f"retained installation marker {filename} differs from the lifecycle receipt")
        link_filename = filename.removesuffix(".json") + "-current-link.txt"
        mode_filename = filename.removesuffix(".json") + "-owner-mode.txt"
        if (_one_line(_evidence_bytes(evidence, link_filename), link_filename) != "releases/" + generation
                or _one_line(_evidence_bytes(evidence, mode_filename), mode_filename) != "0:0:600"):
            raise HostGuestError(f"installation marker {filename} is not root-owned and bound to current generation")
    if (lifecycle["markerSHA256"]["privateAfterInstall"] != _one_line(
            _evidence_bytes(evidence, "host-marker-sha256.txt"), "initial host marker digest")
            or _one_line(_evidence_bytes(evidence, "host-marker-private-after-install-current-link.txt"),
                         "initial host marker generation link") != "releases/" + generation):
        raise HostGuestError("initial host marker digest or active generation evidence differs")

    public_bind_config = lifecycle["publicBindConfig"]
    if not isinstance(public_bind_config, dict) or set(public_bind_config) != set(CADDY_PUBLIC_BIND_CONFIG.values()):
        raise HostGuestError("public Compose Caddy listener configuration is incomplete")
    raw_bind_config = _evidence_bytes(evidence, "public-caddy-bind-config.txt")
    if _parse_caddy_bind_config(raw_bind_config) != public_bind_config:
        raise HostGuestError("public Caddy listener configuration differs from installed deployment.env")
    caddy_groups = {
        "privateCaddy": ("beforePendingReboot", "afterPendingReboot", "afterPublication"),
        "publicCaddy": ("afterActivation", "afterPublicReboot"),
    }
    caddy_evidence_names = {
        "beforePendingReboot": "private-caddy-before-reboot",
        "afterPendingReboot": "private-caddy-after-reboot",
        "afterPublication": "private-caddy-after-publication",
        "afterActivation": "public-caddy-after-activation",
        "afterPublicReboot": "public-caddy-after-reboot",
    }
    caddy_image = None
    for group, names in caddy_groups.items():
        observations = lifecycle[group]
        if not isinstance(observations, dict) or set(observations) != set(names):
            raise HostGuestError(f"first-install {group} observations are incomplete")
        private = group == "privateCaddy"
        for name in names:
            observation = observations[name]
            if (not isinstance(observation, dict) or set(observation) != {
                "inspection", "portBindings", "caddyfileSHA256", "domain",
            } or observation.get("domain") != "localhost"
                    or not isinstance(observation.get("caddyfileSHA256"), str)
                    or SHA256_RE.fullmatch(observation["caddyfileSHA256"]) is None):
                raise HostGuestError(f"first-install Caddy observation {name} has an unsupported schema")
            validated_inspection = _validate_caddy_inspection(
                observation["inspection"], image=caddy_image,
            )
            if caddy_image is None:
                caddy_image = validated_inspection["image"]
            prefix = caddy_evidence_names[name]
            raw_inspection = _parse_caddy_observation(
                _evidence_bytes(evidence, prefix + "-docker-inspect.txt"), private=private,
                image=caddy_image, public_config=public_bind_config if not private else None,
            )
            if (raw_inspection["inspection"] != validated_inspection
                    or raw_inspection["portBindings"] != observation["portBindings"]):
                raise HostGuestError(f"first-install Caddy observation {name} differs from raw Docker inspection")
            config = _evidence_bytes(evidence, prefix + "-active-caddyfile.txt")
            _validate_active_caddyfile(config, private=private)
            if (_digest(config) != observation["caddyfileSHA256"]
                    or _one_line(_evidence_bytes(evidence, prefix + "-active-domain.txt"), "active Caddy domain") != "localhost"):
                raise HostGuestError(f"active Caddy configuration {name} differs from its container receipt")
    private_observations = lifecycle["privateCaddy"]
    public_observations = lifecycle["publicCaddy"]
    if (private_observations["beforePendingReboot"]["portBindings"] != private_observations["afterPendingReboot"]["portBindings"]
            or private_observations["beforePendingReboot"]["portBindings"] != private_observations["afterPublication"]["portBindings"]
            or private_observations["beforePendingReboot"]["caddyfileSHA256"] != private_observations["afterPendingReboot"]["caddyfileSHA256"]
            or private_observations["beforePendingReboot"]["caddyfileSHA256"] != private_observations["afterPublication"]["caddyfileSHA256"]
            or private_observations["beforePendingReboot"]["inspection"]["startedAt"] == private_observations["afterPendingReboot"]["inspection"]["startedAt"]):
        raise HostGuestError("private HTTPS proxy phase did not remain loopback-bound across pending reboot and publication")
    if (public_observations["afterActivation"]["portBindings"] == private_observations["afterPublication"]["portBindings"]
            or public_observations["afterActivation"]["caddyfileSHA256"] == private_observations["afterPublication"]["caddyfileSHA256"]
            or public_observations["afterActivation"]["portBindings"] != public_observations["afterPublicReboot"]["portBindings"]
            or public_observations["afterActivation"]["caddyfileSHA256"] != public_observations["afterPublicReboot"]["caddyfileSHA256"]
            or public_observations["afterActivation"]["inspection"]["startedAt"] == public_observations["afterPublicReboot"]["inspection"]["startedAt"]):
        raise HostGuestError("activated Caddy Compose phase did not use configured binds or survive reboot")

    app_bindings = lifecycle["applicationPortBindings"]
    app_binding_names = {
        "beforePendingReboot": "application-port-bindings-before-reboot.json",
        "afterPendingReboot": "application-port-bindings-after-private-reboot.json",
        "afterPublication": "application-port-bindings-after-publication.json",
        "afterActivation": "application-port-bindings-after-activation.json",
        "afterPublicReboot": "application-port-bindings-after-public-reboot.json",
    }
    if not isinstance(app_bindings, dict) or set(app_bindings) != set(app_binding_names):
        raise HostGuestError("application loopback listener observations are incomplete")
    for name, filename in app_binding_names.items():
        raw_bindings = _json(_evidence_bytes(evidence, filename), filename)
        normalized = _validate_application_port_bindings(raw_bindings)
        if app_bindings[name] != normalized:
            raise HostGuestError(f"application listener {name} differs from actual Docker port bindings")
    if any(bindings != app_bindings["beforePendingReboot"] for bindings in app_bindings.values()):
        raise HostGuestError("application port binding changed across first-install activation")

    if (type(lifecycle.get("activationExitCode")) is not int or lifecycle["activationExitCode"] != 0
            or _one_line(_evidence_bytes(evidence, "first-install-activation-exit-code.txt"), "first-install activation status") != "0"):
        raise HostGuestError("first-install public phase lacks a successful explicit activation command")
    pending_boot, public_boot = lifecycle["bootIDAfterPendingReboot"], lifecycle["bootIDAfterPublicReboot"]
    if (pending_boot != boot_id_after_pending_reboot or BOOT_ID_RE.fullmatch(pending_boot or "") is None
            or BOOT_ID_RE.fullmatch(public_boot or "") is None or public_boot in {pending_boot, boot_id_before}):
        raise HostGuestError("first-install marker phase evidence lacks two distinct reboot identities")
    if (_one_line(_evidence_bytes(evidence, "post-public-reboot-boot-id.txt"), "post-public reboot ID") != public_boot
            or _one_line(_evidence_bytes(evidence, "private-bootstrap-healthz-before-reboot.txt"), "private pre-reboot healthz") != "200"
            or _one_line(_evidence_bytes(evidence, "private-bootstrap-readyz-before-reboot.txt"), "private pre-reboot readyz") != "503"
            or _one_line(_evidence_bytes(evidence, "private-bootstrap-healthz-after-reboot.txt"), "private post-reboot healthz") != "200"
            or _one_line(_evidence_bytes(evidence, "private-bootstrap-readyz-after-reboot.txt"), "private post-reboot readyz") != "503"
            or _one_line(_evidence_bytes(evidence, "first-publication-direct-readyz-after.txt"), "first-publication direct readyz") != "200"
            or _one_line(_evidence_bytes(evidence, "public-activation-healthz.txt"), "public activation healthz") != "200"
            or _one_line(_evidence_bytes(evidence, "public-activation-readyz.txt"), "public activation readyz") != "200"
            or _one_line(_evidence_bytes(evidence, "public-reboot-healthz.txt"), "public reboot healthz") != "200"
            or _one_line(_evidence_bytes(evidence, "public-reboot-readyz.txt"), "public reboot readyz") != "200"
            or _one_line(_evidence_bytes(evidence, "public-activation-https-readiness.txt"), "public activation HTTPS") != "200"
            or _one_line(_evidence_bytes(evidence, "public-reboot-https-readiness.txt"), "public reboot HTTPS") != "200"):
        raise HostGuestError("private liveness/readiness or public post-activation readiness evidence is incomplete")
    return lifecycle


def _validate_receipt(receipt: dict, evidence: Path, *, expected_image: str | None = None,
                      expected_platform: str | None = None) -> dict:
    required = {
        "schemaVersion", "scope", "result", "releaseAdmission", "identity", "runner", "guest",
        "firstPublication", "firstInstallLifecycle", "protectedVerifier", "assertions", "excludedGates", "evidenceInventory",
    }
    if not isinstance(receipt, dict) or set(receipt) != required:
        raise HostGuestError("host guest receipt has an unsupported schema")
    if (receipt["schemaVersion"] != SCHEMA_VERSION or receipt["scope"] != SCOPE
            or receipt["result"] != "passed" or receipt["releaseAdmission"] is not False):
        raise HostGuestError("host guest receipt is not a non-admitting successful record")
    identity = receipt["identity"]
    identity_keys = {
        "archiveSHA256", "controllerSHA256", "controllerBuildIdentitySHA256", "image", "sourceRevision",
        "platform", "installMode", "installerDriver", "sourceBootstrapSHA256", "releaseIdentitySHA256",
    }
    if not isinstance(identity, dict) or set(identity) != identity_keys:
        raise HostGuestError("host guest receipt has incomplete Compose identity")
    for key in ("archiveSHA256", "controllerSHA256", "controllerBuildIdentitySHA256", "releaseIdentitySHA256"):
        if not isinstance(identity[key], str) or SHA256_RE.fullmatch(identity[key]) is None:
            raise HostGuestError(f"host guest receipt {key} is not a SHA-256 digest")
    if (not isinstance(identity["sourceRevision"], str) or REVISION_RE.fullmatch(identity["sourceRevision"]) is None
            or identity["platform"] not in qualification.PLATFORMS
            or not isinstance(identity["image"], str) or qualification.IMAGE_RE.fullmatch(identity["image"]) is None):
        raise HostGuestError("host guest receipt image, revision or platform identity is invalid")
    install_mode = identity["installMode"]
    if not isinstance(install_mode, str):
        raise HostGuestError("host guest receipt has an invalid install mode")
    expected_driver = {"bootstrap": "source-bootstrap-linux.sh", "nix-controller": "nix-archive"}.get(install_mode)
    bootstrap_sha = identity["sourceBootstrapSHA256"]
    if (expected_driver is None or identity["installerDriver"] != expected_driver
            or (install_mode == "bootstrap"
                and (not isinstance(bootstrap_sha, str) or SHA256_RE.fullmatch(bootstrap_sha) is None))
            or (install_mode == "nix-controller" and bootstrap_sha is not None)):
        raise HostGuestError("host guest receipt does not bind an exact supported install mode and driver")
    if expected_image is not None and identity["image"] != expected_image:
        raise HostGuestError("host guest receipt selects a different OCI image")
    if expected_platform is not None and identity["platform"] != expected_platform:
        raise HostGuestError("host guest receipt selects a different architecture")

    runner, guest = receipt["runner"], receipt["guest"]
    if not isinstance(runner, dict) or set(runner) != {
        "hostArchitecture", "guestArchitecture", "sshEndpoint", "virtualizationMode", "tcgEnabled", "kvm",
        "sshKnownHostsSHA256", "launcherReceiptSHA256", "launcherInputs", "qemuSystemBinarySHA256", "qemuVersionSHA256",
    }:
        raise HostGuestError("host guest receipt has incomplete execution-mode evidence")
    expected_arch = ARCHITECTURES[identity["platform"].removeprefix("linux/")]
    endpoint_match = re.fullmatch(r"127\.0\.0\.1:([0-9]{4,5})", runner["sshEndpoint"] or "")
    if (runner["hostArchitecture"] != expected_arch or runner["guestArchitecture"] != expected_arch
            or endpoint_match is None or not 1024 <= int(endpoint_match.group(1)) <= 65535
            or runner["virtualizationMode"] not in {"kvm", "tcg"}
            or runner["tcgEnabled"] is not (runner["virtualizationMode"] == "tcg")):
        raise HostGuestError("host guest receipt claims a cross-ISA or ambiguous execution mode")
    if _one_line(_evidence_bytes(evidence, "host-runner-architecture.txt"), "host runner architecture") != runner["hostArchitecture"]:
        raise HostGuestError("host guest receipt differs from the local runner architecture evidence")
    kvm = runner["kvm"]
    if not isinstance(kvm, dict) or set(kvm) != {"available", "apiVersion"} or type(kvm["available"]) is not bool:
        raise HostGuestError("host guest receipt has an invalid KVM probe")
    if runner["virtualizationMode"] == "kvm" and (kvm["available"] is not True or kvm["apiVersion"] != 12):
        raise HostGuestError("host guest receipt selects KVM without a successful API-version probe")
    if _json(_evidence_bytes(evidence, "host-kvm-probe.json"), "KVM probe evidence") != kvm:
        raise HostGuestError("KVM probe receipt differs from locally retained probe evidence")
    if not isinstance(runner["sshKnownHostsSHA256"], str) or SHA256_RE.fullmatch(runner["sshKnownHostsSHA256"]) is None:
        raise HostGuestError("host guest receipt has no pinned SSH host key inventory hash")
    for key in ("launcherReceiptSHA256", "qemuSystemBinarySHA256", "qemuVersionSHA256"):
        if not isinstance(runner[key], str) or SHA256_RE.fullmatch(runner[key]) is None:
            raise HostGuestError(f"host guest receipt lacks launcher identity {key}")
    launcher_data = _evidence_bytes(evidence, "launcher-receipt.json")
    launcher = _validate_launcher_receipt(
        launcher_data, manifest=_json(_evidence_bytes(evidence, "launcher-manifest.json"), "launcher manifest"),
        manifest_bytes=_evidence_bytes(evidence, "launcher-manifest.json"),
        known_hosts_sha256=runner["sshKnownHostsSHA256"],
    )
    if (_digest(launcher_data) != runner["launcherReceiptSHA256"] or launcher["inputs"] != runner["launcherInputs"]
            or launcher["runner"]["qemuSystemBinarySHA256"] != runner["qemuSystemBinarySHA256"]
            or launcher["runner"]["qemuVersionSHA256"] != runner["qemuVersionSHA256"]):
        raise HostGuestError("host guest runner identity differs from the immutable launcher receipt")
    if not isinstance(guest, dict) or set(guest) != {
        "nonceSHA256", "manifestSHA256", "sourceCloudImageSHA256", "os", "architecture", "kernelBefore",
        "kernelAfter", "bootIDBefore", "bootIDAfter", "systemdPID1", "docker", "container", "markerSHA256",
        "generation", "links", "controllers", "postgresFixture", "physicalPoolArtifactsSHA256",
    }:
        raise HostGuestError("host guest receipt has incomplete guest measurements")

    os_record = guest.get("os")
    if not isinstance(os_record, dict) or set(os_record) != {"id", "versionID"}:
        raise HostGuestError("host guest receipt has invalid OS identity")
    expected_os = {value: key for key, value in GUEST_OS.items()}.get((os_record["id"], os_record["versionID"]))
    if expected_os is None:
        raise HostGuestError("host guest receipt uses an unsupported host OS")
    if (guest.get("architecture") != expected_arch or not isinstance(guest.get("kernelBefore"), str)
            or not guest["kernelBefore"] or not isinstance(guest.get("kernelAfter"), str) or not guest["kernelAfter"]
            or BOOT_ID_RE.fullmatch(guest.get("bootIDBefore", "")) is None
            or BOOT_ID_RE.fullmatch(guest.get("bootIDAfter", "")) is None
            or guest["bootIDBefore"] == guest["bootIDAfter"]):
        raise HostGuestError("host guest receipt lacks a valid automatic reboot identity")
    first_install_lifecycle = _validate_first_install_lifecycle(
        receipt["firstInstallLifecycle"], evidence, image=identity["image"],
        boot_id_before=guest["bootIDBefore"], boot_id_after_pending_reboot=guest["bootIDAfter"],
    )
    for key in ("nonceSHA256", "manifestSHA256", "sourceCloudImageSHA256", "markerSHA256"):
        if not isinstance(guest.get(key), str) or SHA256_RE.fullmatch(guest[key]) is None:
            raise HostGuestError(f"host guest receipt lacks {key}")
    docker = guest.get("docker")
    if docker != {"enabled": "enabled", "active": "active"} or "systemd" not in guest.get("systemdPID1", ""):
        raise HostGuestError("host guest receipt does not prove systemd and Docker are active and enabled")
    container = guest.get("container")
    if not isinstance(container, dict) or set(container) != {
        "id", "configuredImage", "imageID", "state", "health", "project", "service", "repoDigests",
        "startedAtBeforeReboot", "startedAtAfterPrivateReboot", "startedAtAfterReboot",
    }:
        raise HostGuestError("host guest receipt has incomplete container identity")
    if (CONTAINER_RE.fullmatch(container["id"]) is None or container["configuredImage"] != identity["image"]
            or re.fullmatch(r"sha256:[0-9a-f]{64}", container["imageID"]) is None
            or container["state"] != "running" or container["health"] != "healthy"
            or not container["project"] or container["service"] != "leapview"
            or not container["startedAtBeforeReboot"] or not container["startedAtAfterReboot"]
            or container["startedAtBeforeReboot"] == container["startedAtAfterReboot"]
            or not container["startedAtAfterPrivateReboot"]
            or container["startedAtBeforeReboot"] == container["startedAtAfterPrivateReboot"]
            or container["startedAtAfterPrivateReboot"] == container["startedAtAfterReboot"]
            or not isinstance(container["repoDigests"], list) or identity["image"] not in container["repoDigests"]):
        raise HostGuestError("host guest receipt container does not match the healthy immutable image")
    expected_generation = "sha256-" + identity["image"].rsplit("sha256:", 1)[1]
    if guest.get("generation") != expected_generation or guest.get("links") != {
        "current": "releases/" + expected_generation,
        "controllerWrapper": "../../../opt/leapview/current/leapviewctl",
    }:
        raise HostGuestError("host guest receipt generation or wrapper links do not match the image")
    controllers = guest.get("controllers")
    if not isinstance(controllers, dict) or set(controllers) != {
        "nixArchiveSHA256", "ociPayloadSHA256", "installedSHA256", "nixRuntimeIdentity", "installedRuntimeIdentity",
    }:
        raise HostGuestError("host guest receipt has incomplete controller measurements")
    for key in ("nixArchiveSHA256", "ociPayloadSHA256", "installedSHA256"):
        if not isinstance(controllers[key], str) or SHA256_RE.fullmatch(controllers[key]) is None:
            raise HostGuestError("host guest receipt has an invalid controller digest")
    if controllers["nixArchiveSHA256"] != identity["controllerSHA256"] or controllers["ociPayloadSHA256"] != controllers["installedSHA256"]:
        raise HostGuestError("host guest receipt conflates the Nix archive with the OCI-installed controller")
    postgres_fixture = guest["postgresFixture"]
    if not isinstance(postgres_fixture, dict) or set(postgres_fixture) != {
        "image", "repoDigest", "imageID", "repoDigests", "platform", "containerName", "network", "stateVolume", "dataVolume",
        "restartPolicy", "initScriptSHA256", "tlsRoleProbesBeforeInstall",
    }:
        raise HostGuestError("host guest receipt has incomplete PostgreSQL fixture identity")
    if (not isinstance(postgres_fixture["image"], str)
            or re.fullmatch(r"docker\.io/library/postgres:18-alpine@sha256:[0-9a-f]{64}", postgres_fixture["image"]) is None
            or not isinstance(postgres_fixture["repoDigest"], str)
            or postgres_fixture["repoDigest"] != _postgres_repo_digest(postgres_fixture["image"])
            or not isinstance(postgres_fixture["imageID"], str)
            or re.fullmatch(r"sha256:[0-9a-f]{64}", postgres_fixture["imageID"]) is None
            or not isinstance(postgres_fixture["repoDigests"], list)
            or any(not isinstance(item, str) for item in postgres_fixture["repoDigests"])
            or postgres_fixture["platform"] != identity["platform"]
            or postgres_fixture["network"] != "leapview_default"
            or postgres_fixture["stateVolume"] != "leapview_leapview-state"
            or postgres_fixture["restartPolicy"] != "unless-stopped"
            or not re.fullmatch(r"leapview-qualification-pg-[0-9a-f]{16}", postgres_fixture["containerName"])
            or postgres_fixture["dataVolume"] != postgres_fixture["containerName"]
            or postgres_fixture["repoDigest"] not in postgres_fixture["repoDigests"]
            or postgres_fixture["tlsRoleProbesBeforeInstall"] != TLS_ROLE_EXPECTATIONS
            or not isinstance(postgres_fixture["initScriptSHA256"], str)
            or SHA256_RE.fullmatch(postgres_fixture["initScriptSHA256"]) is None):
        raise HostGuestError("host guest PostgreSQL fixture identity is not locked, native, and TLS-enabled")
    pool_artifacts_sha = guest["physicalPoolArtifactsSHA256"]
    if not isinstance(pool_artifacts_sha, str) or SHA256_RE.fullmatch(pool_artifacts_sha) is None:
        raise HostGuestError("host guest receipt lacks canonical physical-pool artifact identity")
    nix_runtime, installed_runtime = controllers["nixRuntimeIdentity"], controllers["installedRuntimeIdentity"]
    runtime_keys = {"product", "version", "revision", "buildTime", "dirty", "development"}
    if (not isinstance(nix_runtime, dict) or set(nix_runtime) != runtime_keys
            or not isinstance(installed_runtime, dict) or set(installed_runtime) != runtime_keys
            or nix_runtime["product"] != "leapviewctl" or installed_runtime["product"] != "leapviewctl"
            or nix_runtime["revision"] != identity["sourceRevision"]
            or installed_runtime["revision"] != identity["sourceRevision"]
            or nix_runtime["version"] != installed_runtime["version"]
            or nix_runtime["buildTime"] != installed_runtime["buildTime"]
            or nix_runtime["dirty"] is not False or installed_runtime["dirty"] is not False
            or nix_runtime["development"] is not False or installed_runtime["development"] is not False):
        raise HostGuestError("host guest receipt lacks Nix and OCI controller runtime identities")
    if receipt["assertions"] != {assertion: True for assertion in ASSERTIONS}:
        raise HostGuestError("host guest receipt does not pass every scoped assertion")
    if receipt["excludedGates"] != ["two-image-upgrade-and-rollback", "full-enterprise-publication-journey"]:
        raise HostGuestError("host guest receipt has an unexpected release-gate scope")

    verifier = receipt["protectedVerifier"]
    if not isinstance(verifier, dict) or set(verifier) != {
        "protectedRevision", "verifierSHA256", "qualificationAssetsSHA256",
    } or REVISION_RE.fullmatch(verifier["protectedRevision"]) is None or any(
        not isinstance(verifier[key], str) or SHA256_RE.fullmatch(verifier[key]) is None
        for key in ("verifierSHA256", "qualificationAssetsSHA256")
    ):
        raise HostGuestError("host guest receipt lacks the protected first-publication verifier identity")
    for filename, expected in (
        ("protected-first-publication-source-revision.txt", verifier["protectedRevision"]),
        ("protected-first-publication-verifier-sha256.txt", verifier["verifierSHA256"]),
        ("protected-qualification-assets-sha256.txt", verifier["qualificationAssetsSHA256"]),
    ):
        if _one_line(_evidence_bytes(evidence, filename), filename) != expected:
            raise HostGuestError(f"host guest evidence {filename} differs from its protected verifier binding")
    assets_manifest = _json(
        _evidence_bytes(evidence, "protected-qualification-assets-manifest.json"),
        "protected qualification asset manifest",
    )
    if (not isinstance(assets_manifest, dict) or set(assets_manifest) != {"schemaVersion", "assets"}
            or type(assets_manifest["schemaVersion"]) is not int or assets_manifest["schemaVersion"] != 1
            or not isinstance(assets_manifest["assets"], dict)
            or set(assets_manifest["assets"]) != set(PROTECTED_QUALIFICATION_ASSETS)
            or any(not isinstance(value, str) or SHA256_RE.fullmatch(value) is None
                   for value in assets_manifest["assets"].values())
            or _digest(_canonical(assets_manifest)) != verifier["qualificationAssetsSHA256"]):
        raise HostGuestError("protected qualification asset manifest differs from its receipt digest")
    first_publication = receipt["firstPublication"]
    if not isinstance(first_publication, dict) or set(first_publication) != {"report", "httpsReadiness"}:
        raise HostGuestError("host guest receipt lacks the first-publication subreport and independent readiness probe")
    report = _validate_first_publication_report(
        first_publication["report"], image=identity["image"],
        image_revision=controllers["installedRuntimeIdentity"]["revision"],
        environment=FIRST_PUBLICATION_ENVIRONMENT,
    )
    readiness = first_publication["httpsReadiness"]
    if (not isinstance(readiness, dict) or set(readiness) != {"url", "before", "after"}
            or readiness != {"url": "https://localhost/readyz", "before": 503, "after": 200}
            or report["readinessBefore"] != readiness["before"]
            or report["readinessAfter"] != readiness["after"]):
        raise HostGuestError("independent HTTPS readiness probe differs from the first-publication report")
    retained_first_publication = _json(
        _evidence_bytes(evidence, "first-publication-report.json"), "retained first-publication report",
    )
    if _canonical(retained_first_publication) != _canonical(report):
        raise HostGuestError("retained first-publication report differs from the receipt subreport")
    for filename, expected in (
        ("first-publication-https-readiness-before.txt", str(readiness["before"])),
        ("first-publication-https-readiness-after.txt", str(readiness["after"])),
    ):
        if _one_line(_evidence_bytes(evidence, filename), filename) != expected:
            raise HostGuestError(f"independent HTTPS readiness evidence {filename} differs from its receipt")

    report = _json(_evidence_bytes(evidence, "qualification-report.json"), "host guest qualification report")
    if (not isinstance(report, dict) or set(report) != {
        "schemaVersion", "scope", "result", "guestOS", "platform", "installMode", "installerDriver",
        "image", "sourceRevision", "releaseAdmission", "postgresFixtureImage", "postgresFixturePlatform",
        "physicalPoolArtifactsSHA256",
    } or report.get("schemaVersion") != SCHEMA_VERSION or report.get("scope") != SCOPE or report.get("result") != "passed"
            or report.get("releaseAdmission") is not False or report.get("image") != identity["image"]
            or report.get("sourceRevision") != identity["sourceRevision"] or report.get("platform") != identity["platform"]
            or report.get("installMode") != identity["installMode"]
            or report.get("installerDriver") != identity["installerDriver"]
            or report.get("postgresFixtureImage") != postgres_fixture["image"]
            or report.get("postgresFixturePlatform") != postgres_fixture["platform"]
            or report.get("physicalPoolArtifactsSHA256") != pool_artifacts_sha):
        raise HostGuestError("host guest evidence report differs from the bound release identity")
    if report["guestOS"] != expected_os:
        raise HostGuestError("launcher guest OS differs from raw /etc/os-release evidence")
    manifest_data = _evidence_bytes(evidence, "launcher-manifest.json")
    manifest = _json(manifest_data, "launcher guest manifest")
    if (not isinstance(manifest, dict) or set(manifest) != {
        "schemaVersion", "nonce", "sourceCloudImageSHA256", "guestOS", "architecture", "virtualizationMode",
    }):
        raise HostGuestError("raw launcher manifest has an unsupported schema")
    _validate_manifest(
        manifest_data, nonce=manifest["nonce"], guest_os=report["guestOS"],
        platform=identity["platform"], mode=runner["virtualizationMode"],
    )
    if (_digest(manifest_data) != guest["manifestSHA256"]
            or _digest(manifest["nonce"].encode("ascii")) != guest["nonceSHA256"]
            or manifest["sourceCloudImageSHA256"] != guest["sourceCloudImageSHA256"]):
        raise HostGuestError("host guest receipt differs from the launcher-bound manifest")
    if (_os_release(_evidence_bytes(evidence, "preinstall-os-release.txt")) != (os_record["id"], os_record["versionID"])
            or _os_release(_evidence_bytes(evidence, "postreboot-os-release.txt")) != (os_record["id"], os_record["versionID"])):
        raise HostGuestError("host guest OS receipt differs from raw /etc/os-release evidence")
    evidence_records = {
        "ssh-root.txt": "root",
        "fresh-install-state.txt": "absent",
        "preinstall-architecture.txt": expected_arch,
        "preinstall-boot-id.txt": guest["bootIDBefore"],
        "preinstall-kernel.txt": guest["kernelBefore"],
        "postreboot-boot-id.txt": guest["bootIDAfter"],
        "postreboot-architecture.txt": expected_arch,
        "postreboot-kernel.txt": guest["kernelAfter"],
        "postreboot-pid1.txt": guest["systemdPID1"],
        "docker-enabled.txt": docker["enabled"],
        "docker-active.txt": docker["active"],
        "oci-image-id.txt": container["imageID"],
        "current-generation-link.txt": guest["links"]["current"],
        "controller-wrapper-link.txt": guest["links"]["controllerWrapper"],
        "host-marker-sha256.txt": guest["markerSHA256"],
        "nix-controller-sha256.txt": controllers["nixArchiveSHA256"],
        "installer-driver.txt": identity["installerDriver"],
        "oci-payload-controller-sha256.txt": controllers["ociPayloadSHA256"],
        "installed-controller-sha256.txt": controllers["installedSHA256"],
        "postgres-image-id.txt": postgres_fixture["imageID"],
        "postgres-image-platform.txt": postgres_fixture["platform"],
        "postgres-init-bundle-sha256.txt": postgres_fixture["initScriptSHA256"],
        "postgres-container-before-reboot.txt": None,
        "postgres-container-after-reboot.txt": None,
        "physical-pool-qualification-sha256.txt": pool_artifacts_sha,
        "operator-bootstrap-cleanup.txt": "absent",
        "pool-probe-completed-before-install.txt": "completed-before-install",
    }
    for filename, expected in evidence_records.items():
        if expected is None:
            continue
        if _one_line(_evidence_bytes(evidence, filename), filename) != expected:
            raise HostGuestError(f"host guest evidence {filename} differs from its receipt")
    if _one_line(_evidence_bytes(evidence, "host-install-exit-code.txt"), "host install exit") != "0":
        raise HostGuestError("host guest evidence does not show a successful fresh install")
    if identity["installMode"] == "bootstrap":
        for filename in ("source-bootstrap-sha256.txt", "guest-bootstrap-sha256.txt"):
            if _one_line(_evidence_bytes(evidence, filename), filename) != bootstrap_sha:
                raise HostGuestError("bootstrap execution differs from the exact source-tree installer")
    elif any((evidence / filename).exists() for filename in ("source-bootstrap-sha256.txt", "guest-bootstrap-sha256.txt")):
        raise HostGuestError("Nix-controller mode unexpectedly retains bootstrap evidence")
    marker_projection = _json(_evidence_bytes(evidence, "host-marker-projection.json"), "host marker projection")
    if (not isinstance(marker_projection, dict) or set(marker_projection) != {
        "schemaVersion", "image", "domain", "environment", "https", "targetId", "bootstrapPhase",
        "generation", "markerSHA256",
    }
            or marker_projection["schemaVersion"] != 1 or marker_projection["image"] != identity["image"]
            or marker_projection["domain"] != "localhost" or marker_projection["environment"] != FIRST_PUBLICATION_ENVIRONMENT
            or marker_projection["https"] is not True
            or marker_projection["targetId"] != first_install_lifecycle["targetID"]
            or marker_projection["bootstrapPhase"] != FIRST_INSTALL_PRIVATE_PHASE
            or marker_projection["generation"] != guest["generation"]
            or marker_projection["markerSHA256"] != guest["markerSHA256"]):
        raise HostGuestError("host marker projection differs from the private install phase or generation")
    repo_digests = _json(_evidence_bytes(evidence, "oci-repo-digests.json"), "OCI RepoDigests")
    if repo_digests != container["repoDigests"]:
        raise HostGuestError("container image digest inventory differs from raw OCI evidence")
    inspect_parts = _one_line(_evidence_bytes(evidence, "container-inspect.txt"), "container inspection").split()
    expected_inspect = [container[key] for key in ("id", "configuredImage", "imageID", "state", "health", "project", "service")]
    if inspect_parts != expected_inspect + [container["startedAtAfterReboot"]]:
        raise HostGuestError("container identity differs from raw Docker inspection evidence")
    pre_inspect = _one_line(_evidence_bytes(evidence, "prereboot-container-inspect.txt"), "pre-reboot inspection").split()
    if (len(pre_inspect) != 8 or pre_inspect[0] != container["id"] or pre_inspect[1] != identity["image"]
            or pre_inspect[2] != container["imageID"] or pre_inspect[3] != "running"
            or pre_inspect[4] not in {"starting", "unhealthy"}
            or pre_inspect[5:7] != [container["project"], "leapview"]
            or pre_inspect[7] != container["startedAtBeforeReboot"]
            or container["startedAtBeforeReboot"] == container["startedAtAfterReboot"]):
        raise HostGuestError("container did not restart from the same running image before first publication")
    before_publication_inspect = _one_line(
        _evidence_bytes(evidence, "postreboot-before-publication-container-inspect.txt"),
        "post-reboot pre-publication inspection",
    ).split()
    if (len(before_publication_inspect) != 8 or before_publication_inspect[0] != container["id"]
            or before_publication_inspect[1] != identity["image"] or before_publication_inspect[2] != container["imageID"]
            or before_publication_inspect[3] != "running" or before_publication_inspect[4] not in {"starting", "unhealthy"}
            or before_publication_inspect[5:7] != [container["project"], "leapview"]
            or before_publication_inspect[7] != container["startedAtAfterPrivateReboot"]):
        raise HostGuestError("first-publication readiness baseline is not the rebooted candidate container")
    probes = _json(_evidence_bytes(evidence, "automatic-restart-probes.json"), "automatic restart probes")
    if (not isinstance(probes, list) or not probes
            or not all(isinstance(probe, dict) and isinstance(probe.get("inspection"), str) for probe in probes)
            or probes[-1]["inspection"] != " ".join(inspect_parts)
            or not any(probe["inspection"] == " ".join(before_publication_inspect) for probe in probes)):
        raise HostGuestError("automatic restart probe history does not end at the retained healthy inspection")
    if _json(_evidence_bytes(evidence, "nix-controller-runtime.json"), "Nix controller runtime") != nix_runtime:
        raise HostGuestError("Nix controller identity differs from raw runtime evidence")
    if _json(_evidence_bytes(evidence, "installed-controller-runtime.json"), "installed controller runtime") != installed_runtime:
        raise HostGuestError("installed controller identity differs from raw runtime evidence")
    if _one_line(_evidence_bytes(evidence, "controller-status.txt"), "installed controller status") != "0":
        raise HostGuestError("host controller status evidence is not a successful exit code")
    if _json(_evidence_bytes(evidence, "postgres-image-repo-digests.json"), "PostgreSQL RepoDigests") != postgres_fixture["repoDigests"]:
        raise HostGuestError("PostgreSQL fixture digest inventory differs from its receipt")
    if (postgres_fixture["repoDigest"] not in postgres_fixture["repoDigests"]
            or _one_line(_evidence_bytes(evidence, "postgres-compose-network-labels.txt"), "Compose network labels") != "leapview default"
            or _one_line(_evidence_bytes(evidence, "postgres-compose-state-volume-labels.txt"), "Compose state-volume labels") != "leapview leapview-state"):
        raise HostGuestError("PostgreSQL fixture is not bound to the exact Compose network and state volume")
    postgres_before = _one_line(_evidence_bytes(evidence, "postgres-container-before-reboot.txt"), "pre-reboot PostgreSQL inspection").split()
    postgres_after = _one_line(_evidence_bytes(evidence, "postgres-container-after-reboot.txt"), "post-reboot PostgreSQL inspection").split()
    if (len(postgres_before) != 5 or len(postgres_after) != 5
            or CONTAINER_RE.fullmatch(postgres_before[0]) is None or postgres_after[0] != postgres_before[0]
            or postgres_before[1:4] != [postgres_fixture["imageID"], "running", "unless-stopped"]
            or postgres_after[1:4] != [postgres_fixture["imageID"], "running", "unless-stopped"]
            or not postgres_before[4] or not postgres_after[4] or postgres_before[4] == postgres_after[4]):
        raise HostGuestError("PostgreSQL fixture did not automatically restart from the same persistent container")
    before_roles = _evidence_bytes(evidence, "postgres-tls-role-probes-before-install.txt").decode("utf-8").strip().splitlines()
    after_control = _one_line(_evidence_bytes(evidence, "postgres-tls-role-probe-after-reboot.txt"), "post-reboot TLS role probe")
    if before_roles != list(TLS_ROLE_EXPECTATIONS.values()) or after_control != TLS_ROLE_EXPECTATIONS["controlRuntime"]:
        raise HostGuestError("PostgreSQL fixture lacks authenticated TLS role probes before install and after reboot")
    pool_order = _json(_evidence_bytes(evidence, "pool-probe-order.json"), "pool probe sequencing evidence")
    if pool_order != {
        "schemaVersion": 1,
        "freshInstallRootAbsentBeforeProbe": True,
        "servingComposeServiceStartedBeforeInstall": False,
        "tlsRuntimeRolesProbedBeforeInstall": True,
        "canonicalPoolCommand": "admin delivery pool qualify",
        "candidateImage": identity["image"],
        "candidateImageID": container["imageID"],
        "candidatePlatform": identity["platform"],
        "sourceRevision": identity["sourceRevision"],
        "operatorBootstrapWrittenAfterPoolProbe": True,
    }:
        raise HostGuestError("pool probe was not completed against a fresh, non-serving install target before host install")
    if _evidence_bytes(evidence, "pool-probe-preconditions.txt").decode("utf-8").strip().splitlines() != [
        "fresh-root-absent", "serving-service-not-running",
    ]:
        raise HostGuestError("pool probe evidence does not prove the fresh target and stopped service preconditions")
    raw_pool = _evidence_bytes(evidence, "physical-pool-qualification-artifacts.json")
    try:
        pool_artifacts = _json(raw_pool, "canonical physical-pool qualification artifacts", 1024**2)
    except qualification.QualificationError as exc:
        raise HostGuestError(f"canonical physical-pool qualification artifacts are invalid: {exc}") from exc
    if (_digest(raw_pool) != pool_artifacts_sha or not isinstance(pool_artifacts, dict)
            or set(pool_artifacts) != {"schema_version", "pool", "evidence"}
            or type(pool_artifacts["schema_version"]) is not int or pool_artifacts["schema_version"] != 1
            or not isinstance(pool_artifacts["pool"], dict) or not isinstance(pool_artifacts["evidence"], dict)):
        raise HostGuestError("physical-pool qualification artifact digest or schema differs from the receipt")
    boundary = _json(_evidence_bytes(evidence, "serving-credential-boundary.json"), "serving credential boundary")
    if boundary != {
        "controlMigratorURLAbsentFromServingEnvironment": True,
        "duckLakeMigratorURLAbsentFromServingEnvironment": True,
    }:
        raise HostGuestError("operation-only migrator URLs were not excluded from the installed serving environment")
    secret_boundary = _json(_evidence_bytes(evidence, "database-secret-boundary.json"), "private credential boundary")
    if secret_boundary != {
        "poolProbeUsedSeparatePrivateEnvironment": True,
        "operationOnlyMigratorURLsAbsentFromServingEnvironment": True,
        "operatorBootstrapRemovedAfterSuccessfulInstall": True,
        "candidateSecretsExcludedFromRetainedEvidence": True,
    }:
        raise HostGuestError("guest first-install private input or credential boundary did not pass")
    try:
        inventory = qualification._qualification_evidence_inventory(evidence)
    except qualification.QualificationError as exc:
        raise HostGuestError(f"cannot validate host guest evidence inventory: {exc}") from exc
    if _canonical(inventory) != _canonical(receipt["evidenceInventory"]):
        raise HostGuestError("host guest raw evidence differs from its retained inventory")
    return receipt


def verify_receipt(receipt_path: Path, evidence_path: Path) -> dict:
    receipt = _json(_read(receipt_path, "host guest receipt", MAX_RECEIPT_BYTES), "host guest receipt")
    return _validate_receipt(receipt, evidence_path)


def _add_identity_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--sidecar", type=Path, required=True)
    parser.add_argument("--controller-build-identity", type=Path, required=True)
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--release-identity", type=Path, required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--source-revision", required=True)
    parser.add_argument("--platform", choices=sorted(qualification.PLATFORMS), required=True)
    parser.add_argument("--original-controller-evidence", type=Path, required=True)
    parser.add_argument("--retained-controller-evidence", type=Path, required=True)
    parser.add_argument("--controller-binary-verifier", type=Path, required=True)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    operations = parser.add_subparsers(dest="operation", required=True)
    qualify = operations.add_parser("qualify-guest")
    _add_identity_arguments(qualify)
    qualify.add_argument("--protected-root", type=Path, required=True)
    qualify.add_argument("--protected-revision", required=True)
    qualify.add_argument("--first-publication-verifier", type=Path, required=True)
    qualify.add_argument("--host", default="127.0.0.1")
    qualify.add_argument("--port", type=int, required=True)
    qualify.add_argument("--ssh-identity", type=Path, required=True)
    qualify.add_argument("--known-hosts", type=Path, required=True)
    qualify.add_argument("--nonce", required=True)
    qualify.add_argument("--guest-manifest", type=Path, required=True)
    qualify.add_argument("--launcher-receipt", type=Path, required=True)
    qualify.add_argument("--guest-os", choices=sorted(GUEST_OS), required=True)
    qualify.add_argument("--install-mode", choices=("bootstrap", "nix-controller"), required=True)
    qualify.add_argument("--virtualization-mode", choices=("kvm", "tcg"), required=True)
    qualify.add_argument("--config", type=Path, required=True)
    qualify.add_argument("--output-dir", type=Path, required=True)
    qualify.add_argument("--reboot-timeout", type=int, default=300)
    qualify.add_argument("--startup-timeout", type=int, default=300)

    verify = operations.add_parser("verify-receipt")
    verify.add_argument("--receipt", type=Path, required=True)
    verify.add_argument("--evidence-dir", type=Path, required=True)

    args = parser.parse_args()
    try:
        if args.operation == "verify-receipt":
            receipt = verify_receipt(args.receipt, args.evidence_dir)
        else:
            if NONCE_RE.fullmatch(args.nonce) is None:
                raise HostGuestError("launcher invocation nonce must be 32–128 lowercase hexadecimal characters")
            if REVISION_RE.fullmatch(args.source_revision) is None:
                raise HostGuestError("source revision must be a full lowercase commit SHA")
            if not 60 <= args.reboot_timeout <= 900:
                raise HostGuestError("reboot timeout must be between 60 and 900 seconds")
            if not 60 <= args.startup_timeout <= 900:
                raise HostGuestError("application restart timeout must be between 60 and 900 seconds")
            receipt = _install_and_collect(args)
        print(json.dumps({
            "scope": receipt["scope"],
            "result": receipt["result"],
            "releaseAdmission": False,
            "receiptSHA256": _digest(_canonical(receipt)),
        }, sort_keys=True))
    except (HostGuestError, qualification.QualificationError, controller_evidence.ControllerEvidenceError,
            OSError, subprocess.SubprocessError, KeyError, TypeError, ValueError) as exc:
        raise SystemExit("Nix Compose host guest qualification rejected: " + str(exc)) from exc


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Bounded, rollback-aware cleanup for LeapView's public-site Docker images."""

from __future__ import annotations

import argparse
import fcntl
import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Any, Iterable

try:
    import tomllib
except ModuleNotFoundError:  # Python before 3.11 can use an explicit storage-path override.
    tomllib = None


SITE_REPOSITORY = "ghcr.io/flidai/leapview-site"
SITE_DIGEST = re.compile(r"^ghcr\.io/flidai/leapview-site@sha256:[0-9a-f]{64}$")
SITE_TAG = re.compile(r"^ghcr\.io/flidai/leapview-site:([A-Za-z0-9_][A-Za-z0-9._-]{0,127})$")
GENERIC_DIGEST = re.compile(r"^[-A-Za-z0-9._/:]+@sha256:[0-9a-f]{64}$")
IMAGE_ID = re.compile(r"^sha256:[0-9a-f]{64}$")
TRANSACTION_PHASES = {
    "prepared",
    "activating",
    "rollback-started",
    "rollback-verified",
    "rollback-failed",
    "activated",
}
DEFAULT_MIN_FREE_BYTES = 5 * 1024 * 1024 * 1024

# 64: command line or state file syntax; 65: unsafe/contradictory identity;
# 69: Docker or filesystem inspection failed; 70: a targeted removal failed;
# 71: cleanup completed but required pull headroom is still unavailable;
# 75: the shared mutation lock is already held by another process.
EXIT_USAGE = 64
EXIT_UNSAFE = 65
EXIT_INSPECTION = 69
EXIT_REMOVE = 70
EXIT_LOW_SPACE = 71
EXIT_LOCKED = 75


class RetentionError(Exception):
    def __init__(self, message: str, code: int) -> None:
        super().__init__(message)
        self.code = code


def is_site_alias(reference: str) -> bool:
    return bool(SITE_DIGEST.fullmatch(reference) or SITE_TAG.fullmatch(reference))


def is_site_digest(reference: str) -> bool:
    return bool(SITE_DIGEST.fullmatch(reference))


def _strings(value: Any) -> list[str]:
    if value is None:
        return []
    if not isinstance(value, list) or any(not isinstance(item, str) for item in value):
        raise RetentionError("Docker returned malformed image references", EXIT_INSPECTION)
    return [item for item in value if item]


def image_aliases(image: dict[str, Any]) -> list[str]:
    """Return every known Docker name; absent/none names stay visible as ambiguity."""
    tags = _strings(image.get("RepoTags"))
    digests = _strings(image.get("RepoDigests"))
    aliases = list(dict.fromkeys(tags + digests))
    if not aliases or any(value in {"<none>:<none>", "<none>", ""} for value in aliases):
        aliases.append("<none>:<none>")
    return aliases


def is_owned_site_image(image: dict[str, Any]) -> tuple[bool, str]:
    """Only exact-repository images with complete, digest-pinned ownership qualify."""
    aliases = image_aliases(image)
    known = [item for item in aliases if item != "<none>:<none>"]
    if not known or len(known) != len(aliases):
        return False, "missing or dangling alias"
    if any(not is_site_alias(alias) for alias in known):
        return False, "foreign or unrecognized alias"
    if not any(is_site_digest(alias) for alias in known):
        return False, "no immutable site repository digest"
    return True, "owned by exact site repository"


def _one_reference_file(path: Path, label: str, pattern: re.Pattern[str]) -> str:
    try:
        content = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise RetentionError(f"cannot read {label}: {exc}", EXIT_UNSAFE) from exc
    lines = content.splitlines()
    if len(lines) != 1 or not pattern.fullmatch(lines[0]):
        raise RetentionError(f"{label} must contain exactly one canonical immutable reference", EXIT_UNSAFE)
    return lines[0]


def _deployment_environment(path: Path) -> tuple[str, str]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise RetentionError(f"cannot read deployment.env: {exc}", EXIT_UNSAFE) from exc
    site_values: list[str] = []
    caddy_values: list[str] = []
    for line in lines:
        if line.startswith("LEAPVIEW_SITE_IMAGE="):
            site_values.append(line.partition("=")[2])
        elif line.startswith("CADDY_IMAGE="):
            caddy_values.append(line.partition("=")[2])
        else:
            raise RetentionError("deployment.env contains an unexpected setting", EXIT_UNSAFE)
    if len(site_values) != 1 or not SITE_DIGEST.fullmatch(site_values[0]):
        raise RetentionError("deployment.env must contain one canonical LEAPVIEW_SITE_IMAGE digest", EXIT_UNSAFE)
    if len(caddy_values) != 1 or not GENERIC_DIGEST.fullmatch(caddy_values[0]):
        raise RetentionError("deployment.env must contain one immutable CADDY_IMAGE digest", EXIT_UNSAFE)
    return site_values[0], caddy_values[0]


def _transaction(path: Path) -> tuple[str, str, str] | None:
    if not path.exists():
        return None
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise RetentionError(f"cannot read deployment-in-progress: {exc}", EXIT_UNSAFE) from exc
    fields: dict[str, str] = {}
    for line in lines:
        key, separator, value = line.partition("=")
        if not separator or key in fields or key not in {"version", "previous_image", "candidate_image", "phase"}:
            raise RetentionError("deployment-in-progress is malformed", EXIT_UNSAFE)
        fields[key] = value
    if set(fields) != {"version", "previous_image", "candidate_image", "phase"}:
        raise RetentionError("deployment-in-progress is incomplete", EXIT_UNSAFE)
    if fields["version"] != "1" or fields["phase"] not in TRANSACTION_PHASES:
        raise RetentionError("deployment-in-progress has an unsupported version or phase", EXIT_UNSAFE)
    previous = fields["previous_image"]
    candidate = fields["candidate_image"]
    if not SITE_DIGEST.fullmatch(previous) or not SITE_DIGEST.fullmatch(candidate) or previous == candidate:
        raise RetentionError("deployment-in-progress has invalid image identities", EXIT_UNSAFE)
    return previous, candidate, fields["phase"]


def load_state(site_root: Path) -> dict[str, Any]:
    active, caddy = _deployment_environment(site_root / "deployment.env")
    deployed = _one_reference_file(site_root / "deployed-image", "deployed-image", SITE_DIGEST)
    previous_path = site_root / "previous-image"
    first_path = site_root / "retention-first-install"
    transaction = _transaction(site_root / "deployment-in-progress")
    first_install = False
    if previous_path.exists():
        previous = _one_reference_file(previous_path, "previous-image", SITE_DIGEST)
        if first_path.exists():
            raise RetentionError("first-install marker remains after previous-image was recorded", EXIT_UNSAFE)
        if transaction is None and previous == active:
            raise RetentionError("previous-image duplicates the active image without an in-progress deployment", EXIT_UNSAFE)
    elif first_path.exists():
        previous = None
        marker = _one_reference_file(first_path, "retention-first-install", SITE_DIGEST)
        if marker != active or marker != deployed or transaction is not None:
            raise RetentionError("first-install marker does not match the verified active image", EXIT_UNSAFE)
        first_install = True
    else:
        raise RetentionError("previous-image is missing and no explicit first-install marker exists", EXIT_UNSAFE)

    if transaction is None:
        if active != deployed:
            raise RetentionError("deployment.env and deployed-image disagree", EXIT_UNSAFE)
    else:
        old, candidate, _ = transaction
        if active not in {old, candidate} or deployed not in {old, candidate}:
            raise RetentionError("deployment state contradicts deployment-in-progress", EXIT_UNSAFE)
        _, _, phase = transaction
        if phase == "prepared" and deployed != old:
            raise RetentionError("prepared deployment has an incompatible deployed-image", EXIT_UNSAFE)
        if phase == "activating" and active != candidate:
            raise RetentionError("activating deployment has an incompatible deployment.env", EXIT_UNSAFE)
        if phase in {"rollback-failed", "rollback-verified"} and active != old:
            raise RetentionError("rollback phase has an incompatible deployment.env", EXIT_UNSAFE)
        if phase == "rollback-verified" and deployed != old:
            raise RetentionError("verified rollback has an incompatible deployed-image", EXIT_UNSAFE)
        if phase == "activated" and (active != candidate or deployed != candidate):
            raise RetentionError("activated deployment has incompatible image evidence", EXIT_UNSAFE)

    refs: dict[str, list[tuple[str, str]]] = {}
    refs.setdefault(active, []).append(("active deployment.env", active))
    refs.setdefault(deployed, []).append(("last qualified deployed-image", deployed))
    refs.setdefault(caddy, []).append(("configured Caddy image", caddy))
    if previous is not None:
        refs.setdefault(previous, []).append(("rollback image", previous))
        if transaction is not None:
            old, candidate, phase = transaction
            refs.setdefault(old, []).append((f"in-progress previous image ({phase})", old))
            refs.setdefault(candidate, []).append((f"in-progress candidate ({phase})", candidate))
    return {
        "active": active,
        "deployed": deployed,
        "previous": previous,
        "caddy": caddy,
        "first_install": first_install,
        "transaction": transaction,
        "references": refs,
    }


def _decode_json_list(output: str, context: str) -> list[dict[str, Any]]:
    try:
        result = json.loads(output)
    except json.JSONDecodeError as exc:
        raise RetentionError(f"Docker returned malformed JSON while inspecting {context}", EXIT_INSPECTION) from exc
    if not isinstance(result, list) or any(not isinstance(item, dict) for item in result):
        raise RetentionError(f"Docker returned malformed records while inspecting {context}", EXIT_INSPECTION)
    return result


def _docker(*arguments: str, allow_not_found: bool = False) -> str:
    try:
        result = subprocess.run(
            ["docker", *arguments],
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
    except OSError as exc:
        raise RetentionError(f"cannot run Docker: {exc}", EXIT_INSPECTION) from exc
    if result.returncode != 0:
        if allow_not_found:
            return ""
        message = result.stderr.strip() or result.stdout.strip() or f"exit status {result.returncode}"
        raise RetentionError(f"docker {' '.join(arguments)} failed: {message}", EXIT_INSPECTION)
    return result.stdout


def _inspect_one(kind: str, identifier: str) -> dict[str, Any]:
    records = _decode_json_list(_docker(kind, "inspect", identifier), f"{kind} {identifier}")
    if len(records) != 1:
        raise RetentionError(f"Docker returned {len(records)} records for {kind} {identifier}", EXIT_INSPECTION)
    return records[0]


def inventory_docker() -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    raw_ids = _docker("image", "ls", "--all", "--quiet", "--no-trunc")
    image_ids = list(dict.fromkeys(line.strip() for line in raw_ids.splitlines() if line.strip()))
    images: list[dict[str, Any]] = []
    for image_id in image_ids:
        if not IMAGE_ID.fullmatch(image_id):
            raise RetentionError(f"Docker listed a malformed image ID: {image_id!r}", EXIT_INSPECTION)
        image = _inspect_one("image", image_id)
        if not IMAGE_ID.fullmatch(str(image.get("Id", ""))) or image["Id"] != image_id:
            raise RetentionError(f"image inspection identity disagrees for {image_id}", EXIT_INSPECTION)
        image["_known_aliases"] = image_aliases(image)
        images.append(image)

    raw_container_ids = _docker("container", "ls", "--all", "--quiet", "--no-trunc")
    containers: list[dict[str, Any]] = []
    for container_id in dict.fromkeys(line.strip() for line in raw_container_ids.splitlines() if line.strip()):
        container = _inspect_one("container", container_id)
        if not IMAGE_ID.fullmatch(str(container.get("Image", ""))):
            raise RetentionError(f"container {container_id} has a malformed image identity", EXIT_INSPECTION)
        containers.append(container)
    return images, containers


def _image_maps(images: list[dict[str, Any]]) -> tuple[dict[str, dict[str, Any]], dict[str, set[str]]]:
    by_id: dict[str, dict[str, Any]] = {}
    aliases: dict[str, set[str]] = {}
    for image in images:
        image_id = image.get("Id")
        if not isinstance(image_id, str) or not IMAGE_ID.fullmatch(image_id) or image_id in by_id:
            raise RetentionError("image inventory contains a malformed or duplicate identity", EXIT_INSPECTION)
        by_id[image_id] = image
        image.setdefault("_known_aliases", image_aliases(image))
        for alias in image["_known_aliases"]:
            if alias != "<none>:<none>":
                aliases.setdefault(alias, set()).add(image_id)
    return by_id, aliases


def build_plan(
    images: list[dict[str, Any]],
    containers: list[dict[str, Any]],
    state: dict[str, Any],
    candidate_ref: str | None = None,
    require_candidate: bool = False,
) -> dict[str, Any]:
    """Pure planner used by the CLI and synthetic policy tests."""
    if candidate_ref is not None and not SITE_DIGEST.fullmatch(candidate_ref):
        raise RetentionError("candidate reference must be a canonical immutable site digest", EXIT_USAGE)
    by_id, aliases = _image_maps(images)
    protected_ids: set[str] = set()
    protected_refs: dict[str, set[str]] = {}

    running_site = [
        container
        for container in containers
        if isinstance((container.get("Config") or {}).get("Labels"), dict)
        and (container.get("Config") or {}).get("Labels", {}).get("com.docker.compose.project") == "leapview-site"
        and (container.get("Config") or {}).get("Labels", {}).get("com.docker.compose.service") == "leapview-site"
        and (container.get("State") or {}).get("Running") is True
    ]
    if len(running_site) > 1:
        raise RetentionError("multiple running LeapView site containers make deployed identity ambiguous", EXIT_UNSAFE)
    if running_site:
        container = running_site[0]
        image_id = container.get("Image")
        config_image = (container.get("Config") or {}).get("Image", "")
        transaction = state.get("transaction")
        allowed_references = set(transaction[:2]) if transaction is not None else {state["active"], state["deployed"]}
        if (
            not isinstance(image_id, str)
            or image_id not in by_id
            or not isinstance(config_image, str)
            or not SITE_DIGEST.fullmatch(config_image)
            or config_image not in allowed_references
            or aliases.get(config_image) != {image_id}
        ):
            raise RetentionError(
                "running LeapView site container contradicts the recorded deployment identity",
                EXIT_UNSAFE,
            )

    def protect_reference(reference: str, reason: str, required: bool = True) -> None:
        found = aliases.get(reference, set())
        if not found:
            if required:
                raise RetentionError(f"protected reference is not present in the local image inventory: {reference}", EXIT_UNSAFE)
            return
        protected_ids.update(found)
        protected_refs.setdefault(reference, set()).add(reason)

    for reference, reasons in state["references"].items():
        # The configured Caddy image is foreign by design, but still record it
        # when present. Site state identities must always resolve locally.
        for reason, exact_reference in reasons:
            required = not (reason == "configured Caddy image" or "candidate" in reason)
            protect_reference(exact_reference, reason, required=required)

    # A locally resolved production tag is a candidate root before the next pull.
    for image in images:
        if f"{SITE_REPOSITORY}:production" in image["_known_aliases"]:
            protected_ids.add(image["Id"])
            protected_refs.setdefault(f"{SITE_REPOSITORY}:production", set()).add("currently resolved production tag")
    if candidate_ref is not None:
        protect_reference(candidate_ref, "selected deployment candidate", required=require_candidate)

    for container in containers:
        image_id = container.get("Image")
        if image_id not in by_id:
            raise RetentionError(f"container image {image_id!r} is absent from the local inventory", EXIT_UNSAFE)
        protected_ids.add(image_id)
        config_image = (container.get("Config") or {}).get("Image", "")
        if config_image and is_site_alias(config_image):
            protect_reference(config_image, f"container {container.get('Id', '<unknown>')} configured image")
        elif config_image:
            protected_refs.setdefault(config_image, set()).add(
                f"container {container.get('Id', '<unknown>')} configured image (foreign or mutable)"
            )

    # Current transaction scratch env files can contain a candidate when a
    # process stopped between writing it and publishing the transaction record.
    # Their references are loaded into state by the caller.
    protected: list[dict[str, Any]] = []
    for image_id in sorted(protected_ids):
        image = by_id.get(image_id)
        if image is not None:
            protected.append({
                "id": image_id,
                "aliases": image["_known_aliases"],
                "reasons": sorted({reason for alias in image["_known_aliases"] for reason in protected_refs.get(alias, set())}),
            })

    removals: list[dict[str, Any]] = []
    preserved: list[dict[str, Any]] = []
    for image_id in sorted(by_id):
        image = by_id[image_id]
        aliases_for_image = image["_known_aliases"]
        owned, ownership_reason = is_owned_site_image(image)
        if image_id in protected_ids:
            continue
        if not owned:
            if any(alias.startswith(SITE_REPOSITORY) or alias == "<none>:<none>" for alias in aliases_for_image):
                preserved.append({"id": image_id, "aliases": aliases_for_image, "reason": ownership_reason})
            continue
        shared_alias = next((alias for alias in aliases_for_image if len(aliases.get(alias, ())) > 1), None)
        if shared_alias:
            preserved.append({
                "id": image_id,
                "aliases": aliases_for_image,
                "reason": f"alias maps to multiple local image identities: {shared_alias}",
            })
            continue
        refs = sorted(alias for alias in aliases_for_image if alias != "<none>:<none>")
        # Remove complete repository references one at a time, never by image
        # ID (which could drop an alias unknown to this inventory).
        removals.append({"id": image_id, "aliases": refs, "reason": "obsolete exact-repository site image"})

    return {
        "protected": protected,
        "proposed_removals": removals,
        "preserved_ambiguous": preserved,
    }


def _read_transaction_scratch(site_root: Path, state: dict[str, Any]) -> None:
    """Protect scratch env files only while they match the recorded transaction."""
    transaction = state["transaction"]
    if transaction is None:
        return
    transaction_refs = {transaction[0], transaction[1]}
    scratch_refs: list[tuple[str, str]] = []
    for pattern in ("deployment.env.next.*", "deployment.env.restore.*"):
        for path in sorted(site_root.glob(pattern)):
            try:
                active, _ = _deployment_environment(path)
            except RetentionError:
                # An unreadable/orphan scratch file cannot name an image root.
                # The transaction record itself already protects both images.
                continue
            if active in transaction_refs:
                scratch_refs.append((active, f"current transaction recovery file {path.name}"))
    # Preserve these refs through the caller's mutable state dictionary.
    for reference, reason in scratch_refs:
        state["references"].setdefault(reference, []).append((reason, reference))


def _storage_path(explicit: str | None) -> Path:
    if explicit:
        path = Path(explicit)
    else:
        status_output = _docker("info", "--format", "{{json .DriverStatus}}").strip()
        try:
            driver_status = json.loads(status_output)
        except json.JSONDecodeError as exc:
            raise RetentionError("Docker returned malformed DriverStatus while locating image storage", EXIT_INSPECTION) from exc
        if driver_status is None:
            driver_status = []
        if not isinstance(driver_status, list) or any(
            not isinstance(pair, list) or len(pair) != 2 or not all(isinstance(item, str) for item in pair)
            for pair in driver_status
        ):
            raise RetentionError("Docker returned malformed DriverStatus while locating image storage", EXIT_INSPECTION)
        driver_values = {key: value for key, value in driver_status}
        image_store_type = driver_values.get("driver-type")
        if image_store_type == "io.containerd.snapshotter.v1":
            path = _containerd_storage_root()
        elif image_store_type is not None:
            raise RetentionError(f"unsupported Docker image storage backend: {image_store_type}", EXIT_INSPECTION)
        else:
            root = _docker("info", "--format", "{{.DockerRootDir}}").strip()
            if not root or "\n" in root:
                raise RetentionError("Docker returned an invalid DockerRootDir", EXIT_INSPECTION)
            path = Path(root)
    if not path.exists() or not path.is_dir():
        raise RetentionError(f"storage path is not an existing directory: {path}", EXIT_INSPECTION)
    return path


def _containerd_storage_root(config_path: Path | None = None) -> Path:
    """Resolve containerd's configured root, rejecting config we cannot safely read."""
    config = config_path or Path("/etc/containerd/config.toml")
    if not config.exists():
        return Path("/var/lib/containerd")
    try:
        contents = config.read_text(encoding="utf-8")
    except OSError as exc:
        raise RetentionError(f"cannot read containerd configuration {config}: {exc}", EXIT_INSPECTION) from exc
    if tomllib is None:
        raise RetentionError(
            f"cannot parse containerd configuration {config} with this Python; pass --storage-path explicitly",
            EXIT_INSPECTION,
        )
    try:
        parsed = tomllib.loads(contents)
    except tomllib.TOMLDecodeError as exc:
        raise RetentionError(f"containerd configuration {config} is malformed: {exc}", EXIT_INSPECTION) from exc
    imports = parsed.get("imports", [])
    if imports:
        raise RetentionError(
            f"containerd configuration {config} imports other files; pass --storage-path explicitly",
            EXIT_INSPECTION,
        )
    root = parsed.get("root", "/var/lib/containerd")
    if not isinstance(root, str) or not root.startswith("/") or "\n" in root:
        raise RetentionError(f"containerd configuration {config} has a non-absolute root", EXIT_INSPECTION)
    return Path(root)


def _free_bytes(path: Path) -> int:
    try:
        usage = os.statvfs(path)
        return usage.f_bavail * usage.f_frsize
    except OSError as exc:
        raise RetentionError(f"cannot measure free space at {path}: {exc}", EXIT_INSPECTION) from exc


def _acquire_lock(site_root: Path, lock_fd: int | None) -> tuple[int, bool]:
    lock_path = site_root / "site-mutation.lock"
    if lock_fd is None:
        try:
            fd = os.open(lock_path, os.O_RDWR | os.O_CREAT, 0o640)
        except OSError as exc:
            raise RetentionError(f"cannot open mutation lock {lock_path}: {exc}", EXIT_LOCKED) from exc
        owns_fd = True
    else:
        fd = lock_fd
        owns_fd = False
        try:
            lock_stat = os.stat(lock_path)
            fd_stat = os.fstat(fd)
        except OSError as exc:
            raise RetentionError(f"cannot validate inherited mutation lock: {exc}", EXIT_LOCKED) from exc
        if (lock_stat.st_dev, lock_stat.st_ino) != (fd_stat.st_dev, fd_stat.st_ino):
            raise RetentionError("inherited lock descriptor is not site-mutation.lock", EXIT_LOCKED)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError as exc:
        if owns_fd:
            os.close(fd)
        raise RetentionError("site mutation lock is already held", EXIT_LOCKED) from exc
    except OSError as exc:
        if owns_fd:
            os.close(fd)
        raise RetentionError(f"cannot acquire site mutation lock: {exc}", EXIT_LOCKED) from exc
    return fd, owns_fd


def _release_lock(fd: int, owns_fd: bool) -> None:
    if owns_fd:
        try:
            fcntl.flock(fd, fcntl.LOCK_UN)
        finally:
            os.close(fd)


def _emit(document: dict[str, Any], stream: Any = sys.stdout) -> None:
    print(json.dumps(document, sort_keys=True, separators=(",", ":")), file=stream)


def execute(args: argparse.Namespace) -> int:
    site_root = Path(args.site_root)
    removed: list[str] = []
    storage: Path | None = None
    free_before: int | None = None
    try:
        fd, owns_fd = _acquire_lock(site_root, args.lock_fd)
    except RetentionError as exc:
        _emit({"error": str(exc), "status": "locked"}, sys.stderr)
        return exc.code
    try:
        storage = _storage_path(args.storage_path)
        state = load_state(site_root)
        _read_transaction_scratch(site_root, state)
        images, containers = inventory_docker()
        plan = build_plan(images, containers, state, args.candidate_ref, args.require_candidate)
        free_before = _free_bytes(storage)
        if args.operation == "apply":
            if state["transaction"] is not None:
                raise RetentionError(
                    "an unresolved deployment-in-progress record must be reconciled before cleanup",
                    EXIT_UNSAFE,
                )
            # Re-read the full inventory and every state root immediately before
            # the destructive phase while the shared lock remains held.
            state = load_state(site_root)
            _read_transaction_scratch(site_root, state)
            images, containers = inventory_docker()
            plan = build_plan(images, containers, state, args.candidate_ref, args.require_candidate)
            for group in plan["proposed_removals"]:
                for reference in group["aliases"]:
                    try:
                        result = subprocess.run(
                            ["docker", "image", "rm", reference],
                            check=False,
                            stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE,
                            text=True,
                        )
                    except OSError as exc:
                        raise RetentionError(f"cannot run targeted docker image removal: {exc}", EXIT_REMOVE) from exc
                    if result.returncode != 0:
                        message = result.stderr.strip() or result.stdout.strip() or f"exit status {result.returncode}"
                        raise RetentionError(f"docker image rm {reference} failed: {message}", EXIT_REMOVE)
                    removed.append(reference)
        free_after = _free_bytes(storage)
        document = {
            "mode": args.operation,
            "storage_path": str(storage),
            "free_bytes_before": free_before,
            "free_bytes_after": free_after,
            "minimum_free_bytes": args.min_free_bytes,
            "active_image": state["active"],
            "previous_image": state["previous"],
            "candidate_image": args.candidate_ref,
            "protected": plan["protected"],
            "proposed_removals": plan["proposed_removals"],
            "removed_references": removed,
            "preserved_ambiguous": plan["preserved_ambiguous"],
            "first_install": state["first_install"],
            "transaction": state["transaction"],
        }
        _emit(document)
        if args.operation == "apply" and free_after < args.min_free_bytes:
            return EXIT_LOW_SPACE
        return 0
    except RetentionError as exc:
        document: dict[str, Any] = {
            "error": str(exc),
            "mode": args.operation,
            "status": "unsafe-or-failed",
            "removed_references": removed,
        }
        if storage is not None:
            document["storage_path"] = str(storage)
        if free_before is not None:
            document["free_bytes_before"] = free_before
        _emit(document, sys.stderr)
        return exc.code
    finally:
        _release_lock(fd, owns_fd)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument(
        "operation",
        nargs="?",
        default="plan",
        choices=("plan", "apply"),
        help="plan is always dry-run; apply removes only listed refs (default: plan)",
    )
    result.add_argument("--site-root", default="/opt/leapview-site")
    result.add_argument("--storage-path", help="filesystem path holding containerd data; defaults to detected storage root")
    result.add_argument("--min-free-bytes", type=int, default=DEFAULT_MIN_FREE_BYTES)
    result.add_argument("--candidate-ref", help="immutable site digest protected for an in-progress deployment")
    result.add_argument("--require-candidate", action="store_true", help="fail if the candidate is not in the local inventory")
    result.add_argument("--lock-fd", type=int, help="reuse the inherited site-mutation.lock descriptor")
    return result


def main(argv: Iterable[str] | None = None) -> int:
    args = parser().parse_args(argv)
    if args.min_free_bytes < 0:
        print("--min-free-bytes cannot be negative", file=sys.stderr)
        return EXIT_USAGE
    if args.candidate_ref is not None and not SITE_DIGEST.fullmatch(args.candidate_ref):
        print("--candidate-ref must be a canonical immutable site digest", file=sys.stderr)
        return EXIT_USAGE
    if args.require_candidate and args.candidate_ref is None:
        print("--require-candidate requires --candidate-ref", file=sys.stderr)
        return EXIT_USAGE
    return execute(args)


if __name__ == "__main__":
    sys.exit(main())

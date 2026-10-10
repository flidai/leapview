"""Private transport checks for the disposable installed coordinator fixture.

These hashes bind fixture bytes and producer executables; they are not recovery
preparation, enrollment, fencing, release admission or qualification authority.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import stat
from urllib.parse import urlencode, urlsplit, urlunsplit


def sha256_file(path):
    result = hashlib.sha256()
    with Path(path).open("rb") as source:
        while chunk := source.read(1024 * 1024):
            result.update(chunk)
    return "sha256:" + result.hexdigest()


def private_bytes(path, limit=16 * 1024 * 1024):
    path = Path(path)
    info = path.lstat()
    parent = path.parent.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid()
            or stat.S_IMODE(info.st_mode) & 0o077 or not 0 < info.st_size <= limit
            or not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.geteuid()
            or stat.S_IMODE(parent.st_mode) & 0o077 or path.resolve(strict=True) != path):
        raise ValueError("private canonical fixture file required")
    with path.open("rb") as source:
        value = source.read(limit + 1)
    if len(value) != info.st_size:
        raise ValueError("fixture file changed while reading")
    return value


def verified_export(directory, producer):
    directory = Path(directory)
    payload = private_bytes(directory / "export.json")
    if private_bytes(directory / "export.sha256").decode() != "sha256:" + hashlib.sha256(payload).hexdigest():
        raise ValueError("fixture export identity differs")
    value = json.loads(payload)
    if (value["schemaVersion"] != 1 or value["scope"] != "disposable-installed-coordinator-component"
            or value["producerSHA256"] != sha256_file(producer)
            or re.fullmatch(r"[0-9a-f]{40}", value["sourceRevision"]) is None
            or any(value[key] is not False for key in (
                "activationQualified", "releaseAdmissionQualified", "fullManagedProfileQualified"))):
        raise ValueError("fixture provenance or gate boundary differs")
    manifest_bytes = private_bytes(directory / "bundle-manifest.json")
    if private_bytes(directory / "bundle.sha256").decode() != "sha256:" + hashlib.sha256(manifest_bytes).hexdigest():
        raise ValueError("fixture bundle identity differs")
    manifest = json.loads(manifest_bytes)
    if manifest["schemaVersion"] != 1 or not 0 < len(manifest["files"]) <= 100000:
        raise ValueError("bounded fixture file manifest required")
    seen = set()
    for entry in manifest["files"]:
        name = entry["path"]
        path = directory / name
        if (not isinstance(name, str) or name in seen or name.startswith("/")
                or any(part in ("", ".", "..") for part in name.split("/"))
                or path.resolve(strict=True) != path or not path.is_file()
                or path.stat().st_size != entry["size"]
                or sha256_file(path) != "sha256:" + entry["sha256"]):
            raise ValueError("exact immutable fixture content differs")
        seen.add(name)
    actual = set()
    for path in directory.rglob("*"):
        if path.is_symlink() or not (path.is_file() or path.is_dir()):
            raise ValueError("fixture contains a linked or special file")
        if path.is_file():
            actual.add(path.relative_to(directory).as_posix())
    if actual != seen | {"bundle-manifest.json", "bundle.sha256"}:
        raise ValueError("fixture manifest does not cover its exact files")
    return value


def tls_url(raw, address, ca_path=None):
    parsed = urlsplit(raw)
    if parsed.scheme not in ("postgres", "postgresql") or not parsed.username or not parsed.password:
        raise ValueError("explicit retained PostgreSQL credentials required")
    credentials = parsed.netloc.rsplit("@", 1)[0]
    query = {"sslmode": ["verify-full"]}
    if ca_path is not None:
        query["sslrootcert"] = [ca_path]
    return urlunsplit(("postgres", credentials + "@" + address, parsed.path, urlencode(query, doseq=True), ""))


def source_configuration(value, ca_path):
    # Production pools reject loopback destinations. Preparation runs on the
    # separate replacement host, already admitted by the source's private HBA.
    result = dict(value["config"])
    for key, raw in result.items():
        if key.startswith("Postgres") and key.endswith("URL") and raw:
            result[key] = tls_url(raw, "192.168.1.2:5432", ca_path)
    return result


def verify_component_receipt(receipt):
    if (receipt.get("schemaVersion") != 1
            or receipt.get("kind") != "leapview/managed-recovery-preactivation-qualification"
            or receipt.get("scope") != "fresh-managed-coordinator-replay-admission"
            or receipt.get("activationQualified") is not False
            or receipt.get("fullManagedProfileQualified") is not False
            or receipt.get("admission", {}).get("originalsFenced") is not True
            or receipt["admission"].get("activationQualified") is not False):
        raise ValueError("installed component did not preserve private admission boundary")
    return receipt

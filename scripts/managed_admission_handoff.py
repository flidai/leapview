#!/usr/bin/env python3
"""Import an exact authenticated GitHub producer receipt into a managed host.

The network boundary is GitHub's authenticated API and artifact SHA-256, not
downloaded JSON decision flags. Canonical authority remains the Go verifier.
This command never pulls an image, changes traffic, or creates an operation.
"""

import argparse
from datetime import datetime, timezone
import hashlib
import io
import json
import os
from pathlib import Path
import re
import secrets
import selectors
import stat
import subprocess
import sys
import tempfile
import time
import zipfile

REPOSITORY = "flidai/leapview"
WORKFLOW = REPOSITORY + "/.github/workflows/artifacts.yml"
PRODUCERS = {".github/workflows/artifacts.yml": "push",
             ".github/workflows/release.yml": "workflow_dispatch"}
BUNDLE_FILES = frozenset({"admission.json", "admission.digest", "binding.json",
                          "verified-attestation.json", "sbom.json", "image-config.json",
                          "container-vulnerability-policy.json", "vulnerability-report.json", "trivy-report.json"})
MAX_ZIP_BYTES = 64 * 1024 * 1024
MAX_MEMBER_BYTES = 32 * 1024 * 1024
MAX_TOTAL_BYTES = 128 * 1024 * 1024
REQUEST_TIMEOUT_SECONDS = 180
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
REVISION = re.compile(r"[0-9a-f]{40}\Z")


class HandoffError(Exception):
    pass


def _timestamp(value):
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
        if parsed.tzinfo is None:
            raise ValueError("timezone missing")
        return parsed
    except (ValueError, TypeError, AttributeError):
        raise HandoffError("GitHub timestamp is missing or invalid") from None


def _positive(value):
    return type(value) is int and value > 0


def _validate_selection(run_id, attempt, artifact_id, source, platform):
    if (not all(_positive(value) for value in (run_id, attempt, artifact_id))
            or not isinstance(source, str) or not REVISION.fullmatch(source)
            or platform not in ("linux/amd64", "linux/arm64")):
        raise HandoffError("run, attempt, artifact, source and platform must be exact identities")


def authorize(run, workflow, artifact, *, run_id, attempt, artifact_id, source, platform, now=None):
    """Cross-check raw authenticated REST responses against operator selection."""
    now = now or datetime.now(timezone.utc)
    _validate_selection(run_id, attempt, artifact_id, source, platform)
    if not all(isinstance(value, dict) for value in (run, workflow, artifact)):
        raise HandoffError("GitHub metadata must be objects")
    repository = run.get("repository") or {}
    head_repository = run.get("head_repository") or {}
    producer = workflow.get("path")
    if (run.get("id") != run_id or run.get("run_attempt") != attempt
            or not _positive(workflow.get("id")) or run.get("workflow_id") != workflow["id"]
            or run.get("path") != producer
            or producer not in PRODUCERS or run.get("event") != PRODUCERS[producer]
            or run.get("status") != "completed" or run.get("conclusion") != "success"
            or run.get("head_branch") != "main" or run.get("head_sha") != source
            or repository.get("full_name") != REPOSITORY or head_repository.get("full_name") != REPOSITORY
            or not _positive(repository.get("id")) or head_repository.get("id") != repository["id"]):
        raise HandoffError("run is not the selected successful protected producer on main")
    artifact_run = artifact.get("workflow_run") or {}
    digest = artifact.get("digest")
    expected_name = f"managed-admission-{run_id}-{attempt}-{platform.split('/')[1]}"
    if (artifact.get("id") != artifact_id or artifact.get("name") != expected_name
            or artifact.get("expired") is not False or not isinstance(digest, str) or not DIGEST.fullmatch(digest)
            or not _positive(artifact.get("size_in_bytes")) or artifact["size_in_bytes"] > MAX_ZIP_BYTES
            or artifact_run.get("id") != run_id or artifact_run.get("repository_id") != repository["id"]
            or artifact_run.get("head_repository_id") != repository["id"]
            or artifact_run.get("head_branch") != "main" or artifact_run.get("head_sha") != source):
        raise HandoffError("artifact is not the exact unexpired producer attempt and source")
    if (not _timestamp(run.get("run_started_at")) <= _timestamp(artifact.get("created_at")) <= _timestamp(run.get("updated_at"))
            or _timestamp(artifact.get("expires_at")) <= now):
        raise HandoffError("artifact timestamp is outside the selected attempt or expired")
    return {"repository": REPOSITORY, "workflow": REPOSITORY + "/" + producer,
            "event": run["event"], "ref": "refs/heads/main", "runId": str(run_id), "runAttempt": str(attempt),
            "sourceRevision": source, "platform": platform, "artifactId": artifact_id,
            "artifactName": expected_name, "artifactDigest": digest, "workflowId": workflow["id"]}


def _strict_json(data):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise HandoffError("JSON repeats an object field")
            result[key] = value
        return result
    try:
        return json.loads(data, object_pairs_hook=pairs)
    except (ValueError, UnicodeError, RecursionError):
        raise HandoffError("invalid JSON in authenticated handoff") from None


def verify_bundle(data, digest, authorization, *, image):
    if len(data) > MAX_ZIP_BYTES or "sha256:" + hashlib.sha256(data).hexdigest() != digest:
        raise HandoffError("downloaded artifact differs from its authenticated GitHub SHA-256")
    files = {}
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            if len(archive.infolist()) != len(BUNDLE_FILES):
                raise HandoffError("artifact must contain exactly the producer handoff files")
            total = 0
            for member in archive.infolist():
                mode = stat.S_IFMT(member.external_attr >> 16)
                if (member.filename not in BUNDLE_FILES or member.filename in files or mode not in (0, stat.S_IFREG)
                        or member.flag_bits & 1 or member.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED)
                        or not 0 < member.file_size <= MAX_MEMBER_BYTES):
                    raise HandoffError("artifact contains an unsafe, repeated or oversized member")
                total += member.file_size
                if total > MAX_TOTAL_BYTES:
                    raise HandoffError("artifact unpacked size exceeds its limit")
                with archive.open(member) as stream:
                    content = stream.read(MAX_MEMBER_BYTES + 1)
                if len(content) != member.file_size:
                    raise HandoffError("artifact member size differs from its metadata")
                files[member.filename] = content
    except (OSError, ValueError, RuntimeError, zipfile.BadZipFile, EOFError):
        raise HandoffError("artifact ZIP cannot be safely read") from None
    binding = _strict_json(files["binding.json"])
    expected_fields = {"schemaVersion", "repository", "workflow", "event", "ref", "sourceRevision", "image",
                       "platform", "admissionDigest", "runId", "runAttempt", "files"}
    if not isinstance(binding, dict) or set(binding) != expected_fields or type(binding["schemaVersion"]) is not int or binding["schemaVersion"] != 1:
        raise HandoffError("producer binding has an unsupported shape")
    for field in ("repository", "workflow", "event", "ref", "sourceRevision", "platform", "runId", "runAttempt"):
        if binding[field] != authorization[field]:
            raise HandoffError(f"producer binding differs from authenticated {field}")
    if binding["image"] != image or not isinstance(binding["admissionDigest"], str) or not DIGEST.fullmatch(binding["admissionDigest"]):
        raise HandoffError("producer binding differs from the selected immutable image or receipt")
    if files["admission.digest"] not in (binding["admissionDigest"].encode(), (binding["admissionDigest"] + "\n").encode()):
        raise HandoffError("producer admission digest sidecar differs from binding")
    expected_hashes = {name: "sha256:" + hashlib.sha256(content).hexdigest() for name, content in files.items() if name != "binding.json"}
    if binding["files"] != expected_hashes:
        raise HandoffError("producer file hashes differ from authenticated evidence")
    return files


def _private_root(root):
    """Hold the same trusted, no-symlink ancestry required by the controller."""
    if sys.platform != "linux" or os.geteuid() != 0:
        raise HandoffError("private admission storage requires Linux root")
    root = Path(root)
    if not root.is_absolute() or ".." in root.parts:
        raise HandoffError("private admission storage must have an absolute canonical path")
    descriptor = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in (None, *root.parts[1:]):
            if part is not None:
                child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=descriptor)
                os.close(descriptor)
                descriptor = child
            info = os.fstat(descriptor)
            if info.st_uid != 0 or (info.st_mode & 0o022 and not info.st_mode & stat.S_ISVTX):
                raise HandoffError("private admission storage has an untrusted ancestor")
        info = os.fstat(descriptor)
        if stat.S_IMODE(info.st_mode) != 0o700:
            raise HandoffError("private admission storage must already be root-owned mode 0700")
        return descriptor
    except (HandoffError, OSError) as error:
        os.close(descriptor)
        raise HandoffError(f"cannot open private admission storage: {error}") from None


def install_receipt(root, digest, receipt):
    """Publish by no-replace hard link using a held root-private directory FD."""
    if not DIGEST.fullmatch(digest):
        raise HandoffError("receipt digest must be exact")
    descriptor = _private_root(root)
    temporary = None
    try:
        temporary_name = ".import-" + secrets.token_hex(16)
        fd = os.open(temporary_name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=descriptor)
        temporary = temporary_name
        with os.fdopen(fd, "wb") as output:
            output.write(receipt)
            output.flush()
            os.fchmod(output.fileno(), 0o400)
            os.fsync(output.fileno())
        name = digest.removeprefix("sha256:") + ".json"
        os.link(temporary, name, src_dir_fd=descriptor, dst_dir_fd=descriptor, follow_symlinks=False)
        os.fsync(descriptor)
        return Path(root) / name
    except OSError as error:
        raise HandoffError(f"cannot install immutable admission receipt: {error.strerror}") from None
    finally:
        try:
            if temporary is not None:
                os.unlink(temporary, dir_fd=descriptor)
                os.fsync(descriptor)
        finally:
            os.close(descriptor)


def retain_evidence(directory, files, artifact, authorization):
    """Retain exact authenticated bytes before installation, without overwrite."""
    directory = Path(directory)
    if directory.name in ("", ".", ".."):
        raise HandoffError("evidence directory must be a fresh named directory")
    parent = _private_root(directory.parent)
    descriptor = None
    created = False
    written = []
    complete = False
    try:
        os.mkdir(directory.name, mode=0o700, dir_fd=parent)
        created = True
        descriptor = os.open(directory.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        contents = {**files, "artifact.zip": artifact,
                    "github-authorization.json": (json.dumps(authorization, sort_keys=True) + "\n").encode()}
        for name, data in contents.items():
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=descriptor)
            written.append(name)
            with os.fdopen(fd, "wb") as output:
                output.write(data)
                output.flush()
                os.fchmod(output.fileno(), 0o400)
                os.fsync(output.fileno())
        os.fsync(descriptor)
        os.fsync(parent)
        complete = True
    except OSError as error:
        raise HandoffError(f"cannot retain authenticated admission evidence: {error.strerror}") from None
    finally:
        try:
            if created and not complete:
                for name in written:
                    os.unlink(name, dir_fd=descriptor)
                os.rmdir(directory.name, dir_fd=parent)
                os.fsync(parent)
        finally:
            if descriptor is not None:
                os.close(descriptor)
            os.close(parent)


def _github(path, *, limit):
    """Use authenticated gh against github.com; never trust caller-supplied URLs."""
    process = subprocess.Popen(["gh", "api", "--hostname", "github.com", "-H", "Accept: application/vnd.github+json",
                                "-H", "X-GitHub-Api-Version: 2022-11-28", f"repos/{REPOSITORY}/{path}"],
                               stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    data = bytearray()
    deadline = time.monotonic() + REQUEST_TIMEOUT_SECONDS
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0 or not selector.select(remaining):
                    raise HandoffError("authenticated GitHub request timed out")
                chunk = os.read(process.stdout.fileno(), min(65536, limit + 1 - len(data)))
                if not chunk:
                    break
                data.extend(chunk)
                if len(data) > limit:
                    raise HandoffError("GitHub response exceeds its size limit")
        process.wait(timeout=max(0.001, deadline - time.monotonic()))
        if process.returncode != 0:
            raise HandoffError("authenticated GitHub request failed; check gh authentication and artifact access")
        return bytes(data)
    except subprocess.TimeoutExpired:
        raise HandoffError("authenticated GitHub request timed out") from None
    finally:
        if process.poll() is None:
            process.kill()
        process.wait()
        process.stdout.close()


def fetch(run_id, attempt, artifact_id, source, platform):
    _validate_selection(run_id, attempt, artifact_id, source, platform)
    run = _strict_json(_github(f"actions/runs/{run_id}/attempts/{attempt}", limit=4 * 1024 * 1024))
    workflow_id = run.get("workflow_id") if isinstance(run, dict) else None
    if not _positive(workflow_id):
        raise HandoffError("run workflow ID is invalid")
    workflow = _strict_json(_github(f"actions/workflows/{workflow_id}", limit=1024 * 1024))
    artifact = _strict_json(_github(f"actions/artifacts/{artifact_id}", limit=1024 * 1024))
    authorization = authorize(run, workflow, artifact, run_id=run_id, attempt=attempt,
                              artifact_id=artifact_id, source=source, platform=platform)
    data = _github(f"actions/artifacts/{artifact_id}/zip", limit=MAX_ZIP_BYTES)
    return authorization, data


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-id", type=int, required=True)
    parser.add_argument("--run-attempt", type=int, required=True)
    parser.add_argument("--artifact-id", type=int, required=True)
    parser.add_argument("--source-revision", required=True)
    parser.add_argument("--platform", choices=("linux/amd64", "linux/arm64"), required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--verifier", type=Path, required=True, help="trusted locally built ociadmission executable")
    parser.add_argument("--admission-root", type=Path, required=True, help="existing root-owned 0700 receipt directory")
    parser.add_argument("--evidence-dir", type=Path, help="fresh evidence directory under an existing root-owned 0700 parent")
    args = parser.parse_args(argv)
    try:
        if os.geteuid() != 0 or sys.platform != "linux":
            raise HandoffError("authenticated managed receipt import requires Linux root")
        authorization, data = fetch(args.run_id, args.run_attempt, args.artifact_id, args.source_revision, args.platform)
        files = verify_bundle(data, authorization["artifactDigest"], authorization, image=args.image)
        binding = _strict_json(files["binding.json"])
        with tempfile.TemporaryDirectory(prefix="managed-admission-") as temporary:
            for name, content in files.items():
                Path(temporary, name).write_bytes(content)
            command = [str(args.verifier), "verify-receipt", "--bundle", temporary, "--image", args.image,
                       "--source-revision", args.source_revision, "--platform", args.platform,
                       "--expected-workflow", authorization["workflow"], "--admission-digest", binding["admissionDigest"]]
            result = subprocess.run(command, check=False, capture_output=True, text=True, timeout=120)
            if result.returncode != 0 or result.stdout.strip() != binding["admissionDigest"]:
                raise HandoffError("Go canonical receipt and evidence verification failed")
        if args.evidence_dir is not None:
            retain_evidence(args.evidence_dir, files, data, authorization)
        path = install_receipt(args.admission_root, binding["admissionDigest"], files["admission.json"])
        result = {**authorization, "image": args.image, "admissionDigest": binding["admissionDigest"], "installedPath": str(path)}
        if args.evidence_dir is not None:
            result["evidencePath"] = str(args.evidence_dir)
        print(json.dumps(result, sort_keys=True))
        return 0
    except (HandoffError, OSError, subprocess.SubprocessError) as error:
        print(f"Managed admission handoff rejected: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())

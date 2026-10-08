#!/usr/bin/env python3
"""Select authenticated inputs for isolated managed-application qualification.

The existing handoff importer and Go verifier remain the admission authority.
This module only discovers their exact inputs and checks source compatibility.
"""

import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import zipfile

import demo_upgrade_plan
import managed_admission_handoff as handoff

IMAGE = re.compile(r"ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}\Z")
PLATFORM = "linux/amd64"


class InputError(Exception):
    pass


def discover_image(data, digest):
    # Read only a bounded binding after authenticating the original ZIP. The
    # importer subsequently validates every member and calls the Go verifier.
    if len(data) > handoff.MAX_ZIP_BYTES or "sha256:" + hashlib.sha256(data).hexdigest() != digest:
        raise InputError("admission archive differs from its authenticated digest")
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            member = archive.getinfo("binding.json")
            if not 0 < member.file_size <= 65536:
                raise InputError("admission binding exceeds its size limit")
            with archive.open(member) as stream:
                binding = handoff._strict_json(stream.read(65537))
        image = binding.get("image") if isinstance(binding, dict) else None
        if not isinstance(image, str) or IMAGE.fullmatch(image) is None:
            raise InputError("admission binding does not select an immutable LeapView image")
        return image
    except (KeyError, ValueError, OSError, zipfile.BadZipFile, handoff.HandoffError) as error:
        raise InputError("cannot read bounded authenticated admission binding") from error


def compatible_sources(selections, *, inspect=demo_upgrade_plan.inspect_transition):
    predecessor, candidate = selections["predecessor"], selections["candidate"]
    if (predecessor["image"] == candidate["image"] or
            predecessor["sourceRevision"] == candidate["sourceRevision"]):
        raise InputError("qualification requires distinct admitted images and source revisions")
    transitions = []
    for first, second in (("bootstrap", "predecessor"), ("predecessor", "candidate")):
        try:
            transition = inspect(selections[first]["sourceRevision"], selections[second]["sourceRevision"])
        except (ValueError, subprocess.SubprocessError) as error:
            raise InputError("source transition requires separate qualification") from error
        if (transition.get("mode") != "image-only" or transition.get("imageOnlyEligible") is not True
                or transition.get("pendingMigrations") != []
                or not transition.get("sourceBefore")
                or transition["sourceBefore"] != transition.get("sourceAfter")):
            raise InputError("bootstrap and lifecycle images require identical complete source contracts")
        transitions.append(transition)
    return {"sourceBefore": transitions[1]["sourceBefore"],
            "sourceAfter": transitions[1]["sourceAfter"], "transitions": transitions}


def _json_api(path):
    return handoff._strict_json(handoff._github(path, limit=4 * 1024 * 1024))


def discover(run_id, *, require_release=False):
    if type(run_id) is not int or run_id <= 0:
        raise InputError("a positive producer run ID is required")
    run = _json_api(f"actions/runs/{run_id}")
    attempt, source = run.get("run_attempt"), run.get("head_sha")
    if require_release and run.get("path") != ".github/workflows/release.yml":
        raise InputError("first publication requires the release producer")
    if (not handoff._positive(attempt) or not isinstance(source, str)
            or handoff.REVISION.fullmatch(source) is None):
        raise InputError("producer did not return an exact attempt and source")
    # Bound discovery; the canonical importer rereads the individual artifact
    # and exact attempt before consuming any downloaded decision or receipt.
    expected = f"managed-admission-{run_id}-{attempt}-amd64"
    matches = []
    for page in range(1, 11):
        listing = _json_api(f"actions/runs/{run_id}/artifacts?per_page=100&page={page}")
        artifacts = listing.get("artifacts")
        if not isinstance(artifacts, list):
            raise InputError("producer artifact listing is invalid")
        if any(not isinstance(item, dict) for item in artifacts):
            raise InputError("producer artifact entries must be objects")
        matches.extend(item for item in artifacts if item.get("name") == expected)
        if len(artifacts) < 100:
            break
    else:
        raise InputError("producer artifact inventory exceeds the discovery limit")
    if len(matches) != 1:
        raise InputError("producer attempt must contain exactly one native admission bundle")
    artifact_id = matches[0].get("id")
    authorization, data = handoff.fetch(run_id, attempt, artifact_id, source, PLATFORM)
    if require_release and authorization["workflow"] != handoff.REPOSITORY + "/.github/workflows/release.yml":
        raise InputError("bootstrap authority is not the protected release producer")
    image = discover_image(data, authorization["artifactDigest"])
    handoff.verify_bundle(data, authorization["artifactDigest"], authorization, image=image)
    return {**authorization, "image": image}


def prepare(run_ids, *, root, verifier, source_root):
    """Root-only fresh storage; never reuse an ambient host admission store."""
    # sudo does not inherit actions/checkout's per-user safe.directory. Trust
    # only this explicit tree for read-only history collection. Ambient Git
    # configuration must not redirect the source evidence to another checkout.
    previous = {key: value for key, value in os.environ.items() if key.startswith("GIT_")}
    for key in previous:
        del os.environ[key]
    os.environ.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_COUNT="1",
                      GIT_CONFIG_KEY_0="safe.directory", GIT_CONFIG_VALUE_0=str(Path(source_root).resolve()))
    try:
        with contextlib.chdir(source_root):
            return _prepare(run_ids, root=root, verifier=verifier, source_root=source_root)
    finally:
        for key in list(os.environ):
            if key.startswith("GIT_"):
                del os.environ[key]
        os.environ.update(previous)


def _prepare(run_ids, *, root, verifier, source_root):
    root = Path(root)
    root.mkdir(mode=0o700)
    admissions, evidence = root / "admissions", root / "evidence"
    admissions.mkdir(mode=0o700)
    evidence.mkdir(mode=0o700)
    selected, imported = {}, {}
    for role in ("bootstrap", "predecessor", "candidate"):
        run_id = run_ids[role]
        selection = discover(run_id, require_release=role == "bootstrap")
        subprocess.run(["git", "-C", str(source_root), "merge-base", "--is-ancestor",
                        selection["sourceRevision"], "origin/main"], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
        if run_id not in imported:
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                result = handoff.main([
                    "--run-id", str(run_id), "--run-attempt", selection["runAttempt"],
                    "--artifact-id", str(selection["artifactId"]),
                    "--source-revision", selection["sourceRevision"], "--platform", PLATFORM,
                    "--image", selection["image"], "--verifier", str(verifier),
                    "--admission-root", str(admissions),
                    "--evidence-dir", str(evidence / str(run_id)),
                ])
            if result != 0:
                raise InputError("canonical authenticated admission import failed")
            imported[run_id] = handoff._strict_json(output.getvalue())
        selected[role] = imported[run_id]
    # The source collector runs relative to the checked out repository; callers
    # explicitly enter it before qualification instead of trusting shell paths.
    compatibility = compatible_sources(selected)
    manifest = {"schemaVersion": 1, "scope": "isolated-managed-application-lifecycle",
                "fullManagedProfileQualified": False, "platform": PLATFORM,
                "images": selected, "compatibility": compatibility}
    path = root / "inputs.json"
    with path.open("x") as stream:
        json.dump(manifest, stream, sort_keys=True)
        stream.write("\n")
    path.chmod(0o400)
    return manifest

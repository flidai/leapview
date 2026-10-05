#!/usr/bin/env python3
"""Authorize an exact protected Nix source revision for a main-dispatched run."""

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys


REPOSITORY = "flidai/leapview"
MAIN_REF = "refs/heads/main"
REVISION_RE = re.compile(r"[0-9a-f]{40}\Z")


class AuthorizationError(ValueError):
    """The selected source revision is not authorized by this protected run."""


def _revision(value: object, label: str) -> str:
    if not isinstance(value, str) or REVISION_RE.fullmatch(value) is None:
        raise AuthorizationError(f"{label} must be a full lowercase commit SHA")
    return value


def _checkout_revision(root: Path, label: str) -> str:
    try:
        result = subprocess.run(
            ["git", "-C", str(root), "rev-parse", "--verify", "HEAD"],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=10,
        )
    except (OSError, subprocess.SubprocessError):
        raise AuthorizationError(f"{label} checkout does not resolve to a commit") from None
    return _revision(result.stdout.strip(), f"{label} checkout revision")


def _pull_request_matches(pull_requests: object, source_revision: str) -> int:
    if not isinstance(pull_requests, list):
        raise AuthorizationError("pull-request metadata must be a JSON array")
    matches = []
    for pull_request in pull_requests:
        if not isinstance(pull_request, dict):
            raise AuthorizationError("pull-request metadata contains a malformed entry")
        base = pull_request.get("base")
        head = pull_request.get("head")
        if not isinstance(base, dict) or not isinstance(head, dict):
            raise AuthorizationError("pull-request metadata omits base or head identity")
        if (
            pull_request.get("state") == "open"
            and base.get("ref") == "main"
            and head.get("sha") == source_revision
        ):
            matches.append(pull_request)
    return len(matches)


def authorize_candidate(
    *,
    repository: str,
    event: str,
    ref: str,
    source_revision: str,
    protected_revision: str,
    source_root: Path,
    protected_root: Path,
    pull_requests: object,
) -> str:
    """Return the authority class after checking event, source and both checkouts.

    A main-dispatched run is pinned to the immutable commit carried by that
    workflow_dispatch event. It remains valid if main advances while native
    qualification is running. PR candidates must still have one exact open PR
    head directly targeting main whenever this function is called.
    """
    source_revision = _revision(source_revision, "source revision")
    protected_revision = _revision(protected_revision, "protected workflow revision")
    if repository != REPOSITORY or event != "workflow_dispatch" or ref != MAIN_REF:
        raise AuthorizationError("protected Nix qualification must be dispatched from repository main")
    if _checkout_revision(protected_root, "protected workflow") != protected_revision:
        raise AuthorizationError("protected checkout differs from the dispatched workflow revision")
    if _checkout_revision(source_root, "source") != source_revision:
        raise AuthorizationError("source checkout differs from the authorized source revision")

    if source_revision == protected_revision:
        return "dispatched-main"

    if _pull_request_matches(pull_requests, source_revision) != 1:
        raise AuthorizationError("source must be one exact open pull-request head based on main")
    return "open-pr-head"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--event", required=True)
    parser.add_argument("--ref", required=True)
    parser.add_argument("--source-revision", required=True)
    parser.add_argument("--protected-revision", required=True)
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--protected-root", type=Path, required=True)
    parser.add_argument("--pull-requests", type=Path, required=True)
    args = parser.parse_args()
    try:
        pull_requests = json.loads(args.pull_requests.read_text(encoding="utf-8"))
        authority = authorize_candidate(
            repository=args.repository,
            event=args.event,
            ref=args.ref,
            source_revision=args.source_revision,
            protected_revision=args.protected_revision,
            source_root=args.source_root,
            protected_root=args.protected_root,
            pull_requests=pull_requests,
        )
        print(json.dumps({"sourceAuthority": authority, "sourceRevision": args.source_revision}))
    except (OSError, json.JSONDecodeError, AuthorizationError) as exc:
        raise SystemExit("Protected Nix source authorization rejected: " + str(exc)) from exc


if __name__ == "__main__":
    main()

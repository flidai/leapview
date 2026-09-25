#!/usr/bin/env python3
"""Explain an immutable source transition. This is not migration admission."""
import hashlib
import re
import subprocess

MIGRATIONS = 'internal/platform/postgres/migrations'
SCHEMA_PATHS = ['internal/platform/postgres', 'internal/analytics/duckdb',
                'internal/analytics/ducklake', 'go.mod', 'go.sum']


def classify(current, target, before, after, changed_paths):
    if target < current:
        raise ValueError('Downgrade requires a separately admitted recovery operation')
    for name, digest in before.items():
        if after.get(name) != digest:
            raise ValueError('Applied migration changed or was removed: '+name)
    pending = sorted(set(after) - set(before))
    versions = []
    for name in pending:
        match = re.fullmatch(r'(\d+)_[^/]+\.sql', name)
        if not match:
            raise ValueError('Unknown migration name: '+name)
        versions.append(int(match[1]))
    if sorted(versions) != list(range(current + 1, target + 1)):
        raise ValueError('Forward migration chain is missing, ambiguous, or rewrites history')
    mode = 'database-upgrade-required' if target != current else 'image-only'
    if target == current and changed_paths:
        mode = 'review-required'
    return dict(mode=mode, currentSchema=current, candidateSchema=target,
                pendingMigrations=pending, changedCompatibilityPaths=sorted(changed_paths),
                imageOnlyEligible=mode == 'image-only', migrationExecutionAuthorized=False)


def git(*args):
    return subprocess.check_output(['git', *args])


def source_schema(revision):
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('Source revision must be a full immutable commit SHA')
    source = git('show', revision+':'+MIGRATIONS+'/goose.go').decode()
    matches = re.findall(r'^\s*CurrentRevision\s+int64\s*=\s*(\d+)\s*$', source, re.M)
    if len(matches) != 1:
        raise ValueError('Cannot resolve the source schema revision')
    files = git('ls-tree', '-r', '--name-only', revision, '--', MIGRATIONS).decode().splitlines()
    migrations = {path.rsplit('/', 1)[-1]: hashlib.sha256(git('show', revision+':'+path)).hexdigest()
                  for path in files if re.fullmatch(r'\d+_[^/]+\.sql', path.rsplit('/', 1)[-1])}
    return int(matches[0]), migrations


def inspect_transition(previous, candidate):
    current, before = source_schema(previous)
    target, after = source_schema(candidate)
    paths = git('diff', '--name-only', previous, candidate, '--', *SCHEMA_PATHS).decode().splitlines()
    result = classify(current, target, before, after, paths)
    return dict(result, predecessorRevision=previous, candidateRevision=candidate)

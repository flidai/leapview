#!/usr/bin/env python3
"""Explain an immutable source transition. This is not migration admission."""
import hashlib
import re
import subprocess

MIGRATIONS = 'internal/platform/postgres/migrations'
ENGINE_PREFIXES = ('github.com/duckdb/', 'github.com/riverqueue/')


def source_compatibility(revision):
    module = git('show', revision+':go.mod').decode()
    engines = dict(re.findall(r'^\s*(github\.com/(?:duckdb|riverqueue)/\S+)\s+(v\S+)', module, re.M))
    if not any(name.startswith('github.com/duckdb/') for name in engines) or not any(name.startswith('github.com/riverqueue/') for name in engines):
        raise ValueError('Cannot resolve immutable engine dependencies')
    # Replacements can alter the selected engine independently of its require
    # line. Reject relevant replacements until independently qualified.
    if re.search(r'(?m)^\s*(?:replace\s+)?github\.com/(?:duckdb|riverqueue)/.*=>', module):
        raise ValueError('Engine module replacements require separate qualification')
    policy = git('show', revision+':internal/app/postgresbaseline/baseline.go')
    match = re.search(rb'const rolePolicySQL = `([\s\S]*?)`', policy)
    if not match: raise ValueError('Cannot resolve immutable role policy')
    return engines, hashlib.sha256(match[1]).hexdigest()


def classify(current, target, before, after, compatibility_changes, policy_changed=False):
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
    if policy_changed: mode = 'database-upgrade-required'
    if compatibility_changes: mode = 'review-required'
    return dict(mode=mode, currentSchema=current, candidateSchema=target,
                pendingMigrations=pending, pendingMigrationDigests={name: after[name] for name in pending},
                compatibilityChanges=sorted(compatibility_changes), rolePolicyChanged=policy_changed,
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
    # Match the runtime's //go:embed *.sql boundary. Nested test fixtures are
    # historical examples, not applied SQL; their basenames can also collide.
    migrations = {path.rsplit('/', 1)[-1]: hashlib.sha256(git('show', revision+':'+path)).hexdigest()
                  for path in files if re.fullmatch(re.escape(MIGRATIONS) + r'/\d+_[^/]+\.sql', path)}
    return int(matches[0]), migrations


def inspect_transition(previous, candidate):
    current, before = source_schema(previous)
    target, after = source_schema(candidate)
    old_engines, old_policy = source_compatibility(previous)
    new_engines, new_policy = source_compatibility(candidate)
    changes = [name for name in set(old_engines) | set(new_engines) if old_engines.get(name) != new_engines.get(name)]
    result = classify(current, target, before, after, changes, old_policy != new_policy)
    result['sourceBefore'] = dict(schema=current,migrations=before,engines=old_engines,rolePolicy=old_policy)
    result['sourceAfter'] = dict(schema=target,migrations=after,engines=new_engines,rolePolicy=new_policy)
    return dict(result, predecessorRevision=previous, candidateRevision=candidate)

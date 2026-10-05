#!/usr/bin/env python3
"""Select exact architecture artifacts from one protected Nix candidate attempt."""

import argparse
import json
from pathlib import Path
import re

import nix_candidate_manifest as candidate


PREFIXES = {
    'candidate': 'nix-candidate',
    'qualified': 'nix-qualified',
    'binding': 'nix-candidate-binding',
    'site-candidate': 'nix-site-candidate',
    'site-qualified': 'nix-site-qualified',
    'site-binding': 'nix-site-candidate-binding',
    'site-published-qualification': 'nix-site-published-qualification',
    'desktop-candidate': 'nix-desktop-candidate',
    'desktop-qualified': 'nix-desktop-qualified',
}


def resolve(pages, run_id, attempt, revision, architecture, phases):
    if (any(type(value) is not int or value <= 0 for value in (run_id, attempt))
            or not isinstance(revision, str) or re.fullmatch(r'[0-9a-f]{40}', revision) is None
            or architecture not in {'amd64', 'arm64'} or not phases
            or len(set(phases)) != len(phases) or any(phase not in PREFIXES for phase in phases)
            or not isinstance(pages, list) or not pages
            or any(not isinstance(page, dict) or not isinstance(page.get('artifacts'), list) for page in pages)):
        raise ValueError('artifact selection requires one exact run, attempt, revision and supported architecture')
    artifacts = [artifact for page in pages for artifact in page['artifacts']]
    if any(not isinstance(artifact, dict) for artifact in artifacts):
        raise ValueError('artifact API returned malformed entries')
    result = {}
    for phase in phases:
        name = f'{PREFIXES[phase]}-{run_id}-{attempt}-{architecture}'
        matches = [artifact for artifact in artifacts if artifact.get('name') == name]
        if len(matches) != 1:
            raise ValueError(f'expected one exact {phase} artifact for {architecture}')
        artifact = matches[0]
        workflow = artifact.get('workflow_run')
        if (artifact.get('expired') is not False or type(artifact.get('id')) is not int or artifact['id'] <= 0
                or not isinstance(artifact.get('digest'), str)
                or re.fullmatch(r'sha256:[0-9a-f]{64}', artifact['digest']) is None
                or not isinstance(workflow, dict) or type(workflow.get('id')) is not int
                or workflow['id'] != run_id or workflow.get('head_branch') != 'main'
                or workflow.get('head_sha') != revision):
            raise ValueError(f'{phase} artifact is expired or belongs to another protected producer')
        result[phase] = artifact['id']
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--metadata', type=Path, required=True)
    parser.add_argument('--run-id', type=int, required=True)
    parser.add_argument('--attempt', type=int, required=True)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--architecture', choices=('amd64', 'arm64'), required=True)
    parser.add_argument('--phase', choices=tuple(PREFIXES), action='append', required=True)
    parser.add_argument('--github-output', type=Path, required=True)
    args = parser.parse_args()
    try:
        pages = candidate.read_json_file(args.metadata)
        selected = resolve(pages, args.run_id, args.attempt, args.revision, args.architecture, args.phase)
        with args.github_output.open('a') as output:
            for phase, artifact_id in selected.items():
                output.write(f'{phase}_id={artifact_id}\n')
        print(json.dumps(selected, sort_keys=True))
    except (OSError, ValueError, TypeError, KeyError) as exc:
        raise SystemExit('Nix artifact selection rejected: ' + str(exc)) from exc


if __name__ == '__main__':
    main()

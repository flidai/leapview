#!/usr/bin/env python3
"""Prepare an OCI archive from successful Actions admission evidence.

Use the retained admission artifact and OCI layout from the retirement archive.
This tool verifies byte identities; it does not perform fresh security admission.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--admission', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--archive-layout', type=Path, help='retained OCI layout; prepare without registry access')
    args = p.parse_args()
    evidence = json.loads(args.admission.read_text())
    ref = evidence['image']
    if not re.fullmatch(r'ghcr\.io/flidai/leapview-site-kamal-trial@sha256:[a-f0-9]{64}', ref):
        raise SystemExit('expected an immutable experimental image')
    repository, digest = ref.split('@')
    if evidence['digest'] != digest or evidence['registryDigest'] != digest:
        raise SystemExit('admission identity mismatch')

    source = 'docker://' + ref
    if args.archive_layout:
        layout = args.archive_layout.resolve()
        if json.loads((layout / 'oci-layout').read_text()).get('imageLayoutVersion') != '1.0.0':
            raise SystemExit('unsupported archived OCI layout')
        descriptors = json.loads((layout / 'index.json').read_text())['manifests']
        selected = [m for m in descriptors if m.get('digest') == digest]
        if len(selected) != 1:
            raise SystemExit('exactly one archived image matching admission required')
        tag = selected[0].get('annotations', {}).get('org.opencontainers.image.ref.name', '')
        if (not re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', tag)
                or sum(m.get('annotations', {}).get('org.opencontainers.image.ref.name') == tag
                       for m in descriptors) != 1):
            raise SystemExit('missing or ambiguous archived image tag')
        source = 'oci:' + str(layout) + ':' + tag

    def manifest(selected):
        if not re.fullmatch(r'sha256:[a-f0-9]{64}', selected):
            raise SystemExit('invalid manifest digest')
        raw = ((layout / 'blobs' / 'sha256' / selected.split(':')[1]).read_bytes()
               if args.archive_layout else subprocess.check_output(
                   ['skopeo', 'inspect', '--raw', 'docker://' + repository + '@' + selected]))
        if 'sha256:' + hashlib.sha256(raw).hexdigest() != selected:
            raise SystemExit('manifest digest mismatch')
        return json.loads(raw)

    index = manifest(digest)
    matches = [m for m in index['manifests'] if m.get('platform', {}).get('os') == 'linux'
               and m.get('platform', {}).get('architecture') == 'amd64']
    if len(matches) != 1:
        raise SystemExit('exactly one admitted amd64 platform required')
    platform = matches[0]['digest']
    config = manifest(platform)['config']['digest']
    args.output.mkdir(parents=True, exist_ok=True)
    archive = (args.output / 'site.oci.tar').resolve()
    if archive.exists():
        raise SystemExit('refusing to overwrite an existing OCI archive')
    subprocess.run(['skopeo', 'copy', '--all', '--preserve-digests', source,
                    'oci-archive:' + str(archive) + ':site-qualified'], check=True)
    with archive.open('rb') as f:
        archive_sha = hashlib.file_digest(f, 'sha256').hexdigest()
    record = {'admission': evidence, 'platform_digest': platform, 'config_digest': config,
              'archive_sha256': archive_sha}
    (args.output / 'site-record.json').write_text(json.dumps(record, indent=2) + '\n')
    print('Prepared exact admitted index, amd64 manifest and config:', ref)


if __name__ == '__main__':
    main()

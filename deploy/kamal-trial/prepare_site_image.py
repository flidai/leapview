#!/usr/bin/env python3
"""Prepare an OCI archive from successful Actions admission evidence.

Download the admission artifact from the successful trial run first. This tool
verifies registry byte identities; it does not perform fresh security admission.
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
    args = p.parse_args()
    evidence = json.loads(args.admission.read_text())
    ref = evidence['image']
    if not re.fullmatch(r'ghcr\.io/flidai/leapview-site-kamal-trial@sha256:[a-f0-9]{64}', ref):
        raise SystemExit('expected an immutable experimental image')
    repository, digest = ref.split('@')
    if evidence['digest'] != digest or evidence['registryDigest'] != digest:
        raise SystemExit('admission identity mismatch')

    def manifest(selected):
        raw = subprocess.check_output(['skopeo', 'inspect', '--raw', 'docker://' + repository + '@' + selected])
        if 'sha256:' + hashlib.sha256(raw).hexdigest() != selected:
            raise SystemExit('registry manifest digest mismatch')
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
    subprocess.run(['skopeo', 'copy', '--all', '--preserve-digests', 'docker://' + ref,
                    'oci-archive:' + str(archive) + ':site-qualified'], check=True)
    with archive.open('rb') as f:
        archive_sha = hashlib.file_digest(f, 'sha256').hexdigest()
    record = {'admission': evidence, 'platform_digest': platform, 'config_digest': config,
              'archive_sha256': archive_sha}
    (args.output / 'site-record.json').write_text(json.dumps(record, indent=2) + '\n')
    print('Prepared exact admitted index, amd64 manifest and config:', ref)


if __name__ == '__main__':
    main()

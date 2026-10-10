#!/usr/bin/env python3
"""Apply reviewed upstream fixes to exact vendored quick-xml source bytes."""

import argparse
import hashlib
import json
from pathlib import Path
import subprocess


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def apply(vendor, patch_command, policy_name="quick-xml-backport-lock.json"):
    policy_root = Path(__file__).resolve().parent
    if policy_name not in ('quick-xml-backport-lock.json', 'delta-quick-xml-backport-lock.json', 'vortex-quick-xml-backport-lock.json'):
        raise ValueError('unexpected backport policy name')
    policy = json.loads((policy_root / policy_name).read_text())
    versions = {'quick-xml-backport-lock.json': ['0.37.5', '0.38.4'],
                'delta-quick-xml-backport-lock.json': ['0.39.2'],
                'vortex-quick-xml-backport-lock.json': ['0.39.4']}[policy_name]
    if policy.get('version') != 1 or [p['version'] for p in policy['backports']] != versions:
        raise ValueError('unexpected backport policy')
    for upstream in policy['upstream']:
        path = policy_root / 'quick-xml' / upstream['path']
        if sha256(path) != upstream['sha256']:
            raise ValueError('upstream patch receipt differs')
    for backport in policy['backports']:
        source = vendor / ('quick-xml-' + backport['version'])
        if source.is_symlink() or not source.is_dir():
            raise ValueError('vendored source must be a regular directory')
        patch = policy_root / 'quick-xml' / backport['path']
        if sha256(patch) != backport['sha256']:
            raise ValueError('backport patch differs')
        if [f['path'] for f in backport['files']] != ['src/events/attributes.rs', 'src/name.rs', 'src/reader/ns_reader.rs']:
            raise ValueError('unexpected backport source paths')
        checksum = source / '.cargo-checksum.json'
        checksums = json.loads(checksum.read_text())
        if checksums.get('package') != backport['crateSHA256']:
            raise ValueError('original crate archive checksum differs')
        for proof in backport['files']:
            path = source / proof['path']
            if path.is_symlink() or not path.is_file() or sha256(path) != proof['originalSHA256']:
                raise ValueError('original vendored source differs: ' + proof['path'])
            previous = checksums['files'].get(proof['path'])
            if previous is not None and previous != proof['originalSHA256']:
                raise ValueError('original Cargo source checksum differs')
            path.write_bytes(path.read_bytes().replace(b'\r\n', b'\n'))
        subprocess.run([patch_command, '--batch', '--fuzz=0', '-p1', '-i', str(patch)], cwd=source, check=True)
        for proof in backport['files']:
            path = source / proof['path']
            if sha256(path) != proof['patchedSHA256']:
                raise ValueError('patched vendored source differs: ' + proof['path'])
            checksums['files'][proof['path']] = proof['patchedSHA256']
        # The package checksum continues to identify the original fetched crate;
        # modified file hashes separately bind this explicitly patched source.
        checksum.write_text(json.dumps(checksums, sort_keys=True) + '\n')
        print('Verified patched quick-xml', backport['version'], backport['sha256'])


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('vendor', type=Path)
    parser.add_argument('--patch-command', required=True)
    parser.add_argument('--policy', default='quick-xml-backport-lock.json')
    args = parser.parse_args()
    apply(args.vendor, args.patch_command, args.policy)

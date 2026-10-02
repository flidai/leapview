#!/usr/bin/env python3
"""Read and bind immutable registry content to freshly verified Nix candidates."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

import nix_candidate_manifest as candidate
import nix_oci_content as oci

REPOSITORIES = {'application-image': 'ghcr.io/flidai/leapview',
                'site-image': 'ghcr.io/flidai/leapview-site'}


def image_digest(image, kind):
    repository = REPOSITORIES.get(kind)
    prefix = (repository or '') + '@'
    if not repository or not isinstance(image, str) or not image.startswith(prefix):
        raise ValueError('image must use the output repository and an immutable digest')
    digest = image[len(prefix):]
    if not candidate.SHA256.fullmatch(digest):
        raise ValueError('image must use the output repository and an immutable digest')
    return digest


def bind_registry(image, kind, records, platforms):
    """Internal adapter: callers must recompute records with candidate.verify."""
    digest = image_digest(image, kind)
    if any(record['artifact']['kind'] != kind for record in records):
        raise ValueError('candidate output kind differs from registry repository')
    with tempfile.TemporaryDirectory(prefix='leapview-nix-registry-') as temporary:
        layout = Path(temporary) / 'layout'
        try:
            # TLS verification stays enabled. Authentication is supplied through
            # Skopeo's credential file, never command arguments or diagnostics.
            subprocess.run(['skopeo', 'copy', '--all', '--preserve-digests', '--src-tls-verify=true',
                            'docker://' + image, 'oci:' + str(layout) + ':candidate'],
                           check=True, timeout=300, stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL)
        except (OSError, subprocess.SubprocessError):
            raise ValueError('registry content could not be fetched') from None
        binding = oci.bind(layout, digest, records, platforms)
    result = {'schemaVersion': 1, 'image': image, 'contentBinding': binding,
              'releaseAdmission': False}
    result['registryBindingDigest'] = candidate.digest_bytes(
        b'leapview/nix-registry-content/v1\n' + candidate.canonical_bytes(result))
    if len(json.dumps(result, indent=2).encode()) + 1 > candidate.MAX_JSON_BYTES:
        raise ValueError('registry binding exceeds JSON byte limit')
    return result


def verify(record, expected):
    if candidate.canonical_bytes(record) != candidate.canonical_bytes(expected):
        raise ValueError('registry binding differs from freshly verified registry and candidate content')


def verified_records(paths, kind, source):
    return [candidate.verify(candidate.read_json_file(Path(manifest)), Path(archive), source,
                             kind=kind, runtime_dir=Path(runtime))
            for archive, manifest, runtime in paths]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--kind', choices=sorted(REPOSITORIES), required=True)
    parser.add_argument('--platform', action='append', required=True)
    parser.add_argument('--candidate', nargs=3, action='append', required=True,
                        metavar=('ARCHIVE', 'MANIFEST', 'RUNTIME_EVIDENCE'))
    output = parser.add_mutually_exclusive_group(required=True)
    output.add_argument('--output', type=Path)
    output.add_argument('--verify', type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        image_digest(args.image, args.kind)
        source = candidate.checkout_source(candidate.ROOT)
        records = verified_records(args.candidate, args.kind, source)
        result = bind_registry(args.image, args.kind, records, args.platform)
        # Recheck source, archive and time-limited runtime evidence after the
        # registry operation. An expired or changed input cannot mint a receipt.
        current = verified_records(args.candidate, args.kind, candidate.checkout_source(candidate.ROOT))
        if candidate.canonical_bytes(current) != candidate.canonical_bytes(records):
            raise ValueError('candidate changed during registry verification')
        if args.verify:
            verify(candidate.read_json_file(args.verify), result)
        else:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            with args.output.open('x') as stream:
                stream.write(json.dumps(result, indent=2) + '\n')
        print(json.dumps({'image': args.image, 'registryBindingDigest': result['registryBindingDigest'],
                          'releaseAdmission': False}))
    except (ValueError, KeyError, TypeError, OSError, EOFError, tarfile.TarError, subprocess.SubprocessError) as error:
        raise SystemExit('registry content binding rejected: ' + str(error)) from error


if __name__ == '__main__':
    main()

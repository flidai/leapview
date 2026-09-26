#!/usr/bin/env python3
"""Trial-only pre-boot guard. Local Docker endpoint is provided by the harness.

The record is test-generated evidence, NOT an OCI admission substitute. Production
integration must consume records derived from a real admitted platform manifest.
"""
import argparse
import json
import re
import subprocess
from pathlib import Path


def validate(record, image, *, rollback=False):
    if record.get('schema') != 1 or record.get('service') != 'leapview-site-trial':
        raise ValueError('invalid trial record schema/service')
    if not re.fullmatch(r'[a-z0-9][a-z0-9-]{0,63}', record.get('version', '')):
        raise ValueError('invalid version')
    if record.get('status') not in ('candidate', 'verified'):
        raise ValueError('invalid record status')
    if rollback and record['status'] != 'verified':
        raise ValueError('rollback requires a verified record')
    if not re.fullmatch(r'127\.0\.0\.1:5000/site@sha256:[a-f0-9]{64}', record.get('reference', '')):
        raise ValueError('invalid immutable reference')
    if not re.fullmatch(r'sha256:[a-f0-9]{64}', record.get('image_id', '')):
        raise ValueError('invalid image identity')
    if image.get('Id') != record['image_id'] or record['reference'] not in image.get('RepoDigests', []):
        raise ValueError('local image does not match recorded immutable identity')
    if image.get('Os') != 'linux' or image.get('Architecture') != 'amd64':
        raise ValueError('wrong target platform')
    if image.get('Config', {}).get('Labels', {}).get('service') != record['service']:
        raise ValueError('wrong service label')
    return record


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--record', type=Path, required=True)
    parser.add_argument('--version', required=True)
    parser.add_argument('--rollback', action='store_true')
    args = parser.parse_args()
    if not re.fullmatch(r'[a-z0-9][a-z0-9-]{0,63}', args.version):
        raise SystemExit('invalid selected version')
    record = json.loads(args.record.read_text())
    if record.get('version') != args.version:
        raise SystemExit('record version differs from selected version')
    output = subprocess.check_output(['docker', 'image', 'inspect', f'127.0.0.1:5000/site:{args.version}'])
    validate(record, json.loads(output)[0], rollback=args.rollback)
    print('Trial local image identity verified')


if __name__ == '__main__':
    main()

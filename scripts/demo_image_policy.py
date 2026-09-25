#!/usr/bin/env python3
"""Admit a digest using the receipt from the exact successful Main artifacts run."""
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import zipfile

REPO = 'flidai/leapview'
IMAGE = r'ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}'

def admit(run, receipt, image):
    if not re.fullmatch(IMAGE, image):
        raise ValueError('Use an immutable LeapView image digest')
    if (run.get('repository', {}).get('full_name') != REPO or
        run.get('path') != '.github/workflows/artifacts.yml' or
        run.get('event') != 'push' or run.get('head_branch') != 'main' or
        run.get('conclusion') != 'success'):
        raise ValueError('Expected a successful main push qualification run in flidai/leapview')
    revision = run['head_sha']
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('Invalid source revision')
    expected = dict(image=image, revision=revision, runId=str(run['id']),
                    runAttempt=str(run['run_attempt']), qualified=True)
    if any(receipt.get(k) != v for k, v in expected.items()):
        raise ValueError('Qualification receipt does not match this digest, revision and run attempt')
    return revision

def api(path):
    return subprocess.check_output(['gh', 'api', f'repos/{REPO}/{path}'])

def main():
    image = os.environ['DEMO_IMAGE']
    run_id = os.environ['QUALIFICATION_RUN']
    if not run_id.isdecimal(): raise ValueError('Qualification run must be a numeric run ID')
    run = json.loads(api(f'actions/runs/{run_id}'))
    name = 'production-image-qualification-' + str(run['run_attempt'])
    artifacts = json.loads(api(f'actions/runs/{run_id}/artifacts?per_page=100'))['artifacts']
    matches = [a for a in artifacts if a['name'] == name and not a['expired']]
    if len(matches) != 1:
        raise ValueError('No unique qualification receipt. Build/qualify with the receipt-enabled workflow first.')
    with zipfile.ZipFile(io.BytesIO(api(f"actions/artifacts/{matches[0]['id']}/zip"))) as archive:
        receipt = json.loads(archive.read('qualification.json'))
    revision = admit(run, receipt, image)
    subprocess.run(['git', 'merge-base', '--is-ancestor', revision, 'origin/main'], check=True)
    receipt_path = Path(os.environ['RUNNER_TEMP'])/'demo-qualification.json'
    receipt_path.write_text(json.dumps(receipt))
    receipt_path.chmod(0o600)
    with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
        output.write(f'revision={revision}\nimage={image}\n')
    print(f'Qualified immutable image admitted at revision {revision}')

if __name__ == '__main__': main()

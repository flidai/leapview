#!/usr/bin/env python3
"""Create the first-install Compose handoff after exact qualification and CFO validation.

The command reads the qualification artifact from the requested successful Main
artifacts run, authenticates the public build, validates all shared-viewer CFO
pages, then invokes the root-only live-state writer over the pinned demo-02 SSH
connection. It never transfers application credentials to the host writer.
"""
import argparse
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import zipfile


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import demo_compose_deploy as deploy
import demo_image_policy as image_policy
from demo_release_contract import read_contract
from demo_upgrade_plan import source_schema


WRITER = Path(__file__).with_name('compose_installation_writer.py')
PUBLIC_ORIGIN = 'https://demo.leapview.dev'


def qualification_identity(run_id, image):
    if not isinstance(run_id, str) or not run_id.isdecimal():
        raise ValueError('Qualification run must be a numeric run ID')
    run = json.loads(image_policy.api(f'actions/runs/{run_id}'))
    artifact_name = 'production-image-qualification-' + str(run.get('run_attempt', ''))
    artifacts = json.loads(image_policy.api(
        f'actions/runs/{run_id}/artifacts?per_page=100')).get('artifacts', [])
    matches = [item for item in artifacts if item.get('name') == artifact_name and not item.get('expired')]
    if len(matches) != 1:
        raise ValueError('No unique fresh-install qualification receipt exists for this run attempt')
    artifact = image_policy.api(f"actions/artifacts/{matches[0]['id']}/zip")
    with zipfile.ZipFile(io.BytesIO(artifact)) as archive:
        names = archive.namelist()
        if names.count('qualification.json') != 1 or names.count('transition.json') != 1:
            raise ValueError('Qualification artifact is incomplete or ambiguous')
        receipt = json.loads(archive.read('qualification.json'))
        transition = json.loads(archive.read('transition.json'))
    revision = image_policy.admit(run, receipt, image)
    schema, _ = source_schema(revision)
    contract = read_contract(revision)
    image_policy.admit_transition(run, transition, image, revision, schema, contract)
    return {
        'image': image,
        'revision': revision,
        'schema': schema,
        'permissionProfile': contract['permissionProfile'],
        'qualificationRunId': str(run['id']),
        'qualificationAttempt': str(run['run_attempt']),
    }


def _invoke_writer(identity, ssh):
    command = [
        'python3', '-',
        '--image', identity['image'],
        '--revision', identity['revision'],
        '--schema', str(identity['schema']),
        '--permission-profile', identity['permissionProfile'],
        '--qualification-run-id', identity['qualificationRunId'],
        '--qualification-attempt', identity['qualificationAttempt'],
    ]
    subprocess.run([*ssh, shlex.join(command)], input=WRITER.read_bytes(), check=True)


def _prepare_live_validation(identity):
    if os.environ.get('DEMO_HOST') != deploy.HOST:
        raise ValueError('DEMO_HOST must match the pinned demo-02 address')
    configured_target = os.environ.get('DEMO_TARGET')
    if configured_target not in (None, PUBLIC_ORIGIN):
        raise ValueError('First-install evidence validation must use the public demo origin')
    if os.environ.get('DEMO_BROWSER_PROXY') or os.environ.get('DEMO_CLONE_ONLY') == '1':
        raise ValueError('Clone-only browser validation cannot create live installation evidence')
    os.environ['DEMO_TARGET'] = PUBLIC_ORIGIN
    os.environ['SOURCE_REVISION'] = identity['revision']
    os.environ['DEMO_PERMISSION_PROFILE'] = identity['permissionProfile']


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', default=os.environ.get('DEMO_IMAGE'), required=False)
    parser.add_argument('--qualification-run', default=os.environ.get('QUALIFICATION_RUN'), required=False)
    args = parser.parse_args(argv)
    if not args.image or not args.qualification_run:
        parser.error('provide --image and --qualification-run, or set DEMO_IMAGE and QUALIFICATION_RUN')
    identity = qualification_identity(args.qualification_run, args.image)
    # The existing validator authenticates the public build through the
    # publisher principal and checks all CFO pages with the pinned viewer login.
    _prepare_live_validation(identity)
    # Authenticate the public build before using host credentials. The
    # remaining shared-viewer browser phase needs SSH to read its secret.
    deploy.verify_public_revision(identity['revision'], identity['permissionProfile'])
    # Reuse one pinned SSH context for validation and evidence writing. The
    # verified_demo_ssh context consumes the private key from the environment
    # once and keeps its temporary key/known-hosts files alive through both.
    with deploy.verified_demo_ssh() as ssh:
        deploy.verify_publication(ssh=ssh, revision_verified=True)
        # Validation must finish before the host receives a writer command.
        _invoke_writer(identity, ssh)
    print('First-install Compose evidence verified and written.')


if __name__ == '__main__':
    main()

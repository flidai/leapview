#!/usr/bin/env python3
"""Use GitHub deployments as the runtime pin; no broad variable-write PAT needed."""
import json
import os
import re
import subprocess
import sys

BASE = 'repos/flidai/leapview'
TASK = 'demo-compose-runtime'
ENVIRONMENT = 'leapview-demo-runtime'

def api(path, body=None):
    args = ['gh', 'api', BASE + '/' + path]
    if body is not None: args += ['--method', 'POST', '--input', '-']
    return json.loads(subprocess.check_output(args, input=None if body is None else json.dumps(body).encode()))

def resolve():
    records = api(f'deployments?environment={ENVIRONMENT}&task={TASK}&per_page=1')
    if records:
        record = records[0]
        statuses = api(f"deployments/{record['id']}/statuses?per_page=1")
        if not statuses or statuses[0]['state'] != 'success':
            raise RuntimeError('Latest runtime deployment is not successful; reconcile before publication')
        revision = record['sha']
    else:
        revision = os.environ.get('DEMO_RUNTIME_REVISION', '')
    if not re.fullmatch(r'[0-9a-f]{40}', revision): raise ValueError('No verified runtime pin')
    with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
        output.write(f'revision={revision}\npublish=true\n')

def main():
    action = sys.argv[1]
    if action == 'resolve': return resolve()
    if action == 'start':
        record = api('deployments', dict(ref=os.environ['SOURCE_REVISION'], task=TASK,
            environment=ENVIRONMENT, auto_merge=False, required_contexts=[],
            production_environment=True, payload={'image': os.environ['DEMO_IMAGE']}))
        deployment_id = str(record['id'])
        with open(os.environ['GITHUB_OUTPUT'], 'a') as output: output.write(f'id={deployment_id}\n')
        state = 'in_progress'
    else:
        deployment_id = os.environ['DEPLOYMENT_ID']
        state = action
        if state not in ('success', 'failure', 'error'): raise ValueError('Unknown deployment status')
    api(f'deployments/{deployment_id}/statuses', dict(state=state,
        environment_url='https://demo.leapview.dev', auto_inactive=False,
        log_url=f"https://github.com/flidai/leapview/actions/runs/{os.environ['GITHUB_RUN_ID']}"))

if __name__ == '__main__': main()

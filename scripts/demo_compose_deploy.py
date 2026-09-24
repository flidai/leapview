#!/usr/bin/env python3
"""Runner-side pinned SSH transport and browser approval of the host transaction."""
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
HOST = '89.58.13.145'
FINGERPRINT = 'SHA256:k3AZrVrLBF5tyItYzRUkcsVJEFVVOqxsHhBvQypTVWE'
# Conservative image-only boundary. Schema/engine dependency changes require
# the canonical host upgrade/recovery path, not an image-only rollback.
SCHEMA_PATHS = ['internal/platform/postgres', 'internal/analytics/duckdb',
                'internal/analytics/ducklake', 'go.mod', 'go.sum']

def verify_public_revision(expected):
    # Capabilities use API workload auth, not a browser session cookie.
    form = urllib.parse.urlencode({
        'grant_type':'client_credentials',
        'client_id':os.environ['DEMO_PUBLISHER_CLIENT_ID'],
        'client_secret':os.environ['DEMO_PUBLISHER_CLIENT_SECRET'],
        'project_id':os.environ['DEMO_PROJECT_ID'],
        'scope':'RESOURCE_READ', 'lifetime_seconds':'300',
    }).encode()
    request = urllib.request.Request('https://demo.leapview.dev/oauth/token',data=form,
        headers={'Content-Type':'application/x-www-form-urlencoded'})
    with urllib.request.urlopen(request,timeout=15) as response:
        token = json.load(response)['access_token']
    request = urllib.request.Request('https://demo.leapview.dev/api/v1/capabilities',
        headers={'Authorization':'Bearer '+token})
    with urllib.request.urlopen(request,timeout=15) as response:
        capabilities = json.load(response)
    if capabilities.get('buildRevision') != expected or capabilities.get('buildDirty') is not False:
        raise RuntimeError('Public runtime revision differs from the deployment target')


def main():
    os.umask(0o077)
    image, revision = os.environ['DEMO_IMAGE'], os.environ['SOURCE_REVISION']
    if not re.fullmatch(r'ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}',image) or not re.fullmatch(r'[0-9a-f]{40}',revision):
        raise ValueError('Invalid admitted identity')
    if os.environ.get('DEMO_HOST') != HOST: raise ValueError('DEMO_HOST must point to demo-02')
    with tempfile.TemporaryDirectory() as directory:
        key, known = Path(directory)/'key', Path(directory)/'known_hosts'
        key.write_text(os.environ.pop('DEMO_SSH_PRIVATE_KEY')+'\n'); key.chmod(0o600)
        scanned = subprocess.check_output(['ssh-keyscan','-T','10','-t','ed25519',HOST],stderr=subprocess.DEVNULL)
        known.write_bytes(scanned)
        fingerprint = subprocess.check_output(['ssh-keygen','-lf',str(known)],text=True).split()[1]
        if fingerprint != FINGERPRINT: raise RuntimeError('Demo-02 SSH host identity mismatch')
        options = ['-i',str(key),'-o','BatchMode=yes','-o','ConnectTimeout=10',
                   '-o','ServerAliveInterval=15','-o','ServerAliveCountMax=3',
                   '-o','StrictHostKeyChecking=yes','-o','UserKnownHostsFile='+str(known)]
        ssh = ['ssh',*options,'root@'+HOST]
        remote = '/run/leapview-demo-deploy-'+secrets.token_hex(12)+'.py'
        # umask protects the temporary script; no secrets are uploaded in it.
        subprocess.run([*ssh,f'umask 077; cat > {remote}'],input=(ROOT/'scripts/demo_compose_runtime.py').read_bytes(),check=True)
        try:
            previous = json.loads(subprocess.check_output([*ssh,'python3',remote,'inspect']))
            old_revision = previous['revision']
            if not re.fullmatch(r'[0-9a-f]{40}',old_revision): raise RuntimeError('Invalid predecessor identity')
            differences = subprocess.check_output(['git','diff','--name-only',old_revision,revision,'--',*SCHEMA_PATHS],text=True)
            if differences:
                raise RuntimeError('Schema/engine paths changed; use reviewed host upgrade/recovery before image deployment:\n'+differences)
            viewer = json.loads(subprocess.check_output([*ssh,'python3',remote,'viewer']))
            browser_env = {k:v for k,v in os.environ.items() if k in ('PATH','HOME','PLAYWRIGHT_BROWSERS_PATH','TMPDIR','LANG','LC_ALL')}
            for key in ['DEMO_VIEWER_EMAIL','DEMO_VIEWER_PASSWORD']:
                value = viewer[key]
                if not isinstance(value,str) or not value or '\n' in value or '\r' in value:
                    raise ValueError('Invalid shared-viewer credential')
                if os.environ.get('GITHUB_ACTIONS') == 'true':
                    print('::add-mask::'+value.replace('%','%25'),flush=True)
                browser_env[key] = value
            # A fresh login before touching the host detects stale viewer credentials.
            verify_public_revision(old_revision)
            subprocess.run(['node','scripts/demo_validate_browser.mjs'],cwd=ROOT,check=True,timeout=240,env=browser_env)
            process = subprocess.Popen([*ssh,'python3',remote,image,revision,old_revision],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
            approved = False
            committed = False
            try:
                for line in process.stdout:
                    print(line.strip(),flush=True)
                    if line.strip() == 'AWAITING_BROWSER_VALIDATION':
                        verify_public_revision(revision)
                        subprocess.run(['node','scripts/demo_validate_browser.mjs'],cwd=ROOT,check=True,timeout=240,env=browser_env)
                        process.stdin.write('commit\n'); process.stdin.flush()
                        approved = True
                    if line.strip() == 'DEPLOYMENT_COMMITTED': committed = True
                if process.wait() != 0 or not approved or not committed:
                    raise RuntimeError('Host deployment did not commit; inspect the run and host recovery receipt')
            finally:
                process.stdin.close() # EOF restores predecessor if no approval arrived.
                try: process.wait(timeout=240)
                except subprocess.TimeoutExpired:
                    process.kill()
                    raise RuntimeError('SSH recovery exceeded timeout; inspect host before retrying')
        finally:
            subprocess.run([*ssh,'rm','-f',remote],check=True)

if __name__ == '__main__': main()

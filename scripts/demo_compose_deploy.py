#!/usr/bin/env python3
"""Runner-side pinned SSH transport and browser approval of the host transaction."""
import argparse
import hashlib
from contextlib import ExitStack, contextmanager
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import urllib.parse
import urllib.request

from demo_client_contract import CLONE_TARGET, scope_for, validate_client_environment
from demo_upgrade_plan import inspect_transition, source_schema
from demo_release_contract import read_contract
from demo_publication import publication_source, publish
import demo_upgrade_transport as upgrade

ROOT = Path(__file__).resolve().parents[1]
HOST = '89.58.13.145'
FINGERPRINT = 'SHA256:k3AZrVrLBF5tyItYzRUkcsVJEFVVOqxsHhBvQypTVWE'

class RejectRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError('Demo client rejected an HTTP redirect')


class PinnedProxyHandler(urllib.request.ProxyHandler):
    """Proxy handler that never consults urllib's ambient proxy_bypass()."""
    def __init__(self, proxy):
        super().__init__({'https': proxy})

    def proxy_open(self, request, proxy, proxy_type):
        origin = urllib.parse.urlsplit(request.full_url)
        parsed_proxy = urllib.parse.urlsplit(proxy)
        if (origin.scheme != 'https' or parsed_proxy.scheme != 'http' or
                parsed_proxy.hostname != '127.0.0.1' or parsed_proxy.port is None or
                parsed_proxy.username or parsed_proxy.password or parsed_proxy.path not in ('', '/')):
            raise ValueError('Demo Python client requires its pinned loopback HTTP proxy')
        # HTTPS Request.set_proxy preserves the origin host as the CONNECT
        # target. Returning None lets HTTPSHandler create the TLS tunnel.
        request.set_proxy(f'127.0.0.1:{parsed_proxy.port}', 'http')
        return None

    https_open = proxy_open


def demo_urlopen(request, timeout, environ=None):
    env = os.environ if environ is None else environ
    if env.get('DEMO_CLONE_ONLY') != '1':
        opener = urllib.request.build_opener(RejectRedirects())
        return opener.open(request, timeout=timeout)
    validate_client_environment(env)
    parsed = urllib.parse.urlsplit(request.full_url)
    target = urllib.parse.urlsplit(CLONE_TARGET)
    if parsed.scheme != 'https' or parsed.hostname != target.hostname or parsed.port is not None or parsed.username or parsed.password:
        raise ValueError('Clone-only Python requests must use the canonical HTTPS demo origin')
    proxy = env['DEMO_CLONE_PROXY']
    # The custom handler forces routing even if actualprocess NO_PROXY says
    # '*'; a failed local proxy request has no direct retry path.
    opener = urllib.request.build_opener(
        PinnedProxyHandler(proxy), RejectRedirects())
    return opener.open(request, timeout=timeout)


def verify_public_revision(expected, permission_profile=None, environ=None):
    # Capabilities use API workload auth, not a browser session cookie.
    env = os.environ if environ is None else environ
    profile = permission_profile or env.get('DEMO_PERMISSION_PROFILE')
    scope = scope_for(profile, 'inspection')
    base = env.get('DEMO_TARGET', CLONE_TARGET).rstrip('/')
    form = urllib.parse.urlencode({
        'grant_type':'client_credentials',
        'client_id':env['DEMO_PUBLISHER_CLIENT_ID'],
        'client_secret':env['DEMO_PUBLISHER_CLIENT_SECRET'],
        'project_id':env['DEMO_PROJECT_ID'],
        'scope':scope, 'lifetime_seconds':'300',
    }).encode()
    request = urllib.request.Request(base+'/oauth/token',data=form,
        headers={'Content-Type':'application/x-www-form-urlencoded'})
    with demo_urlopen(request,timeout=15,environ=env) as response:
        token = json.load(response)['access_token']
    request = urllib.request.Request(base+'/api/v1/capabilities',
        headers={'Authorization':'Bearer '+token})
    with demo_urlopen(request,timeout=15,environ=env) as response:
        capabilities = json.load(response)
    if capabilities.get('buildRevision') != expected or capabilities.get('buildDirty') is not False:
        raise RuntimeError('Public runtime revision differs from the deployment target')


def runtime_binding(action, previous, image, revision, operation_digest):
    binding = dict(version=1, operation=action, operationId=operation_digest,
                   candidate=dict(image=image, revision=revision),
                   predecessor=dict(image=previous['image'], revision=previous['revision']),
                   workflowRunId=os.environ['GITHUB_RUN_ID'], workflowAttempt=os.environ['GITHUB_RUN_ATTEMPT'])
    qualification_run = os.environ.get('QUALIFICATION_RUN', '')
    if qualification_run:
        runner_temp = os.environ.get('RUNNER_TEMP')
        if not runner_temp:
            raise ValueError('RUNNER_TEMP is required to bind the admitted qualification')
        try:
            qualification = json.loads((Path(runner_temp)/'demo-qualification.json').read_text())
        except (OSError, json.JSONDecodeError) as exc:
            raise ValueError('Admitted qualification receipt is unavailable') from exc
        if (not isinstance(qualification, dict)
                or qualification.get('runId') != qualification_run
                or qualification.get('image') != image
                or qualification.get('revision') != revision
                or qualification.get('qualified') is not True
                or not isinstance(qualification.get('runAttempt'), str)
                or not qualification['runAttempt'].isdecimal()):
            raise ValueError('Runtime candidate differs from its admitted qualification receipt')
        binding.update(
            qualificationRunId=qualification_run,
            qualificationAttempt=qualification['runAttempt'],
            permissionProfile=read_contract(revision)['permissionProfile'],
        )
    return binding


def write_binding(binding):
    path = Path(os.environ['RUNNER_TEMP'])/'demo-runtime-binding.json'
    if path.exists() and json.loads(path.read_text()) != binding:
        raise RuntimeError('Installation changed after deployment preflight; start a new reviewed run')
    path.write_text(json.dumps(binding))
    path.chmod(0o600)


@contextmanager
def verified_demo_ssh():
    if os.environ.get('DEMO_HOST') != HOST:
        raise ValueError('DEMO_HOST must point to demo-02')
    with tempfile.TemporaryDirectory() as directory:
        key, known = Path(directory)/'key', Path(directory)/'known_hosts'
        key.write_text(os.environ.pop('DEMO_SSH_PRIVATE_KEY')+'\n')
        key.chmod(0o600)
        known.write_bytes(subprocess.check_output(
            ['ssh-keyscan', '-T', '10', '-t', 'ed25519', HOST], stderr=subprocess.DEVNULL))
        fingerprint = subprocess.check_output(['ssh-keygen', '-lf', str(known)], text=True).split()[1]
        if fingerprint != FINGERPRINT:
            raise RuntimeError('Demo-02 SSH host identity mismatch')
        yield ['ssh', '-i', str(key), '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10',
               '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=3',
               '-o', 'StrictHostKeyChecking=yes', '-o', 'UserKnownHostsFile='+str(known), 'root@'+HOST]


def viewer_environment(ssh, remote):
    viewer = json.loads(subprocess.check_output([*ssh, 'python3', remote, 'viewer']))
    env = {k:v for k,v in os.environ.items()
           if k in ('PATH', 'HOME', 'PLAYWRIGHT_BROWSERS_PATH', 'TMPDIR', 'LANG', 'LC_ALL')}
    for key in ('DEMO_VIEWER_EMAIL', 'DEMO_VIEWER_PASSWORD'):
        value = viewer[key]
        if not isinstance(value, str) or not value or '\n' in value or '\r' in value:
            raise ValueError('Invalid shared-viewer credential')
        if os.environ.get('GITHUB_ACTIONS') == 'true':
            print('::add-mask::'+value.replace('%', '%25'), flush=True)
        env[key] = value
    return env


def verify_publication(ssh=None, revision_verified=False):
    """Read-only post-publication browser gate, with no runtime mutation path.

    Callers that need another pinned host action in the same operation can
    pass the SSH argv yielded by ``verified_demo_ssh`` and keep one verified
    key/host-key context for both actions.
    """
    revision = os.environ['SOURCE_REVISION']
    profile = read_contract(revision)['permissionProfile']
    if not revision_verified:
        verify_public_revision(revision, profile)
    if ssh is None:
        with verified_demo_ssh() as verified_ssh:
            return verify_publication(ssh=verified_ssh, revision_verified=True)
    remote = '/run/leapview-demo-verify-'+secrets.token_hex(12)+'.py'
    subprocess.run([*ssh, f'umask 077; cat > {remote}'],
                   input=(ROOT/'scripts/demo_compose_runtime.py').read_bytes(), check=True)
    try:
        env = viewer_environment(ssh, remote)
        subprocess.run(['node', 'scripts/demo_validate_browser.mjs'], cwd=ROOT,
                       check=True, timeout=240, env=env)
    finally:
        subprocess.run([*ssh, 'rm', '-f', remote], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--preflight', action='store_true', help='check source compatibility without deployment')
    parser.add_argument('--verify-publication', action='store_true', help='verify published CFO pages without changing the runtime')
    args = parser.parse_args()
    if args.verify_publication:
        if args.preflight:
            raise ValueError('Publication verification and preflight are separate operations')
        verify_publication()
        return
    action = os.environ.get("DEMO_ACTION", "deploy")
    if action not in ("deploy", "upgrade", "recover", "prepare", "reconcile"): raise ValueError("Unsupported demo operation")
    os.umask(0o077)
    binding = None
    if action == 'reconcile':
        from demo_runtime_record import binding_for_deployment
        binding = binding_for_deployment(os.environ['DEPLOYMENT_ID'])
        image, revision = binding['candidate']['image'], binding['candidate']['revision']
    else:
        image, revision = os.environ['DEMO_IMAGE'], os.environ['SOURCE_REVISION']
    if not re.fullmatch(r'ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}',image) or not re.fullmatch(r'[0-9a-f]{40}',revision):
        raise ValueError('Invalid admitted identity')
    candidate_profile = read_contract(revision)['permissionProfile']
    os.environ['DEMO_PERMISSION_PROFILE'] = candidate_profile
    validate_client_environment()
    with ExitStack() as resources:
        ssh = resources.enter_context(verified_demo_ssh())
        remote = '/run/leapview-demo-deploy-'+secrets.token_hex(12)+'.py'
        # umask protects the temporary script; no secrets are uploaded in it.
        subprocess.run([*ssh,f'umask 077; cat > {remote}'],input=(ROOT/'scripts/demo_compose_runtime.py').read_bytes(),check=True)
        prepared = None
        try:
            if action == 'reconcile':
                return  # finally collects current host evidence; no apply/recover call
            previous, plan = None, None
            if action != 'recover':
                previous = json.loads(subprocess.check_output([*ssh,'python3',remote,'inspect']))
                old_revision = previous['revision']
                if not re.fullmatch(r'[0-9a-f]{40}',old_revision): raise RuntimeError('Invalid predecessor identity')
                predecessor_profile = read_contract(old_revision)['permissionProfile']
                plan = inspect_transition(old_revision, revision)
                if args.preflight:
                    report = json.dumps(plan, indent=2)
                    print(report)
                    if os.environ.get('GITHUB_STEP_SUMMARY'):
                        with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as summary:
                            summary.write('### Demo deployment preflight\n```json\n'+report+'\n```\n')
                if action == 'deploy' and plan['mode'] == 'database-upgrade-required':
                    action = 'upgrade'
                expected_mode = 'image-only' if action == 'deploy' else 'database-upgrade-required'
                if plan['mode'] != expected_mode and not (action == 'prepare' and plan['mode'] == 'image-only'):
                    raise RuntimeError(f"{plan['mode']}: schema {plan['currentSchema']} -> {plan['candidateSchema']}; "
                                       'select the matching reviewed operation; no runtime deployment was attempted')
            unchanged = action == 'deploy' and previous['image'] == image and previous['revision'] == revision
            if not unchanged:
                prepared = upgrade.prepare(ssh, remote, action, previous, image, revision, plan)
            if action == 'recover':
                previous = dict(image=prepared[2]['predecessorImage'], revision=prepared[2]['predecessorRevision'])
                predecessor_profile = read_contract(previous['revision'])['permissionProfile']
            if action != 'prepare':
                operation_digest = prepared[2]['operationDigest'] if prepared else 'sha256:'+hashlib.sha256(json.dumps([
                    'leapview/verified-runtime-noop/v1', image, revision, os.environ['GITHUB_RUN_ID'], os.environ['GITHUB_RUN_ATTEMPT']]).encode()).hexdigest()
                binding = runtime_binding(action, previous, image, revision, operation_digest)
                write_binding(binding)
            if args.preflight:
                return
            publication_root = resources.enter_context(publication_source(revision)) if action == 'prepare' else None
            browser_env = viewer_environment(ssh, remote)
            # A fresh login before touching the host detects stale viewer credentials.
            if action != 'recover':
                verify_public_revision(old_revision, predecessor_profile)
                subprocess.run(['node','scripts/demo_validate_browser.mjs'],cwd=ROOT,check=True,timeout=240,env=browser_env)
            if unchanged:
                print('Selected image is already deployed; verified without restarting it.')
                return
            if prepared and action != 'deploy':
                helper, request_path, request = prepared
                upgrade.stage_credentials(ssh, helper, request_path, image, revision, request)
                client_env = dict(os.environ, **browser_env)
                def validate_clone(marker, env):
                    is_previous = marker == 'AWAITING_RECOVERY_BROWSER_VALIDATION'
                    env['DEMO_PERMISSION_PROFILE'] = predecessor_profile if is_previous else candidate_profile
                    expected = previous['revision'] if is_previous else revision
                    verify_public_revision(expected, env['DEMO_PERMISSION_PROFILE'], env)
                    if action == 'prepare' and not is_previous:
                        publish(publication_root, revision, env)
                result = upgrade.rollout(ssh, helper, request_path, action, image, revision, client_env, request['operationDigest'], request['profile'], validate=validate_clone)
                if action == 'prepare':
                    verify_public_revision(previous['revision'], predecessor_profile)
                    print('Detached rehearsal passed; live predecessor remains unchanged.')
                    return
                expected = revision if result == 'DEPLOYMENT_COMMITTED' else request['predecessorRevision']
                profile = candidate_profile if result == 'DEPLOYMENT_COMMITTED' else predecessor_profile
                verify_public_revision(expected, profile)
                subprocess.run(['node','scripts/demo_validate_browser.mjs'],cwd=ROOT,check=True,timeout=240,env=browser_env)
                if result == 'PREDECESSOR_RECOVERED':
                    raise RuntimeError('Predecessor safely recovered; candidate was NOT deployed and its runtime pin must not advance')
                return
            process = subprocess.Popen([*ssh,'python3',remote,image,revision,old_revision],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
            approved = False
            committed = False
            try:
                for line in process.stdout:
                    print(line.strip(),flush=True)
                    if line.strip() == 'AWAITING_BROWSER_VALIDATION':
                        verify_public_revision(revision, candidate_profile)
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
            try:
                if not args.preflight and binding is not None and os.environ.get('DEPLOYMENT_ID'):
                    from demo_host_outcome import collect
                    def verify_observed(expected):
                        profile = read_contract(expected)['permissionProfile']
                        verify_public_revision(expected, profile)
                    outcome = collect(ssh, remote, binding, os.environ['DEPLOYMENT_ID'], verify_observed,
                                      expected_schema=lambda source: source_schema(source)[0])
                    if (action == 'reconcile' and binding['operation'] in ('upgrade', 'recover')
                            and outcome.get('hostJournalVerified') is True
                            and outcome.get('journalState') in ('committed', 'succeeded', 'recovered')):
                        helper = remote+'.upgrade'
                        subprocess.run([*ssh, f'umask 077; cat > {helper}'],
                                       input=(ROOT/'scripts/demo_upgrade_remote.py').read_bytes(), check=True)
                        upgrade.clear_credentials(ssh, helper, image, revision,
                                                  operation_id=binding['operationId'])
            finally:
                try:
                    if prepared and not args.preflight:
                        upgrade.clear_credentials(ssh, prepared[0], image, revision, prepared[2])
                finally:
                    subprocess.run([*ssh,'rm','-f',remote+'.upgrade',remote+'.request',remote],check=True)

if __name__ == '__main__': main()

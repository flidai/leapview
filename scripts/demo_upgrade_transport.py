#!/usr/bin/env python3
"""Pinned-SSH upgrade transport and original-origin private browser validation."""
from contextlib import contextmanager, ExitStack
import json
import os
from pathlib import Path
import select
import re
import socket
import socketserver
import subprocess
import threading
import time

from demo_client_contract import clone_only_environment

ROOT = Path(__file__).resolve().parents[1]


class DemoTunnelProxy(socketserver.StreamRequestHandler):
    """CONNECT only the demo TLS origin to a fixed loopback SSH forward."""
    def handle(self):
        self.connection.settimeout(15)
        line = self.rfile.readline(4097)
        if line != b'CONNECT demo.leapview.dev:443 HTTP/1.1\r\n':
            self.wfile.write(b'HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n')
            return
        total = len(line)
        while True:
            header = self.rfile.readline(4097)
            total += len(header)
            if not header or total > 16384: return
            if header == b'\r\n': break
        try:
            upstream = socket.create_connection(('127.0.0.1', self.server.upstream_port), timeout=10)
        except OSError:
            self.wfile.write(b'HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n')
            return
        with upstream:
            self.wfile.write(b'HTTP/1.1 200 Connection Established\r\n\r\n')
            self.wfile.flush()
            self.connection.settimeout(30)
            upstream.settimeout(30)
            while True:
                ready, _, _ = select.select([self.connection, upstream], [], [], 30)
                if not ready: return
                for source in ready:
                    data = source.recv(65536)
                    if not data: return
                    (upstream if source is self.connection else self.connection).sendall(data)


@contextmanager
def private_browser(ssh, remote_port, browser_env):
    if type(remote_port) is not int or not 1024 <= remote_port <= 65535: raise ValueError('Invalid private validation port')
    with socket.socket() as reservation:
        reservation.bind(('127.0.0.1', 0))
        local_port = reservation.getsockname()[1]
    tunnel = subprocess.Popen([*ssh[:-1], '-o', 'ExitOnForwardFailure=yes', '-N', '-L',
                              f'127.0.0.1:{local_port}:127.0.0.1:{remote_port}', ssh[-1]])
    try:
        for _ in range(100):
            if tunnel.poll() is not None: raise RuntimeError('Private SSH forward failed')
            try:
                with socket.create_connection(('127.0.0.1', local_port), timeout=.1): break
            except OSError: time.sleep(.1)
        else: raise RuntimeError('Private SSH forward did not open')
        with socketserver.ThreadingTCPServer(('127.0.0.1', 0), DemoTunnelProxy) as proxy:
            proxy.daemon_threads = True
            proxy.upstream_port = local_port
            thread = threading.Thread(target=proxy.serve_forever, daemon=True)
            thread.start()
            try:
                proxy_url = f'http://127.0.0.1:{proxy.server_address[1]}'
                env = clone_only_environment(browser_env, proxy_url)
                env['DEMO_BROWSER_PROXY'] = proxy_url
                yield env
            finally:
                proxy.shutdown()
                thread.join(timeout=5)
    finally:
        tunnel.terminate()
        try: tunnel.wait(timeout=10)
        except subprocess.TimeoutExpired:
            tunnel.kill(); tunnel.wait()


def run_private_client(ssh, remote_port, client_env, command, *, cwd=ROOT, timeout=None):
    """Run any supported demo client while its clone-only tunnel is alive."""
    with private_browser(ssh, remote_port, client_env) as env:
        return subprocess.run(command, cwd=cwd, check=True, timeout=timeout, env=env)


def qualified_request(previous, image, revision, plan):
    receipt = json.loads((Path(os.environ['RUNNER_TEMP'])/'demo-qualification.json').read_text())
    admission = json.loads((Path(os.environ['RUNNER_TEMP'])/'oci-admission.json').read_text())
    return dict(profile=json.loads((ROOT/'deploy/demo/host-upgrade.json').read_text()),version=1, deploymentRunId=os.environ['GITHUB_RUN_ID'],
                deploymentAttempt=os.environ['GITHUB_RUN_ATTEMPT'],
                predecessorImage=previous['image'], predecessorRevision=previous['revision'],
                candidateImage=image, candidateRevision=revision, qualification=receipt,
                admission=admission, plan=plan)


def prepare(ssh, remote, action, previous, image, revision, plan):
    helper = remote+'.upgrade'
    request_path = remote+'.request'
    subprocess.run([*ssh, f'umask 077; cat > {helper}'],
                   input=(ROOT/'scripts/demo_upgrade_remote.py').read_bytes(), check=True)
    if action == 'recover':
        request = json.loads(subprocess.check_output([*ssh, 'python3', helper, 'pending', image, revision]))
        reference = os.environ.get('DEMO_AGENT_CREDENTIAL_TRANSITION', '')
        if reference and reference != (request.get('agentCredentialTransition') or {}).get('reference'):
            raise ValueError('Recovery must use the original credential transition intent')
    else:
        request = qualified_request(previous, image, revision, plan)
        if plan['sourceBefore']['permissionProfile'] != plan['sourceAfter']['permissionProfile']:
            request['accessTransition'] = json.loads(subprocess.check_output([*ssh, 'python3', helper, 'intent', image, revision]))
        reference = os.environ.get('DEMO_AGENT_CREDENTIAL_TRANSITION', '')
        if reference:
            if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}', reference):
                raise ValueError('Invalid agent credential transition reference')
            # Only intent metadata is captured; actual credentials later stream
            # directly from authenticated SSH to the private browser child's stdin.
            subprocess.run([*ssh, f'umask 077; cat > {request_path}'], input=json.dumps(request).encode(), check=True)
            request['agentCredentialTransition'] = json.loads(subprocess.check_output([
                *ssh, 'python3', helper, 'agent-intent', image, revision, request_path, reference]))
        if action == 'upgrade':
            subprocess.run([*ssh, f'umask 077; cat > {request_path}'], input=json.dumps(request).encode(), check=True)
            rehearsal = json.loads(subprocess.check_output([*ssh, 'python3', helper, 'prepared', image, revision, previous['image'], request_path]))
            request['preparationDigest'] = rehearsal['preparationDigest']
    subprocess.run([*ssh, f'umask 077; cat > {request_path}'],
                   input=json.dumps(request).encode(), check=True)
    # Candidate-owned policy checks the exact reviewed transition before the
    # workflow creates a deployment record or asks the host to close traffic.
    report = json.loads(subprocess.check_output([*ssh, 'python3', helper, 'plan', image, revision, request_path]))
    if report['plan']['mode'] != request['plan']['mode'] or not re.fullmatch(r'sha256:[0-9a-f]{64}', report['operationDigest']):
        raise ValueError('Candidate preflight identity mismatch')
    return helper, request_path, dict(request, operationDigest=report['operationDigest'])




_AGENT_DRIVER_ENVIRONMENT = frozenset((
    'PATH', 'HOME', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LC_ALL',
    'LD_LIBRARY_PATH', 'PLAYWRIGHT_BROWSERS_PATH', 'NIX_LD', 'NIX_LD_LIBRARY_PATH',
))


def agent_driver_environment(environment):
    """Only browser/toolchain runtime variables reach the credential driver."""
    return {name: value for name, value in environment.items() if name in _AGENT_DRIVER_ENVIRONMENT}


class AgentCredentialDriver:
    """One private browser session retains the exact Test token through Save."""
    def __init__(self, ssh, helper, request_path, image, revision, port, browser_env, digest):
        self.stack = ExitStack()
        self.process = None
        self.ssh = ssh
        self.helper, self.request_path = helper, request_path
        self.image, self.revision, self.digest = image, revision, digest
        try:
            env = self.stack.enter_context(private_browser(ssh, port, browser_env))
            proxy_url = env['DEMO_BROWSER_PROXY']
            # Deployment secrets and debugging/preload/TLS overrides have no
            # place in this child; only explicit browser runtime inputs survive.
            env = agent_driver_environment(env)
            self.process = subprocess.Popen([
                'node', 'scripts/demo_agent_credential_transition.mjs',
                '--base-url', 'https://demo.leapview.dev', '--proxy-url', proxy_url],
                cwd=ROOT, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL, text=True)
        except BaseException:
            self.close()
            raise RuntimeError('Private agent credential driver failed to start') from None

    def receipt(self, expected):
        if self.process is None:
            raise RuntimeError('Private agent credential driver is unavailable')
        ready, _, _ = select.select([self.process.stdout], [], [], 150)
        if not ready or self.process.stdout.readline(128) != expected+'\n':
            raise RuntimeError('Private agent credential checkpoint failed')

    def test(self):
        # SSH stdout is attached directly to Node stdin. Neither credential JSON
        # nor its hash is materialized in runner files, environment, or Python.
        try:
            export = subprocess.run([
                *self.ssh, 'python3', self.helper, 'agent-export', self.image,
                self.revision, self.request_path], stdout=self.process.stdin,
                stderr=subprocess.DEVNULL, timeout=30)
            if export.returncode:
                raise RuntimeError('Private agent credential handoff failed')
            self.receipt('TEST_PASSED')
        except (OSError, subprocess.TimeoutExpired):
            raise RuntimeError('Private agent credential handoff failed') from None

    def save(self):
        self.process.stdin.write(json.dumps({'action': 'save', 'operationDigest': self.digest})+'\n')
        self.process.stdin.flush()
        self.receipt('SAVE_PASSED')
        if self.process.wait(timeout=15) != 0:
            raise RuntimeError('Private agent credential driver did not close')

    def close(self):
        process, self.process = self.process, None
        try:
            if process is not None:
                try:
                    process.stdin.close()
                except (OSError, ValueError):
                    pass  # Broken pipe must not skip shutdown or tunnel cleanup.
                try:
                    try:
                        process.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        process.terminate()
                        try:
                            process.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            try:
                                process.wait(timeout=5)
                            except subprocess.TimeoutExpired:
                                raise RuntimeError('Private agent credential driver cleanup failed') from None
                finally:
                    process.stdout.close()
        finally:
            self.stack.close()


def rollout(ssh, helper, request_path, action, image, revision, browser_env, expected_digest, profile, validate=None, agent_transition=None):
    if action not in ('upgrade', 'recover', 'prepare'):
        raise ValueError('Unsupported maintenance transport operation')
    command = {'upgrade':'apply', 'recover':'recover', 'prepare':'verify-copy'}[action]
    if action == 'prepare':
        # This command returns only after the live capture transaction is
        # terminal and the predecessor has reopened. No detached failure below
        # can call the live recovery command.
        capture = json.loads(subprocess.check_output([*ssh, 'python3', helper, 'capture', image, revision, request_path]))
        if capture.get('phase') != 'ready' or capture.get('identity', {}).get('artifactAdmissionDigest') != expected_digest:
            raise RuntimeError('Live capture has not released the exact predecessor')
    process = subprocess.Popen([*ssh, 'python3', helper, command,
                                image, revision, request_path], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    approved = set()
    result = None
    agent = None
    agent_approved = set()
    try:
        for line in process.stdout:
            marker, _, identity = line.strip().partition(' ')
            print(marker, flush=True)
            ports = {'AWAITING_RECOVERY_BROWSER_VALIDATION': int(profile['rehearsalBinding'].rsplit(':',1)[1]), 'AWAITING_REHEARSAL_BROWSER_VALIDATION': int(profile['rehearsalBinding'].rsplit(':',1)[1]), 'AWAITING_CANDIDATE_BROWSER_VALIDATION': int(profile['httpsBinding'].rsplit(':',1)[1])}
            agent_markers = {
                'AWAITING_REHEARSAL_AGENT_CREDENTIAL_TEST': ('test', ports['AWAITING_REHEARSAL_BROWSER_VALIDATION']),
                'AWAITING_REHEARSAL_AGENT_CREDENTIAL_SAVE': ('save', ports['AWAITING_REHEARSAL_BROWSER_VALIDATION']),
                'AWAITING_CANDIDATE_AGENT_CREDENTIAL_TEST': ('test', ports['AWAITING_CANDIDATE_BROWSER_VALIDATION']),
                'AWAITING_CANDIDATE_AGENT_CREDENTIAL_SAVE': ('save', ports['AWAITING_CANDIDATE_BROWSER_VALIDATION']),
            }
            if marker in agent_markers:
                if (not agent_transition or action == 'recover' or identity != expected_digest or marker in agent_approved or
                        (action == 'prepare' and marker.startswith('AWAITING_CANDIDATE_'))):
                    raise RuntimeError('Unexpected agent credential checkpoint')
                stage, port = agent_markers[marker]
                if stage == 'test':
                    preceding = 'AWAITING_RECOVERY_BROWSER_VALIDATION' if marker.startswith('AWAITING_REHEARSAL_') else 'AWAITING_REHEARSAL_BROWSER_VALIDATION'
                    if agent is not None or preceding not in approved:
                        raise RuntimeError('Agent credential checkpoint order differs from admission')
                    agent = AgentCredentialDriver(ssh, helper, request_path, image, revision, port, browser_env, expected_digest)
                    agent.test()
                else:
                    if agent is None or marker.replace('_SAVE', '_TEST') not in agent_approved:
                        raise RuntimeError('Agent Save lacks its same-process Test receipt')
                    agent.save()
                    agent.close()
                    agent = None
                process.stdin.write(f'commit {identity} {marker}\n'); process.stdin.flush()
                agent_approved.add(marker)
            if marker in ports:
                if agent is not None:
                    raise RuntimeError('Browser approval precedes credential cleanup')
                if agent_transition and marker != 'AWAITING_RECOVERY_BROWSER_VALIDATION' and marker.replace('_BROWSER_VALIDATION', '_AGENT_CREDENTIAL_SAVE') not in agent_approved:
                    raise RuntimeError('Browser approval lacks credential activation checkpoint')
                if action not in ('upgrade', 'prepare') or (action == 'prepare' and marker == 'AWAITING_CANDIDATE_BROWSER_VALIDATION') or marker in approved or identity != expected_digest:
                    raise RuntimeError('Unexpected or repeated browser approval request')
                with private_browser(ssh, ports[marker], browser_env) as env:
                    if validate is not None:
                        validate(marker, env)
                    subprocess.run(['node', 'scripts/demo_validate_browser.mjs'], cwd=ROOT, check=True, timeout=240, env=env)
                process.stdin.write(f'commit {identity} {marker}\n'); process.stdin.flush()
                approved.add(marker)
            if marker in ('DEPLOYMENT_COMMITTED', 'PREDECESSOR_RECOVERED'): result = marker
            if action == 'prepare' and line.startswith('{'):
                receipt = json.loads(line)
                if receipt.get('identity', {}).get('artifactAdmissionDigest') != expected_digest or receipt.get('phase') != 'passed':
                    raise RuntimeError('Detached rehearsal did not pass for this operation')
                result = 'REHEARSAL_PASSED'
        if process.wait() != 0 or result is None:
            raise RuntimeError('Upgrade/recovery did not finish; inspect durable host journal before retrying')
        if action == 'upgrade' and (result != 'DEPLOYMENT_COMMITTED' or len(approved) != 3):
            raise RuntimeError('Upgrade did not complete all recovery/rehearsal/candidate browser gates')
        if action == 'prepare' and (result != 'REHEARSAL_PASSED' or len(approved) != 2):
            raise RuntimeError('Detached rehearsal did not complete both clone gates')
        expected_agent_count = (2 if action == 'prepare' else 4) if agent_transition and action != 'recover' else 0
        if len(agent_approved) != expected_agent_count:
            raise RuntimeError('Agent credential checkpoints did not complete')
        return result
    finally:
        try:
            if agent is not None:
                agent.close()
        finally:
            # Driver/tunnel cleanup failure cannot bypass controller EOF or its
            # bounded recovery wait. This is the paired-recovery trigger.
            try:
                process.stdin.close()
            except (OSError, ValueError):
                pass
            try: process.wait(timeout=960)
            except subprocess.TimeoutExpired:
                process.kill(); process.wait()
                raise RuntimeError('Recovery exceeded timeout; use the explicit recover action after inspecting the journal')


def stage_credentials(ssh, helper, request_path, image, revision, request):
    intent = request.get('accessTransition')
    if not intent:
        return
    if os.environ.get('DEMO_RELEASE_CLIENT_ID') != intent['reviewerPrincipalId'] or os.environ.get('DEMO_PUBLISHER_CLIENT_ID') != intent['publisherPrincipalId']:
        raise ValueError('Workflow credential identities differ from the admitted transition intent')
    credentials = dict(publisher=os.environ['DEMO_PUBLISHER_CLIENT_SECRET'], reviewer=os.environ['DEMO_RELEASE_CLIENT_SECRET'])
    for credential in credentials.values():
        if not credential or len(credential.encode()) > 65536 or any(ch in credential for ch in '\x00\n\r'):
            raise ValueError('Invalid workload credential')
    subprocess.run([*ssh, 'python3', helper, 'stage-credentials', image, revision, request_path, request['operationDigest']],
                   input=json.dumps(credentials).encode(), check=True)


def clear_credentials(ssh, helper, image, revision, request=None, *, operation_id=None):
    if request is not None and not request.get('accessTransition'):
        return
    digest = operation_id or (request or {}).get('operationDigest')
    if not isinstance(digest, str) or not re.fullmatch(r'sha256:[0-9a-f]{64}', digest):
        raise ValueError('Transition cleanup requires the original admitted operation digest')
    subprocess.run([*ssh, 'python3', helper, 'clear-credentials', image, revision, digest], check=True)

#!/usr/bin/env python3
"""Pinned-SSH upgrade transport and original-origin private browser validation."""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import select
import socket
import socketserver
import subprocess
import threading
import time

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
    if remote_port not in (8443, 8444): raise ValueError('Unknown private validation port')
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
                yield dict(browser_env, DEMO_BROWSER_PROXY=f'http://127.0.0.1:{proxy.server_address[1]}')
            finally:
                proxy.shutdown()
                thread.join(timeout=5)
    finally:
        tunnel.terminate()
        try: tunnel.wait(timeout=10)
        except subprocess.TimeoutExpired:
            tunnel.kill(); tunnel.wait()


def qualified_request(previous, image, revision, plan):
    receipt = json.loads((Path(os.environ['RUNNER_TEMP'])/'demo-qualification.json').read_text())
    admission = json.loads((Path(os.environ['RUNNER_TEMP'])/'oci-admission.json').read_text())
    return dict(version=1, deploymentRunId=os.environ['GITHUB_RUN_ID'],
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
    else:
        request = qualified_request(previous, image, revision, plan)
    subprocess.run([*ssh, f'umask 077; cat > {request_path}'],
                   input=json.dumps(request).encode(), check=True)
    # Candidate-owned policy checks the exact reviewed transition before the
    # workflow creates a deployment record or asks the host to close traffic.
    subprocess.run([*ssh, 'python3', helper, 'check', image, revision, request_path], check=True)
    return helper, request_path, request


def rollout(ssh, helper, request_path, action, image, revision, browser_env):
    process = subprocess.Popen([*ssh, 'python3', helper, 'apply' if action == 'upgrade' else 'recover',
                                image, revision, request_path], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    approved = set()
    result = None
    try:
        for line in process.stdout:
            marker = line.strip()
            print(marker, flush=True)
            ports = {'AWAITING_RECOVERY_BROWSER_VALIDATION': 8444, 'AWAITING_CANDIDATE_BROWSER_VALIDATION': 8443}
            if marker in ports:
                if action != 'upgrade' or marker in approved:
                    raise RuntimeError('Unexpected or repeated browser approval request')
                with private_browser(ssh, ports[marker], browser_env) as env:
                    subprocess.run(['node', 'scripts/demo_validate_browser.mjs'], cwd=ROOT, check=True, timeout=240, env=env)
                process.stdin.write('commit\n'); process.stdin.flush()
                approved.add(marker)
            if marker in ('DEPLOYMENT_COMMITTED', 'PREDECESSOR_RECOVERED'): result = marker
        if process.wait() != 0 or result is None:
            raise RuntimeError('Upgrade/recovery did not finish; inspect durable host journal before retrying')
        if action == 'upgrade' and (result != 'DEPLOYMENT_COMMITTED' or len(approved) != 2):
            raise RuntimeError('Upgrade did not complete both isolated browser gates')
        return result
    finally:
        process.stdin.close()  # EOF cancels approval and invokes paired recovery.
        try: process.wait(timeout=960)
        except subprocess.TimeoutExpired:
            process.kill(); process.wait()
            raise RuntimeError('Recovery exceeded timeout; use the explicit recover action after inspecting the journal')

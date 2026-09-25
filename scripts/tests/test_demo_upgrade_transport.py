import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import socketserver
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch, MagicMock

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import demo_upgrade_transport as transport
import demo_upgrade_remote as remote


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        data = self.request.recv(1024)
        self.request.sendall(data)


class ProxyTests(unittest.TestCase):
    def test_only_demo_origin_can_use_the_loopback_tunnel(self):
        with socketserver.ThreadingTCPServer(('127.0.0.1', 0), Echo) as upstream, \
             socketserver.ThreadingTCPServer(('127.0.0.1', 0), transport.DemoTunnelProxy) as proxy:
            proxy.daemon_threads = True
            proxy.upstream_port = upstream.server_address[1]
            threads = [threading.Thread(target=s.serve_forever, daemon=True) for s in (upstream, proxy)]
            for thread in threads: thread.start()
            try:
                with socket.create_connection(proxy.server_address) as client:
                    client.sendall(b'CONNECT demo.leapview.dev:443 HTTP/1.1\r\nHost: demo.leapview.dev\r\n\r\n')
                    self.assertIn(b'200 Connection Established', client.recv(4096))
                    client.sendall(b'opaque TLS bytes remain unchanged')
                    self.assertEqual(client.recv(4096), b'opaque TLS bytes remain unchanged')
                for target in ('example.com:443', 'demo.leapview.dev:80', '127.0.0.1:5432'):
                    with socket.create_connection(proxy.server_address) as client:
                        client.sendall(f'CONNECT {target} HTTP/1.1\r\n\r\n'.encode())
                        self.assertIn(b'403 Forbidden', client.recv(4096))
            finally:
                upstream.shutdown(); proxy.shutdown()
                for thread in threads: thread.join(timeout=5)


class ProtocolTests(unittest.TestCase):
    def process(self, lines, code=0):
        process = MagicMock()
        process.stdout = io.StringIO(lines)
        process.stdin = MagicMock()
        process.wait.return_value = code
        return process

    def execute(self, process, action='upgrade', browser_error=None):
        private = MagicMock()
        private.return_value.__enter__.return_value = {'test': 'environment'}
        with patch.object(transport.subprocess, 'Popen', return_value=process), \
             patch.object(transport.subprocess, 'run', side_effect=browser_error) as browser, \
             patch.object(transport, 'private_browser', private), patch('sys.stdout', io.StringIO()):
            result = transport.rollout(['ssh', 'root@host'], '/run/helper', '/run/request', action, 'image', 'revision', {})
            return result, browser

    def test_both_isolated_checks_are_required_before_success(self):
        process = self.process('AWAITING_RECOVERY_BROWSER_VALIDATION\nAWAITING_CANDIDATE_BROWSER_VALIDATION\nDEPLOYMENT_COMMITTED\n')
        result, browser = self.execute(process)
        self.assertEqual(result, 'DEPLOYMENT_COMMITTED')
        self.assertEqual(browser.call_count, 2)
        self.assertEqual(process.stdin.write.call_count, 2)
        process.stdin.close.assert_called_once()

    def test_failed_browser_sends_no_approval_and_closes_stdin_for_recovery(self):
        process = self.process('AWAITING_RECOVERY_BROWSER_VALIDATION\n')
        with self.assertRaisesRegex(RuntimeError, 'browser failed'):
            self.execute(process, browser_error=RuntimeError('browser failed'))
        process.stdin.write.assert_not_called()
        process.stdin.close.assert_called_once()

    def test_premature_commit_duplicate_gate_and_failed_host_are_rejected(self):
        for lines, code in [('DEPLOYMENT_COMMITTED\n', 0),
                            ('AWAITING_RECOVERY_BROWSER_VALIDATION\nAWAITING_RECOVERY_BROWSER_VALIDATION\n', 0),
                            ('PREDECESSOR_RECOVERED\n', 1)]:
            with self.subTest(lines=lines), self.assertRaises(RuntimeError):
                self.execute(self.process(lines, code))

    def test_recovery_distinguishes_old_restored_from_candidate_committed(self):
        for marker in ('PREDECESSOR_RECOVERED', 'DEPLOYMENT_COMMITTED'):
            result, browser = self.execute(self.process(marker+'\n'), action='recover')
            self.assertEqual(result, marker)
            browser.assert_not_called()


class RecoveryRequestTests(unittest.TestCase):
    def test_recovery_cannot_select_another_candidate_or_escape_operation_directory(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(remote, 'PROVIDER', Path(directory)):
            root = Path(directory)
            digest = 'a'*64
            identity = {'candidate': 'image', 'target': 'app-leapview-demo-02', 'artifactAdmissionDigest': 'sha256:'+digest}
            journal = root/'upgrade-operation.json'
            journal.write_text(json.dumps({'state': {'identity': identity}}))
            operation = root/'upgrade-operations'/digest
            operation.mkdir(parents=True)
            (operation/'request.json').write_text(json.dumps({'candidateImage': 'image', 'candidateRevision': 'revision'}))
            self.assertEqual(remote.pending_request('image', 'revision')['candidateImage'], 'image')
            with self.assertRaises(ValueError): remote.pending_request('another', 'revision')
            identity['artifactAdmissionDigest'] = '../../another-file'
            journal.write_text(json.dumps({'state': {'identity': identity}}))
            with self.assertRaises(ValueError): remote.pending_request('image', 'revision')


if __name__ == '__main__': unittest.main()

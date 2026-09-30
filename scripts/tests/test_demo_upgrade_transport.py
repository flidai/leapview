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
        lines = lines.replace('AWAITING_RECOVERY_BROWSER_VALIDATION\n', 'AWAITING_RECOVERY_BROWSER_VALIDATION sha256:'+'a'*64+'\n').replace('AWAITING_REHEARSAL_BROWSER_VALIDATION\n', 'AWAITING_REHEARSAL_BROWSER_VALIDATION sha256:'+'a'*64+'\n').replace('AWAITING_CANDIDATE_BROWSER_VALIDATION\n', 'AWAITING_CANDIDATE_BROWSER_VALIDATION sha256:'+'a'*64+'\n')
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
            result = transport.rollout(['ssh', 'root@host'], '/run/helper', '/run/request', action, 'image', 'revision', {}, 'sha256:'+'a'*64, {'rehearsalBinding':'127.0.0.1:8444','httpsBinding':'127.0.0.1:8443'})
            return result, browser

    def test_all_three_isolated_checks_are_required_before_success(self):
        process = self.process('AWAITING_RECOVERY_BROWSER_VALIDATION\nAWAITING_REHEARSAL_BROWSER_VALIDATION\nAWAITING_CANDIDATE_BROWSER_VALIDATION\nDEPLOYMENT_COMMITTED\n')
        result, browser = self.execute(process)
        self.assertEqual(result, 'DEPLOYMENT_COMMITTED')
        self.assertEqual(browser.call_count, 3)
        self.assertEqual(process.stdin.write.call_count, 3)
        process.stdin.close.assert_called_once()

    def test_wrong_operation_is_never_approved(self):
        process = self.process('AWAITING_RECOVERY_BROWSER_VALIDATION sha256:'+'b'*64+'\n')
        with self.assertRaisesRegex(RuntimeError, 'Unexpected'):
            self.execute(process)
        process.stdin.write.assert_not_called()
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

    def test_detached_failure_cannot_execute_live_recovery(self):
        digest = 'sha256:'+'a'*64
        capture = dict(phase='ready', identity=dict(artifactAdmissionDigest=digest))
        process = self.process('AWAITING_RECOVERY_BROWSER_VALIDATION\n')
        with patch.object(transport.subprocess, 'check_output', return_value=json.dumps(capture)) as captured, \
             patch.object(transport.subprocess, 'Popen', return_value=process) as started, \
             patch.object(transport.subprocess, 'run', side_effect=RuntimeError('clone failed')), \
             patch.object(transport, 'private_browser') as proxy, patch('sys.stdout', io.StringIO()):
            proxy.return_value.__enter__.return_value = {}
            with self.assertRaisesRegex(RuntimeError, 'clone failed'):
                transport.rollout(['ssh','host'], 'helper', 'request', 'prepare', 'image', 'revision', {}, digest,
                                  {'rehearsalBinding':'127.0.0.1:8444','httpsBinding':'127.0.0.1:8443'})
            self.assertIn('capture', captured.call_args.args[0])
            self.assertIn('verify-copy', started.call_args.args[0])
            self.assertNotIn('recover', started.call_args.args[0])
            started.assert_called_once()
            process.stdin.write.assert_not_called()
            process.stdin.close.assert_called_once()

    def test_incomplete_capture_never_starts_detached_clone(self):
        with patch.object(transport.subprocess, 'check_output', return_value=json.dumps({'phase':'captured'})), \
             patch.object(transport.subprocess, 'Popen') as started:
            with self.assertRaisesRegex(RuntimeError, 'capture'):
                transport.rollout(['ssh','host'], 'helper', 'request', 'prepare', 'image', 'revision', {}, 'sha256:'+'a'*64, {})
            started.assert_not_called()

    def test_recovery_distinguishes_old_restored_from_candidate_committed(self):
        for marker in ('PREDECESSOR_RECOVERED', 'DEPLOYMENT_COMMITTED'):
            result, browser = self.execute(self.process(marker+'\n'), action='recover')
            self.assertEqual(result, marker)
            browser.assert_not_called()


class TransitionCredentialTests(unittest.TestCase):
    def test_credentials_are_bound_and_sent_only_over_private_stdin(self):
        request = dict(operationDigest='sha256:'+'a'*64,
                       accessTransition=dict(publisherPrincipalId='publisher-id', reviewerPrincipalId='reviewer-id'))
        env = dict(DEMO_PUBLISHER_CLIENT_ID='publisher-id', DEMO_RELEASE_CLIENT_ID='reviewer-id',
                   DEMO_PUBLISHER_CLIENT_SECRET='synthetic-publisher-secret', DEMO_RELEASE_CLIENT_SECRET='synthetic-reviewer-secret')
        with patch.dict(os.environ, env, clear=True), patch.object(transport.subprocess,'run') as run:
            transport.stage_credentials(['ssh','host'],'helper','request','image','revision',request)
            args = run.call_args.args[0]
            self.assertIn('stage-credentials', args)
            self.assertNotIn(env['DEMO_PUBLISHER_CLIENT_SECRET'], repr(args))
            self.assertNotIn(env['DEMO_RELEASE_CLIENT_SECRET'], repr(args))
            self.assertEqual(json.loads(run.call_args.kwargs['input']),
                             {'publisher':env['DEMO_PUBLISHER_CLIENT_SECRET'], 'reviewer':env['DEMO_RELEASE_CLIENT_SECRET']})
            self.assertNotIn('secret', json.dumps(request))
            run.reset_mock()
            os.environ['DEMO_RELEASE_CLIENT_ID'] = 'another-principal'
            with self.assertRaisesRegex(ValueError, 'identities'):
                transport.stage_credentials(['ssh','host'],'helper','request','image','revision',request)
            run.assert_not_called()

    def test_ordinary_update_does_not_stage_transition_credentials(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(transport.subprocess,'run') as run:
            transport.stage_credentials(['ssh','host'],'helper','request','image','revision',{})
            transport.clear_credentials(['ssh','host'],'helper','image','revision',{})
            run.assert_not_called()

    def test_reconcile_cleanup_uses_original_operation_digest(self):
        digest = 'sha256:'+'a'*64
        with patch.object(transport.subprocess, 'run') as run:
            transport.clear_credentials(['ssh','host'], 'helper', 'image', 'revision', operation_id=digest)
        args = run.call_args.args[0]
        self.assertEqual(args[-4:], ['clear-credentials', 'image', 'revision', digest])
        self.assertNotIn('request', args)


class TerminalTransitionCleanupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.provider = self.root/'provider'
        self.installation = self.root/'installation'
        self.provider.mkdir(mode=0o700)
        self.installation.mkdir(mode=0o700)
        self.digest = 'sha256:'+'a'*64
        self.image = 'ghcr.io/flidai/leapview@sha256:'+'b'*64
        self.revision = 'c'*40
        self.predecessor = 'ghcr.io/flidai/leapview@sha256:'+'d'*64
        self.target = 'app-leapview-demo-02'
        self.operation = self.provider/'upgrade-operations'/self.digest[7:]
        self.operation.mkdir(parents=True, mode=0o700)
        self.identity = {
            'artifactAdmissionDigest': self.digest, 'candidate': self.image,
            'predecessor': self.predecessor, 'target': self.target,
        }
        self.request = {
            'version': 1, 'candidateImage': self.image, 'candidateRevision': self.revision,
            'predecessorImage': self.predecessor, 'predecessorRevision': 'e'*40,
            'profile': {'id': self.target, 'stateRoot': str(self.provider)},
            'accessTransition': {'publisherPrincipalId': 'publisher', 'reviewerPrincipalId': 'reviewer'},
            'preparationDigest': 'sha256:'+'f'*64,
        }
        self.write_json(self.operation/'request.json', self.request)
        controllers = self.provider/'upgrade-controllers'/self.image.rsplit(':', 1)[1]
        controllers.mkdir(parents=True, mode=0o700)
        self.binary = controllers/'leapviewctl'
        self.binary.write_text('candidate controller')
        self.binary.chmod(0o500)
        self.write_journal('succeeded')
        self.lock = self.installation/'.leapviewctl.lock'
        self.lock.write_text('')
        self.addCleanup(patch.stopall)
        patch.object(remote, 'PROVIDER', self.provider).start()
        patch.object(remote, 'INSTALLATION', self.installation).start()
        self.docker_calls = []
        def check_output(args, **kwargs):
            self.docker_calls.append(args)
            if args[0] == str(self.binary):
                return json.dumps({'operationDigest': self.digest}).encode()
            if args[:2] == ['docker', 'ps']:
                return '' if kwargs.get('text') else b''
            raise AssertionError(args)
        patch.object(remote.subprocess, 'check_output', side_effect=check_output).start()

    def write_json(self, path, value):
        path.write_text(json.dumps(value))
        path.chmod(0o600)

    def write_journal(self, phase):
        self.write_json(self.installation/'upgrade-operation.json', {
            'version': 1, 'state': {'phase': phase, 'identity': self.identity}})

    def add_transition_files(self):
        names = ('publisher.secret', 'reviewer.secret', 'transition-request.json',
                 'transition-journal.json', 'transition-publisher.secret',
                 'transition-reviewer.secret', 'transition.env')
        for name in names:
            path = self.operation/name
            path.write_text('private staged input')
            path.chmod(0o400 if name.startswith('transition-') else 0o600)
        return names

    def clear(self):
        remote.transition_credentials('clear-credentials', self.image, self.revision, '', self.digest)

    def test_terminal_verified_operation_removes_only_transition_inputs(self):
        names = self.add_transition_files()
        with patch.object(remote.subprocess, 'check_output', wraps=remote.subprocess.check_output):
            self.clear()
        for name in names:
            self.assertFalse((self.operation/name).exists(), name)
        self.assertTrue((self.operation/'request.json').exists())
        self.assertEqual(len(self.docker_calls), 2)

    def test_nonterminal_journal_retains_credentials(self):
        names = self.add_transition_files()
        self.write_journal('migrating')
        with self.assertRaisesRegex(ValueError, 'not terminal'):
            self.clear()
        self.assertTrue(all((self.operation/name).exists() for name in names))
        self.assertEqual(len(self.docker_calls), 1)  # Exact persisted request was checked first.

    def test_recovered_capture_failure_without_detached_receipt_clears_inputs(self):
        names = self.add_transition_files()
        self.request.pop('preparationDigest')
        self.write_json(self.operation/'request.json', self.request)
        self.write_journal('recovered')
        self.clear()
        self.assertTrue(all(not (self.operation/name).exists() for name in names))
        self.assertTrue((self.operation/'request.json').exists())

    def test_prepare_cleanup_preserves_inputs_until_exact_detached_terminal_state(self):
        self.request.pop('preparationDigest')
        self.write_json(self.operation/'request.json', self.request)
        self.write_journal('recovered')
        names = self.add_transition_files()
        for phase in ('captured', 'ready', 'running', 'unknown'):
            self.write_json(self.operation/'detached-rehearsal.json', {'phase': phase, 'identity': self.identity})
            with self.subTest(phase=phase), self.assertRaises(ValueError):
                self.clear()
            self.assertTrue(all((self.operation/name).exists() for name in names))
        self.write_json(self.operation/'detached-rehearsal.json', {'phase': 'passed', 'identity': {}})
        with self.assertRaises(ValueError):
            self.clear()
        for phase in ('passed', 'failed'):
            if phase == 'failed':
                names = self.add_transition_files()
            self.write_json(self.operation/'detached-rehearsal.json', {'phase': phase, 'identity': self.identity})
            self.clear()
            self.assertTrue(all(not (self.operation/name).exists() for name in names))

    def test_detached_retry_holds_inputs_even_with_previous_terminal_receipt(self):
        names = self.add_transition_files()
        lock_path = self.operation/'.detached-rehearsal.lock'
        with lock_path.open('w') as lock:
            remote.fcntl.flock(lock.fileno(), remote.fcntl.LOCK_EX | remote.fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError):
                self.clear()
        self.assertTrue(all((self.operation/name).exists() for name in names))

    def test_active_transition_container_retains_credentials(self):
        names = self.add_transition_files()
        def active(args, **kwargs):
            if args[0] == str(self.binary):
                return json.dumps({'operationDigest': self.digest}).encode()
            if args[:2] == ['docker', 'ps']:
                return 'leapview-recovery-'+'a'*16+'-transition'
            raise AssertionError(args)
        with patch.object(remote.subprocess, 'check_output', side_effect=active):
            with self.assertRaisesRegex(ValueError, 'still running'):
                self.clear()
        self.assertTrue(all((self.operation/name).exists() for name in names))

    def test_wrong_persisted_operation_digest_retains_credentials(self):
        names = self.add_transition_files()
        with patch.object(remote.subprocess, 'check_output', return_value=json.dumps({'operationDigest':'sha256:'+'f'*64}).encode()):
            with self.assertRaisesRegex(ValueError, 'original operation'):
                self.clear()
        self.assertTrue(all((self.operation/name).exists() for name in names))


class RecoveryRequestTests(unittest.TestCase):
    def test_recovery_cannot_select_another_candidate_or_escape_operation_directory(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(remote, 'PROVIDER', Path(directory)), patch.object(remote, 'INSTALLATION', Path(directory)):
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

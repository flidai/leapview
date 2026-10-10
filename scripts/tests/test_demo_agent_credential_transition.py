import contextlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import demo_upgrade_remote as remote
import demo_upgrade_transport as transport

DIGEST = 'sha256:'+'a'*64
IMAGE = 'ghcr.io/flidai/leapview@sha256:'+'b'*64
PREDECESSOR = 'ghcr.io/flidai/leapview@sha256:'+'c'*64
REVISION = 'd'*40


class PrivateDriverEnvironmentTests(unittest.TestCase):
    def test_explicit_runtime_allowlist_excludes_deployment_credentials_and_hooks(self):
        environment = {'PATH': '/runtime/bin', 'HOME': '/runtime/home',
                       'PLAYWRIGHT_BROWSERS_PATH': '/runtime/browsers',
                       'DEMO_SSH_PRIVATE_KEY': 'fixture-ssh-private-key',
                       'DEMO_VIEWER_PASSWORD': 'fixture-viewer-password',
                       'PROVIDER_CREDENTIAL': 'fixture-provider-credential',
                       'ENCRYPTION_KEY': 'fixture-encryption-key', 'KEYRING': 'fixture-keyring',
                       'DEBUG': '*', 'PWDEBUG': '1', 'NODE_OPTIONS': '--require hook',
                       'NODE_EXTRA_CA_CERTS': '/fixture/unapproved-ca', 'NODE_TLS_REJECT_UNAUTHORIZED': '0'}
        self.assertEqual(transport.agent_driver_environment(environment),
                         {name: environment[name] for name in ('PATH', 'HOME', 'PLAYWRIGHT_BROWSERS_PATH')})


class PreparedCredentialIntentTests(unittest.TestCase):
    def test_selector_matches_full_private_intent_before_latest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            expected = {'reference': 'fixture', 'transitionVersion': 'version1', 'fileDigest': DIGEST,
                        'installationId': 'host', 'instanceId': 'product', 'customerOwnerId': 'owner',
                        'expectedRevision': 2, 'actorId': 'admin', 'providerAddress': '93.184.215.14',
                        'provider': {'enabled': True, 'model': 'fixture-model', 'baseUrl': 'https://provider.example/v1'}}
            def stage(index, intent):
                digest = 'sha256:'+str(index)*64
                operation = root/'upgrade-operations'/digest[7:]
                operation.mkdir(parents=True)
                (operation/'request.json').write_text(json.dumps(dict(candidateRevision=REVISION,agentCredentialTransition=intent)))
                (operation/'detached-rehearsal.json').write_text(json.dumps(dict(phase='passed',identity=dict(candidate=IMAGE,predecessor=PREDECESSOR,artifactAdmissionDigest=digest))))
                return digest
            first = stage(1, expected)
            stage(2, dict(expected, fileDigest='sha256:'+'e'*64))
            stage(3, dict(expected, providerAddress='1.1.1.1'))
            none = stage(4, None)
            with patch.object(remote, 'PROVIDER', root), contextlib.redirect_stdout(io.StringIO()) as output:
                remote.prepared_request(IMAGE, REVISION, PREDECESSOR, expected)
                self.assertEqual(json.loads(output.getvalue())['preparationDigest'], first)
            with patch.object(remote, 'PROVIDER', root), contextlib.redirect_stdout(io.StringIO()) as output:
                remote.prepared_request(IMAGE, REVISION, PREDECESSOR, None)
                self.assertEqual(json.loads(output.getvalue())['preparationDigest'], none)
            with patch.object(remote, 'PROVIDER', root), self.assertRaises(ValueError):
                remote.prepared_request(IMAGE, REVISION, PREDECESSOR, dict(expected, transitionVersion='other'))


class FakeProcess:
    def __init__(self, markers):
        self.stdin = io.StringIO()
        self.stdout = io.StringIO(''.join(marker+' '+DIGEST+'\n' for marker in markers))
    def wait(self, timeout=None):
        return 0


class CredentialDriverCleanupTests(unittest.TestCase):
    def test_broken_stdin_does_not_skip_process_and_tunnel_shutdown(self):
        events = []
        class BrokenInput:
            def close(self):
                events.append('stdin-close')
                raise BrokenPipeError('fixture closed pipe')
        class Process:
            stdin = BrokenInput()
            stdout = io.StringIO()
            def wait(self, timeout=None):
                events.append('process-wait')
                return 0
        driver = object.__new__(transport.AgentCredentialDriver)
        driver.process = Process()
        driver.stack = contextlib.ExitStack()
        driver.stack.callback(lambda: events.append('tunnel-close'))
        driver.close()
        self.assertEqual(events, ['stdin-close', 'process-wait', 'tunnel-close'])
        self.assertIsNone(driver.process)

    def test_driver_cleanup_failure_still_cancels_and_waits_for_controller(self):
        process = FakeProcess(['AWAITING_RECOVERY_BROWSER_VALIDATION', 'AWAITING_REHEARSAL_AGENT_CREDENTIAL_TEST'])
        waits = []
        process.wait = lambda timeout=None: waits.append(timeout) or 0
        child_waits = []
        original_driver = transport.AgentCredentialDriver
        class Driver:
            def __init__(self, *args):
                self.driver = object.__new__(original_driver)
                self.driver.process = FakeProcess([])
                self.driver.process.wait = lambda timeout=None: child_waits.append(timeout) or 0
                self.driver.stack = contextlib.ExitStack()
                def failed_tunnel_cleanup():
                    raise RuntimeError('fixture tunnel cleanup failed')
                self.driver.stack.callback(failed_tunnel_cleanup)
            def test(self): raise RuntimeError('fixture Test failed')
            def close(self): self.driver.close()
        @contextlib.contextmanager
        def browser(*args): yield {}
        with patch.object(transport.subprocess, 'Popen', return_value=process), patch.object(transport.subprocess, 'run'), patch.object(transport, 'private_browser', browser), patch.object(transport, 'AgentCredentialDriver', Driver), contextlib.redirect_stdout(io.StringIO()), self.assertRaises(RuntimeError):
            transport.rollout(['ssh', 'host'], '/private/helper', '/private/request', 'upgrade', IMAGE, REVISION, {}, DIGEST,
                              dict(rehearsalBinding='127.0.0.1:8444', httpsBinding='127.0.0.1:8443'), agent_transition={'reference':'fixture'})
        self.assertTrue(process.stdin.closed)
        self.assertEqual(waits, [960])
        self.assertEqual(child_waits, [15])


class CredentialCheckpointTransportTests(unittest.TestCase):
    profile = dict(rehearsalBinding='127.0.0.1:8444', httpsBinding='127.0.0.1:8443')
    def execute(self, markers, intent):
        process = FakeProcess(markers)
        events = []
        class Driver:
            def __init__(self, *args): events.append('private-driver-start')
            def test(self): events.append('test-receipt')
            def save(self): events.append('save-receipt')
            def close(self): events.append('private-driver-close')
        @contextlib.contextmanager
        def browser(*args):
            yield {}
        with patch.object(transport.subprocess, 'Popen', return_value=process), patch.object(transport.subprocess, 'run'), patch.object(transport, 'private_browser', browser), patch.object(transport, 'AgentCredentialDriver', Driver), contextlib.redirect_stdout(io.StringIO()):
            result = transport.rollout(['ssh', 'host'], '/private/helper', '/private/request', 'upgrade', IMAGE, REVISION, {}, DIGEST, self.profile, agent_transition=intent)
        return result, events

    def test_all_exact_checkpoints_complete_before_browser_approval(self):
        markers = ['AWAITING_RECOVERY_BROWSER_VALIDATION', 'AWAITING_REHEARSAL_AGENT_CREDENTIAL_TEST',
                   'AWAITING_REHEARSAL_AGENT_CREDENTIAL_SAVE', 'AWAITING_REHEARSAL_BROWSER_VALIDATION',
                   'AWAITING_CANDIDATE_AGENT_CREDENTIAL_TEST', 'AWAITING_CANDIDATE_AGENT_CREDENTIAL_SAVE',
                   'AWAITING_CANDIDATE_BROWSER_VALIDATION', 'DEPLOYMENT_COMMITTED']
        result, events = self.execute(markers, {'reference': 'fixture'})
        self.assertEqual(result, 'DEPLOYMENT_COMMITTED')
        self.assertEqual(events, ['private-driver-start', 'test-receipt', 'save-receipt', 'private-driver-close']*2)

    def test_unchanged_request_cannot_accept_credential_marker(self):
        with self.assertRaisesRegex(RuntimeError, 'Unexpected agent credential checkpoint'):
            self.execute(['AWAITING_RECOVERY_BROWSER_VALIDATION', 'AWAITING_REHEARSAL_AGENT_CREDENTIAL_TEST'], None)

    def test_save_before_test_and_browser_before_save_fail_closed(self):
        for markers in [
            ['AWAITING_RECOVERY_BROWSER_VALIDATION', 'AWAITING_REHEARSAL_AGENT_CREDENTIAL_SAVE'],
            ['AWAITING_RECOVERY_BROWSER_VALIDATION', 'AWAITING_REHEARSAL_AGENT_CREDENTIAL_TEST', 'AWAITING_REHEARSAL_BROWSER_VALIDATION']]:
            with self.assertRaises(RuntimeError):
                self.execute(markers, {'reference': 'fixture'})

    def test_no_opt_in_retains_three_existing_browser_gates(self):
        result, events = self.execute(['AWAITING_RECOVERY_BROWSER_VALIDATION', 'AWAITING_REHEARSAL_BROWSER_VALIDATION', 'AWAITING_CANDIDATE_BROWSER_VALIDATION', 'DEPLOYMENT_COMMITTED'], None)
        self.assertEqual(result, 'DEPLOYMENT_COMMITTED')
        self.assertEqual(events, [])


if __name__ == '__main__':
    unittest.main()

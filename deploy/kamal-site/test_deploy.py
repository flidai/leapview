import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import deploy
import host
from test_contract import record


class RunnerTest(unittest.TestCase):
    def test_operator_status_needs_no_github_environment(self):
        with patch.dict(os.environ, {}, clear=True), patch('sys.argv', ['deploy.py', 'status']), \
                patch.object(deploy, 'connect'), patch.object(deploy, 'remote', return_value={}) as remote, \
                contextlib.redirect_stdout(io.StringIO()):
            deploy.main()
        remote.assert_called_once_with('status')

    def test_identical_version_verifies_without_pull_or_cleanup(self):
        r = record()
        def remote(op, **kwargs):
            if op == 'preflight': return {'state': {'active': r['version'], 'records': {r['version']: r}}}
            self.assertEqual(op, 'verify')
        with tempfile.TemporaryDirectory() as tmp, patch.object(deploy, 'remote', side_effect=remote), \
                patch.object(deploy, 'read_record', return_value=r), patch.object(deploy, 'prepare', return_value=r), \
                patch.object(deploy, 'ownership', return_value=contextlib.nullcontext()), patch.object(deploy, 'public_check'), \
                patch.object(deploy, 'run') as run, contextlib.redirect_stdout(io.StringIO()):
            deploy.deploy(Path(tmp), Path('evidence.json'))
            run.assert_not_called()

    def test_uncertain_acceptance_does_not_guess_a_rollback(self):
        previous = record()
        candidate = record(); candidate['image'] = candidate['image'].replace('a' * 64, 'e' * 64)
        candidate['version'] = 'k' + 'e' * 64
        calls = []
        def remote(op, **kwargs):
            calls.append(op)
            if op == 'preflight': return {'state': {'active': previous['version'], 'records': {previous['version']: previous}}}
            if op == 'accept': raise RuntimeError('SSH reply lost')
            if op == 'state': return {'active': candidate['version'], 'pending': None}
            return {}
        with tempfile.TemporaryDirectory() as tmp, patch.object(deploy, 'remote', side_effect=remote), \
                patch.object(deploy, 'read_record', return_value=candidate), patch.object(deploy, 'prepare', return_value=candidate), \
                patch.object(deploy, 'ownership', return_value=contextlib.nullcontext()), patch.object(deploy, 'public_check'), \
                patch.object(deploy, 'run'), patch.object(deploy, 'configure', return_value=Path('config')), \
                patch.object(deploy, 'manifest', return_value=({}, 'sha256:' + 'e' * 64)), \
                patch.object(deploy, 'kamal') as kamal:
            with self.assertRaisesRegex(RuntimeError, 'uncertain'): deploy.deploy(Path(tmp), Path('evidence.json'))
            self.assertEqual(kamal.call_count, 1)
            self.assertNotIn('restored', calls)
            self.assertNotIn('cleanup', calls)

    def test_host_blocks_unresolved_or_maintenance_state_before_mutation(self):
        for state in ({'pending': 'candidate'}, {'maintenance_pending': True}):
            with patch.object(host, 'command') as command:
                with self.assertRaises(ValueError): host.preflight({}, state)
                command.assert_not_called()

    def test_missing_handover_blocks_before_docker(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(host, 'ROOT', Path(tmp) / 'absent'), \
                patch.object(host, 'command') as command:
            with self.assertRaises(FileNotFoundError): host.load_ready()
            command.assert_not_called()

    def test_edited_prepared_record_cannot_deploy(self):
        r = record()
        edited = dict(r, release={'edited': True})
        with tempfile.TemporaryDirectory() as tmp, patch.object(deploy, 'read_record', return_value=edited), \
                patch.object(deploy, 'prepare', return_value=r), \
                patch.object(deploy, 'ownership', return_value=contextlib.nullcontext()), \
                patch.object(deploy, 'remote', return_value={'state': {'active': r['version'], 'records': {r['version']: r}}}):
            with self.assertRaisesRegex(ValueError, 'differs from verified active record'):
                deploy.deploy(Path(tmp), Path('record'))

    def test_trial_repository_rejected_before_tools(self):
        with patch.object(deploy, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'production repository'):
                deploy.prepare(Path('/unused'), 'ghcr.io/flidai/leapview-site-kamal-trial@sha256:' + 'a' * 64)
            run.assert_not_called()

    def test_rollback_never_inspects_broken_current(self):
        for broken in ('stopped', 'missing', 'unhealthy'):
            prior = dict(record(), verified=True)
            state = {'active': broken, 'prior': prior['version'], 'records': {prior['version']: prior}}
            with self.subTest(broken=broken), patch.object(host, 'scope'), \
                    patch.object(host, 'local_image', return_value={'Id': 'prior'}), \
                    patch.object(host, 'inspect', return_value={'Image': 'prior'}) as inspect, \
                    patch.object(host, 'validate_container') as validate:
                host.rollback_ready(state)
                inspect.assert_called_once_with('container', 'leapview-site-web-' + prior['version'])
                validate.assert_called_once_with(prior, {'Image': 'prior'}, require_running=False)

    def test_missing_prior_cannot_pull_or_mutate(self):
        prior = dict(record(), verified=True)
        state = {'active': 'broken', 'prior': prior['version'], 'records': {prior['version']: prior}}
        with patch.object(host, 'scope'), patch.object(host, 'local_image', return_value={'Id': 'prior'}), \
                patch.object(host, 'inspect', side_effect=RuntimeError('missing')), patch.object(host, 'command') as command:
            with self.assertRaisesRegex(ValueError, 'recovery container missing'):
                host.rollback_ready(state)
            command.assert_not_called()

    def test_saved_runtime_config_has_private_proxy_and_bounded_logs(self):
        with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ, {'SITE_SSH_CONFIG': '/tmp/pinned-ssh'}):
            config = json.loads(deploy.configure(Path(tmp), record()).read_text())
            self.assertEqual(config['registry'], {'server': 'localhost:5555'})
            self.assertFalse(config['proxy']['run']['publish'])
            self.assertEqual(config['logging']['options'], {'max-size': '10m', 'max-file': '3'})
            self.assertTrue(config['servers']['web']['options']['read-only'])
            self.assertIn(record()['image'], config['servers']['web']['cmd'])



class RecoveryFailureTest(unittest.TestCase):
    def test_failed_pull_never_starts_rollback_or_prune(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(deploy, 'configure'), \
                patch.object(deploy, 'remote', side_effect=RuntimeError('uncertain pull')) as remote, \
                patch.object(deploy, 'kamal') as kamal:
            with self.assertRaisesRegex(RuntimeError, 'uncertain pull'):
                deploy.transition(Path(tmp), record(), record(), pull=True)
            remote.assert_called_once_with('pull', version=record()['version'])
            kamal.assert_not_called()

    def test_interrupted_switch_keeps_attempt_pending_without_guessing(self):
        previous = record()
        candidate = record()
        candidate['image'] = candidate['image'].replace('a' * 64, 'e' * 64)
        candidate['version'] = 'k' + 'e' * 64
        calls = []

        def remote(operation, **values):
            calls.append((operation, values))
            return {}

        with tempfile.TemporaryDirectory() as tmp, patch.object(deploy, 'configure'), \
                patch.object(deploy, 'remote', side_effect=remote), \
                patch.object(deploy, 'kamal', side_effect=ConnectionError('switch reply lost')) as kamal:
            with self.assertRaisesRegex(ConnectionError, 'switch reply lost'):
                deploy.transition(Path(tmp), candidate, previous, pull=True)

        self.assertEqual([operation for operation, _ in calls], ['pull', 'image'])
        self.assertEqual(calls[0][1], {'version': candidate['version']})
        self.assertEqual(calls[1][1], {'version': candidate['version']})
        kamal.assert_called_once()

    def test_lost_acceptance_reply_does_not_rollback_or_prune(self):
        previous = record()
        candidate = record()
        candidate['image'] = candidate['image'].replace('a' * 64, 'e' * 64)
        candidate['version'] = 'k' + 'e' * 64
        calls = []

        def remote(operation, **values):
            calls.append(operation)
            if operation == 'accept': raise ConnectionError('accept response lost')
            return {}

        with tempfile.TemporaryDirectory() as tmp, patch.object(deploy, 'configure'), \
                patch.object(deploy, 'remote', side_effect=remote), patch.object(deploy, 'public_check'), \
                patch.object(deploy, 'kamal') as kamal:
            with self.assertRaisesRegex(ConnectionError, 'accept response lost'):
                deploy.transition(Path(tmp), candidate, previous, pull=True)

        self.assertEqual(calls, ['pull', 'image', 'verify', 'accept'])
        self.assertEqual(kamal.call_count, 1)

    def test_cleanup_failure_does_not_rollback_accepted_version(self):
        calls = []
        def remote(operation, **kwargs):
            calls.append(operation)
            if operation == 'cleanup': raise RuntimeError('cleanup failed')
        with tempfile.TemporaryDirectory() as tmp, patch.object(deploy, 'configure'), \
                patch.object(deploy, 'remote', side_effect=remote), patch.object(deploy, 'public_check'), \
                patch.object(deploy, 'kamal') as kamal:
            with self.assertRaisesRegex(RuntimeError, 'cleanup failed'):
                deploy.transition(Path(tmp), record(), record(), pull=True)
            self.assertIn('accept', calls)
            self.assertNotIn('restored', calls)
            self.assertEqual(kamal.call_args.args[1:3], ('app', 'boot'))
            self.assertEqual(kamal.call_count, 1)


if __name__ == '__main__': unittest.main()

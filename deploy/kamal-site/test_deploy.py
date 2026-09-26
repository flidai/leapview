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
    def test_disabled_mode_never_connects(self):
        for mode in ('', 'paused', 'invalid'):
            with self.subTest(mode=mode), patch.dict(os.environ, {'LEAPVIEW_SITE_DEPLOYMENT_MODE': mode}), \
                    patch('sys.argv', ['deploy.py', 'deploy']), patch.object(deploy, 'connect') as connect:
                with self.assertRaises(SystemExit): deploy.main()
                connect.assert_not_called()

    def test_identical_version_verifies_without_pull_or_cleanup(self):
        r = record()
        def remote(op, **kwargs):
            if op == 'preflight': return {'state': {'active': r['version'], 'records': {r['version']: r}}}
            self.assertEqual(op, 'verify')
        with tempfile.TemporaryDirectory() as tmp, patch.object(deploy, 'remote', side_effect=remote), \
                patch.object(deploy, 'admitted_record', return_value=r), patch.object(deploy, 'public_check'), \
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
                patch.object(deploy, 'admitted_record', return_value=candidate), patch.object(deploy, 'public_check'), \
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

    def test_saved_runtime_config_has_private_proxy_and_bounded_logs(self):
        with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ, {'SITE_SSH_CONFIG': '/tmp/pinned-ssh'}):
            config = json.loads(deploy.configure(Path(tmp), record()).read_text())
            self.assertFalse(config['proxy']['run']['publish'])
            self.assertEqual(config['logging']['options'], {'max-size': '10m', 'max-file': '3'})
            self.assertTrue(config['servers']['web']['options']['read-only'])
            self.assertIn(record()['image'], config['servers']['web']['cmd'])


if __name__ == '__main__': unittest.main()

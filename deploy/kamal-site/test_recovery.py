import contextlib
from pathlib import Path
import unittest
from unittest.mock import patch

import deploy
import host
from test_contract import record


class InterruptedRecoveryTest(unittest.TestCase):
    def state(self):
        active = dict(record(), verified=True)
        return {'active': active['version'], 'pending': 'candidate',
                'records': {active['version']: active}}

    def test_recovery_requires_pending_and_verified_active(self):
        for updates in ({'pending': None}, {'pending': self.state()['active']}):
            state = dict(self.state(), **updates)
            with patch.object(host, 'command') as command:
                with self.assertRaises(ValueError): host.recovery_ready(state)
                command.assert_not_called()
        state = self.state()
        state['records'][state['active']]['verified'] = False
        with patch.object(host, 'command') as command:
            with self.assertRaises(ValueError): host.recovery_ready(state)
            command.assert_not_called()

    def test_recovery_validates_saved_image_and_stopped_container(self):
        state = self.state()
        with patch.object(host, 'scope'), patch.object(host, 'local_image', return_value={'Id': 'saved'}), \
                patch.object(host, 'inspect', return_value={'Image': 'different'}), \
                patch.object(host, 'validate_container') as validate:
            with self.assertRaisesRegex(ValueError, 'disagree'): host.recovery_ready(state)
            self.assertFalse(validate.call_args.kwargs['require_running'])

    def test_recovery_restores_saved_active_without_pull_or_admission(self):
        state = self.state()
        calls = []
        def remote(op, **kwargs):
            calls.append(op)
            return state if op == 'recovery-begin' else {}
        with patch.object(deploy, 'remote', side_effect=remote), \
                patch.object(deploy, 'configure', return_value=Path('config')), \
                patch.object(deploy, 'kamal') as kamal, patch.object(deploy, 'public_check'), \
                patch.object(deploy, 'prepare') as prepare:
            deploy.recover(Path('/unused'))
        prepare.assert_not_called()
        self.assertEqual(calls, ['recovery-begin', 'verify', 'restored', 'preserve-prior', 'cleanup', 'maintained'])
        self.assertEqual(kamal.call_args_list[0].args, (Path('config'), 'rollback', state['active']))

    def test_failed_public_check_keeps_pending_and_skips_cleanup(self):
        state = self.state()
        calls = []
        def remote(op, **kwargs):
            calls.append(op)
            return state if op == 'recovery-begin' else {}
        with patch.object(deploy, 'remote', side_effect=remote), patch.object(deploy, 'configure'), \
                patch.object(deploy, 'kamal'), patch.object(deploy, 'public_check', side_effect=ValueError('bad public response')):
            with self.assertRaisesRegex(ValueError, 'bad public'): deploy.recover(Path('/unused'))
        self.assertEqual(calls, ['recovery-begin', 'verify'])


if __name__ == '__main__': unittest.main()

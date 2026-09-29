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
                'records': {active['version']: active, 'candidate': dict(record(), version='candidate')}}

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
        with patch.object(host, 'command', return_value='[]'), patch.object(host, 'scope'), patch.object(host, 'local_image', return_value={'Id': 'saved'}), \
                patch.object(host, 'inspect', return_value={'Image': 'different'}), \
                patch.object(host, 'validate_container') as validate:
            with self.assertRaisesRegex(ValueError, 'disagree'): host.recovery_ready(state)
            self.assertFalse(validate.call_args.kwargs['require_running'])

    def test_completed_pull_is_reconciled_before_scope_validation(self):
        state = self.state()
        candidate = dict(record(), version='candidate')
        state['records']['candidate'] = candidate
        def scope(records):
            self.assertEqual(records['candidate']['local_id'], 'pulled')
        with patch.object(host, 'command', side_effect=['[{"Id":"pulled"}]', '[]']), \
                patch.object(host, 'local_image', side_effect=lambda r: {'Id': 'pulled' if r is candidate else 'saved'}), \
                patch.object(host, 'scope', side_effect=scope), \
                patch.object(host, 'inspect', return_value={'Image': 'saved'}), patch.object(host, 'validate_container'):
            host.recovery_ready(state)

    def test_pending_canonical_pull_is_recovered_without_network(self):
        state = self.state()
        candidate = state['records']['candidate']
        image = {'Id': 'pulled', 'Descriptor': {'digest': candidate['platform']}}
        calls = []
        local = host.LOCAL_REPOSITORY + ':' + candidate['version']
        def command(*args, **kwargs):
            calls.append(args)
            if args == ('docker', 'image', 'inspect', local): return '[]'
            if args == ('docker', 'image', 'inspect', candidate['image']): return __import__('json').dumps([image])
            if '--platform' in args: return __import__('json').dumps([image])
            return ''
        with patch.object(host, 'command', side_effect=command), patch.object(host, 'validate_image'), \
                patch.object(host, 'local_image', return_value=image):
            host.reconcile_pending(state)
        self.assertEqual(candidate['local_id'], 'pulled')
        self.assertIn(('docker', 'tag', candidate['image'], local), calls)
        self.assertIn(('docker', 'image', 'rm', candidate['image']), calls)
        self.assertFalse(any('pull' in call for call in calls))

    def test_pending_canonical_wrong_platform_never_tags_or_removes(self):
        state = self.state()
        calls = []
        def command(*args, **kwargs):
            calls.append(args)
            if len(calls) == 1: return '[]'
            return '[{"Id":"pulled","Descriptor":{"digest":"wrong"}}]'
        with patch.object(host, 'command', side_effect=command), patch.object(host, 'validate_image'):
            with self.assertRaisesRegex(ValueError, 'platform'): host.reconcile_pending(state)
        self.assertFalse(any('tag' in call or 'rm' in call for call in calls))

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

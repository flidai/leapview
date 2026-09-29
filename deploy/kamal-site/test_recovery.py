import contextlib
import io
import json
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

    def container(self, version, image, *, cid=None, name=None, running=False, role='web', label_version=None):
        base_record = record()
        labels = {'service': host.SERVICE, 'role': role, 'destination': ''}
        if label_version is not None: labels['version'] = label_version
        return {'Id': cid or version, 'Name': '/' + (name or host.SERVICE + '-web-' + version),
                'Image': image, 'Created': '2026-01-01T00:00:00Z',
                'State': {'Running': running}, 'ImageManifestDescriptor': {'digest': base_record['platform']},
                'Config': {'Labels': labels},
                'HostConfig': {}}

    def stale_state(self):
        state = self.state()
        active = state['active']
        state['prior'] = 'prior'
        state['records'][active]['local_id'] = 'active-image'
        state['records']['candidate']['local_id'] = 'candidate-image'
        state['records']['prior'] = dict(record(), version='prior', local_id='prior-image', verified=True)
        return state

    def test_stale_ready_checks_every_service_container_and_native_selection(self):
        state = self.stale_state()
        active = state['active']
        containers = {
            'active-id': self.container(active, 'active-image', cid='active-id', running=True),
            'candidate-id': self.container('candidate', 'candidate-image', cid='candidate-id', running=True,
                                           name=host.SERVICE + '-web-candidate_replacement'),
            'prior-id': self.container('prior', 'prior-image', cid='prior-id'),
        }
        active_container = containers['active-id']
        calls = []
        inspected = []
        def command(*args, **kwargs):
            calls.append(args)
            if args[:3] == ('docker', 'ps', '-aq'): return 'active-id candidate-id prior-id'
            if args[:3] == ('docker', 'ps', '--latest'): return 'active-id'
            return ''
        def inspect(kind, name):
            inspected.append((kind, name))
            if kind == 'image': return {'Id': 'active-image'}
            return containers[name]
        with patch.object(host, 'command', side_effect=command), patch.object(host, 'scope'), \
                patch.object(host, 'running', return_value=active_container), \
                patch.object(host, 'local_image', return_value={'Id': 'active-image'}), \
                patch.object(host, 'inspect', side_effect=inspect), \
                patch.object(host, 'validate_container') as validate:
            host.stale_ready(state, active)
        self.assertEqual(validate.call_count, 3)
        self.assertTrue(all(call.kwargs == {'require_running': False} for call in validate.call_args_list))
        self.assertIn(('image', host.LOCAL_REPOSITORY + ':latest'), inspected)
        native = next(call for call in calls if call[:3] == ('docker', 'ps', '--latest'))
        self.assertIn('--no-trunc', native)
        self.assertIn('status=running', native)
        self.assertIn('status=restarting', native)
        self.assertIn('label=service=' + host.SERVICE, native)
        self.assertIn('label=destination=', native)
        self.assertIn('label=role=web', native)
        self.assertIn('ancestor=active-image', native)

    def test_stale_ready_rejects_unrecorded_foreign_and_malformed_containers(self):
        cases = (
            ('unrecorded', self.container('ghost', 'foreign-image', cid='ghost-id')),
            ('foreign owner', dict(self.container('candidate', 'candidate-image', cid='candidate-id'),
                                   Config={'Labels': {'service': 'foreign', 'role': 'web', 'version': 'candidate'}})),
            ('wrong role', self.container('candidate', 'candidate-image', cid='candidate-id', role='worker')),
            ('wrong version label', self.container('candidate', 'candidate-image', cid='candidate-id', label_version='other')),
            ('wrong destination', dict(self.container('candidate', 'candidate-image', cid='candidate-id'),
                                       Config={'Labels': {'service': host.SERVICE, 'role': 'web',
                                                          'destination': 'staging', 'version': 'candidate'}})),
            ('missing native destination label', dict(self.container('candidate', 'candidate-image', cid='candidate-id', running=True),
                                                       Config={'Labels': {'service': host.SERVICE, 'role': 'web'}})),
            ('wrong runtime', self.container('candidate', 'candidate-image', cid='candidate-id')),
        )
        for label, candidate in cases:
            with self.subTest(label=label):
                state = self.stale_state()
                active = state['active']
                active_container = self.container(active, 'active-image', cid='active-id', running=True)
                containers = {'active-id': active_container, 'candidate-id': candidate}
                def command(*args, **kwargs):
                    if args[:3] == ('docker', 'ps', '-aq'): return 'active-id candidate-id'
                    if args[:3] == ('docker', 'ps', '--latest'): return 'active-id'
                    return ''
                def inspect(kind, name):
                    if kind == 'image': return {'Id': 'active-image'}
                    return containers[name]
                def validate(record, container, *, require_running=True):
                    if label == 'wrong runtime' and container is candidate:
                        raise ValueError('runtime differs')
                with patch.object(host, 'command', side_effect=command), patch.object(host, 'scope'), \
                        patch.object(host, 'running', return_value=active_container), \
                        patch.object(host, 'local_image', return_value={'Id': 'active-image'}), \
                        patch.object(host, 'inspect', side_effect=inspect), \
                        patch.object(host, 'validate_container', side_effect=validate):
                    with self.assertRaises(ValueError): host.stale_ready(state, active)

    def test_stale_ready_rejects_nonactive_version_or_wrong_latest_selection(self):
        state = self.stale_state()
        active = state['active']
        active_container = self.container(active, 'active-image', cid='active-id', running=True)
        containers = {'active-id': active_container, 'candidate-id': self.container('candidate', 'candidate-image', cid='candidate-id', running=True)}
        for requested, latest_image, native_id in ((active, 'candidate-image', 'active-id'),
                                                    (active, 'active-image', 'candidate-id'),
                                                    ('candidate', 'active-image', 'active-id')):
            def command(*args, **kwargs):
                if args[:3] == ('docker', 'ps', '-aq'): return 'active-id candidate-id'
                if args[:3] == ('docker', 'ps', '--latest'): return native_id
                return ''
            def inspect(kind, name):
                if kind == 'image': return {'Id': latest_image}
                return containers[name]
            with patch.object(host, 'command', side_effect=command), patch.object(host, 'scope'), \
                    patch.object(host, 'running', return_value=active_container), \
                    patch.object(host, 'local_image', return_value={'Id': 'active-image'}), \
                    patch.object(host, 'inspect', side_effect=inspect), patch.object(host, 'validate_container'):
                with self.assertRaises(ValueError): host.stale_ready(state, requested)

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
        self.assertEqual(calls, ['recovery-begin', 'verify', 'stale-ready', 'verify', 'restored', 'preserve-prior', 'cleanup', 'maintained'])
        self.assertEqual(kamal.call_args_list[0].args, (Path('config'), 'rollback', state['active']))
        self.assertEqual(kamal.call_args_list[1].args, (Path('config'), 'app', 'stale_containers', '--stop'))

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

    def test_native_stale_stop_failure_leaves_pending_and_skips_cleanup(self):
        state = self.state()
        calls = []
        def remote(op, **kwargs):
            calls.append(op)
            return state if op == 'recovery-begin' else {}
        with patch.object(deploy, 'remote', side_effect=remote), patch.object(deploy, 'configure'), \
                patch.object(deploy, 'public_check'), \
                patch.object(deploy, 'kamal', side_effect=[None, RuntimeError('stale stop failed')]) as kamal:
            with self.assertRaisesRegex(RuntimeError, 'stale stop failed'): deploy.recover(Path('/unused'))
        self.assertEqual(calls, ['recovery-begin', 'verify', 'stale-ready'])
        self.assertNotIn('restored', calls)
        self.assertNotIn('cleanup', calls)
        self.assertEqual(kamal.call_args_list[1].args[1:], ('app', 'stale_containers', '--stop'))

    def test_restored_does_not_clear_pending_while_a_live_extra_remains(self):
        state = self.stale_state()
        active = state['active']
        containers = [self.container(active, 'active-image', cid='active-id', running=True),
                      self.container('candidate', 'candidate-image', cid='candidate-id', running=True),
                      self.container('prior', 'prior-image', cid='prior-id')]
        with patch.object(host, 'load_ready', return_value=({}, state)), \
                patch.object(host, 'running'), patch.object(host, 'scope'), \
                patch.object(host, 'service_containers', return_value=containers), \
                patch.object(host, 'save') as save, \
                patch('sys.stdin', io.StringIO(json.dumps({'operation': 'restored', 'version': active}))):
            with self.assertRaisesRegex(ValueError, 'unexpected live version'):
                host.main()
        self.assertEqual(state['pending'], 'candidate')
        self.assertFalse(state.get('maintenance_pending'))
        save.assert_not_called()

    def test_automatic_restore_uses_stale_stop_before_clearing_pending(self):
        previous = record()
        candidate = record()
        candidate['image'] = candidate['image'].replace('a' * 64, 'e' * 64)
        candidate['version'] = 'k' + 'e' * 64
        calls = []
        def remote(op, **kwargs):
            calls.append(op)
            if op == 'accept': raise RuntimeError('candidate acceptance failed')
            if op == 'state': return {'active': previous['version'], 'pending': candidate['version']}
            return {}
        with patch.object(deploy, 'configure', return_value=Path('config')), \
                patch.object(deploy, 'remote', side_effect=remote), \
                patch.object(deploy, 'kamal') as kamal, patch.object(deploy, 'public_check'):
            with self.assertRaisesRegex(RuntimeError, 'candidate acceptance failed'):
                deploy.transition(Path('/unused'), candidate, previous, pull=True)
        self.assertLess(calls.index('stale-ready'), calls.index('restored'))
        self.assertLess(calls.index('restored'), calls.index('cleanup'))
        self.assertEqual(kamal.call_args_list[-3].args[1:], ('rollback', previous['version']))
        self.assertEqual(kamal.call_args_list[-2].args[1:], ('app', 'stale_containers', '--stop'))


if __name__ == '__main__': unittest.main()

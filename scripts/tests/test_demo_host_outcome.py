"""Read-only host outcome reconciliation tests; SSH and source gates are mocked."""
import importlib.util
import json
import os
import pathlib
import stat
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts' / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


host_outcome = load('demo_host_outcome')


class HostOutcomeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.output = self.root / 'demo-host-outcome.json'
        self.binding = {
            'version': 1, 'operation': 'upgrade', 'operationId': 'sha256:' + 'e' * 64,
            'candidate': {'image': 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64, 'revision': 'c' * 40},
            'predecessor': {'image': 'ghcr.io/flidai/leapview@sha256:' + 'b' * 64, 'revision': 'd' * 40},
            'workflowRunId': '777', 'workflowAttempt': '1',
        }
        self.public_calls = []

    def snapshot(self, result='committed'):
        candidate, predecessor = self.binding['candidate'], self.binding['predecessor']
        recovered = result == 'recovered'
        observed = predecessor if recovered else candidate
        phase = 'recovered' if recovered else 'succeeded'
        return {
            'version': 1, 'operation': self.binding['operation'], 'operationId': self.binding['operationId'],
            'journalState': phase,
            'journal': {'phase': phase, 'identity': {
                'artifactAdmissionDigest': self.binding['operationId'], 'candidate': candidate['image'],
                'predecessor': predecessor['image'], 'target': 'app-leapview-demo-02'}},
            'request': {'candidateImage': candidate['image'], 'candidateRevision': candidate['revision'],
                        'predecessorImage': predecessor['image'], 'predecessorRevision': predecessor['revision'],
                        'currentSchema': 28, 'candidateSchema': 30, 'profileID': 'app-leapview-demo-02'},
            'runtime': {**observed, 'schema': 28 if recovered else 30, 'containerImageID': 'sha256:'+'9'*64,
                        'repositoryDigests': [observed['image']], 'descriptorImage': observed['image'],
                        'markerImage': observed['image'], 'receiptImage': observed['image'],
                        'receiptRevision': observed['revision'], 'receiptPreviousImage': predecessor['image']},
        }

    def collect(self, snapshot=None, verify=None):
        snapshot = snapshot or self.snapshot()
        verify = verify or (lambda revision: self.public_calls.append(revision))
        with patch.object(host_outcome.subprocess, 'check_output', return_value=json.dumps(snapshot).encode()) as ssh:
            value = host_outcome.collect(
                ['ssh', 'root@demo'], '/run/inspector.py', self.binding, '12345', verify,
                lambda rev: 30 if rev == self.binding['candidate']['revision'] else 28,
                output_path=self.output)
        self.assertIn(' outcome ', ssh.call_args.args[0][-1])
        self.assertEqual(value, json.loads(self.output.read_text()))
        return value

    def test_candidate_commit_checks_host_identity_schema_and_public_revision(self):
        result = self.collect()
        self.assertEqual(result['result'], 'committed')
        self.assertTrue(result['hostJournalVerified'])
        self.assertTrue(result['hostRuntimeVerified'])
        self.assertTrue(result['containerImageVerified'])
        self.assertTrue(result['publicIdentityVerified'])
        self.assertTrue(result['schemaVerified'])
        self.assertEqual(self.public_calls, [self.binding['candidate']['revision']])
        self.assertEqual(stat.S_IMODE(self.output.stat().st_mode), 0o600)

    def test_recovered_snapshot_matches_predecessor_after_restore(self):
        result = self.collect(self.snapshot('recovered'))
        self.assertEqual(result['result'], 'recovered')
        self.assertEqual(result['observed'], self.binding['predecessor'])
        self.assertEqual(self.public_calls, [self.binding['predecessor']['revision']])

    def test_mismatched_request_digest_image_schema_or_phase_is_unresolved(self):
        cases = []
        bad = self.snapshot(); bad['journal']['identity']['artifactAdmissionDigest'] = 'sha256:'+'f'*64; cases.append(bad)
        bad = self.snapshot(); bad['request']['candidateImage'] = self.binding['predecessor']['image']; cases.append(bad)
        bad = self.snapshot(); bad['runtime']['repositoryDigests'] = []; cases.append(bad)
        bad = self.snapshot(); bad['request']['candidateSchema'] = 29; cases.append(bad)
        bad = self.snapshot(); bad['journalState'] = bad['journal']['phase'] = 'migrating'; cases.append(bad)
        for snapshot in cases:
            with self.subTest(snapshot=snapshot):
                self.public_calls.clear()
                self.assertEqual(self.collect(snapshot)['result'], 'unresolved')
                self.assertEqual(self.public_calls, [])

    def test_public_and_source_checks_fail_closed_and_ssh_error_writes_receipt(self):
        def reject(_revision): raise RuntimeError('private callback detail')
        result = self.collect(verify=reject)
        self.assertEqual(result['result'], 'unresolved')
        self.assertNotIn('private callback detail', self.output.read_text())
        with patch.object(host_outcome.subprocess, 'check_output', side_effect=OSError('secret transport detail')):
            result = host_outcome.collect(['ssh', 'root@demo'], '/run/inspector.py', self.binding, '12345',
                lambda _revision: None, lambda _revision: 30, output_path=self.output)
        self.assertEqual(result['result'], 'unresolved')
        self.assertNotIn('secret transport detail', self.output.read_text())

    def test_image_only_commit_and_noop_are_source_schema_bound(self):
        self.binding['operation'] = 'deploy'
        snapshot = self.snapshot(); snapshot.update(operation='deploy', journalState=None, journal=None, request=None)
        with patch.object(host_outcome.subprocess, 'check_output', return_value=json.dumps(snapshot).encode()):
            result = host_outcome.collect(['ssh','root@demo'], '/run/inspector.py', self.binding, '12345',
                lambda _revision: None, lambda _revision: 30, output_path=self.output)
        self.assertEqual(result['result'], 'committed')
        self.assertEqual(result['journalState'], 'image-only-committed')
        self.binding['predecessor'] = dict(self.binding['candidate'])
        snapshot = self.snapshot(); snapshot.update(operation='deploy', journalState=None, journal=None, request=None)
        snapshot['runtime']['receiptPreviousImage'] = 'ghcr.io/flidai/leapview@sha256:'+'f'*64
        with patch.object(host_outcome.subprocess, 'check_output', return_value=json.dumps(snapshot).encode()):
            result = host_outcome.collect(['ssh','root@demo'], '/run/inspector.py', self.binding, '12346',
                lambda _revision: None, lambda _revision: 30, output_path=self.output)
        self.assertEqual(result['result'], 'committed')

    def test_reconcile_only_recovers_after_late_public_failure_without_host_rollout(self):
        import demo_runtime_record as record
        deploy = load('demo_compose_deploy')
        self.binding['operation'] = 'deploy'
        snapshot = self.snapshot(); snapshot.update(operation='deploy', journalState=None, journal=None, request=None)
        with patch.object(host_outcome.subprocess, 'check_output', return_value=json.dumps(snapshot).encode()):
            first = host_outcome.collect(['ssh','root@demo'], '/run/inspector.py', self.binding, '12345',
                lambda _revision: (_ for _ in ()).throw(RuntimeError('late public failure')),
                lambda _revision: 30, output_path=self.output)
        self.assertEqual(first['result'], 'unresolved')
        deployment = {'id':12345, 'sha':self.binding['candidate']['revision'], 'task':record.TASK,
                      'environment':record.ENVIRONMENT, 'payload':record._record_payload(self.binding)}
        statuses = [{'state':'in_progress'}]
        def api(path, body=None):
            if path == 'deployments/12345': return deployment
            if path == 'deployments/12345/statuses?per_page=100': return list(statuses)
            if path == 'deployments/12345/statuses': statuses.insert(0, dict(body)); return statuses[0]
            raise AssertionError(path)
        env = {'DEMO_ACTION':'reconcile', 'DEPLOYMENT_ID':'12345', 'DEMO_HOST':deploy.HOST,
               'DEMO_PERMISSION_PROFILE':'leapview.permissions/v1', 'DEMO_PROJECT_ID':'test',
               'DEMO_PUBLISHER_CLIENT_ID':'id', 'DEMO_PUBLISHER_CLIENT_SECRET':'secret',
               'DEMO_SSH_PRIVATE_KEY':'test-only', 'RUNNER_TEMP':str(self.root),
               'GITHUB_OUTPUT':str(self.root/'github-output')}
        pathlib.Path(env['GITHUB_OUTPUT']).write_text('')
        commands, cleanup = [], []
        subprocess_module = deploy.subprocess
        def check_output(args, **_kwargs):
            if args[0] == 'ssh-keyscan': return b'test host key'
            if args[0] == 'ssh-keygen': return '256 '+deploy.FINGERPRINT+' demo\n'
            commands.append(args[-1])
            if ' outcome deploy ' not in args[-1]: raise AssertionError('not read-only')
            return json.dumps(snapshot).encode()
        def run(args, **_kwargs): cleanup.append(args)
        with patch.dict(os.environ, env, clear=True), \
             patch.dict(sys.modules, {'demo_runtime_record':record, 'demo_host_outcome':host_outcome}), \
             patch.object(record, 'api', side_effect=api), \
             patch.object(record, 'read_contract', return_value={'permissionProfile':'leapview.permissions/v1'}), \
             patch.object(deploy, 'read_contract', return_value={'permissionProfile':'leapview.permissions/v1'}), \
             patch.object(deploy, 'source_schema', return_value=(30,{})), \
             patch.object(deploy, 'verify_public_revision') as public, \
             patch.object(subprocess_module, 'check_output', side_effect=check_output), \
             patch.object(subprocess_module, 'run', side_effect=run), \
             patch.object(subprocess_module, 'Popen') as popen, \
             patch.object(sys, 'argv', ['demo_compose_deploy.py']):
            deploy.main()
            self.assertEqual(json.loads(self.output.read_text())['result'], 'committed',
                             json.loads(self.output.read_text()))
            record.reconcile()
            popen.assert_not_called()
        self.assertEqual(len(commands), 1)
        self.assertEqual(len(cleanup), 2)
        self.assertIn('cat > ', cleanup[0][-1])
        self.assertEqual(cleanup[1][-4:-3], ['-f'])
        public.assert_called_once_with(self.binding['candidate']['revision'], 'leapview.permissions/v1')
        self.assertEqual(statuses[0]['state'], 'success')
        self.assertEqual(json.loads(self.output.read_text())['result'], 'committed')
        self.assertIn('running_revision='+self.binding['candidate']['revision'], pathlib.Path(env['GITHUB_OUTPUT']).read_text())

    def test_terminal_upgrade_reconcile_clears_bound_credentials_without_rollout(self):
        import demo_runtime_record as record
        deploy = load('demo_compose_deploy')
        deployment = {'id': 12345, 'sha': self.binding['candidate']['revision'],
                      'task': record.TASK, 'environment': record.ENVIRONMENT,
                      'payload': record._record_payload(self.binding)}
        statuses = [{'state': 'in_progress'}]

        def api(path, body=None):
            if path == 'deployments/12345': return deployment
            if path == 'deployments/12345/statuses?per_page=100': return list(statuses)
            if path == 'deployments/12345/statuses':
                statuses.insert(0, dict(body)); return statuses[0]
            raise AssertionError(path)

        env = {'DEMO_ACTION':'reconcile', 'DEPLOYMENT_ID':'12345', 'DEMO_HOST':deploy.HOST,
               'DEMO_PROJECT_ID':'test', 'DEMO_PUBLISHER_CLIENT_ID':'id',
               'DEMO_PUBLISHER_CLIENT_SECRET':'secret', 'DEMO_SSH_PRIVATE_KEY':'test-only',
               'RUNNER_TEMP':str(self.root), 'GITHUB_OUTPUT':str(self.root/'github-output')}
        pathlib.Path(env['GITHUB_OUTPUT']).write_text('')
        snapshot = self.snapshot()
        commands, runs = [], []
        def check_output(args, **_kwargs):
            if args[0] == 'ssh-keyscan': return b'test host key'
            if args[0] == 'ssh-keygen': return '256 '+deploy.FINGERPRINT+' demo\n'
            commands.append(args[-1])
            if ' outcome upgrade ' not in args[-1]: raise AssertionError('not read-only outcome inspection')
            return json.dumps(snapshot).encode()
        def run(args, **_kwargs): runs.append(args)

        with patch.dict(os.environ, env, clear=True), \
             patch.dict(sys.modules, {'demo_runtime_record':record, 'demo_host_outcome':host_outcome}), \
             patch.object(record, 'api', side_effect=api), \
             patch.object(record, 'read_contract', return_value={'permissionProfile':'leapview.permissions/v1'}), \
             patch.object(deploy, 'read_contract', return_value={'permissionProfile':'leapview.permissions/v1'}), \
             patch.object(deploy, 'source_schema', side_effect=lambda rev: (30 if rev == self.binding['candidate']['revision'] else 28, {})), \
             patch.object(deploy, 'verify_public_revision') as public, \
             patch.object(deploy.subprocess, 'check_output', side_effect=check_output), \
             patch.object(deploy.subprocess, 'run', side_effect=run), \
             patch.object(deploy.subprocess, 'Popen') as popen, \
             patch.object(sys, 'argv', ['demo_compose_deploy.py']):
            deploy.main()
            self.assertEqual(json.loads(self.output.read_text())['result'], 'committed')
            self.assertTrue(json.loads(self.output.read_text())['hostJournalVerified'])
            record.reconcile()
            popen.assert_not_called()

        self.assertEqual(len(commands), 1)
        cleanup = [args for args in runs if 'clear-credentials' in args]
        self.assertEqual(len(cleanup), 1)
        self.assertEqual(cleanup[0][-1], self.binding['operationId'])
        self.assertEqual(statuses[0]['state'], 'success')
        self.assertEqual(public.call_count, 1)


if __name__ == '__main__': unittest.main()

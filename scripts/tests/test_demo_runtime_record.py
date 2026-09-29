"""Verified runtime record outcomes (GitHub API is always mocked)."""
import importlib.util
import json
import os
import pathlib
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
spec = importlib.util.spec_from_file_location(
    'demo_runtime_record', ROOT / 'scripts' / 'demo_runtime_record.py')
record_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(record_module)


class FakeGitHub:
    def __init__(self):
        self.deployments = []
        self.statuses = {}
        self.next_id = 100
        self.drop_status_response = False
        self.drop_deployment_response = False

    def api(self, path, body=None):
        if path.startswith('deployments?'):
            records = list(reversed(self.deployments))
            if path.endswith('per_page=1'):
                records = records[:1]
            return records
        if path == 'deployments':
            self.next_id += 1
            record = {
                'id': self.next_id,
                'sha': body['ref'],
                'task': body['task'],
                'environment': body['environment'],
                'payload': body['payload'],
            }
            self.deployments.append(record)
            if self.drop_deployment_response:
                self.drop_deployment_response = False
                raise RuntimeError('simulated lost deployment-create response')
            return record
        if path.startswith('deployments/'):
            parts = path.split('/')
            deployment_id = int(parts[1])
            deployment = next(
                item for item in self.deployments if item['id'] == deployment_id)
            if len(parts) == 2:
                return deployment
            if parts[2].startswith('statuses?'):
                return self.statuses.get(deployment_id, [])
            if parts[2] == 'statuses':
                status = dict(body)
                self.statuses.setdefault(deployment_id, []).insert(0, status)
                if self.drop_status_response:
                    self.drop_status_response = False
                    raise RuntimeError('simulated lost status response')
                return status
        raise AssertionError(f'unexpected GitHub API call: {path}')


class RuntimeOutcomeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.output = self.root / 'github-output'
        self.output.write_text('')
        self.binding_path = self.root / record_module.BINDING_FILENAME
        self.binding = {
            'version': 1,
            'operation': 'upgrade',
            'operationId': 'sha256:' + 'e' * 64,
            'candidate': {
                'image': 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64,
                'revision': 'c' * 40,
            },
            'predecessor': {
                'image': 'ghcr.io/flidai/leapview@sha256:' + 'b' * 64,
                'revision': 'd' * 40,
            },
            'workflowRunId': '777',
            'workflowAttempt': '1',
            'qualificationRunId': '888',
            'qualificationAttempt': '2',
            'permissionProfile': 'leapview.permissions/v1',
        }
        self.binding_path.write_text(json.dumps(self.binding))
        (self.root/'demo-qualification.json').write_text(json.dumps({
            'runId':self.binding['qualificationRunId'],
            'runAttempt':self.binding['qualificationAttempt'],
            'image':self.binding['candidate']['image'],
            'revision':self.binding['candidate']['revision'],
            'qualified':True,
        }))
        self.outcome_path = self.root / record_module.OUTCOME_FILENAME
        self.env = {
            'RUNNER_TEMP': str(self.root),
            'GITHUB_OUTPUT': str(self.output),
            'GITHUB_RUN_ID': '777',
            'SOURCE_REVISION': self.binding['candidate']['revision'],
            'DEMO_IMAGE': self.binding['candidate']['image'],
            'QUALIFICATION_RUN': self.binding['qualificationRunId'],
        }
        self.github = FakeGitHub()
        self.patch_api = patch.object(record_module, 'api', side_effect=self.github.api)
        self.patch_api.start()
        self.addCleanup(self.patch_api.stop)
        self.patch_contract = patch.object(record_module, 'read_contract', return_value={
            'permissionProfile': 'leapview.permissions/v1'})
        self.patch_contract.start()
        self.addCleanup(self.patch_contract.stop)
        with patch.dict('os.environ', self.env, clear=True):
            record_module.start()
        self.candidate_deployment_id = self.github.deployments[0]['id']

    def outcome(self, result='committed', **changes):
        observed = (self.binding['candidate'] if result == 'committed'
                    else self.binding['predecessor'] if result == 'recovered'
                    else None)
        value = {
            'version': 1,
            'deploymentId': str(self.candidate_deployment_id),
            'operation': self.binding['operation'],
            'operationId': self.binding['operationId'],
            'result': result,
            'candidate': self.binding['candidate'],
            'predecessor': self.binding['predecessor'],
            'observed': observed,
            'journalState': ('succeeded' if result == 'committed'
                             else 'recovered' if result == 'recovered' else 'unknown'),
            'hostRuntimeVerified': True,
            'hostEvidenceType': 'deployment',
            'deploymentReceiptVerified': True,
            'installationEvidenceVerified': False,
            'hostJournalVerified': True,
            'containerImageVerified': True,
            'publicIdentityVerified': True,
            'candidateSchema': 30,
            'predecessorSchema': 28,
            'observedSchema': (30 if result == 'committed' else
                               28 if result == 'recovered' else None),
            'schemaVerified': result in {'committed', 'recovered'},
        }
        value.update(changes)
        self.outcome_path.write_text(json.dumps(value))

    def reconcile(self):
        with patch.dict(os.environ, dict(self.env, DEPLOYMENT_ID=str(self.candidate_deployment_id)), clear=True):
            record_module.reconcile()

    def test_start_binds_candidate_predecessor_and_operation_digest(self):
        payload = self.github.deployments[0]['payload']
        self.assertEqual(payload['recordType'], record_module.ATTEMPT_KIND)
        self.assertEqual(payload['operationId'], self.binding['operationId'])
        self.assertNotEqual(payload['operationId'], self.binding['candidate']['image'].rsplit(':', 1)[1])
        self.assertEqual(payload['candidate'], self.binding['candidate'])
        self.assertEqual(payload['predecessor'], self.binding['predecessor'])
        self.assertEqual(payload['workflowRunId'], '777')

    def test_binding_for_deployment_reads_and_validates_the_original_record(self):
        binding = record_module.binding_for_deployment(str(self.candidate_deployment_id))
        self.assertEqual(binding, self.binding)

    def test_binding_for_deployment_rejects_records_outside_the_runtime_contract(self):
        record = self.github.deployments[0]
        record['task'] = 'another-task'
        with self.assertRaisesRegex(ValueError, 'different task or environment'):
            record_module.binding_for_deployment(str(self.candidate_deployment_id))

    def test_start_retry_reuses_the_same_attempt_record(self):
        with patch.dict('os.environ', self.env, clear=True):
            record_module.start()
        self.assertEqual(len(self.github.deployments), 1)

    def test_start_rejects_qualification_attempt_mismatch(self):
        path = self.root/'demo-qualification.json'
        receipt = json.loads(path.read_text())
        receipt['runAttempt'] = '3'
        path.write_text(json.dumps(receipt))
        with patch.dict('os.environ', self.env, clear=True):
            with self.assertRaisesRegex(ValueError, 'qualification receipt'):
                record_module.start()
        self.assertEqual(len(self.github.deployments), 1)

    def test_duplicate_attempt_records_are_rejected(self):
        self.github.deployments.append(dict(self.github.deployments[0], id=999))
        with patch.dict('os.environ', self.env, clear=True):
            with self.assertRaisesRegex(RuntimeError, 'Duplicate GitHub runtime records'):
                record_module.start()

    def test_committed_candidate_is_recorded_only_for_exact_verified_identity(self):
        self.outcome()
        self.reconcile()
        statuses = self.github.statuses[self.candidate_deployment_id]
        self.assertEqual(statuses[0]['state'], 'success')
        self.assertEqual(
            statuses[0]['description'],
            record_module._status_description('committed', self.binding['operationId']))
        self.assertIn(f'running_revision={self.binding["candidate"]["revision"]}', self.output.read_text())
        self.assertIn(f'running_image={self.binding["candidate"]["image"]}', self.output.read_text())

    def test_summary_distinguishes_recovered_runtime_and_omits_diagnostics(self):
        summary = self.root/'summary'
        self.env['GITHUB_STEP_SUMMARY'] = str(summary)
        self.outcome('recovered', failure='private secret from remote error')
        self.reconcile()
        rendered = summary.read_text()
        self.assertIn('recovered', rendered)
        self.assertIn(self.binding['predecessor']['image'], rendered)
        self.assertIn('28 → 30', rendered)
        self.assertNotIn('private secret', rendered)
        self.assertIn('candidate was not deployed', rendered)

    def test_lost_github_status_response_reconciles_without_duplicate_status(self):
        self.outcome()
        self.github.drop_status_response = True
        with self.assertRaisesRegex(RuntimeError, 'lost status response'):
            self.reconcile()
        before = len(self.github.statuses[self.candidate_deployment_id])
        self.reconcile()
        self.assertEqual(len(self.github.statuses[self.candidate_deployment_id]), before)

    def test_recovered_predecessor_keeps_candidate_failure_and_records_truth(self):
        self.outcome('recovered')
        self.reconcile()
        candidate_status = self.github.statuses[self.candidate_deployment_id][0]
        self.assertEqual(candidate_status['state'], 'failure')
        self.assertIn('recovered', candidate_status['description'])
        self.assertEqual(len(self.github.deployments), 2)
        predecessor_record = self.github.deployments[-1]
        self.assertEqual(predecessor_record['sha'], self.binding['predecessor']['revision'])
        self.assertEqual(predecessor_record['payload']['image'], self.binding['predecessor']['image'])
        self.assertEqual(predecessor_record['payload']['recoveredFrom'], str(self.candidate_deployment_id))
        self.assertEqual(self.github.statuses[predecessor_record['id']][0]['state'], 'success')

    def test_recovered_outcome_is_idempotent_after_each_github_write(self):
        self.outcome('recovered')
        self.github.drop_deployment_response = True
        with self.assertRaisesRegex(RuntimeError, 'lost deployment-create response'):
            self.reconcile()
        # Candidate failure and the recovery deployment were written before the lost response.
        candidate_status_count = len(self.github.statuses[self.candidate_deployment_id])
        self.reconcile()
        self.reconcile()
        self.assertEqual(len(self.github.deployments), 2)
        self.assertEqual(len(self.github.statuses[self.candidate_deployment_id]), candidate_status_count)
        recovery_id = self.github.deployments[-1]['id']
        self.assertEqual(len(self.github.statuses[recovery_id]), 1)

    def test_wrong_observed_image_or_revision_cannot_be_recorded_as_success(self):
        wrong = dict(self.binding['candidate'], image=self.binding['predecessor']['image'])
        self.outcome(observed=wrong)
        with self.assertRaisesRegex(RuntimeError, 'unresolved'):
            self.reconcile()
        self.assertEqual(self.github.statuses[self.candidate_deployment_id][0]['state'], 'error')

    def test_unverified_container_or_public_identity_is_unresolved(self):
        self.outcome(containerImageVerified=False)
        with self.assertRaisesRegex(RuntimeError, 'unresolved'):
            self.reconcile()
        self.assertEqual(self.github.statuses[self.candidate_deployment_id][0]['state'], 'error')

    def test_wrong_live_schema_cannot_be_recorded_as_success(self):
        self.outcome(observedSchema=29)
        with self.assertRaisesRegex(RuntimeError, 'unresolved'):
            self.reconcile()
        self.assertEqual(self.github.statuses[self.candidate_deployment_id][0]['state'], 'error')

    def test_image_only_commit_uses_host_identity_without_claiming_a_journal(self):
        self.binding['operation'] = 'deploy'
        self.binding['operationId'] = 'sha256:' + 'f' * 64
        self.binding_path.write_text(json.dumps(self.binding))
        self.github.deployments.clear()
        self.github.statuses.clear()
        with patch.dict(os.environ, self.env, clear=True):
            record_module.start()
        self.candidate_deployment_id = self.github.deployments[0]['id']
        self.outcome('committed', journalState='image-only-committed', hostJournalVerified=False,
                     predecessorSchema=30)
        self.reconcile()
        self.assertEqual(self.github.statuses[self.candidate_deployment_id][0]['state'], 'success')

    def test_installation_evidence_is_limited_to_qualified_same_image_deploy(self):
        same = dict(self.binding['candidate'])
        self.binding['operation'] = 'deploy'
        self.binding['operationId'] = 'sha256:' + 'f' * 64
        self.binding['predecessor'] = same
        self.binding_path.write_text(json.dumps(self.binding))
        self.github.deployments.clear()
        self.github.statuses.clear()
        with patch.dict('os.environ', self.env, clear=True):
            record_module.start()
        self.candidate_deployment_id = self.github.deployments[0]['id']
        self.outcome('committed', journalState='image-only-committed', hostJournalVerified=False,
                     predecessorSchema=30, hostEvidenceType='installation',
                     deploymentReceiptVerified=False, installationEvidenceVerified=True)
        self.reconcile()
        self.assertEqual(self.github.statuses[self.candidate_deployment_id][0]['state'], 'success')

        invalid_binding = dict(self.binding, predecessor={
            'image':'ghcr.io/flidai/leapview@sha256:'+'b'*64, 'revision':'d'*40})
        invalid_outcome = {
            'result':'committed', 'operation':'deploy', 'hostRuntimeVerified':True,
            'containerImageVerified':True, 'publicIdentityVerified':True,
            'hostEvidenceType':'installation', 'deploymentReceiptVerified':False,
            'installationEvidenceVerified':True, 'candidateSchema':30,
            'predecessorSchema':30, 'observedSchema':30, 'schemaVerified':True,
            'observed':self.binding['candidate'], 'journalState':'image-only-committed',
        }
        self.assertEqual(record_module._verified_result(invalid_outcome, invalid_binding), 'unresolved')

    def test_installation_evidence_cannot_substitute_for_deployment_receipt(self):
        self.outcome(hostEvidenceType='installation', deploymentReceiptVerified=False,
                     installationEvidenceVerified=True)
        with self.assertRaisesRegex(RuntimeError, 'unresolved'):
            self.reconcile()
        self.assertEqual(self.github.statuses[self.candidate_deployment_id][0]['state'], 'error')

    def test_reconcile_rejects_changed_binding_after_attempt_recorded(self):
        changed = dict(self.binding)
        changed['predecessor'] = {
            'image': 'ghcr.io/flidai/leapview@sha256:' + 'f' * 64,
            'revision': 'f' * 40,
        }
        self.binding_path.write_text(json.dumps(changed))
        self.outcome(predecessor=changed['predecessor'])
        with self.assertRaisesRegex(ValueError, 'Host outcome predecessor'):
            self.reconcile()
        self.assertEqual(self.github.statuses[self.candidate_deployment_id][0]['state'], 'error')

    def test_unknown_result_or_operation_id_fails_closed(self):
        for changes in (
            {'result': 'success'},
            {'operationId': 'sha256:' + 'f' * 64},
            {'journalState': 'future-state'},
        ):
            with self.subTest(changes=changes):
                self.outcome(**changes)
                with self.assertRaises((ValueError, RuntimeError)):
                    self.reconcile()
                self.assertEqual(self.github.statuses[self.candidate_deployment_id][0]['state'], 'error')
                self.github.statuses[self.candidate_deployment_id].clear()

    def test_mutable_candidate_environment_mismatch_is_rejected(self):
        self.outcome()
        with patch.dict(os.environ, dict(self.env, DEPLOYMENT_ID=str(self.candidate_deployment_id),
                                         DEMO_IMAGE='ghcr.io/flidai/leapview@sha256:' + 'f' * 64), clear=True):
            with self.assertRaisesRegex(ValueError, 'DEMO_IMAGE'):
                record_module.reconcile()
        self.assertEqual(self.github.statuses[self.candidate_deployment_id][0]['state'], 'in_progress')


class RuntimeResolveTests(unittest.TestCase):
    def test_latest_failed_attempt_blocks_fallback_to_older_success(self):
        record = {
            'id': 200,
            'sha': 'b' * 40,
            'task': record_module.TASK,
            'environment': record_module.ENVIRONMENT,
            'payload': {'image': 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64},
        }
        with tempfile.NamedTemporaryFile() as output:
            with patch.dict('os.environ', GITHUB_OUTPUT=output.name, DEMO_RUNTIME_REVISION='c' * 40,
                            clear=True), patch.object(record_module, 'api', side_effect=[
                                [record], [{'state': 'failure'}],
                            ]):
                with self.assertRaisesRegex(RuntimeError, 'Latest runtime deployment'):
                    record_module.resolve()
            self.assertEqual(pathlib.Path(output.name).read_text(), '')

    def test_resolve_returns_image_and_sha_for_verified_recovery(self):
        opid = 'sha256:' + 'e' * 64
        record = {
            'id': 201,
            'sha': 'd' * 40,
            'task': record_module.TASK,
            'environment': record_module.ENVIRONMENT,
            'payload': {
                'recordType': record_module.RECOVERY_KIND,
                'operationId': opid,
                'image': 'ghcr.io/flidai/leapview@sha256:' + 'b' * 64,
                'revision': 'd' * 40,
                'predecessor': {
                    'image': 'ghcr.io/flidai/leapview@sha256:' + 'b' * 64,
                    'revision': 'd' * 40,
                },
            },
        }
        status = {
            'state': 'success',
            'description': record_module._status_description('recovered', opid),
        }
        with tempfile.NamedTemporaryFile() as output:
            with patch.dict('os.environ', GITHUB_OUTPUT=output.name, clear=True), \
                 patch.object(record_module, 'api', side_effect=[[record], [status]]), \
                 patch.object(record_module, 'read_contract', return_value={
                     'permissionProfile': 'legacy-capabilities/v1'}):
                record_module.resolve()
            result = pathlib.Path(output.name).read_text()
            self.assertIn('revision=' + 'd' * 40, result)
            self.assertIn('image=ghcr.io/flidai/leapview@sha256:' + 'b' * 64, result)
            self.assertIn('permission_profile=legacy-capabilities/v1', result)

    def test_resolve_fails_closed_without_contract_for_the_selected_source(self):
        record = {
            'id': 202,
            'sha': 'd' * 40,
            'task': record_module.TASK,
            'environment': record_module.ENVIRONMENT,
            'payload': {'image': 'ghcr.io/flidai/leapview@sha256:' + 'b' * 64},
        }
        with tempfile.NamedTemporaryFile() as output:
            with patch.dict('os.environ', GITHUB_OUTPUT=output.name, clear=True), \
                 patch.object(record_module, 'api', side_effect=[[record], [{'state': 'success'}]]), \
                 patch.object(record_module, 'read_contract', side_effect=ValueError('unsupported source')):
                with self.assertRaisesRegex(ValueError, 'unsupported source'):
                    record_module.resolve()
            self.assertEqual(pathlib.Path(output.name).read_text(), '')


if __name__ == '__main__':
    unittest.main()

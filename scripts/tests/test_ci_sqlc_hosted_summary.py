import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import ci_sqlc_hosted_summary as summary


class HostedSummaryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = 'a' * 40
        jobs = []
        for kind, pairs in [('producer', [0]), ('consumer', [1, 2, 3])]:
            for pair in pairs:
                for mode in ['baseline', 'treatment']:
                    path = self.root / f'sqlc-sample-1-{kind}-{mode}-{pair}'
                    path.mkdir()
                    receipt = dict(status='passed', source=self.source, mode=mode, kind=kind,
                                   archive_sha256='b' * 64, original_dockerfile_sha256='c' * 64,
                                   original_script_sha256='d' * 64, paths=['api/gen'],
                                   manifest={'api/gen/a.go': 'e' * 64}, sqlc_seconds=1,
                                   marker='producer\n' if kind == 'producer' else 'changed-source\n',
                                   context_sha256=mode + kind, recipe_sha256=mode, script_sha256=mode,
                                   tool_identity={'binary.sha256': 'f' * 64} if mode == 'treatment' else {})
                    if mode == 'treatment':
                        receipt['tool_cache_proof'] = {'cached': kind == 'consumer'}
                    (path / 'receipt.json').write_text(json.dumps(receipt))
                    (path / 'image-identity.json').write_text(json.dumps({'revision': self.source, 'dirty': False}))
                    (path / 'image-inspect.json').write_text(json.dumps([{'Id': 'sha256:' + '1' * 64}]))
                    (path / 'transition.json').write_text(json.dumps({
                        'version': 1, 'status': 'passed', 'validatorRevision': self.source,
                        'candidate': {'image': 'sha256:' + '1' * 64, 'revision': self.source},
                        'checks': {key: True for key in ['legacyPublication', 'legacyViewer', 'typedPolicyCaptured',
                            'independentApproval', 'publisherNoSelfApproval', 'viewerLeastPrivilege',
                            'realPublicationAdapter', 'subsequentDeploy']}}))
                    (path / 'outcome.txt').write_text('success\n')
                    (path / 'pair.txt').write_text(str(pair))
                    for clock in ['build', 'qualification', 'sample', 'storage']:
                        (path / (clock + '-start.txt')).write_text('100')
                        (path / (clock + '-end.txt')).write_text('200')
                    if kind == 'producer':
                        (path / 'storage.json').write_text(json.dumps({'local_export_bytes': 1000, 'gha_billed_bytes': None}))
                    jobs.append(dict(name=f'SQLC {kind} {mode} pair {pair}', conclusion='success',
                                     started_at='2026-10-07T00:00:00Z', completed_at='2026-10-07T00:10:00Z'))
        (self.root / 'jobs.json').write_text(json.dumps([{'jobs': jobs}]))

    def test_complete_cohort_preserves_producer_cost_and_unresolved_billing(self):
        result = summary.summarize(self.root, self.source)
        self.assertEqual(result['status'], 'passed')
        self.assertEqual(len(result['samples']), 8)
        self.assertEqual(result['modes']['baseline']['median_consumer_job_seconds'], 600)
        self.assertEqual(result['modes']['baseline']['median_plus_one_third_producer_job_seconds'], 800)
        self.assertFalse(result['adoption_qualified'])

    def test_failed_or_missing_sample_cannot_be_hidden(self):
        path = self.root / 'sqlc-sample-1-consumer-treatment-2'
        (path / 'outcome.txt').write_text('failure')
        with self.assertRaises(ValueError):
            summary.summarize(self.root, self.source)
        (path / 'outcome.txt').write_text('success')
        (path / 'transition.json').unlink()
        with self.assertRaises((ValueError, FileNotFoundError)):
            summary.summarize(self.root, self.source)

    def test_another_attempt_cannot_reuse_old_successful_samples(self):
        with self.assertRaises(FileNotFoundError):
            summary.summarize(self.root, self.source, attempt=2)

    def test_mismatched_generation_or_source_is_rejected(self):
        path = self.root / 'sqlc-sample-1-consumer-treatment-1/receipt.json'
        original = json.loads(path.read_text())
        for key, value in [('source', 'f' * 40), ('manifest', {'api/gen/a.go': 'f' * 64}),
                           ('context_sha256', 'different-consumer-context')]:
            changed = copy.deepcopy(original)
            changed[key] = value
            path.write_text(json.dumps(changed))
            with self.subTest(key=key), self.assertRaises(ValueError):
                summary.summarize(self.root, self.source)
        path.write_text(json.dumps(original))

    def test_wrong_job_outcome_or_missing_timing_is_rejected(self):
        path = self.root / 'jobs.json'
        pages = json.loads(path.read_text())
        pages[0]['jobs'][0]['conclusion'] = 'failure'
        path.write_text(json.dumps(pages))
        with self.assertRaises(ValueError):
            summary.summarize(self.root, self.source)

    def test_qualification_must_bind_this_image_and_all_checks(self):
        path = self.root / 'sqlc-sample-1-consumer-baseline-1/transition.json'
        original = json.loads(path.read_text())
        for key, value in [('status', 'failed'), ('validatorRevision', 'f' * 40),
                           ('checks', {}), ('candidate', {'image': 'sha256:' + '2' * 64, 'revision': self.source})]:
            changed = copy.deepcopy(original)
            changed[key] = value
            path.write_text(json.dumps(changed))
            with self.subTest(key=key), self.assertRaises(ValueError):
                summary.summarize(self.root, self.source)


if __name__ == '__main__':
    unittest.main()

import copy
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))
import managed_admission_handoff as handoff
from test_managed_admission_handoff import archive, metadata

SOURCE, PRODUCER = 'a' * 40, 'b' * 40
IMAGE = 'ghcr.io/flidai/leapview-site@sha256:' + 'c' * 64


class NixHandoffTests(unittest.TestCase):
    def fixture(self):
        run, workflow, artifact = metadata()
        workflow['path'] = '.github/workflows/nix-output-admission.yml'
        run.update(path=workflow['path'], event='workflow_dispatch', head_sha=PRODUCER)
        artifact['name'] = 'nix-admission-site-image-12-2-amd64'
        artifact['workflow_run']['head_sha'] = PRODUCER
        authorization = handoff.authorize(run, workflow, artifact, run_id=12, attempt=2, artifact_id=56,
            source=SOURCE, platform='linux/amd64', producer_revision=PRODUCER, kind='site-image',
            now=datetime(2026, 10, 9, tzinfo=timezone.utc))
        digest = 'sha256:' + 'd' * 64
        files = {name: b'{}' for name in handoff.NIX_FILES - {'binding.json'}}
        files['admission.json'] = json.dumps({'nixEvidence': {'kind': 'site-image', 'verifierRevision': PRODUCER}}).encode()
        files['admission.digest'] = (digest + '\n').encode()
        files['evidence/go/site/govulncheck.json'] = b'{"progress":{}}'
        binding = {key: authorization[key] for key in ('repository', 'workflow', 'event', 'ref', 'sourceRevision',
                  'platform', 'runId', 'runAttempt', 'producerRevision')}
        binding.update(schemaVersion=1, image=IMAGE, admissionDigest=digest,
                       files={name: 'sha256:' + hashlib.sha256(data).hexdigest() for name, data in files.items()})
        files['binding.json'] = json.dumps(binding).encode()
        return authorization, files, (run, workflow, artifact)

    def test_distinct_source_and_authenticated_verifier(self):
        authorization, files, _ = self.fixture()
        self.assertEqual(authorization['sourceRevision'], SOURCE)
        self.assertEqual(authorization['producerRevision'], PRODUCER)
        payload = archive(files)
        self.assertEqual(handoff.verify_bundle(payload, 'sha256:' + hashlib.sha256(payload).hexdigest(), authorization, image=IMAGE), files)

    def test_cannot_borrow_source_sha_as_producer(self):
        _, _, metadata = self.fixture()
        for revision, kind in [(None, 'site-image'), (SOURCE, 'site-image'), (PRODUCER, 'application-image')]:
            with self.assertRaises(handoff.HandoffError):
                handoff.authorize(*metadata, run_id=12, attempt=2, artifact_id=56, source=SOURCE,
                    platform='linux/amd64', producer_revision=revision, kind=kind)

    def test_raw_inventory_substitution_and_unsafe_names_fail(self):
        authorization, original, _ = self.fixture()
        for name, data in [('evidence/go/site/govulncheck.json', b'changed'), ('evidence/../escape', b'{}'),
                           ('evidence/go', b'file collision'), ('extra.json', b'{}')]:
            with self.subTest(name=name):
                files = copy.deepcopy(original)
                files[name] = data
                payload = archive(files)
                with self.assertRaises(handoff.HandoffError):
                    handoff.verify_bundle(payload, 'sha256:' + hashlib.sha256(payload).hexdigest(), authorization, image=IMAGE)
        for field in ['verifierRevision', 'kind']:
            files = copy.deepcopy(original)
            receipt = json.loads(files['admission.json'])
            receipt['nixEvidence'][field] = 'foreign'
            files['admission.json'] = json.dumps(receipt).encode()
            payload = archive(files)
            with self.assertRaises(handoff.HandoffError):
                handoff.verify_bundle(payload, 'sha256:' + hashlib.sha256(payload).hexdigest(), authorization, image=IMAGE)


if __name__ == '__main__':
    unittest.main()

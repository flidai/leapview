import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_candidate_manifest as candidate
import nix_signed_evidence as signed

IMAGE = 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64
WORKFLOW = 'flidai/leapview/.github/workflows/artifacts.yml'
SITE_WORKFLOW = 'flidai/leapview/.github/workflows/nix-site-candidate.yml'
SIGNER = 'b' * 40
SPDX = {'spdxVersion': 'SPDX-2.3', 'SPDXID': 'SPDXRef-DOCUMENT',
        'documentNamespace': 'https://fixture.invalid/spdx/native', 'packages': [{'name': 'glibc'}]}


def statement(image, predicate_type, predicate):
    repository, digest = image.split('@')
    return {'_type': 'https://in-toto.io/Statement/v1',
            'subject': [{'name': repository, 'digest': {'sha256': digest[7:]}}],
            'predicateType': predicate_type, 'predicate': predicate}


class SignedEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.commands = []

    def gh(self, args, **kwargs):
        self.commands.append((args, kwargs))
        predicate_type = args[args.index('--predicate-type') + 1]
        image = args[3].removeprefix('oci://')
        predicate = SPDX if predicate_type == signed.SPDX else {'buildDefinition': {}}
        result = [{'verificationResult': {'statement': statement(image, predicate_type, predicate)}}]
        kwargs['stdout'].write(candidate.canonical_bytes(result))
        return subprocess.CompletedProcess(args, 0)

    def test_only_live_registry_discoverable_attestations_under_protected_identity(self):
        with patch.object(signed.subprocess, 'run', side_effect=self.gh):
            result = signed.verify_attestation(IMAGE, WORKFLOW, SIGNER, signed.SPDX, SPDX)
        args, kwargs = self.commands[0]
        self.assertEqual(args[:4], ['gh', 'attestation', 'verify', 'oci://' + IMAGE])
        expected = {'--repo': 'flidai/leapview', '--signer-workflow': WORKFLOW,
                    '--source-digest': SIGNER, '--source-ref': 'refs/heads/main',
                    '--predicate-type': signed.SPDX, '--format': 'json', '--limit': '10'}
        for option, value in expected.items():
            self.assertEqual(args[args.index(option) + 1], value)
        self.assertIn('--bundle-from-oci', args)
        self.assertIn('--deny-self-hosted-runners', args)
        self.assertTrue(kwargs['check'])
        self.assertEqual(kwargs['timeout'], 120)
        self.assertEqual(kwargs['stderr'], subprocess.DEVNULL)
        self.assertEqual(result, candidate.digest_bytes(candidate.canonical_bytes(statement(IMAGE, signed.SPDX, SPDX))))

    def test_different_subject_type_predicate_and_unverified_bundle_are_rejected(self):
        valid = statement(IMAGE, signed.SPDX, SPDX)
        mutations = [lambda s: s.update(predicateType=signed.PROVENANCE),
                     lambda s: s.update(_type='https://in-toto.io/Statement/v0.1'),
                     lambda s: s['subject'][0]['digest'].update(sha256='f' * 64),
                     lambda s: s['subject'][0].update(name='ghcr.io/attacker/leapview'),
                     lambda s: s['subject'].append(copy.deepcopy(s['subject'][0])),
                     lambda s: s['predicate']['packages'].append({'name': 'other'})]
        for mutation in mutations:
            altered = copy.deepcopy(valid)
            mutation(altered)
            with self.subTest(statement=altered), self.assertRaises(ValueError):
                signed.match_statement([{'verificationResult': {'statement': altered}}], IMAGE, signed.SPDX, SPDX)
        with self.assertRaises(ValueError):
            signed.match_statement([{'attestation': {'statement': valid}}], IMAGE, signed.SPDX, SPDX)

    def test_cryptographic_verifier_failure_cannot_be_overridden_by_json(self):
        def failure(args, **kwargs):
            self.gh(args, **kwargs)
            raise subprocess.CalledProcessError(1, args, stderr=b'token=secret')
        with patch.object(signed.subprocess, 'run', side_effect=failure):
            with self.assertRaisesRegex(ValueError, '^trusted registry attestation verification failed$'):
                signed.verify_attestation(IMAGE, WORKFLOW, SIGNER, signed.SPDX, SPDX)

    def test_malformed_duplicate_or_oversized_verifier_json_is_rejected(self):
        for data in [b'{}', b'[]', b'{broken', b'[{"verificationResult":{},"verificationResult":{}}]']:
            def invalid(_args, **kwargs):
                kwargs['stdout'].write(data)
            with self.subTest(data=data), patch.object(signed.subprocess, 'run', side_effect=invalid), self.assertRaises(ValueError):
                signed.verify_attestation(IMAGE, WORKFLOW, SIGNER, signed.SPDX, SPDX)
        with patch.object(signed, 'MAX_ATTESTATION_BYTES', 4), patch.object(signed.subprocess, 'run', side_effect=self.gh):
            with self.assertRaises(ValueError):
                signed.verify_attestation(IMAGE, WORKFLOW, SIGNER, signed.SPDX, SPDX)

    def test_kind_workflow_or_signer_revision_mismatch_fails_before_fetch(self):
        for kind, workflow, revision in [('application-image', 'flidai/leapview/.github/workflows/ci.yml', SIGNER),
                                         ('site-image', WORKFLOW, SIGNER),
                                         ('application-image', WORKFLOW, 'main')]:
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                signed.validate_signer(kind, workflow, revision)

    def test_site_signer_is_only_the_protected_site_candidate_workflow(self):
        signed.validate_signer('site-image', SITE_WORKFLOW, SIGNER)
        for kind, workflow in [('site-image', WORKFLOW),
                               ('application-image', SITE_WORKFLOW),
                               ('site-image', 'flidai/leapview/.github/workflows/site-image.yml')]:
            with self.subTest(kind=kind, workflow=workflow), self.assertRaises(ValueError):
                signed.validate_signer(kind, workflow, SIGNER)

    def test_binds_platform_sbom_to_same_runtime_report_and_registry_manifest(self):
        with tempfile.TemporaryDirectory() as temporary:
            runtime = Path(temporary)
            spdx_bytes = (json.dumps(SPDX, indent=2) + '\n').encode()
            (runtime / 'sbom.spdx.json').write_bytes(spdx_bytes)
            record = {'artifact': {'platform': 'linux/amd64', 'kind': 'application-image'},
                      'source': {'repository': 'flidai/leapview', 'revision': 'c' * 40},
                      'candidateDigest': 'sha256:' + 'd' * 64,
                      'evidence': {'nix-runtime': {'reports': [{'path': 'sbom.spdx.json', 'sha256': candidate.digest_bytes(spdx_bytes)}]}}}
            registry_record = {'registryBindingDigest': 'sha256:' + 'e' * 64,
                               'contentBinding': {'platforms': [{'platform': 'linux/amd64', 'manifestDigest': IMAGE.split('@')[1]}]}}
            with patch.object(signed.registry, 'bind_registry', return_value=registry_record), \
                    patch.object(signed.subprocess, 'run', side_effect=self.gh):
                result = signed.collect(IMAGE, 'application-image', [record], [('archive', 'manifest', runtime)],
                                        ['linux/amd64'], WORKFLOW, SIGNER)
            self.assertFalse(result['releaseAdmission'])
            self.assertEqual(result['source']['revision'], 'c' * 40)
            self.assertEqual(result['signer']['sourceRevision'], SIGNER)
            self.assertEqual(result['spdx'][0]['reportSHA256'], candidate.digest_bytes(spdx_bytes))
            self.assertEqual(result['spdx'][0]['image'], IMAGE)
            self.assertEqual(len(self.commands), 2)
            (runtime / 'sbom.spdx.json').write_text('{}')
            with patch.object(signed.registry, 'bind_registry', return_value=registry_record), \
                    patch.object(signed.subprocess, 'run', side_effect=self.gh), self.assertRaises(ValueError):
                signed.collect(IMAGE, 'application-image', [record], [('archive', 'manifest', runtime)],
                               ['linux/amd64'], WORKFLOW, SIGNER)

    def test_index_requires_individual_spdx_subjects_for_every_platform(self):
        with tempfile.TemporaryDirectory() as temporary:
            records, paths, platforms = [], [], []
            for architecture, digest_char in [('amd64', 'd'), ('arm64', 'e')]:
                platform = 'linux/' + architecture
                runtime = Path(temporary) / architecture
                runtime.mkdir()
                data = candidate.canonical_bytes(SPDX)
                (runtime / 'sbom.spdx.json').write_bytes(data)
                records.append({'artifact': {'platform': platform, 'kind': 'application-image'},
                                'source': {'revision': 'c' * 40}, 'candidateDigest': 'sha256:' + digest_char * 64,
                                'evidence': {'nix-runtime': {'reports': [{'path': 'sbom.spdx.json', 'sha256': candidate.digest_bytes(data)}]}}})
                paths.append(('archive', 'manifest', runtime))
                platforms.append({'platform': platform, 'manifestDigest': 'sha256:' + digest_char * 64})
            binding = {'registryBindingDigest': 'sha256:' + 'f' * 64, 'contentBinding': {'platforms': platforms}}
            with patch.object(signed.registry, 'bind_registry', return_value=binding), \
                    patch.object(signed.subprocess, 'run', side_effect=self.gh):
                result = signed.collect(IMAGE, 'application-image', records, paths, ['linux/amd64', 'linux/arm64'], WORKFLOW, SIGNER)
            subjects = [args[3] for args, _ in self.commands]
            self.assertEqual(subjects, ['oci://' + IMAGE] + ['oci://ghcr.io/flidai/leapview@' + p['manifestDigest'] for p in platforms])
            self.assertEqual([entry['platform'] for entry in result['spdx']], ['linux/amd64', 'linux/arm64'])

            def missing_arm(args, **kwargs):
                if args[3].endswith('e' * 64):
                    kwargs['stdout'].write(b'[]')
                else:
                    self.gh(args, **kwargs)

            with patch.object(signed.registry, 'bind_registry', return_value=binding), \
                    patch.object(signed.subprocess, 'run', side_effect=missing_arm), self.assertRaises(ValueError):
                signed.collect(IMAGE, 'application-image', records, paths, ['linux/amd64', 'linux/arm64'], WORKFLOW, SIGNER)


if __name__ == '__main__':
    unittest.main()

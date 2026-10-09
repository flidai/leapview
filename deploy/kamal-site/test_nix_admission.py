import contextlib
import copy
import json
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import deploy
import nix_admission
from contract import validate_record
from test_contract import record


class NixSiteAdmissionTests(unittest.TestCase):
    def selection(self):
        return {'runId': 456, 'runAttempt': 1, 'artifactId': 789,
                'sourceRevision': 'b' * 40, 'producerRevision': 'e' * 40}

    def admission(self):
        return {'version': 'oci-artifact-admission/v1', 'release': {
            'distribution': 'nix', 'platform': 'linux/amd64', 'sourceRevision': 'b' * 40,
            'image': record()['image'], 'version': '0.3.0-alpha.1'},
            'repository': 'ghcr.io/flidai/leapview-site', 'ociDigest': 'sha256:' + 'a' * 64,
            'decision': 'admitted', 'architectureMarker': 'public-site/v1',
            'provenance': {'verified': True, 'repository': 'flidai/leapview',
                'sourceRevision': 'b' * 40,
                'workflow': 'flidai/leapview/.github/workflows/nix-site-candidate.yml'},
            'sbom': {'verified': True}, 'securityPolicy': {'passed': True},
            'nixEvidence': {'version': 'nix-oci-artifact-evidence/v1', 'kind': 'site-image',
                'verifierRevision': 'e' * 40}}

    def test_authenticated_receipt_is_bound_to_single_platform_site_manifest(self):
        admission = self.admission()
        platform = {'schemaVersion': 2, 'config': {'digest': 'sha256:' + 'd' * 64, 'size': 10},
                    'layers': [{'size': 20}]}
        with tempfile.TemporaryDirectory() as temporary, \
                patch.object(nix_admission, 'authenticate', return_value=(admission, 'sha256:' + 'f' * 64,
                    'sha256:' + '1' * 64)), \
                patch.object(deploy, 'manifest', return_value=(platform, 'sha256:' + 'a' * 64)), \
                patch.object(deploy, 'run', return_value=json.dumps({'fixture': 'release'}).encode()):
            result = deploy.prepare_nix(Path(temporary), record()['image'], self.selection())
        self.assertEqual(result['platform'], 'sha256:' + 'a' * 64)
        self.assertEqual(result['config'], 'sha256:' + 'd' * 64)
        self.assertEqual(result['nixAdmission']['admissionDigest'], 'sha256:' + 'f' * 64)
        self.assertEqual(result['nixAdmission']['artifactDigest'], 'sha256:' + '1' * 64)
        self.assertIs(validate_record(result), result)

    def test_selection_or_admitted_output_changes_are_denied_before_registry(self):
        for key, value in [('runId', 0), ('runAttempt', True), ('artifactId', -1),
                           ('sourceRevision', 'main'), ('producerRevision', 'HEAD')]:
            selected = self.selection()
            selected[key] = value
            with self.subTest(key=key), patch.object(nix_admission, 'authenticate') as authenticate:
                with self.assertRaises(ValueError):
                    deploy.prepare_nix(Path('/unused'), record()['image'], selected)
                authenticate.assert_not_called()
        for key, value in [('decision', 'rejected'), ('repository', 'ghcr.io/flidai/leapview'),
                           ('architectureMarker', 'postgresql')]:
            admission = self.admission()
            admission[key] = value
            with self.subTest(key=key), patch.object(nix_admission, 'authenticate',
                    return_value=(admission, 'sha256:' + 'f' * 64, 'sha256:' + '1' * 64)), \
                    patch.object(deploy, 'manifest') as manifest:
                with self.assertRaises(ValueError):
                    deploy.prepare_nix(Path('/unused'), record()['image'], self.selection())
                manifest.assert_not_called()

    def test_new_candidate_repeats_exact_nix_authentication_before_begin(self):
        previous = record()
        candidate = copy.deepcopy(previous)
        candidate['image'] = candidate['image'].replace('a' * 64, '2' * 64)
        candidate['version'] = 'k' + '2' * 64
        candidate['nixAdmission'] = dict(self.selection(), admissionDigest='sha256:' + 'f' * 64,
                                         artifactDigest='sha256:' + '1' * 64)
        calls = []
        def remote(operation, **values):
            calls.append(operation)
            if operation == 'preflight':
                return {'state': {'active': previous['version'], 'records': {previous['version']: previous}}}
            return {}
        with tempfile.TemporaryDirectory() as temporary, \
                patch.object(deploy, 'read_record', return_value=candidate), \
                patch.object(deploy, 'ownership', return_value=contextlib.nullcontext()), \
                patch.object(deploy, 'remote', side_effect=remote), \
                patch.object(deploy, 'prepare_nix', return_value=candidate) as prepare, \
                patch.object(deploy, 'prepare') as classic, patch.object(deploy, 'transition'):
            deploy.deploy(Path(temporary), Path('record'))
        classic.assert_not_called()
        prepare.assert_called_once_with(Path(temporary), candidate['image'], self.selection())
        self.assertEqual(calls, ['preflight', 'begin'])

    def test_failed_canonical_verifier_cannot_emit_an_admitted_candidate(self):
        authorization = {'artifactDigest': 'sha256:' + '1' * 64}
        files = {'admission.json': json.dumps(self.admission()).encode(),
                 'binding.json': json.dumps({'admissionDigest': 'sha256:' + 'f' * 64}).encode()}
        with tempfile.TemporaryDirectory() as temporary, \
                patch.object(nix_admission.handoff, 'fetch', return_value=(authorization, b'zip')), \
                patch.object(nix_admission.handoff, 'verify_bundle', return_value=files), \
                patch.object(deploy, 'run', side_effect=RuntimeError('canonical verifier denied')):
            with self.assertRaisesRegex(RuntimeError, 'canonical verifier denied'):
                nix_admission.authenticate(Path(temporary), record()['image'], self.selection(), deploy.run)

    def test_wrong_receipt_source_platform_or_verifier_is_closed(self):
        for section, key, value in [('release', 'sourceRevision', '9' * 40),
                ('release', 'platform', 'linux/arm64'), ('release', 'distribution', 'oci'),
                ('nixEvidence', 'verifierRevision', '9' * 40), ('provenance', 'verified', False)]:
            admission = self.admission()
            admission[section][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                nix_admission.validate_receipt(admission, record()['image'], self.selection())

    def test_mutable_index_or_changed_registry_digest_cannot_prepare(self):
        for manifest, digest in [({'schemaVersion': 2, 'manifests': []}, 'sha256:' + 'a' * 64),
                ({'schemaVersion': 2, 'layers': [], 'config': {'digest': 'sha256:' + 'd' * 64}},
                 'sha256:' + '9' * 64)]:
            with self.subTest(digest=digest), \
                    patch.object(nix_admission, 'authenticate', return_value=(self.admission(),
                        'sha256:' + 'f' * 64, 'sha256:' + '1' * 64)), \
                    patch.object(deploy, 'manifest', return_value=(manifest, digest)), \
                    patch.object(deploy, 'run') as run:
                with self.assertRaises(ValueError):
                    deploy.prepare_nix(Path('/unused'), record()['image'], self.selection())
                run.assert_not_called()

    def test_missing_exact_cli_selection_is_denied_before_connect_or_authentication(self):
        with patch('sys.argv', ['deploy.py', 'prepare-nix', '--image', record()['image']]), \
                patch.object(deploy, 'connect') as connect, \
                patch.object(nix_admission, 'authenticate') as authenticate, \
                contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                deploy.main()
            self.assertEqual(error.exception.code, 2)
        connect.assert_not_called()
        authenticate.assert_not_called()

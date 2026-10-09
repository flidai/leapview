import copy
import json
from pathlib import Path
import shutil
import subprocess
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_desktop_attestation as attestation
import test_nix_desktop_qualification as fixtures


class DesktopAttestationTests(unittest.TestCase):
    def setUp(self):
        self.fixture = fixtures.DesktopQualificationTests('runTest')
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.root = self.fixture.root
        self.source = self.fixture.source
        self.verifier = self.fixture.verifier
        self.revision = self.fixture.revision
        self.original_deb = self.fixture.deb
        self.original = self.root / 'original-qualified'
        self.qualified = self.root / 'signed'
        self.host = {'id': 'ubuntu', 'versionID': '22.04', 'architecture': 'x86_64', 'nativeExecution': True}
        self.controls = patch.object(attestation.desktop, 'deb_control_fields',
                                     return_value=self.fixture.control_fields())
        self.controls.start()
        self.addCleanup(self.controls.stop)
        host = patch.object(attestation.desktop, 'require_host', return_value=self.host)
        host.start()
        self.addCleanup(host.stop)
        record, _ = attestation.desktop.candidate_record(
            self.original_deb, self.source, self.revision, self.verifier)
        self.record = record
        evidence = self.original / 'qualified'
        evidence.mkdir(parents=True)
        (self.original / 'candidate').mkdir()
        shutil.copyfile(self.original_deb, self.original / 'candidate' / attestation.ARCHIVE_NAME)
        self.write(evidence / 'package-verification.json', {
            'platform': 'linux', 'architecture': 'x64', 'packageFormat': 'deb', 'startup': 'trusted-shell-ready'})
        self.write(evidence / 'candidate.release.json', {
            'source': {'commit': self.revision, 'dirty': False},
            'artifact': {'sha256': record['artifact']['sha256'].removeprefix('sha256:'), 'format': 'deb'},
            'application': {'packageName': '@leapview/desktop', 'version': '0.1.0'},
            'support': {'minimumVersion': 'Ubuntu 22.04 LTS', 'qualification': 'candidate'},
            'signing': {'state': 'unsigned-candidate', 'productionEligible': False, 'identity': None}})
        self.sbom = {'spdxVersion': 'SPDX-2.3', 'packages': [{'name': 'fixture-package'}]}
        self.write(evidence / 'candidate.spdx.json', self.sbom)
        self.write(evidence / 'candidate-manifest.json', record)
        (evidence / 'checksums.txt').write_text('original package checksum\n')
        (evidence / 'verify-release-evidence.mjs').write_text('protected verifier\n')
        report = attestation.desktop.qualification_report(record, self.host,
            attestation.desktop.verifier_identity(self.verifier),
            attestation.desktop.aligned_policy_inputs(self.source, self.verifier),
            attestation.desktop.evidence_files(evidence, record))
        self.write(evidence / 'qualification-report.json', report)
        shutil.copytree(self.original, self.qualified)
        self.commands = []
        self.real_run = subprocess.run

    @staticmethod
    def write(path, value):
        path.write_text(json.dumps(value, sort_keys=True) + '\n')

    def verify(self):
        return attestation.verify(self.original_deb, self.qualified, self.source, self.verifier, self.revision)

    def signed(self):
        return attestation.verify_signed(self.original_deb, self.original, self.qualified,
            self.source, self.verifier, self.revision, self.revision)

    def gh(self, command, **kwargs):
        if command[0] != 'gh':
            return self.real_run(command, **kwargs)
        self.assertEqual(command[:3], ['gh', 'attestation', 'verify'])
        self.assertTrue(kwargs['check'])
        self.commands.append(command)
        path = Path(command[3])
        predicate_type = command[command.index('--predicate-type') + 1]
        predicate = self.sbom if predicate_type == attestation.SPDX else {'buildDefinition': {}}
        statement = {'_type': 'https://in-toto.io/Statement/v1',
                     'subject': [{'name': path.name, 'digest': {'sha256':
                         attestation.candidate.digest_file(path).removeprefix('sha256:')}}],
                     'predicateType': predicate_type, 'predicate': predicate}
        kwargs['stdout'].write(json.dumps([{'verificationResult': {'statement': statement}}]).encode())
        return subprocess.CompletedProcess(command, 0)

    def test_offline_verifier_uses_original_bytes_and_preserves_non_admission(self):
        with patch.object(attestation.desktop, 'run_command', side_effect=AssertionError('candidate execution')):
            binding = self.verify()
        self.assertEqual(binding['archive']['sha256'], self.record['artifact']['sha256'])
        self.assertEqual(binding['archive']['version'], self.record['artifact']['version'])
        self.assertEqual(binding['archive']['platform'], self.record['artifact']['platform'])
        self.assertEqual(binding['candidateDigest'], self.record['candidateDigest'])
        self.assertIs(binding['releaseAdmission'], False)
        self.assertEqual(len(binding['evidence']), 7)

    def test_substituted_builder_or_qualified_package_is_rejected(self):
        for path in [self.original_deb, self.qualified / 'candidate' / attestation.ARCHIVE_NAME]:
            original = path.read_bytes()
            with self.subTest(path=path):
                path.write_bytes(b'substituted Debian bytes')
                with self.assertRaisesRegex(ValueError, 'original builder'):
                    self.verify()
                path.write_bytes(original)

    def test_evidence_inventory_rejects_extra_files_and_symlinks(self):
        path = self.qualified / 'qualified/extra'
        path.write_text('unbound evidence')
        with self.assertRaisesRegex(ValueError, 'inventory'):
            self.verify()
        path.unlink()
        path = self.qualified / 'qualified/checksums.txt'
        path.unlink()
        path.symlink_to(self.original / 'qualified/checksums.txt')
        with self.assertRaisesRegex(ValueError, 'symlink'):
            self.verify()

    def test_self_consistent_rewritten_evidence_cannot_replace_original_qualification(self):
        evidence = self.qualified / 'qualified'
        (evidence / 'checksums.txt').write_text('changed by signer')
        report = json.loads((evidence / 'qualification-report.json').read_text())
        report['evidence'] = attestation.desktop.evidence_files(evidence, self.record)
        self.write(evidence / 'qualification-report.json', report)
        self.verify()  # Internally consistent; the original artifact is the independent anchor.
        with self.assertRaisesRegex(ValueError, 'original qualification'):
            self.signed()

    def test_live_verification_pins_workflow_main_revision_hosted_runner_and_exact_predicates(self):
        with patch.object(attestation.subprocess, 'run', side_effect=self.gh):
            binding = self.signed()
        self.assertEqual(len(self.commands), 3)
        for command in self.commands:
            self.assertIn('--deny-self-hosted-runners', command)
            self.assertEqual(command[command.index('--signer-workflow') + 1], attestation.WORKFLOW)
            self.assertEqual(command[command.index('--source-digest') + 1], self.revision)
            self.assertEqual(command[command.index('--source-ref') + 1], 'refs/heads/main')
        self.assertEqual(binding['spdx']['predicateSHA256'],
            attestation.candidate.digest_bytes(attestation.candidate.canonical_bytes(self.sbom)))
        self.assertIs(binding['releaseAdmission'], False)
        self.assertEqual(binding['signer']['sourceRevision'], self.revision)

    def test_live_spdx_subject_and_predicate_must_match_exact_package(self):
        path = self.qualified / 'candidate' / attestation.ARCHIVE_NAME
        expected = copy.deepcopy(self.sbom)
        self.sbom['packages'][0]['name'] = 'different-package'
        with patch.object(attestation.subprocess, 'run', side_effect=self.gh), \
                self.assertRaisesRegex(ValueError, 'subject and predicate'):
            attestation.verify_attestation(path, self.record['artifact']['sha256'],
                                            self.revision, attestation.SPDX, expected)
        with patch.object(attestation.subprocess, 'run', side_effect=self.gh), \
                self.assertRaisesRegex(ValueError, 'subject and predicate'):
            attestation.verify_attestation(path, 'sha256:' + '0' * 64, self.revision, attestation.PROVENANCE)

    def test_failed_live_verification_and_wrong_signer_revision_fail_closed(self):
        path = self.qualified / 'candidate' / attestation.ARCHIVE_NAME
        with patch.object(attestation.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, ['gh'])), \
                self.assertRaisesRegex(ValueError, 'verification failed'):
            attestation.verify_attestation(path, self.record['artifact']['sha256'], self.revision, attestation.PROVENANCE)
        with self.assertRaisesRegex(ValueError, 'protected verifier'):
            attestation.verify_signed(self.original_deb, self.original, self.qualified,
                self.source, self.verifier, self.revision, '0' * 40)

    def test_package_mutation_during_live_verification_is_rejected(self):
        def mutate(*_args):
            (self.qualified / 'candidate' / attestation.ARCHIVE_NAME).write_bytes(b'changed after attestation')
            return 'sha256:' + '1' * 64
        with patch.object(attestation, 'verify_attestation', side_effect=mutate), \
                self.assertRaisesRegex(ValueError, 'original builder'):
            self.signed()


if __name__ == '__main__':
    unittest.main()

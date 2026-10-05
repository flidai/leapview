import copy
import io
import json
import os
from pathlib import Path
import struct
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_candidate_manifest as candidate
import nix_cli_publication as publication


def elf(machine=62, segments=(1,)):
    identification = b'\x7fELF' + bytes([2, 1, 1]) + bytes(9)
    header = struct.pack('<HHIQQQIHHHHHH', 2, machine, 1, 0, 64, 0, 0, 64, 56,
                         len(segments), 0, 0, 0)
    return identification + header + b''.join(
        struct.pack('<IIQQQQQQ', kind, 5, 0, 0, 0, 0, 0, 1) for kind in segments)


class CliPublicationTests(unittest.TestCase):
    revision = 'a' * 40
    build_time = '2026-10-03T00:00:00Z'
    version = '0.3.0-alpha.1+nix.aaaaaaaaaaaa'

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source_root = self.root / 'source'
        self.source_root.mkdir()
        (self.source_root / 'VERSION').write_text('0.3.0-alpha.1\n')
        self.archive = self.root / 'leapviewctl-linux-amd64.tar.gz'
        self.identity_path = self.root / 'archive-identity.json'
        self.identity = {'platform': 'linux/amd64', 'version': self.version,
                         'sourceRevision': self.revision}
        self.write_archive()
        self.identity_path.write_text(json.dumps(self.identity))
        self.evidence_dir = self.root / 'evidence'
        self.verifier = self.root / 'go-binary-verifier'
        self.verifier.write_text('#!/bin/sh\nexit 0\n')
        self.verifier.chmod(0o755)
        self.source = {'repository': 'flidai/leapview', 'revision': self.revision,
                       'inputs': [{'path': 'flake.lock', 'sha256': candidate.digest_bytes(b'lock')}]}
        self.build_info = {
            'Path': 'github.com/flidai/leapview/cmd/leapviewctl',
            'GoVersion': 'go1.26.8',
            'Main': {'Path': 'github.com/flidai/leapview', 'Version': '(devel)'},
            'Deps': [{'Path': 'example.com/dependency', 'Version': 'v1.2.3'}],
            'Settings': [{'Key': key, 'Value': value} for key, value in [
                ('GOOS', 'linux'), ('GOARCH', 'amd64'), ('CGO_ENABLED', '0'), ('GOAMD64', 'v1')]],
        }
        self.commands = []
        self.runtime_identity = {'product': 'leapviewctl', 'version': self.version, 'revision': self.revision,
                                 'buildTime': self.build_time, 'dirty': False, 'development': True}
        self.os_releases = {
            'debian12': b'PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"\nID=debian\nVERSION_ID="12"\n',
            'ubuntu2404': b'PRETTY_NAME="Ubuntu 24.04 LTS"\nID=ubuntu\nVERSION_ID="24.04"\n',
            'debian13': b'PRETTY_NAME="Debian GNU/Linux 13 (trixie)"\nID=debian\nVERSION_ID="13"\n',
        }
        self.syft_version = {'version': publication.SYFT_VERSION}
        self.spdx_mode = 'valid'
        self.runtime_mode = 'valid'
        self.fail_fixture = None
        self.bad_os_fixture = None
        self.bad_runtime_fixture = None
        self.oversized_fixture = None
        self.attestation_calls = 0

    def write_archive(self, members=None):
        members = members or [('leapviewctl', elf())]
        with tarfile.open(self.archive, 'w:gz') as output:
            for name, body in members:
                info = tarfile.TarInfo(name)
                if isinstance(body, bytes):
                    info.mode = 0o755
                    info.size = len(body)
                    output.addfile(info, io.BytesIO(body))
                else:
                    info.type, info.linkname = body
                    output.addfile(info)

    def module_package(self, name, version):
        locator = 'pkg:golang/' + name
        if version != 'UNKNOWN':
            locator += '@' + version
        return {'SPDXID': 'SPDXRef-' + name.replace('/', '-'), 'name': name,
                'versionInfo': version,
                'externalRefs': [{'referenceCategory': 'PACKAGE-MANAGER',
                                  'referenceType': 'purl', 'referenceLocator': locator}]}

    def spdx(self, binary_path):
        data = Path(binary_path).read_bytes()
        digest = candidate.digest_bytes(data).removeprefix('sha256:')
        stdlib = self.module_package('stdlib', '1.26.8')
        stdlib['versionInfo'] = 'go1.26.8'
        packages = [
            self.module_package('github.com/flidai/leapview', 'UNKNOWN'),
            self.module_package('example.com/dependency', 'v1.2.3'),
            stdlib,
            {'SPDXID': 'SPDXRef-DocumentRoot-File-leapviewctl', 'name': 'leapviewctl',
             'versionInfo': 'sha256:' + digest, 'primaryPackagePurpose': 'FILE',
             'checksums': [{'algorithm': 'SHA256', 'checksumValue': digest}]},
        ]
        if self.spdx_mode == 'empty':
            packages = []
        if self.spdx_mode == 'wrong-version':
            packages[1]['versionInfo'] = 'v9.9.9'
            packages[1]['externalRefs'][0]['referenceLocator'] = 'pkg:golang/example.com/dependency@v9.9.9'
        document = {'spdxVersion': 'SPDX-2.3', 'SPDXID': 'SPDXRef-DOCUMENT',
                    'documentNamespace': 'https://example.test/spdx/controller',
                    'creationInfo': {'creators': ['Tool: syft-1.52.0']},
                    'packages': packages,
                    'relationships': [{'spdxElementId': 'SPDXRef-DOCUMENT',
                                       'relatedSpdxElement': 'SPDXRef-DocumentRoot-File-leapviewctl',
                                       'relationshipType': 'DESCRIBES'}]}
        return json.dumps(document, separators=(',', ':')).encode()

    def fake_run(self, args, **kwargs):
        args = [str(item) for item in args]
        self.commands.append((args, kwargs))
        if args[0] == str(self.verifier):
            if '-verify-binary-evidence' not in args:
                binary = Path(args[args.index('-binary') + 1])
                report_dir = Path(args[args.index('-binary-evidence') + 1])
                report_dir.mkdir(parents=True)
                raw = b'{"fixture":"go vuln report"}\n'
                (report_dir / 'govulncheck.json').write_bytes(raw)
                (report_dir / 'summary.json').write_text(json.dumps({
                    'binarySHA256': candidate.digest_file(binary),
                    'reportSHA256': candidate.digest_bytes(raw),
                    'buildInfo': {'Path': 'github.com/flidai/leapview/cmd/leapviewctl'},
                    'scanner': {'version': 'fixture'}, 'scannedAt': self.build_time,
                }))
            return subprocess.CompletedProcess(args, 0, stdout=b'')
        if args[:3] == ['syft', 'version', '-o']:
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps(self.syft_version).encode())
        if args[0] == 'syft':
            output = Path(next(item.split('=', 1)[1] for item in args if item.startswith('spdx-json=')))
            binary = args[1].removeprefix('file:')
            output.write_bytes(self.spdx(binary))
            return subprocess.CompletedProcess(args, 0, stdout=b'')
        if args[:2] == ['docker', 'pull']:
            self.assertIn(args[-1], [fixture['image'] for fixture in publication.HOST_FIXTURES])
            return subprocess.CompletedProcess(args, 0, stdout=b'')
        if args[:3] == ['docker', 'rm', '--force']:
            return subprocess.CompletedProcess(args, 0, stdout=b'')
        if args[:2] == ['docker', 'run']:
            fixture_image = next((fixture['image'] for fixture in publication.HOST_FIXTURES
                                  if fixture['image'] in args), None)
            self.assertIsNotNone(fixture_image)
            fixture = next(item for item in publication.HOST_FIXTURES
                           if item['image'] == fixture_image)
            self.assertIn('--network', args)
            self.assertEqual(args[args.index('--network') + 1], 'none')
            self.assertIn('--read-only', args)
            self.assertIn('--cap-drop', args)
            self.assertEqual(args[args.index('--cap-drop') + 1], 'ALL')
            self.assertIn('65534:65534', args)
            self.assertIn('no-new-privileges', args)
            self.assertEqual(args[args.index('--memory') + 1], '256m')
            self.assertEqual(args[args.index('--cpus') + 1], '1')
            self.assertEqual(args[args.index('--pids-limit') + 1], '64')
            self.assertEqual(args[args.index('--ulimit') + 1], 'core=0')
            self.assertNotIn('--privileged', args)
            if args[args.index('--entrypoint') + 1] == '/usr/local/bin/leapviewctl':
                self.assertIn('readonly', args[args.index('--mount') + 1])
                mounted = args[args.index('--mount') + 1].split('src=', 1)[1].split(',', 1)[0]
                self.assertEqual(Path(mounted).stat().st_mode & 0o777, 0o755)
            else:
                self.assertNotIn('--mount', args)
                self.assertEqual(args[args.index('--entrypoint') + 1], '/bin/cat')
            self.assertIn('--name', args)
            self.assertEqual(args[args.index('--log-driver') + 1], 'none')
            self.assertTrue(hasattr(kwargs['stderr'], 'write'))
            self.assertIsNotNone(kwargs.get('preexec_fn'))
            self.assertEqual(args[args.index('--platform') + 1], self.identity['platform'])
            self.assertEqual(kwargs['timeout'], 45)
            if fixture['id'] == self.fail_fixture:
                raise subprocess.CalledProcessError(1, args)
            if args[args.index('--entrypoint') + 1] == '/bin/cat':
                self.assertEqual(args[-1], '/etc/os-release')
                output = self.os_releases[fixture['id']]
                if fixture['id'] == self.bad_os_fixture:
                    output = b'ID=wrong\nVERSION_ID=0\n'
                kwargs['stdout'].write(output)
                return subprocess.CompletedProcess(args, 0)
            if self.runtime_mode == 'timeout':
                raise subprocess.TimeoutExpired(args, 45)
            if args[-2:] == ['version', '--json']:
                runtime = dict(self.runtime_identity)
                if self.runtime_mode == 'wrong-revision':
                    runtime['revision'] = 'f' * 40
                if fixture['id'] == self.bad_runtime_fixture:
                    runtime['version'] = 'stale'
                output = json.dumps(runtime).encode()
            else:
                output = b'leapviewctl usage fixture\n'
            if self.runtime_mode == 'oversized':
                output = b'x' * (publication.MAX_RUNTIME_BYTES + 1)
            if fixture['id'] == self.oversized_fixture:
                output = b'x' * (publication.MAX_RUNTIME_BYTES + 1)
            if self.runtime_mode == 'stderr-oversized':
                kwargs['stderr'].write(b'x' * (publication.MAX_RUNTIME_BYTES + 1))
            kwargs['stdout'].write(output)
            return subprocess.CompletedProcess(args, 0)
        if args[:3] == ['gh', 'attestation', 'verify']:
            document = json.loads((self.evidence_dir / 'sbom.spdx.json').read_bytes())
            digest = candidate.digest_file(self.archive).removeprefix('sha256:')
            subject = [{'name': self.archive.name, 'digest': {'sha256': digest}}]
            if '--predicate-type' not in args:
                raise AssertionError('predicate type is required')
            predicate_type = args[args.index('--predicate-type') + 1]
            if predicate_type == publication.PROVENANCE:
                predicate = {'buildType': 'https://example.test/build'}
            else:
                predicate = document
            if self.runtime_mode == 'wrong-subject':
                subject = [{'name': 'other.tar.gz', 'digest': {'sha256': digest}}]
            statement = {'_type': 'https://in-toto.io/Statement/v1', 'subject': subject,
                         'predicateType': predicate_type, 'predicate': predicate}
            payload = json.dumps([{'verificationResult': {'statement': statement}}]).encode()
            kwargs['stdout'].write(payload)
            self.attestation_calls += 1
            if self.runtime_mode == 'change-after-attest' and self.attestation_calls == 2:
                with (self.evidence_dir / 'sbom.spdx.json').open('ab') as report:
                    report.write(b' ')
            return subprocess.CompletedProcess(args, 0)
        raise AssertionError('unexpected command: ' + ' '.join(args))

    def run_qualify(self):
        with patch.object(publication.os, 'geteuid', return_value=1000), \
                patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run):
            return publication.qualify(self.archive, self.identity_path, self.source_root,
                                       self.revision, self.verifier, self.evidence_dir)

    def run_verify(self, source=None):
        with patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=source or self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run):
            return publication.verify(self.archive, self.identity_path, self.source_root,
                                      self.revision, self.verifier, self.evidence_dir)

    def run_probe_hosts(self):
        with patch.object(publication.os, 'geteuid', return_value=1000), \
                patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run):
            return publication.probe_hosts(self.archive, self.identity_path, self.source_root,
                                           self.revision, self.evidence_dir)

    def test_qualification_binds_exact_source_archive_runtime_static_spdx_and_go_evidence(self):
        manifest = self.run_qualify()
        report = json.loads((self.evidence_dir / 'qualification.json').read_bytes())
        self.assertFalse(manifest['releaseAdmission'])
        self.assertFalse(report['releaseAdmission'])
        self.assertEqual(report['candidateDigest'], manifest['candidateDigest'])
        self.assertEqual(manifest['artifact']['sha256'], candidate.digest_file(self.archive))
        self.assertEqual(manifest['source'], self.source)
        self.assertEqual(report['schemaVersion'], 2)
        self.assertEqual([host['id'] for host in report['hosts']],
                         [fixture['id'] for fixture in publication.HOST_FIXTURES])
        for host, fixture in zip(report['hosts'], publication.HOST_FIXTURES):
            self.assertEqual(host, {'id': fixture['id'], 'image': fixture['image'],
                                    'platform': 'linux/amd64', 'machine': 'x86_64',
                                    'runtimeIdentity': self.runtime_identity})
        self.assertEqual(report['platform'], 'linux/amd64')
        self.assertEqual(report['goEvidence'], manifest['evidence']['go-binaries'])
        self.assertEqual(manifest['evidence'][publication.QUALIFICATION_KEY]['staticReport']['cgoEnabled'], False)
        self.assertEqual({record['path'] for record in report['reports']}, set(publication.REPORTS))
        docker_runs = [args for args, _ in self.commands if args[:2] == ['docker', 'run']]
        self.assertEqual(len(docker_runs), len(publication.HOST_FIXTURES) * 4)
        self.assertEqual(sum(args[args.index('--entrypoint') + 1] == '/bin/cat'
                             for args in docker_runs), len(publication.HOST_FIXTURES))
        self.assertEqual(sum(args[args.index('--entrypoint') + 1] == '/usr/local/bin/leapviewctl'
                             for args in docker_runs), len(publication.HOST_FIXTURES) * 3)
        pulls = [args for args, _ in self.commands if args[:2] == ['docker', 'pull']]
        self.assertEqual([args[-1] for args in pulls],
                         [fixture['image'] for fixture in publication.HOST_FIXTURES])
        self.assertTrue(all('--privileged' not in args for args in docker_runs))
        self.assertTrue(all('--bundle' not in args for args, _ in self.commands))
        self.assertFalse(any(args[0] == str(self.archive) for args, _ in self.commands))
        self.assertEqual({record['path'] for record in report['reports']}, set(publication.REPORTS))
        scan = next(args for args, _ in self.commands if args[0] == str(self.verifier)
                    and '-verify-binary-evidence' not in args)
        self.assertEqual(scan[scan.index('-root') + 1], str(candidate.ROOT))
        self.assertNotEqual(scan[scan.index('-root') + 1], str(self.source_root))

    def test_offline_verify_rechecks_go_reports_and_rejects_every_bound_report_change(self):
        self.run_qualify()
        self.commands.clear()
        manifest = self.run_verify()
        self.assertEqual(manifest['candidateDigest'],
                         json.loads((self.evidence_dir / 'candidate-manifest.json').read_bytes())['candidateDigest'])
        self.assertFalse(any(args[0] in {'docker', 'syft', 'gh'} for args, _ in self.commands))
        for filename in publication.REPORTS:
            path = self.evidence_dir / filename
            original = path.read_bytes()
            path.write_bytes(original + b'changed')
            with self.subTest(filename=filename), self.assertRaises(ValueError):
                self.run_verify()
            path.write_bytes(original)
        report = self.evidence_dir / 'go' / 'leapviewctl' / 'summary.json'
        original = report.read_bytes()
        report.write_bytes(original + b' ')
        with self.assertRaises(ValueError):
            self.run_verify()
        report.write_bytes(original)

    def test_source_change_archive_identity_and_symlink_reports_are_rejected(self):
        self.run_qualify()
        changed_source = {**self.source,
                          'inputs': [{'path': 'flake.lock', 'sha256': candidate.digest_bytes(b'changed')}]}
        with self.assertRaises(ValueError):
            self.run_verify(changed_source)
        original_identity = self.identity_path.read_bytes()
        altered = dict(self.identity, version='1.2.3+nix.' + self.revision[:12])
        self.identity_path.write_text(json.dumps(altered))
        with self.assertRaises(ValueError):
            self.run_verify()
        self.identity_path.write_bytes(original_identity)
        report = self.evidence_dir / 'debian12-runtime-help.txt'
        content = report.read_bytes()
        report.unlink()
        report.symlink_to(self.archive)
        with self.assertRaises(ValueError):
            self.run_verify()
        report.unlink()
        report.write_bytes(content)

    def test_qualification_rejects_non_native_root_mismatched_runtime_and_bad_archives(self):
        with patch.object(publication.os, 'geteuid', return_value=0), \
                patch.object(publication.subprocess, 'run') as run, self.assertRaisesRegex(ValueError, 'unprivileged'):
            publication.qualify(self.archive, self.identity_path, self.source_root, self.revision,
                                 self.verifier, self.evidence_dir)
        run.assert_not_called()
        with patch.object(publication.os, 'geteuid', return_value=1000), \
                patch.object(publication, '_native_arch', return_value=('arm64', 'aarch64')), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication.subprocess, 'run') as run, self.assertRaisesRegex(ValueError, 'native'):
            publication.qualify(self.archive, self.identity_path, self.source_root, self.revision,
                                 self.verifier, self.evidence_dir)
        run.assert_not_called()
        with patch.object(publication.os, 'geteuid', return_value=1000), \
                patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run):
            self.runtime_mode = 'wrong-revision'
            with self.assertRaisesRegex(ValueError, 'runtime identity'):
                publication.qualify(self.archive, self.identity_path, self.source_root,
                                    self.revision, self.verifier, self.evidence_dir)
        self.assertFalse((self.evidence_dir / 'candidate-manifest.json').exists())

        self.evidence_dir = self.root / 'malformed-evidence'
        self.commands.clear()
        self.write_archive([('leapviewctl', elf()), ('extra', b'unexpected')])
        self.identity_path.write_text(json.dumps(self.identity))
        with patch.object(publication.os, 'geteuid', return_value=1000), \
                patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run), self.assertRaises(ValueError):
            publication.qualify(self.archive, self.identity_path, self.source_root,
                                self.revision, self.verifier, self.evidence_dir)
        self.assertFalse(any(args[0] == 'docker' for args, _ in self.commands))

    def test_host_probe_output_and_container_lifetime_are_bounded(self):
        for mode in ('oversized', 'stderr-oversized', 'timeout'):
            with self.subTest(mode=mode):
                self.evidence_dir = self.root / ('limited-' + mode)
                self.runtime_mode = mode
                self.commands.clear()
                with patch.object(publication.os, 'geteuid', return_value=1000), \
                        patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                        patch.object(publication, '_source_build_identity',
                                     return_value=(self.version, self.build_time)), \
                        patch.object(publication, '_trusted_source', return_value=self.source), \
                        patch.object(publication.subprocess, 'check_output',
                                     return_value=json.dumps(self.build_info).encode()), \
                        patch.object(publication.subprocess, 'run', side_effect=self.fake_run), \
                        self.assertRaises(ValueError):
                    publication.qualify(self.archive, self.identity_path, self.source_root,
                                        self.revision, self.verifier, self.evidence_dir)
                self.assertTrue(any(args[:3] == ['docker', 'rm', '--force']
                                    for args, _ in self.commands))
                self.assertFalse((self.evidence_dir / 'candidate-manifest.json').exists())
        self.runtime_mode = 'valid'

    def test_offline_verifier_reconstructs_all_fixture_os_and_runtime_evidence(self):
        self.run_qualify()
        for fixture in publication.HOST_FIXTURES:
            os_report = self.evidence_dir / (fixture['id'] + '-os-release.txt')
            runtime_report = self.evidence_dir / (fixture['id'] + '-runtime-version.json')
            self.assertEqual(os_report.read_bytes(), self.os_releases[fixture['id']])
            self.assertEqual(json.loads(runtime_report.read_bytes()), self.runtime_identity)
        self.run_verify()

    def test_offline_verifier_rejects_inconsistent_later_host_and_matrix_reordering(self):
        self.run_qualify()
        os_report = self.evidence_dir / 'debian13-os-release.txt'
        original_os = os_report.read_bytes()
        os_report.write_bytes(b'ID=debian\nVERSION_ID=12\n')
        self._reseal_qualification_report('debian13-os-release.txt')
        with self.assertRaisesRegex(ValueError, 'OS release'):
            self.run_verify()
        os_report.write_bytes(original_os)
        self.run_qualify_again_after_cleanup()

        report_path = self.evidence_dir / 'qualification.json'
        manifest_path = self.evidence_dir / 'candidate-manifest.json'
        report = json.loads(report_path.read_bytes())
        report['hosts'][0], report['hosts'][1] = report['hosts'][1], report['hosts'][0]
        self._write_resealed(report, report_path, manifest_path)
        with self.assertRaisesRegex(ValueError, 'manifest|qualification'):
            self.run_verify()

    def test_offline_verifier_rejects_missing_extra_duplicate_and_forged_hosts(self):
        self.run_qualify()
        missing = self.evidence_dir / 'debian13-runtime-help.txt'
        original = missing.read_bytes()
        missing.unlink()
        with self.assertRaisesRegex(ValueError, 'inventory'):
            self.run_verify()
        missing.write_bytes(original)
        extra = self.evidence_dir / 'unexpected-host.txt'
        extra.write_text('extra')
        with self.assertRaisesRegex(ValueError, 'inventory'):
            self.run_verify()
        extra.unlink()

        report_path = self.evidence_dir / 'qualification.json'
        manifest_path = self.evidence_dir / 'candidate-manifest.json'
        for mutation in ('duplicate', 'forged-image'):
            self.run_qualify_again_after_cleanup()
            report = json.loads(report_path.read_bytes())
            if mutation == 'duplicate':
                report['hosts'][1] = copy.deepcopy(report['hosts'][0])
            else:
                report['hosts'][2]['image'] = publication.HOST_FIXTURES[0]['image']
            self._write_resealed(report, report_path, manifest_path)
            with self.subTest(mutation=mutation), self.assertRaisesRegex(ValueError, 'manifest|qualification'):
                self.run_verify()

    def _reseal_qualification_report(self, changed_report):
        report_path = self.evidence_dir / 'qualification.json'
        manifest_path = self.evidence_dir / 'candidate-manifest.json'
        report = json.loads(report_path.read_bytes())
        for record in report['reports']:
            if record['path'] == changed_report:
                record['sha256'] = candidate.digest_file(self.evidence_dir / changed_report)
        self._write_resealed(report, report_path, manifest_path)

    def _write_resealed(self, report, report_path, manifest_path):
        manifest = json.loads(manifest_path.read_bytes())
        qualification = {key: value for key, value in report.items() if key != 'candidateDigest'}
        manifest['evidence'][publication.QUALIFICATION_KEY] = qualification
        manifest.pop('candidateDigest', None)
        manifest['candidateDigest'] = candidate.digest_bytes(
            b'leapview/nix-candidate-manifest/v1\n' + candidate.canonical_bytes(manifest))
        report['candidateDigest'] = manifest['candidateDigest']
        manifest_path.write_bytes(json.dumps(manifest, indent=2).encode() + b'\n')
        report_path.write_bytes(json.dumps(report, indent=2).encode() + b'\n')

    def run_qualify_again_after_cleanup(self):
        import shutil
        shutil.rmtree(self.evidence_dir)
        self.commands.clear()
        self.run_qualify()

    def test_probe_hosts_writes_matrix_receipt_without_duplicate_scans(self):
        receipt = self.run_probe_hosts()
        self.assertEqual(receipt['scope'], 'static-cli-host-compatibility')
        self.assertEqual(receipt['archive']['sha256'], candidate.digest_file(self.archive))
        self.assertEqual(receipt['staticBinarySHA256'], receipt['staticReport']['binarySHA256'])
        self.assertEqual(receipt['source'], self.source)
        self.assertEqual([host['id'] for host in receipt['hosts']],
                         [fixture['id'] for fixture in publication.HOST_FIXTURES])
        self.assertFalse(receipt['releaseAdmission'])
        self.assertEqual({path.name for path in self.evidence_dir.iterdir()},
                         {'static.json', 'host-compatibility.json', *(
                             fixture['id'] + suffix for fixture in publication.HOST_FIXTURES
                             for suffix in ('-os-release.txt', '-runtime-version.json',
                                            '-runtime-help.txt', '-runtime-host-help.txt'))})
        self.assertEqual(self.evidence_dir.stat().st_mode & 0o777, 0o700)
        self.assertFalse(any(args[0] in {'syft', 'gh'} or args[0] == str(self.verifier)
                             for args, _ in self.commands))

    def test_probe_hosts_rejects_later_fixture_failure_and_cleans_containers(self):
        self.evidence_dir = self.root / 'failed-probe'
        self.fail_fixture = 'debian13'
        with self.assertRaisesRegex(ValueError, 'host probe'):
            self.run_probe_hosts()
        runs = [args for args, _ in self.commands if args[:2] == ['docker', 'run']]
        removed = [args[3] for args, _ in self.commands if args[:3] == ['docker', 'rm', '--force']]
        self.assertEqual(len(runs), 9)
        self.assertEqual(len(removed), len(runs))
        self.assertEqual([args[args.index('--name') + 1] for args in runs], removed)
        self.assertFalse((self.evidence_dir / 'host-compatibility.json').exists())

    def test_probe_hosts_rejects_later_fixture_oversized_output_and_cleans_container(self):
        self.evidence_dir = self.root / 'oversized-later-probe'
        self.oversized_fixture = 'debian13'
        with self.assertRaisesRegex(ValueError, 'output|host-probe'):
            self.run_probe_hosts()
        runs = [args for args, _ in self.commands if args[:2] == ['docker', 'run']]
        removed = [args[3] for args, _ in self.commands if args[:3] == ['docker', 'rm', '--force']]
        self.assertEqual(len(runs), 10)
        self.assertEqual(len(removed), len(runs))
        self.assertEqual([args[args.index('--name') + 1] for args in runs], removed)
        self.assertFalse((self.evidence_dir / 'host-compatibility.json').exists())

    def test_probe_hosts_rejects_inconsistent_os_release_and_runtime_on_later_fixture(self):
        for bad_fixture in ('debian13-os', 'debian13-runtime'):
            self.evidence_dir = self.root / bad_fixture
            if bad_fixture.endswith('-os'):
                self.bad_os_fixture = 'debian13'
            else:
                self.bad_runtime_fixture = 'debian13'
            with self.subTest(bad_fixture=bad_fixture), self.assertRaises(ValueError):
                self.run_probe_hosts()
            self.assertFalse((self.evidence_dir / 'host-compatibility.json').exists())
            import shutil
            shutil.rmtree(self.evidence_dir)
            self.commands.clear()
            self.bad_os_fixture = None
            self.bad_runtime_fixture = None

    def test_source_version_and_build_time_come_from_trusted_checkout(self):
        source = self.source
        with patch.object(publication.subprocess, 'check_output', return_value=b'1790985600\n') as git:
            version, build_time = publication._source_build_identity(
                self.source_root, source, self.identity)
        self.assertEqual(version, self.version)
        self.assertEqual(build_time, self.build_time)
        self.assertEqual(git.call_args.args[0][0:3], ['git', '-C', str(self.source_root)])
        wrong = dict(self.identity, version='development')
        with self.assertRaisesRegex(ValueError, 'source VERSION'):
            publication._source_build_identity(self.source_root, source, wrong)

    def test_attestation_match_requires_one_exact_archive_subject_and_spdx_predicate(self):
        document = {'spdxVersion': 'SPDX-2.3', 'packages': []}
        entries = [{'verificationResult': {'statement': {
            '_type': 'https://in-toto.io/Statement/v1',
            'subject': [{'name': self.archive.name,
                         'digest': {'sha256': candidate.digest_file(self.archive).removeprefix('sha256:')}}],
            'predicateType': publication.SPDX_PREDICATE, 'predicate': document}}}]
        digest = candidate.digest_file(self.archive)
        self.assertTrue(publication._match_attestation(
            entries, self.archive, digest, publication.SPDX_PREDICATE, document))
        self.assertTrue(publication._match_attestation(
            entries * 2, self.archive, digest, publication.SPDX_PREDICATE, document))
        altered = copy.deepcopy(entries)
        altered[0]['verificationResult']['statement']['predicate']['packages'] = [{'name': 'wrong'}]
        with self.assertRaises(ValueError):
            publication._match_attestation(altered, self.archive, digest,
                                            publication.SPDX_PREDICATE, document)
        altered = copy.deepcopy(entries)
        altered[0]['verificationResult']['statement']['subject'].append(
            {'name': 'other', 'digest': {'sha256': 'b' * 64}})
        with self.assertRaises(ValueError):
            publication._match_attestation(altered, self.archive, digest,
                                            publication.SPDX_PREDICATE, document)

    def test_empty_or_wrong_version_spdx_and_wrong_syft_are_rejected(self):
        for mode in ('empty', 'wrong-version'):
            self.evidence_dir = self.root / ('evidence-' + mode)
            self.spdx_mode = mode
            with self.subTest(mode=mode), patch.object(publication.os, 'geteuid', return_value=1000), \
                    patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                    patch.object(publication, '_source_build_identity',
                                 return_value=(self.version, self.build_time)), \
                    patch.object(publication, '_trusted_source', return_value=self.source), \
                    patch.object(publication.subprocess, 'check_output',
                                 return_value=json.dumps(self.build_info).encode()), \
                    patch.object(publication.subprocess, 'run', side_effect=self.fake_run), self.assertRaises(ValueError):
                publication.qualify(self.archive, self.identity_path, self.source_root,
                                    self.revision, self.verifier, self.evidence_dir)
        self.evidence_dir = self.root / 'wrong-syft'
        self.spdx_mode = 'valid'
        self.syft_version = {'version': '1.51.0'}
        with patch.object(publication.os, 'geteuid', return_value=1000), \
                patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run), \
                self.assertRaisesRegex(ValueError, 'pinned Syft'):
            publication.qualify(self.archive, self.identity_path, self.source_root,
                                self.revision, self.verifier, self.evidence_dir)

    def test_signed_verification_uses_live_gh_provenance_and_exact_spdx_without_bundle(self):
        self.run_qualify()
        self.commands.clear()
        output = self.root / 'signed' / 'binding.json'
        with patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run):
            binding = publication.verify_signed(self.archive, self.identity_path, self.source_root,
                self.revision, self.verifier, self.evidence_dir, 'b' * 40, output)
        self.assertEqual(json.loads(output.read_bytes()), binding)
        self.assertFalse(binding['releaseAdmission'])
        self.assertEqual(binding['archive']['basename'], self.archive.name)
        calls = [args for args, _ in self.commands if args[:3] == ['gh', 'attestation', 'verify']]
        self.assertEqual(len(calls), 2)
        for args in calls:
            self.assertEqual(args[3], str(self.archive))
            self.assertIn('--deny-self-hosted-runners', args)
            self.assertNotIn('--bundle', args)
            self.assertEqual(args[args.index('--source-ref') + 1], 'refs/heads/main')
            self.assertEqual(args[args.index('--source-digest') + 1], 'b' * 40)

    def test_signed_verification_rejects_wrong_subject_before_writing_binding(self):
        self.run_qualify()
        self.runtime_mode = 'wrong-subject'
        output = self.root / 'must-not-exist.json'
        with patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run), \
                self.assertRaisesRegex(ValueError, 'exact archive subject'):
            publication.verify_signed(self.archive, self.identity_path, self.source_root,
                self.revision, self.verifier, self.evidence_dir, 'b' * 40, output)
        self.assertFalse(output.exists())

    def test_signed_verification_rechecks_evidence_after_live_attestation_calls(self):
        self.run_qualify()
        self.runtime_mode = 'change-after-attest'
        self.attestation_calls = 0
        output = self.root / 'changed-during-signature.json'
        with patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run), \
                self.assertRaises(ValueError):
            publication.verify_signed(self.archive, self.identity_path, self.source_root,
                self.revision, self.verifier, self.evidence_dir, 'b' * 40, output)
        self.assertFalse(output.exists())

    def test_offline_arm64_archive_verifies_on_amd64_without_candidate_execution(self):
        self.archive = self.root / 'leapviewctl-linux-arm64.tar.gz'
        self.write_archive([('leapviewctl', elf(machine=183))])
        self.identity = {'platform': 'linux/arm64', 'version': self.version,
                         'sourceRevision': self.revision}
        self.identity_path.write_text(json.dumps(self.identity))
        self.build_info['Settings'][1]['Value'] = 'arm64'
        self.build_info['Settings'][-1] = {'Key': 'GOARM64', 'Value': 'v8.0'}
        with patch.object(publication.os, 'geteuid', return_value=1000), \
                patch.object(publication, '_native_arch', return_value=('arm64', 'aarch64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run):
            publication.qualify(self.archive, self.identity_path, self.source_root,
                                self.revision, self.verifier, self.evidence_dir)
        self.commands.clear()
        with patch.object(publication, '_native_arch', return_value=('amd64', 'x86_64')), \
                patch.object(publication, '_source_build_identity',
                             return_value=(self.version, self.build_time)), \
                patch.object(publication, '_trusted_source', return_value=self.source), \
                patch.object(publication.subprocess, 'check_output',
                             return_value=json.dumps(self.build_info).encode()), \
                patch.object(publication.subprocess, 'run', side_effect=self.fake_run):
            verified = publication.verify(self.archive, self.identity_path, self.source_root,
                                           self.revision, self.verifier, self.evidence_dir)
        self.assertEqual(verified['artifact']['platform'], 'linux/arm64')
        self.assertFalse(any(args[0] in {'docker', 'syft', 'gh'} for args, _ in self.commands))


class CliHostRuntimeTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get('LEAPVIEW_TEST_NIX_CLI_RUNTIME') == '1',
                         'requires the pinned Docker host fixture')
    def test_real_host_probe_bounds_stderr_and_preserves_normal_output(self):
        import uuid
        from types import SimpleNamespace

        arch, _ = publication._native_arch()
        for mode in ('normal', 'stderr-flood'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                binary = root / 'leapviewctl'
                flood = "head -c 16777216 /dev/zero | tr '\\000' x >&2\n" if mode == 'stderr-flood' else ''
                binary.write_text('#!/bin/sh\n' + flood +
                                  'if [ "$1" = version ]; then\n'
                                  '  printf \'{"fixture":"bounded"}\\n\'\n'
                                  'else\n  printf \'fixture help\\n\'\nfi\n')
                binary.chmod(0o755)
                evidence = root / 'evidence'
                evidence.mkdir()
                identifiers = [uuid.uuid4().hex for _ in publication.HOST_FIXTURES
                               for _ in range(len(publication.RUNTIME_COMMANDS) + 1)]
                names = ['leapview-cli-probe-' + identifier for identifier in identifiers]
                generated = iter(identifiers)
                with patch.object(publication.uuid, 'uuid4',
                                  side_effect=lambda: SimpleNamespace(hex=next(generated))):
                    if mode == 'stderr-flood':
                        with self.assertRaisesRegex(ValueError, 'host.probe'):
                            publication._run_host(binary, arch, evidence)
                    else:
                        hosts, machine = publication._run_host(binary, arch, evidence)
                        self.assertEqual([host['id'] for host in hosts],
                                         [fixture['id'] for fixture in publication.HOST_FIXTURES])
                        self.assertTrue(all(host['runtimeIdentity'] == {'fixture': 'bounded'}
                                            for host in hosts))
                        self.assertEqual(machine, publication.ARCH_MACHINE[arch])
                        for fixture in publication.HOST_FIXTURES:
                            publication._validate_os_release(
                                (evidence / (fixture['id'] + '-os-release.txt')).read_bytes(), fixture)
                            self.assertEqual((evidence / (fixture['id'] + '-runtime-help.txt')).read_bytes(),
                                             b'fixture help\n')
                            self.assertEqual((evidence / (fixture['id'] + '-runtime-host-help.txt')).read_bytes(),
                                             b'fixture help\n')
                for name in names:
                    inspected = subprocess.run(['docker', 'inspect', name],
                                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    self.assertNotEqual(inspected.returncode, 0, 'probe container was not removed')



if __name__ == '__main__':
    unittest.main()

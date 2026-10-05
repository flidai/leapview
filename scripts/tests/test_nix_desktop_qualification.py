import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import tarfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('desktop_qualification', ROOT / 'scripts/nix_desktop_qualification.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class DesktopQualificationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.verifier = self.root / 'protected'
        self.source = self.root / 'candidate'
        self.verifier.mkdir()
        self.write_checkout(self.verifier)
        self.git(self.verifier, 'init', '-q')
        self.git(self.verifier, 'config', 'user.name', 'Desktop candidate test')
        self.git(self.verifier, 'config', 'user.email', 'desktop@example.test')
        self.git(self.verifier, 'add', '.')
        self.git(self.verifier, 'commit', '-m', 'protected qualification fixture')
        subprocess.run(['git', 'clone', '-q', '--local', str(self.verifier), str(self.source)], check=True)
        self.git(self.source, 'config', 'user.name', 'Desktop candidate test')
        self.git(self.source, 'config', 'user.email', 'desktop@example.test')
        self.revision = self.git(self.source, 'rev-parse', 'HEAD')
        self.deb = self.root / 'leapview-desktop-linux-x64.deb'
        self.deb.write_bytes(b'fixed Debian candidate bytes')

    @staticmethod
    def write_checkout(root):
        files = {
            'flake.nix': '{}\n',
            'flake.lock': '{}\n',
            'go.mod': 'module fixture\n',
            'go.sum': '',
            'desktop/package.json': json.dumps({'name': '@leapview/desktop', 'version': '0.1.0'}),
            'desktop/bun.lock': '{}\n',
            'desktop/release-policy.json': '{}\n',
            'desktop/scripts/verify-package.mjs': 'protected verifier\n',
            'desktop/scripts/verify-installer.mjs': 'protected installer verifier\n',
            'desktop/scripts/release-evidence.mjs': 'protected release evidence\n',
            'desktop/scripts/verify-release-evidence.mjs': 'protected release evidence verifier\n',
            'desktop/scripts/qualify-installer-linux.sh': '#!/bin/sh\n',
            'internal/app/testing/maliciousinstance/packaged_proof_test.go': 'package maliciousinstance\n',
            'nix/desktop.nix': '{}\n',
            'scripts/nix_candidate_manifest.py': '# protected candidate binder\n',
            'scripts/nix_desktop_qualification.py': '# protected desktop qualification\n',
        }
        for relative, content in files.items():
            path = root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)

    @staticmethod
    def git(root, *arguments):
        return subprocess.check_output(['git', '-C', str(root), *arguments], text=True).strip()

    @staticmethod
    def control_fields(**overrides):
        fields = {
            'package': 'leapview-desktop',
            'version': '0.1.0',
            'architecture': 'amd64',
            'installed-size': '290538',
            **m.EXPECTED_DEB_CONTROL,
        }
        fields.update(overrides)
        return fields

    def test_os_release_requires_native_ubuntu_2204(self):
        os_release = self.root / 'os-release'
        os_release.write_text('ID=ubuntu\nVERSION_ID="22.04"\n')
        self.assertEqual(m.require_host(os_release, 'x86_64'), {
            'id': 'ubuntu', 'versionID': '22.04', 'architecture': 'x86_64', 'nativeExecution': True,
        })
        for data, machine in [('ID=ubuntu\nVERSION_ID=24.04\n', 'x86_64'),
                              ('ID=debian\nVERSION_ID=22.04\n', 'x86_64'),
                              ('ID=ubuntu\nVERSION_ID=22.04\n', 'aarch64')]:
            os_release.write_text(data)
            with self.subTest(data=data, machine=machine), self.assertRaisesRegex(ValueError, 'Ubuntu 22.04'):
                m.require_host(os_release, machine)
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            m.os_release_identity('ID=ubuntu\nID=debian\n')

    def test_candidate_binds_control_identity_exact_bytes_and_source(self):
        with patch.object(m, 'deb_control_fields', return_value=self.control_fields()):
            record, identity = m.candidate_record(self.deb, self.source, self.revision, self.verifier)
        self.assertEqual(identity, {'Package': 'leapview-desktop', 'Version': '0.1.0', 'Architecture': 'amd64'})
        self.assertEqual(record['artifact']['format'], 'deb')
        self.assertEqual(record['artifact']['platform'], 'linux/amd64')
        self.assertEqual(record['artifact']['sha256'], 'sha256:' + hashlib.sha256(self.deb.read_bytes()).hexdigest())
        self.assertEqual(record['source']['revision'], self.revision)
        self.assertEqual(record['requiredReleaseEvidence'], sorted(m.candidate.DESKTOP_GATES))
        self.assertIs(record['releaseAdmission'], False)

        with patch.object(m, 'deb_control_fields', return_value=self.control_fields(package='other')):
            with self.assertRaisesRegex(ValueError, 'control identity'):
                m.candidate_record(self.deb, self.source, self.revision, self.verifier)
        with patch.object(m, 'deb_control_fields', return_value=self.control_fields()):
            with self.assertRaisesRegex(ValueError, 'source checkout'):
                m.candidate_record(self.deb, self.source, 'a' * 40, self.verifier)

    def test_deb_control_contract_rejects_injected_dependencies_and_conflicts(self):
        with self.assertRaisesRegex(ValueError, 'control field differs.*depends'):
            m.validate_deb_control(self.control_fields(depends='curl'), '0.1.0')
        with self.assertRaisesRegex(ValueError, 'control field inventory'):
            m.validate_deb_control(self.control_fields(conflicts='base-files'), '0.1.0')
        with self.assertRaisesRegex(ValueError, 'control field inventory'):
            m.validate_deb_control(self.control_fields(pre_depends='malicious-preinstall'), '0.1.0')

    def test_candidate_protected_policy_drift_is_rejected(self):
        (self.source / 'desktop/bun.lock').write_text('{"changed":true}\n')
        self.git(self.source, 'add', 'desktop/bun.lock')
        self.git(self.source, 'commit', '-m', 'candidate lock change')
        revision = self.git(self.source, 'rev-parse', 'HEAD')
        with patch.object(m, 'deb_control_fields', return_value=self.control_fields()), \
                self.assertRaisesRegex(ValueError, 'differs'):
            m.candidate_record(self.deb, self.source, revision, self.verifier)

    def test_failed_commands_without_text_stderr_become_qualification_errors(self):
        with patch.object(m.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, ['missing'])):
            with self.assertRaisesRegex(ValueError, 'qualification command failed: missing'):
                m.run_command(['missing'])
        with patch.object(m.subprocess, 'run', side_effect=subprocess.TimeoutExpired(['dpkg-deb'], 1,
                                                                                     stderr=b'first\nlast')):
            with self.assertRaisesRegex(ValueError, 'qualification command failed: dpkg-deb: last'):
                m.command_output(['dpkg-deb'])

    def test_partial_candidate_installation_is_removed_and_payload_verified(self):
        installed = self.root / 'installed' / 'usr/lib/leapview-desktop/LeapView'
        installed.parent.mkdir(parents=True)
        installed.write_bytes(b'candidate payload')
        statuses = iter(['install ok unpacked', 'deinstall ok config-files'])
        commands = []

        def fake_run(arguments, **_kwargs):
            if arguments[0] == 'dpkg-query':
                return subprocess.CompletedProcess(arguments, 0, next(statuses), '')
            raise AssertionError('unexpected subprocess: ' + ' '.join(arguments))

        def fake_command(arguments, **_kwargs):
            commands.append(arguments)
            if arguments[:3] == ['sudo', 'dpkg', '--remove']:
                installed.unlink()

        with patch.object(m, 'OWNED_INSTALL_PATHS', [installed]), \
                patch.object(m.subprocess, 'run', side_effect=fake_run), \
                patch.object(m, 'run_command', side_effect=fake_command):
            m.remove_candidate_installation(self.verifier)
        self.assertEqual(commands, [['sudo', 'dpkg', '--remove', '--force-remove-reinstreq', m.PACKAGE_NAME]])
        self.assertFalse(installed.exists())

    @staticmethod
    def package_member(name, kind='file', mode=0o644, linkname=''):
        member = tarfile.TarInfo(name)
        member.uid = member.gid = 0
        member.mode = mode
        if kind == 'directory':
            member.type = tarfile.DIRTYPE
        elif kind == 'symlink':
            member.type = tarfile.SYMTYPE
            member.linkname = linkname
        else:
            member.type = tarfile.REGTYPE
        return member

    def valid_members(self):
        dirs = ['', 'usr', 'usr/bin', 'usr/lib', 'usr/lib/leapview-desktop',
                'usr/share', 'usr/share/applications']
        files = ['usr/lib/leapview-desktop/LeapView',
                 'usr/lib/leapview-desktop/chrome-sandbox',
                 'usr/share/applications/leapview-desktop.desktop']
        members = [self.package_member(path or '.', 'directory', 0o755) for path in dirs]
        members.extend([self.package_member(files[0], mode=0o755),
                        self.package_member(files[1], mode=0o4755),
                        self.package_member(files[2]),
                        self.package_member('usr/bin/leapview-desktop', 'symlink', 0o777,
                                            '../lib/leapview-desktop/LeapView')])
        return members

    def test_deb_payload_contract_rejects_escape_paths_extra_privilege_and_wrong_helper_mode(self):
        self.assertIsNotNone(m.validate_package_members(self.valid_members()))
        mutations = [
            self.package_member('etc/profile.d/runner.sh'),
            self.package_member('usr/lib/leapview-desktop/extra', mode=0o4755),
            self.package_member('usr/lib/leapview-desktop/extra', mode=0o2755),
        ]
        for extra in mutations:
            with self.subTest(extra=extra.name), self.assertRaises(ValueError):
                m.validate_package_members(self.valid_members() + [extra])
        bad_helper = [self.package_member(member.name, 'file', 0o755)
                      if member.name == 'usr/lib/leapview-desktop/chrome-sandbox' else member
                      for member in self.valid_members()]
        with self.assertRaisesRegex(ValueError, 'setuid 4755'):
            m.validate_package_members(bad_helper)
        regular_launcher = [self.package_member(member.name, mode=0o755)
                            if member.name == 'usr/bin/leapview-desktop' else member
                            for member in self.valid_members()]
        with self.assertRaisesRegex(ValueError, 'must remain the reviewed symbolic link'):
            m.validate_package_members(regular_launcher)
        directory_executable = [self.package_member(member.name, 'directory', 0o755)
                                if member.name == 'usr/lib/leapview-desktop/LeapView' else member
                                for member in self.valid_members()]
        with self.assertRaisesRegex(ValueError, 'wrong entry type'):
            m.validate_package_members(directory_executable)

    def test_tar_inspection_bounds_members_bytes_and_reaps_dpkg(self):
        class FakeArchive:
            def __init__(self, members):
                self.members = members

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def __iter__(self):
                return iter(self.members)

        oversized = self.package_member('oversized')
        oversized.size = m.MAX_DEB_MEMBER_BYTES + 1
        total_overflow = [self.package_member('part-' + str(index)) for index in range(5)]
        for member in total_overflow:
            member.size = m.MAX_DEB_MEMBER_BYTES
        cases = [
            ([self.package_member('first'), self.package_member('second')], 1,
             'member count exceeds'),
            ([oversized], 10, 'member exceeds the qualification size bound'),
            (total_overflow, 10, 'unpacked content exceeds the qualification size bound'),
        ]
        for members, maximum_members, message in cases:
            with self.subTest(message=message):
                process = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(0.5)'],
                                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                with patch.object(m.subprocess, 'Popen', return_value=process), \
                        patch.object(m.tarfile, 'open', return_value=FakeArchive(members)), \
                        self.assertRaisesRegex(ValueError, message):
                    m.deb_tar_headers(self.deb, '--fsys-tarfile', maximum_members)
                self.assertIsNotNone(process.poll())
                self.assertTrue(process.stdout.closed)
                self.assertTrue(process.stderr.closed)

    def test_tar_extended_header_is_rejected_before_tarfile_consumes_its_body(self):
        header = tarfile.TarInfo('PaxHeader')
        header.type = tarfile.XHDTYPE
        header.size = m.MAX_DEB_MEMBER_BYTES + 1
        code = ("import sys,time;sys.stdout.buffer.write(bytes.fromhex('" + header.tobuf().hex()
                + "'));sys.stdout.flush();time.sleep(0.5)")
        process = subprocess.Popen([sys.executable, '-c', code], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        with patch.object(m.subprocess, 'Popen', return_value=process), \
                self.assertRaisesRegex(ValueError, 'extended or sparse tar headers'):
            m.deb_tar_headers(self.deb, '--fsys-tarfile', 10)
        self.assertIsNotNone(process.poll())
        self.assertTrue(process.stdout.closed)
        self.assertTrue(process.stderr.closed)

    def test_deb_control_scripts_are_rejected_before_root_install(self):
        root = self.verifier / 'desktop/out'
        root.mkdir(parents=True)
        archive_members = [self.package_member('.', 'directory', 0o755),
                           self.package_member('./control'),
                           self.package_member('./postinst', mode=0o755)]
        with patch.object(m, 'deb_tar_headers', return_value=archive_members), \
                self.assertRaisesRegex(ValueError, 'maintainer script'):
            m.reject_maintainer_scripts(self.deb, self.verifier)

    def test_candidate_verifier_files_are_never_used_as_protected_tools(self):
        target = self.source / 'desktop/scripts/verify-package.mjs'
        target.write_text('candidate controlled verifier that must not be executed\n')
        self.git(self.source, 'add', 'desktop/scripts/verify-package.mjs')
        self.git(self.source, 'commit', '-m', 'candidate verifier substitution')
        revision = self.git(self.source, 'rev-parse', 'HEAD')
        with patch.object(m, 'deb_control_fields', return_value=self.control_fields()):
            record, _ = m.candidate_record(self.deb, self.source, revision, self.verifier)
        verifier = m.verifier_identity(self.verifier)
        candidate_text = target.read_text()
        self.assertNotIn(candidate_text.strip(), [Path(self.verifier / item['path']).read_text().strip()
                                                  for item in verifier['files']])

        calls = []
        def capture(arguments, *, cwd=None, env=None):
            calls.append((arguments, Path(cwd), env))
        staged = self.verifier / 'desktop/out/LeapView-linux-x64'
        staged.mkdir(parents=True)
        copied_archive = self.verifier / 'desktop/out/make/leapview-desktop-linux-x64.deb'
        copied_archive.parent.mkdir(parents=True, exist_ok=True)
        with (patch.object(m, 'run_command', side_effect=capture),
              patch.object(m, 'refuse_preexisting_installation'),
              patch.object(m, 'OWNED_INSTALL_PATHS', []),
              patch.object(m, 'installed_executable', return_value=Path('/usr/lib/leapview-desktop/LeapView')),
              patch.object(m.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, 'deinstall ok config-files', ''))):
            m.run_desktop_verifiers(self.verifier, copied_archive, staged,
                                    Path('usr/lib/leapview-desktop/LeapView'), revision)
        self.assertTrue(calls)
        self.assertTrue(all(path == self.verifier / 'desktop' or path == self.verifier for _, path, _ in calls))
        self.assertTrue(any(args[:4] == ['xvfb-run', '--auto-servernum', 'node_modules/node/bin/node',
                                         'scripts/verify-package.mjs']
                            for args, _, _ in calls))
        self.assertTrue(any(args[0].endswith('/desktop/scripts/qualify-installer-linux.sh')
                            for args, _, _ in calls))
        self.assertNotIn(self.source.as_posix(), '\n'.join(' '.join(args) for args, _, _ in calls))
        self.assertEqual(record['source']['revision'], revision)

    def test_relative_cli_roots_are_resolved_before_verification(self):
        captured = {}

        def capture(archive, source_root, verifier_root, source_revision, evidence_directory):
            captured.update(archive=archive, source_root=source_root, verifier_root=verifier_root,
                            source_revision=source_revision, evidence_directory=evidence_directory)
            return {'candidateDigest': 'sha256:' + 'a' * 64, 'artifact': {}, 'result': 'success'}

        previous_directory = Path.cwd()
        try:
            os.chdir(self.root)
            arguments = ['nix_desktop_qualification.py', 'verify', '--deb', self.deb.name,
                         '--source-root', self.source.name, '--verifier-root', self.verifier.name,
                         '--source-revision', self.revision, '--evidence-dir', 'relative-evidence']
            with patch('sys.argv', arguments), patch.object(m, 'verify_report', side_effect=capture), \
                    patch('sys.stdout', new_callable=io.StringIO):
                m.main()
        finally:
            os.chdir(previous_directory)

        self.assertEqual(captured['archive'], self.deb.absolute())
        self.assertEqual(captured['source_root'], self.source.resolve())
        self.assertEqual(captured['verifier_root'], self.verifier.resolve())
        self.assertEqual(captured['evidence_directory'], (self.root / 'relative-evidence').resolve())
        self.assertEqual(captured['source_revision'], self.revision)

    def test_verifier_rejects_changed_deb_report_evidence_and_admission(self):
        with patch.object(m, 'deb_control_fields', return_value=self.control_fields()):
            record, _ = m.candidate_record(self.deb, self.source, self.revision, self.verifier)
        evidence = self.root / 'evidence'
        evidence.mkdir()
        (evidence / 'package-verification.json').write_text(json.dumps({
            'platform': 'linux', 'architecture': 'x64', 'packageFormat': 'deb',
            'startup': 'trusted-shell-ready',
        }))
        (evidence / 'candidate.release.json').write_text(json.dumps({
            'source': {'commit': self.revision, 'dirty': False},
            'artifact': {'sha256': record['artifact']['sha256'].removeprefix('sha256:'), 'format': 'deb'},
            'application': {'packageName': '@leapview/desktop', 'version': '0.1.0'},
            'support': {'minimumVersion': 'Ubuntu 22.04 LTS', 'qualification': 'candidate'},
            'signing': {'state': 'unsigned-candidate', 'productionEligible': False, 'identity': None},
        }))
        (evidence / 'candidate.spdx.json').write_text(json.dumps({'spdxVersion': 'SPDX-2.3', 'packages': [{}]}))
        (evidence / 'checksums.txt').write_text('candidate hashes\n')
        (evidence / 'verify-release-evidence.mjs').write_text('protected verifier\n')
        (evidence / 'candidate-manifest.json').write_text(json.dumps(record, sort_keys=True))
        evidence_items = m.evidence_files(evidence, record)
        report = m.qualification_report(record, {
            'id': 'ubuntu', 'versionID': '22.04', 'architecture': 'x86_64', 'nativeExecution': True,
        }, m.verifier_identity(self.verifier), m.aligned_policy_inputs(self.source, self.verifier), evidence_items)
        (evidence / 'qualification-report.json').write_text(json.dumps(report, indent=2, sort_keys=True) + '\n')
        os_release = self.root / 'os-release'
        os_release.write_text('ID=ubuntu\nVERSION_ID=22.04\n')
        with patch.object(m, 'deb_control_fields', return_value=self.control_fields()):
            verified = m.verify_report(self.deb, self.source, self.verifier, self.revision, evidence,
                                       os_release, 'x86_64')
        self.assertEqual(verified['candidateDigest'], record['candidateDigest'])

        bad = json.loads((evidence / 'qualification-report.json').read_text())
        bad['releaseAdmission'] = True
        (evidence / 'qualification-report.json').write_text(json.dumps(bad))
        with patch.object(m, 'deb_control_fields', return_value=self.control_fields()), \
                self.assertRaisesRegex(ValueError, 'report'):
            m.verify_report(self.deb, self.source, self.verifier, self.revision, evidence, os_release, 'x86_64')

        (evidence / 'qualification-report.json').write_text(json.dumps(report))
        (evidence / 'checksums.txt').write_text('changed\n')
        with patch.object(m, 'deb_control_fields', return_value=self.control_fields()), \
                self.assertRaisesRegex(ValueError, 'report'):
            m.verify_report(self.deb, self.source, self.verifier, self.revision, evidence, os_release, 'x86_64')


if __name__ == '__main__':
    unittest.main()

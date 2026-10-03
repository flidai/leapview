import copy
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_archive_go_evidence as evidence
import nix_candidate_manifest as candidate
from test_nix_archive_go_evidence import pax_header, raw_member


class CliGoEvidenceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.archive = self.root / 'cli.tar.gz'
        self.source = {'repository': 'flidai/leapview', 'revision': 'a' * 40,
                       'inputs': [{'path': 'flake.lock', 'sha256': candidate.digest_bytes(b'lock')}]}
        self.identity = {'platform': 'linux/arm64', 'version': 'test', 'sourceRevision': self.source['revision']}
        self.write_archive([('leapviewctl', b'controller')])

    def write_archive(self, entries, mode=0o755):
        with tarfile.open(self.archive, 'w:gz') as archive:
            for name, value in entries:
                member = tarfile.TarInfo(name)
                member.mode = mode
                if isinstance(value, bytes):
                    member.size = len(value)
                    archive.addfile(member, io.BytesIO(value))
                else:
                    member.type, member.linkname = value
                    archive.addfile(member)
        self.refresh_identity()

    def refresh_identity(self):
        self.artifact = candidate.collect(self.archive, 'cli-archive', self.source,
                                          archive_identity=self.identity)['artifact']

    def extract(self):
        with tempfile.TemporaryDirectory(dir=self.root) as destination:
            return {name: path.read_bytes() for name, path in
                    evidence.extract(self.archive, self.artifact, Path(destination)).items()}

    def test_exact_single_binary_is_read_without_execution(self):
        self.assertEqual(self.extract(), {'leapviewctl': b'controller'})

    def test_duplicate_extra_redirected_traversing_and_nonexecutable_members_fail(self):
        cases = [[('leapviewctl', b'clean'), ('leapviewctl', b'other')],
                 [('leapviewctl', b'clean'), ('extra', b'x')],
                 [('leapviewctl', (tarfile.SYMTYPE, 'other'))],
                 [('leapviewctl', (tarfile.LNKTYPE, 'other'))],
                 [('../leapviewctl', b'x')], [('/leapviewctl', b'x')], []]
        for entries in cases:
            self.write_archive(entries)
            with self.subTest(entries=entries), self.assertRaises(ValueError):
                self.extract()
        for mode in [0o644, 0o4755]:
            self.write_archive([('leapviewctl', b'x')], mode)
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                self.extract()

    def test_ambiguous_extended_headers_and_size_overrides_fail(self):
        for prefix in [pax_header({'path': 'leapviewctl'}, tarfile.XGLTYPE),
                       pax_header({'size': ''}), pax_header({'GNU.sparse.name': 'leapviewctl'})]:
            self.archive.write_bytes(prefix + raw_member('leapviewctl', b'controller') + b'\0' * 1024)
            self.refresh_identity()
            with self.assertRaisesRegex(ValueError, 'extended header'):
                self.extract()

    def test_size_archive_mutation_and_unsupported_kind_fail(self):
        with patch.object(evidence, 'MAX_BINARY_BYTES', 2), self.assertRaises(ValueError):
            self.extract()
        self.archive.write_bytes(b'changed')
        with self.assertRaises(ValueError):
            self.extract()
        self.write_archive([('leapviewctl', b'controller')])
        self.artifact['kind'] = 'desktop-archive'
        with self.assertRaises(ValueError):
            self.extract()

    def test_exact_reports_bind_manifest_and_cannot_be_removed_or_substituted(self):
        reports = self.root / 'go'
        commands = []
        def run(args, **kwargs):
            commands.append(args)
            if '-verify-binary-evidence' in args:
                return
            binary = Path(args[args.index('-binary') + 1])
            target = Path(args[args.index('-binary-evidence') + 1])
            target.mkdir()
            raw = b'{"config":"fixture"}\n'
            (target / 'govulncheck.json').write_bytes(raw)
            (target / 'summary.json').write_text(json.dumps({
                'binarySHA256': candidate.digest_file(binary), 'reportSHA256': candidate.digest_bytes(raw),
                'buildInfo': {'Path': args[args.index('-binary-package') + 1]},
                'scanner': {'scan_mode': 'binary'}, 'scannedAt': '2026-10-03T07:00:00Z'}))
        verifier = Path('/trusted/verifier')
        options = {'archive_identity': self.identity, 'go_dir': reports, 'binary_verifier': verifier}
        with patch.object(evidence.subprocess, 'run', side_effect=run):
            evidence.scan(self.archive, self.artifact, reports, verifier)
            manifest = candidate.collect(self.archive, 'cli-archive', self.source, **options)
            candidate.verify(manifest, self.archive, self.source, **options)
            self.assertFalse(manifest['releaseAdmission'])
            self.assertEqual(manifest['evidence']['go-binaries']['binaries'][0]['path'], 'leapviewctl')
            self.assertTrue(all(args[0] == str(verifier) for args in commands))
            self.assertTrue(all(args[args.index('-binary-platform') + 1] == 'linux/arm64' for args in commands))
            self.assertTrue(all(args[args.index('-binary-package') + 1] ==
                                'github.com/flidai/leapview/cmd/leapviewctl' for args in commands))
            with self.assertRaises(ValueError):
                candidate.verify(manifest, self.archive, self.source, archive_identity=self.identity)
            for field in ['archiveSHA256', 'scope']:
                changed = copy.deepcopy(manifest)
                changed['evidence']['go-binaries'][field] = 'wrong'
                with self.assertRaises(ValueError):
                    candidate.verify(changed, self.archive, self.source, **options)
            (reports / 'leapviewctl' / 'govulncheck.json').write_bytes(b'substituted')
            with self.assertRaises(ValueError):
                candidate.verify(manifest, self.archive, self.source, **options)

    @unittest.skipUnless(os.environ.get('LEAPVIEW_TEST_GO_ARCHIVE') == '1', 'requires locked Go and live govulncheck')
    def test_real_cross_architecture_scanner_rejects_wrong_platform_and_package(self):
        module = self.root / 'module'
        package = module / 'cmd' / 'leapviewctl'
        package.mkdir(parents=True)
        (module / 'go.mod').write_text('module github.com/flidai/leapview\n\ngo 1.26.8\n')
        (package / 'main.go').write_text('package main\nfunc main() {}\n')
        binary = self.root / 'leapviewctl'
        verifier = Path(os.environ['LEAPVIEW_TEST_GO_BINARY_VERIFIER']).resolve()
        for arch in ['amd64', 'arm64']:
            subprocess.run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags=-w', '-o', str(binary),
                            './cmd/leapviewctl'], cwd=module, env={**os.environ, 'CGO_ENABLED': '0',
                            'GOOS': 'linux', 'GOARCH': arch}, check=True)
            self.identity['platform'] = 'linux/' + arch
            self.write_archive([('leapviewctl', binary.read_bytes())])
            reports = self.root / ('go-' + arch)
            evidence.scan(self.archive, self.artifact, reports, verifier)
            result = evidence.verify(self.archive, self.artifact, reports, verifier)
            self.assertEqual(result['binaries'][0]['buildInfo']['Path'], 'github.com/flidai/leapview/cmd/leapviewctl')
            self.artifact['platform'] = 'linux/' + ('arm64' if arch == 'amd64' else 'amd64')
            with self.assertRaises(ValueError):
                evidence.verify(self.archive, self.artifact, reports, verifier)
            self.artifact['platform'] = 'linux/' + arch
            with patch.object(evidence, 'CLI_ENTRYPOINTS', [{'id': 'leapviewctl', 'path': 'leapviewctl',
                    'package': 'github.com/flidai/leapview/cmd/leapview'}]), self.assertRaises(ValueError):
                evidence.verify(self.archive, self.artifact, reports, verifier)


if __name__ == '__main__':
    unittest.main()

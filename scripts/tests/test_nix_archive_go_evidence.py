import copy
import hashlib
import io
import json
import os
from pathlib import Path
import sys
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_candidate_manifest as candidate
import nix_archive_go_evidence as evidence


def layer(entries, mode=0o755):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode='w') as archive:
        for name, value in entries:
            member = tarfile.TarInfo(name)
            member.mode = mode
            if isinstance(value, bytes):
                member.size = len(value)
                archive.addfile(member, io.BytesIO(value))
            else:
                member.type, member.linkname = value
                archive.addfile(member)
    return output.getvalue()


def raw_member(name, data, kind=tarfile.REGTYPE):
    member = tarfile.TarInfo(name)
    member.type, member.mode, member.size = kind, 0o755, len(data)
    return (member.tobuf(format=tarfile.USTAR_FORMAT) + data
            + b'\0' * (-len(data) % tarfile.BLOCKSIZE))


def pax_header(fields, kind=tarfile.XHDTYPE):
    records = b''
    for key, value in fields.items():
        data = (key + '=' + value + '\n').encode()
        length = len(data) + 2
        while length != len(str(length)) + 1 + len(data):
            length = len(str(length)) + 1 + len(data)
        records += str(length).encode() + b' ' + data
    return raw_member('PaxHeaders/entry', records, kind)


class ArchiveGoEvidenceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.archive = self.root / 'image.tar'
        self.source = {'repository': 'flidai/leapview', 'revision': 'a' * 40,
                       'inputs': [{'path': 'flake.lock', 'sha256': candidate.digest_bytes(b'lock')}]}
        self.files = [(entry['path'], b'app' if entry['id'] == 'leapview' else b'controller')
                      for entry in evidence.ENTRYPOINTS]
        self.write_archive([layer(self.files)])

    def write_archive(self, layers, order=None, config_override=None):
        config = {'os': 'linux', 'architecture': 'amd64',
                  'rootfs': {'type': 'layers', 'diff_ids': [candidate.digest_bytes(data) for data in layers]},
                  'config': {'Entrypoint': ['/usr/local/bin/leapview'], 'Env': ['PATH=/usr/local/bin:/busybox/bin'],
                             'Healthcheck': {'Test': ['CMD', '/usr/local/bin/leapview', 'healthcheck']}, 'Labels': {
                      'org.opencontainers.image.source': 'https://github.com/flidai/leapview',
                      'org.opencontainers.image.revision': self.source['revision'],
                      'org.opencontainers.image.version': 'test', 'dev.leapview.build.dirty': 'false',
                      'dev.leapview.build.kind': 'application-image'}}}
        if config_override:
            config['config'].update(config_override)
        encoded = json.dumps(config).encode()
        config_name = hashlib.sha256(encoded).hexdigest() + '.json'
        names = [f'{index}/layer.tar' for index in range(len(layers))]
        members = [('manifest.json', json.dumps([{'Config': config_name, 'Layers': names}]).encode()),
                   (config_name, encoded)]
        members.extend((names[index], layers[index]) for index in (order or range(len(layers))))
        with tarfile.open(self.archive, 'w') as archive:
            for name, data in members:
                member = tarfile.TarInfo(name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        self.artifact = candidate.image_identity(self.archive, self.source, 'application-image')
        self.artifact.update(kind='application-image', sha256=candidate.digest_file(self.archive))

    def extract(self):
        destination = self.root / 'binaries'
        destination.mkdir()
        return evidence.extract(self.archive, self.artifact, destination)

    def test_extracts_exact_declared_entrypoints_without_running_them(self):
        result = self.extract()
        self.assertEqual(set(result), {entry['id'] for entry in evidence.ENTRYPOINTS})
        self.assertEqual([result[entry['id']].read_bytes() for entry in evidence.ENTRYPOINTS],
                         [value for _, value in self.files])
        self.assertTrue(all(path.stat().st_mode & 0o777 == 0o600 for path in result.values()))

    def test_final_regular_overlay_wins_in_manifest_order_not_outer_tar_order(self):
        replacement = [(path, b'new-app' if path.endswith('/bin/leapview') else b'new-controller')
                       for path, _ in self.files]
        self.write_archive([layer(self.files), layer(replacement)], order=[1, 0])
        result = self.extract()
        self.assertEqual(result['leapview'].read_bytes(), b'new-app')
        self.assertEqual(result['leapviewctl'].read_bytes(), b'new-controller')

    def test_conflicting_extended_headers_cannot_hide_the_runtime_binary(self):
        app = evidence.ENTRYPOINTS[0]['path']
        pax = pax_header({'path': app})
        gnu = raw_member('././@LongLink', b'usr/local/bin/unused\0', tarfile.GNUTYPE_LONGNAME)
        chains = [pax + gnu, gnu + pax,
                  pax + pax_header({'path': 'usr/local/bin/unused'}),
                  gnu + raw_member('././@LongLink', app.encode() + b'\0', tarfile.GNUTYPE_LONGNAME)]
        for index, headers in enumerate(chains):
            with self.subTest(chain=index):
                replacement = headers + raw_member('placeholder', b'clean-replacement') + b'\0' * 1024
                self.write_archive([layer(self.files), replacement])
                try:
                    with self.assertRaisesRegex(ValueError, 'extended header'):
                        self.extract()
                finally:
                    shutil.rmtree(self.root / 'binaries')

    def test_global_pax_path_cannot_replace_the_scanned_path(self):
        replacement = (pax_header({'path': evidence.ENTRYPOINTS[0]['path']}, tarfile.XGLTYPE)
                       + raw_member('usr/local/bin/unused', b'clean-replacement') + b'\0' * 1024)
        self.write_archive([layer(self.files), replacement])
        with self.assertRaisesRegex(ValueError, 'extended header'):
            self.extract()

    def test_independent_pax_and_gnu_headers_and_gnu_name_link_pair_are_supported(self):
        app = evidence.ENTRYPOINTS[0]['path']
        replacement = pax_header({'path': app}) + raw_member('placeholder', b'new-app')
        # Normal GNU archives can need both a long name and a long link target.
        replacement += raw_member('././@LongLink', b'elsewhere/' + b'x' * 120 + b'\0', tarfile.GNUTYPE_LONGNAME)
        replacement += raw_member('././@LongLink', b'/nix/store/' + b'y' * 120 + b'\0', tarfile.GNUTYPE_LONGLINK)
        replacement += raw_member('placeholder', b'', tarfile.SYMTYPE) + b'\0' * 1024
        self.write_archive([layer(self.files), replacement])
        self.assertEqual(self.extract()['leapview'].read_bytes(), b'new-app')

    def test_single_extended_header_replacement_is_supported_and_bounded(self):
        app = evidence.ENTRYPOINTS[0]['path']
        for header in [pax_header({'path': app}), raw_member('././@LongLink', app.encode() + b'\0', tarfile.GNUTYPE_LONGNAME)]:
            self.write_archive([layer(self.files), header + raw_member('placeholder', b'new-app') + b'\0' * 1024])
            self.assertEqual(self.extract()['leapview'].read_bytes(), b'new-app')
            shutil.rmtree(self.root / 'binaries')
            with patch.object(evidence, 'MAX_EXTENDED_HEADER_BYTES', 2), self.assertRaisesRegex(ValueError, 'byte limit'):
                self.extract()
            shutil.rmtree(self.root / 'binaries')

    def test_pax_sparse_name_without_a_sparse_map_cannot_redirect_the_scan(self):
        replacement = (pax_header({'GNU.sparse.name': evidence.ENTRYPOINTS[0]['path']})
                       + raw_member('usr/local/bin/unused', b'clean-replacement') + b'\0' * 1024)
        self.write_archive([layer(self.files), replacement])
        with self.assertRaisesRegex(ValueError, 'extended header'):
            self.extract()

    def test_pax_size_override_cannot_expose_fake_headers_inside_a_file(self):
        hidden = raw_member(evidence.ENTRYPOINTS[0]['path'], b'clean-replacement') + b'\0' * 1024
        replacement = pax_header({'size': ''}) + raw_member('unrelated', hidden) + b'\0' * 1024
        self.write_archive([layer(self.files), replacement])
        with self.assertRaisesRegex(ValueError, 'extended header'):
            self.extract()

    def test_basename_launch_cannot_fall_through_to_an_unscanned_path(self):
        content = io.BytesIO()
        with tarfile.open(fileobj=content, mode='w') as archive:
            for name, data in [*self.files, ('alternate/leapview', b'unscanned')]:
                member = tarfile.TarInfo(name)
                member.uid = member.gid = 1000
                member.mode = 0o001 if name == self.files[0][0] else 0o755
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        for override in [{'Entrypoint': ['leapview']},
                         {'Healthcheck': {'Test': ['CMD', 'leapview', 'healthcheck']}}]:
            self.write_archive([content.getvalue()], config_override={
                'User': '1000:1000', 'Env': ['PATH=/usr/local/bin:/alternate'], **override})
            with self.subTest(override=override), self.assertRaisesRegex(ValueError, 'redirects'):
                self.extract()
            shutil.rmtree(self.root / 'binaries')

    def test_absolute_launch_keeps_additional_path_binaries_outside_scan_selection(self):
        self.write_archive([layer([*self.files, ('alternate/leapview', b'unscanned')])], config_override={
            'Env': ['PATH=/usr/local/bin:/alternate']})
        self.assertEqual(self.extract()['leapview'].read_bytes(), b'app')

    def test_container_absolute_paths_are_canonicalized_without_host_extraction(self):
        self.write_archive([layer([('/' + path, data) for path, data in self.files])])
        self.assertEqual(self.extract()['leapview'].read_bytes(), b'app')

    def test_runtime_entrypoint_path_or_healthcheck_redirection_is_rejected(self):
        for override in [{'Entrypoint': ['/evil']}, {'Entrypoint': ['/usr/local/bin/leapview', 'serve']},
                         {'Env': ['PATH=/evil:/usr/local/bin']},
                         {'Env': ['PATH=/usr/local/bin', 'PATH=/evil']},
                         {'Healthcheck': {'Test': ['CMD', '/evil', 'healthcheck']}}]:
            self.write_archive([layer(self.files)], config_override=override)
            with self.subTest(override=override), self.assertRaises(ValueError):
                self.extract()
            shutil.rmtree(self.root / 'binaries')

    def test_missing_binary_and_altered_controller_replica_fail(self):
        for entries in [self.files[:-1], [*self.files[:-1], (self.files[-1][0], b'other-controller')]]:
            with self.subTest(entries=entries):
                self.write_archive([layer(entries)])
                with self.assertRaises(ValueError):
                    self.extract()
                if (self.root / 'binaries').exists():
                    shutil.rmtree(self.root / 'binaries')

    def test_target_or_ancestor_symlinks_hardlinks_and_whiteouts_are_rejected(self):
        redirects = [('usr', (tarfile.SYMTYPE, '/elsewhere')),
                     (self.files[0][0], (tarfile.SYMTYPE, '/evil')),
                     (self.files[1][0], (tarfile.LNKTYPE, self.files[-1][0])),
                     ('usr/local/bin/.wh.leapview', b''),
                     ('usr/.wh.local', b''), ('usr/local/.wh..wh..opq', b'')]
        for redirect in redirects:
            with self.subTest(redirect=redirect):
                self.write_archive([layer(self.files), layer([redirect])])
                with self.assertRaises(ValueError):
                    self.extract()
                if (self.root / 'binaries').exists():
                    shutil.rmtree(self.root / 'binaries')

    def test_duplicate_paths_traversal_nonexecutable_and_oversized_binary_fail(self):
        for entries in [[*self.files, self.files[0]], [*self.files, ('../escape', b'x')]]:
            self.write_archive([layer(entries)])
            with self.assertRaises(ValueError):
                self.extract()
            shutil.rmtree(self.root / 'binaries')
        self.write_archive([layer(self.files, mode=0o644)])
        with self.assertRaises(ValueError):
            self.extract()
        shutil.rmtree(self.root / 'binaries')
        self.write_archive([layer(self.files)])
        with patch.object(evidence, 'MAX_BINARY_BYTES', 2), self.assertRaises(ValueError):
            self.extract()

    def test_changed_layer_or_archive_cannot_substitute_scanner_input(self):
        self.artifact['layerDiffIDs'][0] = candidate.digest_bytes(b'other-layer')
        with self.assertRaises(ValueError):
            self.extract()
        shutil.rmtree(self.root / 'binaries')
        self.write_archive([layer(self.files)])
        original = copy.deepcopy(self.artifact)
        self.write_archive([layer([(path, b'changed') for path, _ in self.files])])
        self.artifact = original
        with self.assertRaises(ValueError):
            self.extract()

    def test_offline_binding_runs_only_protected_verifier_and_retains_exact_reports(self):
        directory = self.root / 'go'
        commands = []
        def run(args, **kwargs):
            commands.append((args, kwargs))
            if '-verify-binary-evidence' in args:
                return
            binary = Path(args[args.index('-binary') + 1])
            reports = Path(args[args.index('-binary-evidence') + 1])
            reports.mkdir()
            raw = b'{"config":"scanner fixture"}\n'
            (reports / 'govulncheck.json').write_bytes(raw)
            (reports / 'summary.json').write_text(json.dumps({
                'binarySHA256': candidate.digest_file(binary), 'buildInfo': {'Path': args[args.index('-binary-package') + 1]},
                'scanner': {'scan_mode': 'binary'}, 'scannedAt': '2026-10-03T03:00:00Z',
                'reportSHA256': candidate.digest_bytes(raw)}))
        with patch.object(evidence.subprocess, 'run', side_effect=run):
            evidence.scan(self.archive, self.artifact, directory, Path('/protected/verifier'))
            result = evidence.verify(self.archive, self.artifact, directory, Path('/protected/verifier'))
        self.assertEqual(len(commands), 6)
        self.assertTrue(all(args[0] == '/protected/verifier' for args, _ in commands))
        self.assertTrue(all('-verify-binary-evidence' in args for args, _ in commands[3:]))
        self.assertEqual(result['archiveSHA256'], self.artifact['sha256'])
        self.assertEqual(result['scope'], 'go-binary-only')
        self.assertEqual(len(result['binaries']), 3)
        self.assertEqual(result['binaries'][0]['binarySHA256'], candidate.digest_bytes(b'app'))
        self.assertEqual(result['binaries'][0]['reportSHA256'], candidate.digest_bytes(b'{"config":"scanner fixture"}\n'))
        for extra in [directory / 'unexpected', directory / 'leapview' / 'scanner.stderr.txt']:
            extra.write_text('scanner failed')
            with patch.object(evidence.subprocess, 'run') as verifier, self.assertRaises(ValueError):
                evidence.verify(self.archive, self.artifact, directory, Path('/protected/verifier'))
            verifier.assert_not_called()
            extra.unlink()
        summary = directory / 'leapview' / 'summary.json'
        saved = summary.read_bytes()
        wrong = json.loads(saved)
        wrong['binarySHA256'] = candidate.digest_bytes(b'substituted')
        summary.write_text(json.dumps(wrong))
        with patch.object(evidence.subprocess, 'run'), self.assertRaises(ValueError):
            evidence.verify(self.archive, self.artifact, directory, Path('/protected/verifier'))
        summary.write_bytes(saved)
        report = directory / 'leapview' / 'govulncheck.json'
        report.write_bytes(b'malformed scanner output')
        with patch.object(evidence.subprocess, 'run'), self.assertRaises(ValueError):
            evidence.verify(self.archive, self.artifact, directory, Path('/protected/verifier'))
        report.write_bytes(b'{"config":"scanner fixture"}\n')
        report.unlink()
        report.symlink_to(directory / 'leapviewctl' / 'govulncheck.json')
        with patch.object(evidence.subprocess, 'run'), self.assertRaises(ValueError):
            evidence.verify(self.archive, self.artifact, directory, Path('/protected/verifier'))
        report.unlink()
        report.write_bytes(b'{"config":"scanner fixture"}\n')
        with patch.object(evidence.subprocess, 'run', side_effect=ValueError('stale report')), self.assertRaises(ValueError):
            evidence.verify(self.archive, self.artifact, directory, Path('/protected/verifier'))
        (directory / 'leapview' / 'summary.json').unlink()
        with patch.object(evidence.subprocess, 'run'), self.assertRaises((ValueError, OSError)):
            evidence.verify(self.archive, self.artifact, directory, Path('/protected/verifier'))

    def test_go_reports_are_bound_into_manifest_and_cannot_be_removed_or_changed(self):
        receipt = {'scope': 'go-binary-only', 'archiveSHA256': self.artifact['sha256'], 'binaries': []}
        with patch.object(evidence, 'verify', return_value=receipt) as verify:
            manifest = candidate.collect(self.archive, 'application-image', self.source,
                                         go_dir=self.root / 'go', binary_verifier=Path('/protected/verifier'))
            self.assertEqual(manifest['evidence']['go-binaries'], receipt)
            self.assertFalse(manifest['releaseAdmission'])
            verify.assert_called_once()
            candidate.verify(manifest, self.archive, self.source,
                             go_dir=self.root / 'go', binary_verifier=Path('/protected/verifier'))
            changed = copy.deepcopy(manifest)
            changed['evidence']['go-binaries']['scope'] = 'source'
            with self.assertRaises(ValueError):
                candidate.verify(changed, self.archive, self.source,
                                 go_dir=self.root / 'go', binary_verifier=Path('/protected/verifier'))
            with self.assertRaises(ValueError):
                candidate.verify(manifest, self.archive, self.source)
        for options in [{'go_dir': self.root}, {'binary_verifier': Path('/protected/verifier')}]:
            with self.assertRaises(ValueError):
                candidate.collect(self.archive, 'application-image', self.source, **options)

    @unittest.skipUnless(os.environ.get('LEAPVIEW_TEST_GO_ARCHIVE') == '1', 'requires locked Go and live govulncheck')
    def test_real_protected_verifier_scans_archive_then_rejects_stale_malformed_and_substituted_reports(self):
        module = self.root / 'module'
        module.mkdir()
        (module / 'go.mod').write_text('module github.com/flidai/leapview\n\ngo 1.26.8\n')
        binaries = {}
        for name in ['leapview', 'leapviewctl']:
            package = module / 'cmd' / name
            package.mkdir(parents=True)
            (package / 'main.go').write_text('package main\nfunc main() {}\n')
            binary = self.root / name
            subprocess.run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags=-s -w',
                            '-o', str(binary), './cmd/' + name], cwd=module, check=True)
            binaries[name] = binary.read_bytes()
        self.write_archive([layer([(entry['path'], binaries['leapview' if entry['id'] == 'leapview' else 'leapviewctl'])
                                   for entry in evidence.ENTRYPOINTS])])
        reports = self.root / 'go'
        verifier = Path(os.environ['LEAPVIEW_TEST_GO_BINARY_VERIFIER']).resolve()
        evidence.scan(self.archive, self.artifact, reports, verifier)
        result = evidence.verify(self.archive, self.artifact, reports, verifier)
        self.assertEqual(len(result['binaries']), 3)
        summary = reports / 'leapview' / 'summary.json'
        saved = summary.read_bytes()
        for field, value in [('scannedAt', '2000-01-01T00:00:00Z'), ('binarySHA256', candidate.digest_bytes(b'other')),
                             ('scanner', {'scan_mode': 'source'})]:
            altered = json.loads(saved)
            altered[field] = value
            summary.write_text(json.dumps(altered))
            with self.subTest(field=field), self.assertRaises(ValueError):
                evidence.verify(self.archive, self.artifact, reports, verifier)
        summary.write_bytes(saved)
        report = reports / 'leapview' / 'govulncheck.json'
        report.write_bytes(b'malformed')
        with self.assertRaises(ValueError):
            evidence.verify(self.archive, self.artifact, reports, verifier)


if __name__ == '__main__':
    unittest.main()

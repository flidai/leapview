import importlib.util
import base64
import io
import json
import pathlib
import tempfile
import unittest
from unittest import mock

SCRIPT = pathlib.Path(__file__).resolve().parents[1] / 'nix_native_build_receipt.py'
spec = importlib.util.spec_from_file_location('receipt', SCRIPT)
receipt = importlib.util.module_from_spec(spec)
spec.loader.exec_module(receipt)
REPO = SCRIPT.parent.parent


class BuildReceipts(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.output = self.root / 'output'
        (self.output / 'lib').mkdir(parents=True)
        (self.output / 'lib/liblance_duckdb_ffi.a').write_bytes(b'!<arch>\nfixture')
        self.evidence = self.root / 'evidence'
        self.evidence.mkdir()
        (self.evidence / 'compiler.txt').write_text('rustc 1.98.1\nhost: x86_64-unknown-linux-gnu\n')
        (self.evidence / 'cargo.txt').write_text('cargo 1.98.1\n')
        receipt.write(self.evidence / 'patches.json', receipt.patched_files(REPO))
        (self.evidence / 'cargo.jsonl').write_text('\n'.join(map(json.dumps, [
            {'reason': 'compiler-artifact', 'package_id': 'registry+https://github.com/rust-lang/crates.io-index#quick-xml@0.38.4',
             'target': {'name': 'quick_xml', 'kind': ['lib']}, 'features': ['serialize'],
             'profile': {'opt_level': '3', 'test': False}, 'filenames': ['/build/target/libquick_xml.rlib']},
            {'reason': 'compiler-artifact', 'package_id': 'path+file:///build/source#lance_duckdb_ffi@0.1.0',
             'target': {'name': 'lance_duckdb_ffi', 'kind': ['staticlib']}, 'features': [],
             'profile': {'opt_level': '3', 'test': False}, 'filenames': ['/build/target/release/liblance_duckdb_ffi.a']},
            {'reason': 'build-finished', 'success': True},
        ])))

    def create(self):
        destination = self.root / 'receipt'
        receipt.create_component('lance', 'linux/amd64', REPO, self.evidence, self.output, destination)
        return destination

    def test_compiled_features_and_current_patched_lock_roundtrip(self):
        destination = self.create()
        result = receipt.verify_component(destination, 'lance', 'linux/amd64', REPO, self.output)
        self.assertEqual(result['compiledCargo'][0]['features'], ['serialize'])
        self.assertNotIn('/build', json.dumps(result['compiledCargo']))
        self.assertEqual(result['sources']['cargoLockSHA256'], receipt.digest(REPO / 'nix/lance-Cargo.lock'))
        self.assertNotIn('coverageComplete', result)

    def test_substituted_archive_rejected(self):
        destination = self.create()
        (self.output / 'lib/liblance_duckdb_ffi.a').write_bytes(b'!<arch>\nother')
        with self.assertRaisesRegex(ValueError, 'output'):
            receipt.verify_component(destination, 'lance', 'linux/amd64', REPO, self.output)

    def test_wrong_platform_and_missing_receipt_rejected(self):
        destination = self.create()
        with self.assertRaisesRegex(ValueError, 'platform'):
            receipt.verify_component(destination, 'lance', 'linux/arm64', REPO, self.output)
        (destination / 'receipt.json').unlink()
        with self.assertRaises(ValueError):
            receipt.verify_component(destination, 'lance', 'linux/amd64', REPO, self.output)

    def test_arm64_compiler_identity_roundtrip(self):
        (self.evidence / 'compiler.txt').write_text('rustc 1.98.1\nhost: aarch64-unknown-linux-gnu\n')
        destination = self.root / 'arm64'
        receipt.create_component('lance', 'linux/arm64', REPO, self.evidence, self.output, destination)
        result = receipt.verify_component(destination, 'lance', 'linux/arm64', REPO, self.output)
        self.assertEqual(result['platform'], 'linux/arm64')
        with self.assertRaisesRegex(ValueError, 'compiler/platform'):
            receipt.create_component('lance', 'linux/amd64', REPO, self.evidence, self.output, self.root / 'wrong-arch')

    def test_changed_features_evidence_rejected(self):
        destination = self.create()
        path = destination / 'evidence/cargo.jsonl.b64'
        path.write_bytes(base64.b64encode(base64.b64decode(path.read_bytes()).replace(b'serialize', b'unserialize')))
        with self.assertRaisesRegex(ValueError, 'evidence'):
            receipt.verify_component(destination, 'lance', 'linux/amd64', REPO, self.output)

    def test_old_or_unlocked_cargo_package_rejected(self):
        path = self.evidence / 'cargo.jsonl'
        path.write_text(path.read_text().replace('quick-xml@0.38.4', 'quick-xml@0.1.0'))
        with self.assertRaisesRegex(ValueError, 'patched Cargo.lock'):
            self.create()

    def test_failed_or_inventory_only_build_rejected(self):
        path = self.evidence / 'cargo.jsonl'
        path.write_text(json.dumps({'packages': ['quick-xml']}))
        with self.assertRaisesRegex(ValueError, 'successful Cargo build'):
            self.create()

    def test_evidence_symlink_rejected(self):
        (self.evidence / 'compiler.txt').unlink()
        (self.evidence / 'compiler.txt').symlink_to(self.evidence / 'cargo.txt')
        with self.assertRaisesRegex(ValueError, 'regular file'):
            self.create()

    def test_patch_substitution_rejected(self):
        receipt.write(self.evidence / 'patches.json', {'quick-xml-0.38.4/src/name.rs': '0' * 64})
        with self.assertRaisesRegex(ValueError, 'patch evidence'):
            self.create()

    def test_compiler_capture_is_bounded_and_never_overwrites(self):
        path = self.root / 'bounded'
        with mock.patch.object(receipt, 'LIMIT', 8):
            with self.assertRaisesRegex(ValueError, 'size bound'):
                receipt.capture(io.BytesIO(b'large compiler output'), path)
        self.assertLessEqual(path.stat().st_size, 8)
        with self.assertRaises(FileExistsError):
            receipt.capture(io.BytesIO(b'other'), path)

    def test_evidence_envelopes_reject_oversize_and_noncanonical_padding(self):
        path = self.root / 'encoded.b64'
        path.write_bytes(base64.b64encode(b'123456789'))
        with mock.patch.object(receipt, 'LIMIT', 8):
            with self.assertRaisesRegex(ValueError, 'bounded evidence'):
                receipt.evidence_read(self.root / 'encoded')
        path.write_bytes(b'Zh==')  # Decodes to f, whose canonical envelope is Zg==.
        with self.assertRaisesRegex(ValueError, 'bounded evidence'):
            receipt.evidence_read(self.root / 'encoded')

    def test_receipt_output_traversal_rejected(self):
        destination = self.create()
        value = receipt.load(destination / 'receipt.json')
        value['outputs'] = {'../../escaped.a': '0' * 64}
        receipt.write(destination / 'receipt.json', value)
        with self.assertRaisesRegex(ValueError, 'output identity'):
            receipt.verify_component(destination, 'lance', 'linux/amd64', REPO)

    def application(self):
        lance = self.root / 'lance'
        (lance / 'lib').mkdir(parents=True)
        (lance / 'lib/liblance_duckdb_ffi.a').write_bytes(b'!<arch>\nlance')
        (lance / 'share/leapview').mkdir(parents=True)
        receipt.create_component('lance', 'linux/amd64', REPO, self.evidence, lance, lance / 'share/leapview/native-build')
        duckdb = self.root / 'duckdb'
        (duckdb / 'lib').mkdir(parents=True)
        for name in ('duckdb_static', 'lance_extension', 'sqlite_scanner_extension', 'dummy_static_extension_loader'):
            (duckdb / f'lib/lib{name}.a').write_bytes(b'!<arch>\n' + name.encode())
        evidence = self.root / 'cmake'
        evidence.mkdir()
        (evidence / 'compiler.txt').write_text('gcc 15\ncompiler-target: x86_64-unknown-linux-gnu\n')
        (evidence / 'cmake-cache.txt').write_text('DUCKDB_EXPLICIT_PLATFORM:STRING=linux_amd64\n')
        receipt.write(evidence / 'compile-commands.json', [{'file': '/store/sqlite/src/sqlite/sqlite3.c', 'command': 'cc -c sqlite3.c'}])
        (evidence / 'extensions.cmake').write_text('duckdb_extension_load(lance SOURCE_DIR /store/lance)\nduckdb_extension_load(sqlite_scanner SOURCE_DIR /store/sqlite)\n')
        sqlite = receipt.sources(REPO, 'duckdb')['sqlite']['amalgamation']
        receipt.write(evidence / 'sqlite-source.json', {name: sqlite[name + 'SHA256'].removeprefix('sha256:') for name in ('sqlite3.c', 'sqlite3.h')})
        (duckdb / 'share/leapview').mkdir(parents=True)
        receipt.create_component('duckdb', 'linux/amd64', REPO, evidence, duckdb, duckdb / 'share/leapview/native-build')
        selected = [str(p) for p in sorted((duckdb / 'lib').glob('*.a')) if 'dummy_' not in p.name] + [str(lance / 'lib/liblance_duckdb_ffi.a')]
        inputs = self.root / 'link-inputs'
        inputs.write_text('\n'.join(selected) + '\n')
        app_evidence = self.root / 'app-evidence'
        app_evidence.mkdir()
        (app_evidence / 'link-flags.txt').write_text(' '.join(['-Wl,--start-group', *selected, '-Wl,--end-group', '-lstdc++', '-ldl', '-lm']))
        (app_evidence / 'go.txt').write_text('go version go1.26 linux/amd64\n')
        (app_evidence / 'tags.txt').write_text('duckdb_arrow,duckdb_use_static_lib,leapview_static_lance,leapview_static_sqlite\n')
        binaries = self.root / 'bin'
        binaries.mkdir()
        for name in ('leapview', 'leapviewctl'):
            (binaries / name).write_bytes(b'\x7fELF\x02\x01' + b'\0' * 12 + bytes([62, 0]) + name.encode())
        destination = self.root / 'application'
        receipt.compose(REPO, 'linux/amd64', 'a' * 40, duckdb, lance, binaries, inputs, app_evidence, destination)
        return destination, binaries

    def test_application_composition_roundtrip_explicitly_not_admission(self):
        directory, binaries = self.application()
        result = receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)
        self.assertIn('exhaustive binary closure', result['scope'])
        self.assertTrue(result['unresolved'])
        self.assertFalse({'coverageComplete', 'admitted', 'verifiedDigest'} & result.keys())

    def test_evidence_retains_exact_bytes_without_nix_runtime_references(self):
        original = self.evidence / 'compiler.txt'
        original.write_text(original.read_text() + '/nix/store/' + 'a' * 32 + '-rustc/bin/rustc\n')
        destination = self.create()
        self.assertEqual(receipt.evidence_read(destination / 'evidence/compiler.txt'), original.read_bytes())
        for path in destination.rglob('*'):
            if path.is_file():
                self.assertNotIn(b'/nix/store/', path.read_bytes())
        path = destination / 'evidence/compiler.txt.b64'
        path.write_bytes(path.read_bytes() + b'\n')
        with self.assertRaises(ValueError):
            receipt.verify_component(destination, 'lance', 'linux/amd64', REPO, self.output)

    def test_application_substitutions_rejected(self):
        directory, binaries = self.application()
        with self.assertRaisesRegex(ValueError, 'source/platform/recipe'):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'b' * 40, binaries)
        with (binaries / 'leapview').open('ab') as handle:
            handle.write(b'substitute')
        with self.assertRaisesRegex(ValueError, 'output substitution'):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)

    def test_missing_component_and_changed_link_inputs_rejected(self):
        directory, binaries = self.application()
        value = receipt.load(directory / 'application.json')
        value['linkInputs']['duckdb']['lib/libduckdb_static.a'] = '0' * 64
        receipt.write(directory / 'application.json', value)
        with self.assertRaisesRegex(ValueError, 'link input substitution'):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)
        (directory / 'duckdb/receipt.json').unlink()
        with self.assertRaises(ValueError):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)

    def test_portable_transformation_binds_actual_shipped_bytes(self):
        directory, binaries = self.application()
        exported = self.root / 'portable-bin'
        exported.mkdir()
        for binary in binaries.iterdir():
            (exported / binary.name).write_bytes(binary.read_bytes() + b'changed interpreter and rpath')
        tool = self.root / 'patchelf-version'
        tool.write_text('patchelf 0.15.2\n')
        destination = self.root / 'portable-receipts'
        result = receipt.portable(directory, REPO, 'linux/amd64', 'a' * 40, binaries, exported, destination, '/lib64/ld-linux-x86-64.so.2', tool)
        self.assertIn('portableReceiptSHA256', result)
        with self.assertRaisesRegex(ValueError, 'portable output substitution'):
            receipt.verify_portable(destination, REPO, 'linux/amd64', 'a' * 40, binaries)
        value = receipt.load(destination / 'portable.json')
        value['inputOutputs']['leapview'] = '0' * 64
        receipt.write(destination / 'portable.json', value)
        with self.assertRaisesRegex(ValueError, 'input substitution'):
            receipt.verify_portable(destination, REPO, 'linux/amd64', 'a' * 40, exported)

    def test_wrong_elf_architecture_rejected(self):
        directory, binaries = self.application()
        path = binaries / 'leapview'
        data = bytearray(path.read_bytes())
        data[18:20] = bytes([183, 0])
        path.write_bytes(data)
        with self.assertRaisesRegex(ValueError, 'ELF architecture'):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)


if __name__ == '__main__':
    unittest.main()

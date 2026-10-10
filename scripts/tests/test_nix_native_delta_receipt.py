"""Reject substituted selected Rust graphs and generated-header bindings."""
import json
import hashlib
import tomllib
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_native_delta_receipt as delta


def cargo_fixture():
    return [
        {'reason': 'compiler-artifact', 'package_id': 'path+file:///build/kernel/ffi#delta_kernel_ffi@0.21.0',
         'target': {'name': 'delta_kernel_ffi', 'kind': ['lib', 'cdylib', 'staticlib']},
         'features': ['arrow', 'arrow-58', 'default', 'default-engine-base', 'default-engine-rustls', 'delta-kernel-unity-catalog', 'test-ffi', 'tracing', 'tracing-core', 'tracing-subscriber'],
         'profile': {'opt_level': '3', 'test': False}, 'filenames': ['/build/target/x86_64-unknown-linux-gnu/release/libdelta_kernel_ffi.a']},
        {'reason': 'compiler-artifact', 'package_id': 'registry+https://github.com/rust-lang/crates.io-index#rustls@0.23.38',
         'target': {'name': 'rustls', 'kind': ['lib']}, 'features': ['ring'],
         'profile': {'opt_level': '3', 'test': False}, 'filenames': ['/build/target/rustls.rlib']},
        {'reason': 'build-finished', 'success': True},
    ]



def delta_fixture(policy, lock):
    messages = cargo_fixture()
    rustls = next(p for p in lock['package'] if p['name'] == 'rustls')
    messages[1]['package_id'] = rustls['source'] + '#rustls@' + rustls['version']
    files = {'compiler.txt': b'rustc 1.98.1\nhost: x86_64-unknown-linux-gnu\n',
             'cargo.txt': b'cargo 1.98.1\n', 'source.json': json.dumps(policy['kernel']['selectedFiles']).encode(),
             'patches.json': json.dumps(policy['patchedFiles']).encode(),
             'cargo.jsonl': '\n'.join(map(json.dumps, messages)).encode(),
             'checks.txt': b'Delta selected FFI upstream unit tests passed\n',
             'check.log': ('cargoCheckHook flags: -j 2 --profile release --features=' + ','.join(policy['features']) + ' --target x86_64-unknown-linux-gnu --offline --package=delta_kernel_ffi --lib --\ntest result: ok. 4 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out\nFinished cargoCheckHook\n').encode()}
    files.update({name: ('kernel_generated_header_' + name).encode() for name in delta.HEADERS})
    files['headers.json'] = json.dumps({'include/' + name: hashlib.sha256(files[name]).hexdigest() for name in delta.HEADERS}).encode()
    return files

class DeltaReceiptTests(unittest.TestCase):
    def setUp(self):
        self.messages = cargo_fixture()
        self.lock = {'package': [{'name': 'delta_kernel_ffi', 'version': '0.21.0'},
            {'name': 'rustls', 'version': '0.23.38', 'source': 'registry+https://github.com/rust-lang/crates.io-index', 'checksum': 'a' * 64}]}

    def check(self):
        return delta.compiled_cargo('\n'.join(map(json.dumps, self.messages)), self.lock, 'linux/amd64')

    def test_selected_static_ffi_and_locked_compiled_graph(self):
        values = self.check()
        self.assertEqual({v['name'] for v in values}, {'delta_kernel_ffi', 'rustls'})
        self.assertNotIn('/build', json.dumps(values))

    def test_rejects_missing_root_failed_build_and_wrong_architecture(self):
        for edit in ('missing', 'failed', 'platform', 'test'):
            self.messages = cargo_fixture()
            if edit == 'missing':
                self.messages.pop(0)
            elif edit == 'failed':
                self.messages[-1]['success'] = False
            elif edit == 'platform':
                self.messages[0]['filenames'][0] = '/build/target/aarch64-unknown-linux-gnu/release/libdelta_kernel_ffi.a'
            else:
                self.messages[0]['profile']['test'] = True
            with self.subTest(edit=edit), self.assertRaises(ValueError):
                self.check()

    def test_rejects_changed_features_unlocked_packages_and_native_tls(self):
        for edit in ('features', 'unlocked', 'native-tls'):
            self.messages = cargo_fixture()
            if edit == 'features':
                self.messages[0]['features'].remove('delta-kernel-unity-catalog')
            elif edit == 'unlocked':
                self.messages[1]['package_id'] = self.messages[1]['package_id'].replace('0.23.38', '0.23.37')
            else:
                self.messages[0]['features'].append('default-engine-native-tls')
            with self.subTest(edit=edit), self.assertRaises(ValueError):
                self.check()

    def test_source_patch_header_and_compiler_substitution(self):
        root = Path(__file__).resolve().parents[2]
        policy = json.loads((root / 'nix/delta-source-lock.json').read_text())
        lock = tomllib.loads((root / 'nix/delta-Cargo.lock').read_text())
        files = delta_fixture(policy, lock)
        delta.check(Path('/evidence'), policy, 'linux/amd64', lock, lambda p: files[p.name])
        for name in ('source.json', 'patches.json', 'headers.json', 'compiler.txt', 'checks.txt', *delta.HEADERS):
            original = files[name]
            files[name] = b'{}'
            with self.subTest(file=name), self.assertRaises(ValueError):
                delta.check(Path('/evidence'), policy, 'linux/amd64', lock, lambda p: files[p.name])
            files[name] = original

    def test_upstream_tests_require_selected_optional_profile(self):
        root = Path(__file__).resolve().parents[2]
        policy = json.loads((root / 'nix/delta-source-lock.json').read_text())
        lock = tomllib.loads((root / 'nix/delta-Cargo.lock').read_text())
        files = delta_fixture(policy, lock)
        original = files['check.log']
        for changed in (original.replace(b'--features=' + ','.join(policy['features']).encode(), b''),
                        original.replace(b'test-ffi,', b''),
                        original.replace(b'4 passed; 0 failed', b'0 passed; 0 failed') + b'test result: ok. 2 passed; 0 failed;\n',
                        original.replace(b'Finished cargoCheckHook\n', b''),
                        original.replace(b'4 passed; 0 failed', b'3 passed; 1 failed')):
            files['check.log'] = changed
            with self.subTest(log=changed), self.assertRaises(ValueError):
                delta.check(Path('/evidence'), policy, 'linux/amd64', lock, lambda p: files[p.name])

    def test_rejects_duplicate_json_fields(self):
        with self.assertRaises(ValueError):
            delta.decode('{"reason":"compiler-artifact","reason":"build-finished"}')

    def test_header_and_archive_substitution_rejected(self):
        binding = {'archive': '/nix/store/kernel/lib/libdelta_kernel_ffi.a', 'sha256': 'a' * 64,
                   'headers': {'include/' + name: 'b' * 64 for name in delta.HEADERS}}
        delta.binding(json.dumps(binding))
        for field in ('archive', 'sha256', 'headers'):
            changed = dict(binding)
            changed[field] = 'invalid'
            with self.subTest(field=field), self.assertRaises(ValueError):
                delta.binding(json.dumps(changed))


if __name__ == '__main__':
    unittest.main()

"""Selected Vortex bridge must bind its actual engine headers and Rust output."""
import base64
import json
import tomllib
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_native_vortex_receipt as vortex
import nix_native_delta_receipt as delta


def vortex_fixture(policy, lock):
    tls = next(p for p in lock['package'] if p['name'] == 'rustls')
    items = [
        {'reason': 'compiler-artifact', 'package_id': 'path+file:///build/vortex-duckdb#vortex-duckdb@0.1.0',
         'target': {'name': 'vortex_duckdb', 'kind': ['staticlib']}, 'features': [],
         'profile': {'opt_level': '3', 'test': False}, 'filenames': ['/build/target/x86_64-unknown-linux-gnu/release/libvortex_duckdb.a']},
        {'reason': 'compiler-artifact', 'package_id': tls['source'] + '#rustls@' + tls['version'],
         'target': {'name': 'rustls', 'kind': ['lib']}, 'features': ['ring'],
         'profile': {'opt_level': '3', 'test': False}, 'filenames': ['/build/target/rustls.rlib']},
        {'reason': 'build-finished', 'success': True}]
    engine = '/nix/store/' + 'a' * 32 + '-source'
    return {'compiler.txt': b'rustc 1.98.1\nhost: x86_64-unknown-linux-gnu\n',
            'cargo.txt': b'cargo 1.98.1\n', 'cargo.jsonl': '\n'.join(map(json.dumps, items)).encode(),
            'source.json': json.dumps(policy['rust']['selectedFiles']).encode(),
            'engine-source.json': json.dumps(policy['engine']['selectedFiles']).encode(),
            'patches.json': json.dumps(policy['patchedFiles']).encode(),
            'engine-path.txt': engine.encode(),
            'cpp-build.txt': '\n'.join('running: c++ -std=c++20 -fPIC -isystem ' + engine + '/src/include -c ' + name for name in policy['bridgeSources']).encode(),
            'headers.json': json.dumps({name: base64.b64encode(('vortex_generated ' + name).encode()).decode() for name in policy['headers']}).encode(),
            'cpp.rs': b'pub fn duckdb_vx_generated() {}',
            'checks.txt': b'Vortex file upstream unit tests and selected XML boundary passed\n',
            'cxx.txt': b'gcc 15\ncompiler-target: x86_64-unknown-linux-gnu\n',
            'clang.txt': b'clang version 21\n'}


class VortexReceiptTests(unittest.TestCase):
    def test_source_engine_patch_compiler_and_bridge_substitution(self):
        repo = Path(__file__).resolve().parents[2]
        policy = json.loads((repo / 'nix/vortex-source-lock.json').read_text())
        lock = tomllib.loads((repo / 'nix/vortex-Cargo.lock').read_text())
        files = vortex_fixture(policy, lock)
        vortex.check(Path('/evidence'), policy, 'linux/amd64', lock, lambda p: files[p.name])
        for name in vortex.EVIDENCE:
            original = files[name]
            files[name] = b'{}'
            with self.subTest(file=name), self.assertRaises((ValueError, KeyError)):
                vortex.check(Path('/evidence'), policy, 'linux/amd64', lock, lambda p: files[p.name])
            files[name] = original

    def test_rejects_other_ffi_family_and_unselected_features(self):
        lock = {'package': [{'name': 'vortex-duckdb', 'version': '0.1.0'},
            {'name': 'rustls', 'version': '0.23.38', 'source': 'registry+https://github.com/rust-lang/crates.io-index', 'checksum': 'a' * 64}]}
        items = [
            {'reason': 'compiler-artifact', 'package_id': 'path+file:///build/vortex-duckdb#vortex-duckdb@0.1.0',
             'target': {'name': 'vortex_duckdb', 'kind': ['staticlib']}, 'features': [],
             'profile': {'opt_level': '3', 'test': False}, 'filenames': ['/build/target/x86_64-unknown-linux-gnu/release/libvortex_duckdb.a']},
            {'reason': 'compiler-artifact', 'package_id': 'registry+https://github.com/rust-lang/crates.io-index#rustls@0.23.38',
             'target': {'name': 'rustls', 'kind': ['lib']}, 'features': ['ring'],
             'profile': {'opt_level': '3', 'test': False}, 'filenames': ['/build/target/rustls.rlib']},
            {'reason': 'build-finished', 'success': True}]
        data = '\n'.join(map(json.dumps, items))
        self.assertEqual(len(vortex.compiled(data, lock, 'linux/amd64')), 2)
        with self.assertRaises(ValueError):
            delta.compiled_cargo(data, lock, 'linux/amd64')
        items[0]['features'] = ['unreviewed']
        with self.assertRaises(ValueError):
            vortex.compiled('\n'.join(map(json.dumps, items)), lock, 'linux/amd64')

    def test_headers_reject_substitution_traversal_and_noncanonical_encoding(self):
        policy = {'headers': ['include/vortex.h']}
        data = {'include/vortex.h': base64.b64encode(b'generated ABI header').decode()}
        self.assertEqual(set(vortex.headers(json.dumps(data), policy)), {'include/vortex.h'})
        for changed in ({'../escape': data['include/vortex.h']}, {'include/vortex.h': 'Zh=='}, {}):
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                vortex.headers(json.dumps(changed), policy)

    def test_bridge_requires_actual_sources_selected_engine_and_pic(self):
        policy = {'bridgeSources': ['cpp/data.cpp', 'cpp/vector.cpp']}
        data = '\n'.join('running: "c++" "-std=c++20" "-fPIC" "-isystem" "/nix/store/engine/src/include" "-c" "' + name + '"' for name in policy['bridgeSources'])
        vortex.bridge(data, policy, '/nix/store/engine')
        for changed in (data.replace('cpp/vector.cpp', 'wrong.cpp'), data.replace('/engine/', '/substituted/'), data.replace('-fPIC', '-fno-PIC'), data + '\nrunning: cc -march=native'):
            with self.assertRaises(ValueError):
                vortex.bridge(changed, policy, '/nix/store/engine')


if __name__ == '__main__':
    unittest.main()

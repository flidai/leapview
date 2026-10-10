import importlib.util
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('iceberg', ROOT / 'scripts/nix_native_iceberg_receipt.py')
iceberg = importlib.util.module_from_spec(spec)
spec.loader.exec_module(iceberg)


def fixture(policy):
    evidence = {'consumer.txt': iceberg.SUCCESS.encode(), 'consumer-needed.txt': b'(NEEDED) libc.so.6\n'}
    links = {name: {'archive': '/store/http/' + name, 'sha256': 'a' * 64} for name in iceberg.HTTP_ARCHIVES}
    evidence['http-link.json'] = json.dumps(links).encode()
    for name, selected in policy['libraries'].items():
        evidence[name + '-source.json'] = json.dumps(selected['selectedFiles']).encode()
        evidence[name + '-compiler.txt'] = b'compiler-target: x86_64-unknown-linux-gnu\n'
        cache = '\n'.join(key + ':BOOL=' + ('ON' if enabled else 'OFF') for key, enabled in policy['features'].items())
        cache += '\nICEBERG_HTTP_STATIC_LIBRARIES:STRING=' + ';'.join(v['archive'] for v in links.values())
        cache += '\nBUILD_DEPS:BOOL=OFF\nBUILD_ONLY:STRING=sso;sts\n'
        for key, archive in [('CURL_LIBRARY_RELEASE','libcurl.a'),('OPENSSL_SSL_LIBRARY','libssl.a'),
                             ('OPENSSL_CRYPTO_LIBRARY','libcrypto.a'),('ZLIB_LIBRARY','libz.a')]:
            cache += key + ':FILEPATH=' + links['lib/' + archive]['archive'] + '\n'
        evidence[name + '-cache.txt'] = cache.encode()
        evidence[name + '-commands.json'] = b'[{"file":"/build/source.cpp","command":"c++ -fPIC -c source.cpp"}]'
    return evidence


class IcebergReceiptTests(unittest.TestCase):
    def setUp(self):
        self.policy = json.loads((ROOT / 'nix/iceberg-source-lock.json').read_bytes())
        self.evidence = fixture(self.policy)

    def check(self):
        return iceberg.check(self.evidence, self.policy, 'linux/amd64')

    def test_selected_aws_profile(self):
        self.check()

    def test_substituted_source_compiler_and_success(self):
        for field in ('aws-c-auth-source.json', 'aws-sdk-cpp-compiler.txt', 'consumer.txt'):
            old = self.evidence[field]
            self.evidence[field] = b'{}'
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.check()
            self.evidence[field] = old

    def test_shared_bundled_or_unselected_dependencies(self):
        for before, after in [(b'BUILD_SHARED_LIBS:BOOL=OFF', b'BUILD_SHARED_LIBS:BOOL=ON'),
                              (b'BUILD_DEPS:BOOL=OFF', b'BUILD_DEPS:BOOL=ON'),
                              (b'BUILD_ONLY:STRING=sso;sts', b'BUILD_ONLY:STRING=*'),
                              (b'/store/http/', b'/unselected/')]:
            old = self.evidence['aws-sdk-cpp-cache.txt']
            self.evidence['aws-sdk-cpp-cache.txt'] = old.replace(before, after)
            with self.subTest(change=after), self.assertRaises(ValueError):
                self.check()
            self.evidence['aws-sdk-cpp-cache.txt'] = old

    def test_dynamic_native_dependency_rejected(self):
        for name in ['libaws-cpp-sdk-core.so', 'libs2n.so', 'libssl.so', 'libcrypto.so', 'libcurl.so', 'libz.so', 'libnghttp2.so']:
            self.evidence['consumer-needed.txt'] = ('NEEDED libc.so.6\nNEEDED ' + name).encode()
            with self.subTest(library=name), self.assertRaises(ValueError):
                self.check()

    def test_native_isa_or_missing_pic_rejected(self):
        for command in ['c++ -c source.cpp', 'c++ -fPIC -march=native -c source.cpp']:
            self.evidence['aws-c-common-commands.json'] = json.dumps([{'command':command}]).encode()
            with self.subTest(command=command), self.assertRaises(ValueError):
                self.check()


if __name__ == '__main__': unittest.main()

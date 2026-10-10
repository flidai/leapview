import importlib.util
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('azure', ROOT / 'scripts/nix_native_azure_receipt.py')
azure = importlib.util.module_from_spec(spec)
spec.loader.exec_module(azure)


def fixture(policy):
    evidence = {'consumer.txt': b'selected static Azure SDK signed blob/datalake XML and identity roundtrips passed\n',
                'consumer-needed.txt': b'(NEEDED) Shared library: [libc.so.6]\n'}
    bindings = {'lib/libcurl.a': {'archive': '/nix/store/http/lib/libcurl.a', 'sha256': '1' * 64},
                'lib/libssl.a': {'archive': '/nix/store/http/lib/libssl.a', 'sha256': '2' * 64},
                'lib/libcrypto.a': {'archive': '/nix/store/http/lib/libcrypto.a', 'sha256': '3' * 64},
                'lib/libz.a': {'archive': '/nix/store/http/lib/libz.a', 'sha256': '5' * 64},
                'lib/libnghttp2.a': {'archive': '/nix/store/http/lib/libnghttp2.a', 'sha256': '6' * 64},
                'lib/libxml2.a': {'archive': '/nix/store/xml/lib/libxml2.a', 'sha256': '4' * 64}}
    evidence['native-link.json'] = json.dumps(bindings).encode()
    for name, selected in policy['libraries'].items():
        evidence[name + '-source.json'] = json.dumps(selected).encode()
        evidence[name + '-compiler.txt'] = b'gcc version 15.3.0\ncompiler-target: x86_64-unknown-linux-gnu\n'
        if name == 'libxml2':
            continue
        cache = '\n'.join(key + ':BOOL=' + ('ON' if value else 'OFF') for key, value in policy['sdkFeatures'].items())
        for key, archive in [('CURL_LIBRARY_RELEASE','libcurl.a'),('OPENSSL_SSL_LIBRARY','libssl.a'),('OPENSSL_CRYPTO_LIBRARY','libcrypto.a'),('LIBXML2_LIBRARY','libxml2.a')]:
            cache += '\n' + key + ':FILEPATH=' + bindings['lib/' + archive]['archive']
        evidence[name + '-cache.txt'] = cache.encode()
        evidence[name + '-commands.json'] = json.dumps([{'file': '/build/' + path, 'command': 'c++ -fPIC -c /build/' + path} for path in selected['selectedFiles']]).encode()
    evidence['libxml2-checks.txt'] = b'libxml2 upstream checks passed\n'
    evidence['libxml2-options.txt'] = b'--enable-static --disable-shared --with-pic --without-modules --without-icu --without-python --without-http --without-zlib'
    evidence['libxml2-config.txt'] = b'#define LIBXML_TREE_ENABLED\n#define LIBXML_READER_ENABLED\n'
    evidence['libxml2-build.txt'] = b'gcc -fPIC -c parser.c\n'
    return evidence


class AzureReceiptTests(unittest.TestCase):
    def setUp(self):
        self.policy = json.loads((ROOT / 'nix/azure-source-lock.json').read_text())
        self.evidence = fixture(self.policy)

    def check(self):
        return azure.check(self.evidence, self.policy, 'linux/amd64')

    def test_selected_static_profile(self):
        self.check()

    def test_wrong_compiler_target(self):
        self.evidence['core-compiler.txt'] = b'compiler-target: aarch64-unknown-linux-gnu'
        with self.assertRaises(ValueError): self.check()

    def test_changed_source(self):
        self.evidence['identity-source.json'] = b'{}'
        with self.assertRaises(ValueError): self.check()

    def test_feature_drift(self):
        for key, value in self.policy['sdkFeatures'].items():
            with self.subTest(feature=key):
                original = self.evidence['core-cache.txt']
                self.evidence['core-cache.txt'] = original.replace((key + ':BOOL=' + ('ON' if value else 'OFF')).encode(), (key + ':BOOL=' + ('OFF' if value else 'ON')).encode())
                with self.assertRaises(ValueError): self.check()
                self.evidence['core-cache.txt'] = original

    def test_cleared_or_enabled_identity_and_common_fetch_option(self):
        for name in ('identity', 'storage-common'):
            original = self.evidence[name + '-cache.txt']
            for replacement in (b'', b'FETCH_SOURCE_DEPS:BOOL=TRUE'):
                with self.subTest(library=name, replacement=replacement):
                    self.evidence[name + '-cache.txt'] = original.replace(b'FETCH_SOURCE_DEPS:BOOL=OFF', replacement)
                    with self.assertRaisesRegex(ValueError, 'SDK feature changed'):
                        self.check()
            self.evidence[name + '-cache.txt'] = original

    def test_dynamic_xml_plugins(self):
        self.evidence['libxml2-config.txt'] += b'#define LIBXML_MODULES_ENABLED\n'
        with self.assertRaises(ValueError): self.check()

    def test_missing_pic(self):
        self.evidence['storage-blobs-commands.json'] = self.evidence['storage-blobs-commands.json'].replace(b'-fPIC', b'')
        with self.assertRaises(ValueError): self.check()

    def test_native_isa(self):
        self.evidence['core-commands.json'] = self.evidence['core-commands.json'].replace(b'-fPIC', b'-fPIC -march=native')
        with self.assertRaises(ValueError): self.check()

    def test_rebound_curl_or_xml(self):
        for name in ['core', 'storage-common']:
            with self.subTest(library=name):
                original = self.evidence[name + '-cache.txt']
                self.evidence[name + '-cache.txt'] = original.replace(b'/nix/store/', b'/unselected/')
                with self.assertRaises(ValueError): self.check()
                self.evidence[name + '-cache.txt'] = original

    def test_dynamic_sdk_or_parser(self):
        for library in ['libazure-core.so', 'libxml2.so', 'libcurl.so', 'libssl.so', 'libcrypto.so', 'libz.so.1', 'libnghttp2.so.14']:
            with self.subTest(library=library):
                self.evidence['consumer-needed.txt'] = ('(NEEDED) Shared library: [libc.so.6]\n(NEEDED) Shared library: [' + library + ']').encode()
                with self.assertRaises(ValueError): self.check()

    def test_skipped_xml_upstream_checks(self):
        del self.evidence['libxml2-checks.txt']
        with self.assertRaisesRegex(ValueError, 'missing|upstream checks'):
            self.check()

    def test_missing_successful_consumer(self):
        self.evidence['consumer.txt'] = b''
        with self.assertRaises(ValueError): self.check()


if __name__ == '__main__': unittest.main()

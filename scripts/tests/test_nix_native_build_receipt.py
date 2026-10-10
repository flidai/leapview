import importlib.util
import base64
import io
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest
import tomllib
from unittest import mock
from test_nix_native_database_receipt import database_fixture
from test_nix_native_excel_receipt import excel_fixture
from test_nix_native_avro_receipt import avro_fixture
from test_nix_native_delta_receipt import delta_fixture
from test_nix_native_azure_receipt import fixture as azure_fixture
from test_nix_native_vortex_receipt import vortex_fixture

SCRIPT = pathlib.Path(__file__).resolve().parents[1] / 'nix_native_build_receipt.py'
sys.path.insert(0, str(SCRIPT.parent))
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

    def croaring(self):
        root = self.root / 'croaring'
        (root / 'lib').mkdir(parents=True)
        (root / 'lib/libroaring.a').write_bytes(b'!<arch>\nroaring')
        evidence = self.root / 'croaring-evidence'
        evidence.mkdir()
        policy = receipt.sources(REPO, 'croaring')
        receipt.write(evidence / 'source.json', policy['patchedFiles'])
        (evidence / 'compiler.txt').write_text('gcc 15\ncompiler-target: x86_64-unknown-linux-gnu\n')
        (evidence / 'cmake-cache.txt').write_text('CMAKE_HOME_DIRECTORY:INTERNAL=/source\n' + ''.join(
            name + ':BOOL=' + ('ON' if value else 'OFF') + '\n' for name, value in policy['cmake'].items()))
        receipt.write(evidence / 'compile-commands.json', [{'file': '/source/src/roaring.c', 'command': 'cc -c roaring.c'}])
        (root / 'share/leapview').mkdir(parents=True)
        receipt.create_component('croaring', 'linux/amd64', REPO, evidence, root, root / 'share/leapview/native-build')
        return root, evidence

    def test_croaring_static_output_and_patched_source_bound(self):
        root, evidence = self.croaring()
        result = receipt.verify_component(root / 'share/leapview/native-build', 'croaring', 'linux/amd64', REPO, root)
        self.assertEqual(result['sources']['version'], '5.2.2')
        (root / 'lib/libroaring.a').write_bytes(b'!<arch>\nsubstituted')
        with self.assertRaisesRegex(ValueError, 'substitution'):
            receipt.verify_component(root / 'share/leapview/native-build', 'croaring', 'linux/amd64', REPO, root)
        receipt.write(evidence / 'source.json', {})
        with self.assertRaisesRegex(ValueError, 'source/patch'):
            receipt.create_component('croaring', 'linux/amd64', REPO, evidence, root, self.root / 'changed-source')

    def test_croaring_accepts_nix_cmake_boolean_spelling(self):
        root, evidence = self.croaring()
        cache = evidence / 'cmake-cache.txt'
        # lib.cmakeBool passes TRUE/FALSE, which CMake retains in its cache.
        cache.write_text(cache.read_text().replace('=ON', '=TRUE').replace('=OFF', '=FALSE'))
        receipt.create_component('croaring', 'linux/amd64', REPO, evidence, root, self.root / 'nix-booleans')

    def test_croaring_rejects_shared_network_fetch_and_disabled_tests(self):
        root, evidence = self.croaring()
        cache = evidence / 'cmake-cache.txt'
        original = cache.read_text()
        for old, new in [('BUILD_SHARED_LIBS:BOOL=OFF', 'BUILD_SHARED_LIBS:BOOL=ON'),
                         ('ROARING_USE_CPM:BOOL=OFF', 'ROARING_USE_CPM:BOOL=ON'),
                         ('ENABLE_ROARING_TESTS:BOOL=ON', 'ENABLE_ROARING_TESTS:BOOL=OFF')]:
            with self.subTest(option=old):
                cache.write_text(original.replace(old, new))
                with self.assertRaisesRegex(ValueError, 'option differs'):
                    receipt.create_component('croaring', 'linux/amd64', REPO, evidence, root, self.root / 'changed-options')
        cache.write_text(original)
        (root / 'lib/libroaring.so').write_bytes(b'shared')
        with self.assertRaisesRegex(ValueError, 'static output'):
            receipt.create_component('croaring', 'linux/amd64', REPO, evidence, root, self.root / 'shared')

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

    def http(self):
        root = self.root / 'http'
        (root / 'lib').mkdir(parents=True)
        for name in receipt.HTTP_ARCHIVES:
            (root / name).write_bytes(b'!<arch>\n' + name.encode())
        evidence = self.root / 'http-evidence'
        evidence.mkdir()
        for name in receipt.HTTP_LIBRARIES:
            receipt.write(evidence / (name + '-source.json'), receipt.sources(REPO, 'http')['libraries'][name]['selectedFiles'])
            (evidence / (name + '-compiler.txt')).write_text('gcc 15\ncompiler-target: x86_64-unknown-linux-gnu\n')
            source = {'curl': 'url.c', 'openssl': 'ssl_lib.c', 'nghttp2': 'nghttp2_session.c', 'zlib': 'inflate.c'}[name]
            (evidence / (name + '-build.txt')).write_text('gcc -fPIC -c ' + source + ' -o object.o\n')
        (evidence / 'curl-config.txt').write_text('#define USE_OPENSSL 1\n#define USE_NGHTTP2 1\n#define HAVE_LIBZ 1\n')
        (evidence / 'openssl-config.txt').write_text('Disabled features:\n    shared [option]\n    module [option]\nConfig target attributes:\n')
        (evidence / 'nghttp2-config.txt').write_text('#define PACKAGE_VERSION "1.70.0"\n')
        (evidence / 'zlib-config.txt').write_text('SHAREDLIB=\n')
        for name in ('curl', 'nghttp2'):
            (evidence / (name + '-options.txt')).write_text('--enable-static --disable-shared')
        (root / 'share/leapview').mkdir(parents=True)
        receipt.create_component('http', 'linux/amd64', REPO, evidence, root, root / 'share/leapview/native-build')
        return root, evidence

    def test_http_actual_archive_and_selected_source_substitution_rejected(self):
        root, evidence = self.http()
        directory = root / 'share/leapview/native-build'
        result = receipt.verify_component(directory, 'http', 'linux/amd64', REPO, root)
        self.assertEqual(set(result['outputs']), receipt.HTTP_ARCHIVES)
        (root / 'lib/libssl.a').write_bytes(b'!<arch>\nsubstituted')
        with self.assertRaisesRegex(ValueError, 'substitution'):
            receipt.verify_component(directory, 'http', 'linux/amd64', REPO, root)
        receipt.write(evidence / 'openssl-source.json', {})
        with self.assertRaisesRegex(ValueError, 'selected source identity'):
            receipt.check_http(evidence, REPO, 'linux/amd64')

    def test_http_missing_compile_wrong_tls_and_host_isa_rejected(self):
        _, evidence = self.http()
        for file, replacement, expected in (
            ('openssl-build.txt', 'make: nothing to do', 'compilation missing'),
            ('curl-build.txt', 'gcc -march=native -c url.c', 'host-specific'),
            ('curl-config.txt', '#define USE_GNUTLS 1', 'feature selection'),
            ('curl-options.txt', '--enable-static --enable-shared', 'static libraries'),
            ('openssl-config.txt', 'Disabled features:\n', 'provider modules'),
        ):
            with self.subTest(file=file):
                path=evidence/file; old=path.read_text(); path.write_text(replacement)
                with self.assertRaisesRegex(ValueError, expected):
                    receipt.check_http(evidence, REPO, 'linux/amd64')
                path.write_text(old)

    def database(self):
        root = self.root / 'database'
        (root / 'lib').mkdir(parents=True)
        for name in receipt.database.ARCHIVES:
            (root / name).write_bytes(b'!<arch>\ndatabase')
        evidence = self.root / 'database-evidence'
        evidence.mkdir()
        for name, data in database_fixture(receipt.sources(REPO, 'database')).items():
            (evidence / name).write_bytes(data)
        (root / 'share/leapview').mkdir(parents=True)
        receipt.create_component('database', 'linux/amd64', REPO, evidence, root, root / 'share/leapview/native-build')
        return root, evidence

    def excel(self, http):
        root = self.root / 'excel'
        (root / 'lib').mkdir(parents=True)
        for name in receipt.excel.ARCHIVES:
            (root / name).write_bytes(b'!<arch>\nexcel')
        evidence = self.root / 'excel-evidence'
        evidence.mkdir()
        for name, data in excel_fixture(receipt.sources(REPO, 'excel')).items():
            (evidence / name).write_bytes(data)
        receipt.write(evidence / 'zlib-link.json', {'archive': str(http / 'lib/libz.a'), 'sha256': receipt.digest(http / 'lib/libz.a')})
        cache = evidence / 'minizip-cache.txt'
        cache.write_text(cache.read_text().replace('/nix/store/selected-zlib/lib/libz.a', str(http / 'lib/libz.a')))
        (root / 'share/leapview').mkdir(parents=True)
        receipt.create_component('excel', 'linux/amd64', REPO, evidence, root, root / 'share/leapview/native-build')
        return root, evidence

    def avro(self, http):
        root = self.root / 'avro'
        (root / 'lib').mkdir(parents=True)
        for name in receipt.avro.ARCHIVES:
            (root / name).write_bytes(b'!<arch>\navro')
        evidence = self.root / 'avro-evidence'
        evidence.mkdir()
        for name, data in avro_fixture(receipt.sources(REPO, 'avro')).items():
            (evidence / name).write_bytes(data)
        bindings = {name: {'archive': str(root / name), 'sha256': receipt.digest(root / name)} for name in receipt.avro.ARCHIVES}
        bindings['lib/libz.a'] = {'archive': str(http / 'lib/libz.a'), 'sha256': receipt.digest(http / 'lib/libz.a')}
        receipt.write(evidence / 'library-link.json', bindings)
        (evidence / 'avro-link.txt').write_text('cc ' + ' '.join(v['archive'] for v in bindings.values()))
        (root / 'share/leapview').mkdir(parents=True)
        receipt.create_component('avro', 'linux/amd64', REPO, evidence, root, root / 'share/leapview/native-build')
        return root, evidence

    def vortex(self):
        root = self.root / 'vortex'
        (root / 'lib').mkdir(parents=True)
        (root / 'lib/libvortex_duckdb.a').write_bytes(b'!<arch>\nvortex')
        evidence = self.root / 'vortex-evidence'
        evidence.mkdir()
        for name, data in vortex_fixture(receipt.sources(REPO, 'vortex'), tomllib.loads((REPO / 'nix/vortex-Cargo.lock').read_text())).items():
            (evidence / name).write_bytes(data)
        for name, encoded in json.loads((evidence / 'headers.json').read_bytes()).items():
            (root / name).parent.mkdir(parents=True, exist_ok=True)
            (root / name).write_bytes(base64.b64decode(encoded))
        (root / 'share/leapview').mkdir(parents=True)
        receipt.create_component('vortex', 'linux/amd64', REPO, evidence, root, root / 'share/leapview/native-build')
        return root, evidence

    def delta(self):
        root = self.root / 'delta'
        (root / 'lib').mkdir(parents=True)
        (root / 'include').mkdir()
        (root / 'lib/libdelta_kernel_ffi.a').write_bytes(b'!<arch>\ndelta')
        evidence = self.root / 'delta-evidence'
        evidence.mkdir()
        for name, data in delta_fixture(receipt.sources(REPO, 'delta'), tomllib.loads((REPO / 'nix/delta-Cargo.lock').read_text())).items():
            (evidence / name).write_bytes(data)
            if name in receipt.delta.HEADERS:
                (root / 'include' / name).write_bytes(data)
        (root / 'share/leapview').mkdir(parents=True)
        receipt.create_component('delta', 'linux/amd64', REPO, evidence, root, root / 'share/leapview/native-build')
        return root, evidence

    def azure(self, http):
        root = self.root / 'azure'
        (root / 'lib').mkdir(parents=True)
        for name in receipt.azure.ARCHIVES:
            (root / name).write_bytes(b'!<arch>\nazure')
        evidence = self.root / 'azure-evidence'
        evidence.mkdir()
        files = azure_fixture(receipt.sources(REPO, 'azure'))
        bindings = receipt.decode_json(files['native-link.json'])
        for name, binding in bindings.items():
            binding['sha256'] = receipt.digest((root if name == 'lib/libxml2.a' else http) / name)
        files['native-link.json'] = json.dumps(bindings).encode()
        for name, data in files.items():
            (evidence / name).write_bytes(data)
        (root / 'share/leapview').mkdir(parents=True)
        receipt.create_component('azure', 'linux/amd64', REPO, evidence, root, root / 'share/leapview/native-build')
        return root, evidence

    def application(self):
        croaring, _ = self.croaring()
        http, _ = self.http()
        database, _ = self.database()
        excel, _ = self.excel(http)
        avro, _ = self.avro(http)
        delta, _ = self.delta()
        azure, _ = self.azure(http)
        vortex, _ = self.vortex()
        lance = self.root / 'lance'
        (lance / 'lib').mkdir(parents=True)
        (lance / 'lib/liblance_duckdb_ffi.a').write_bytes(b'!<arch>\nlance')
        (lance / 'share/leapview').mkdir(parents=True)
        receipt.create_component('lance', 'linux/amd64', REPO, self.evidence, lance, lance / 'share/leapview/native-build')
        duckdb = self.root / 'duckdb'
        (duckdb / 'lib').mkdir(parents=True)
        for name in ('duckdb_static', 'lance_extension', 'sqlite_scanner_extension', 'ducklake_extension', 'httpfs_extension', 'quack_extension', 'postgres_scanner_extension', 'mysql_scanner_extension', 'excel_extension', 'avro_extension', 'delta_extension', 'azure_extension', 'vortex_extension', 'dummy_static_extension_loader'):
            (duckdb / f'lib/lib{name}.a').write_bytes(b'!<arch>\n' + name.encode())
        evidence = self.root / 'cmake'
        evidence.mkdir()
        (evidence / 'compiler.txt').write_text('gcc 15\ncompiler-target: x86_64-unknown-linux-gnu\n')
        (evidence / 'cmake-cache.txt').write_text('DUCKDB_EXPLICIT_PLATFORM:STRING=linux_amd64\nroaring_DIR:PATH=' + str(croaring) + '/lib/cmake/roaring\n')
        receipt.write(evidence / 'compile-commands.json', [{'file': '/store/sqlite/src/sqlite/sqlite3.c', 'command': 'cc -c sqlite3.c'}, {'file': '/store/ducklake/src/storage/ducklake_deletion_vector.cpp', 'command': 'c++ -c ducklake_deletion_vector.cpp'}])
        (evidence / 'extensions.cmake').write_text('duckdb_extension_load(lance SOURCE_DIR /store/lance)\nduckdb_extension_load(sqlite_scanner SOURCE_DIR /store/sqlite)\nduckdb_extension_load(ducklake SOURCE_DIR /store/ducklake EXTENSION_VERSION ' + receipt.sources(REPO, 'duckdb')['ducklake']['revision'] + ')\n')
        receipt.write(evidence / 'croaring-link.json', {'archive': str(croaring / 'lib/libroaring.a'), 'sha256': receipt.digest(croaring / 'lib/libroaring.a')})
        commands = receipt.load(evidence / 'compile-commands.json')
        cache = evidence / 'cmake-cache.txt'
        cache.write_text(cache.read_text() + ''.join(macro + ':FILEPATH=' + str(http / name) + '\n' for name, macro in (
            ('lib/libcurl.a', 'CURL_LIBRARY_RELEASE'), ('lib/libssl.a', 'OPENSSL_SSL_LIBRARY'), ('lib/libcrypto.a', 'OPENSSL_CRYPTO_LIBRARY'))))
        cache.write_text(cache.read_text() + 'HTTPFS_STATIC_LIBRARIES:STRING=' + ';'.join(str(http / name) for name in ('lib/libssl.a', 'lib/libcrypto.a', 'lib/libnghttp2.a', 'lib/libz.a')) + '\n')
        selection = evidence / 'extensions.cmake'
        for name, file in (('httpfs', 'src/httpfs.cpp'), ('quack', 'src/quack_client.cpp')):
            commands.append({'file': '/store/' + name + '/' + file, 'command': 'c++ -c ' + file})
            selection.write_text(selection.read_text() + 'duckdb_extension_load(' + name + ' SOURCE_DIR /store/' + name + ' EXTENSION_VERSION ' + receipt.sources(REPO, 'duckdb')['http'][name]['revision'] + ')\n')
        receipt.write(evidence / 'azure-link.json', {name: {'archive': str(azure / name), 'sha256': receipt.digest(azure / name)} for name in receipt.azure.ARCHIVES})
        cache.write_text(cache.read_text() + 'AZURE_LIBRARIES:STRING=' + ';'.join(str(azure / name) for name in sorted(receipt.azure.ARCHIVES)) + '\n')
        cache.write_text(cache.read_text() + 'AZURE_HTTP_LIBRARIES:STRING=' + ';'.join(str(http / name) for name in sorted(receipt.HTTP_ARCHIVES)) + '\n')
        selection.write_text(selection.read_text() + 'duckdb_extension_load(azure SOURCE_DIR /store/azure EXTENSION_VERSION ' + receipt.sources(REPO, 'azure')['wrapper']['revision'] + ')\n')
        commands += [{'file': '/store/azure/' + name, 'command': 'c++ -c source.cpp'} for name in ('src/azure_extension.cpp', 'src/azure_blob_filesystem.cpp', 'src/azure_dfs_filesystem.cpp', 'src/azure_secret.cpp')]
        receipt.write(evidence / 'avro-link.json', {name: {'archive': str(avro / name), 'sha256': receipt.digest(avro / name)} for name in receipt.avro.ARCHIVES})
        cache.write_text(cache.read_text() + ''.join(macro + ':FILEPATH=' + str(avro / name) + '\n' for name, macro in (('lib/libavro.a', 'AVRO_LIBRARY'), ('lib/libjansson.a', 'JANSSON_LIBRARY'), ('lib/libsnappy.a', 'SNAPPY_LIBRARY'), ('lib/liblzma.a', 'LZMA_LIBRARY'))))
        cache.write_text(cache.read_text() + 'ZLIB_LIBRARY:FILEPATH=' + str(http / 'lib/libz.a') + '\n')
        selection.write_text(selection.read_text() + 'duckdb_extension_load(avro SOURCE_DIR /store/avro EXTENSION_VERSION ' + receipt.sources(REPO, 'avro')['wrapper']['revision'] + ')\n')
        commands += [{'file': '/store/avro/' + name, 'command': 'c++ -c source.cpp'} for name in ('src/avro_extension.cpp', 'src/avro_reader.cpp', 'src/avro_copy.cpp', 'src/field_ids.cpp')]
        receipt.write(evidence / 'excel-link.json', {name: {'archive': str(excel / name), 'sha256': receipt.digest(excel / name)} for name in receipt.excel.ARCHIVES})
        cache.write_text(cache.read_text() + ''.join(macro + ':FILEPATH=' + str(excel / name) + '\n' for name, macro in (('lib/libexpat.a', 'EXPAT_LIBRARY'), ('lib/libminizip-ng.a', 'MINIZIP_LIBRARY'))))
        cache.write_text(cache.read_text() + 'ZLIB_LIBRARY_RELEASE:FILEPATH=' + str(http / 'lib/libz.a') + '\n')
        selection.write_text(selection.read_text() + 'duckdb_extension_load(excel SOURCE_DIR /store/excel INCLUDE_DIR /store/excel/src/excel/include EXTENSION_VERSION ' + receipt.sources(REPO, 'excel')['wrapper']['revision'] + ')\n')
        commands += [{'file': '/store/excel/' + name, 'command': 'c++ -c source.cpp'} for name in ('src/excel/excel_extension.cpp', 'src/excel/xlsx/zip_file.cpp', 'src/excel/numformat/nf_zformat.cpp')]
        commands.append({'file': '/build/generated_extension_loader.cpp', 'command': 'c++ -I/store/excel/src/excel/include -c generated_extension_loader.cpp'})
        receipt.write(evidence / 'compile-commands.json', commands)
        receipt.write(evidence / 'http-link.json', {name: {'archive': str(http / name), 'sha256': receipt.digest(http / name)} for name in receipt.HTTP_ARCHIVES})
        receipt.write(evidence / 'database-link.json', {name: {'archive': str(database / name), 'sha256': receipt.digest(database / name)} for name in receipt.database.ARCHIVES})
        cache.write_text(cache.read_text() + ''.join(macro + ':FILEPATH=' + str(database / name) + '\n' for name, macro in (('lib/libpq.a', 'PostgreSQL_LIBRARY_RELEASE'), ('lib/libmariadbclient.a', 'MYSQL_LIBRARIES'))))
        cache.write_text(cache.read_text() + 'DATABASE_STATIC_LIBRARIES:STRING=' + ';'.join([str(database / name) for name in ('lib/libpgcommon.a', 'lib/libpgport.a')] + [str(http / name) for name in ('lib/libssl.a', 'lib/libcrypto.a', 'lib/libz.a')]) + '\n')
        for name in ('postgres', 'mysql'):
            commands.append({'file': '/store/' + name + '/src/' + name + '_connection.cpp', 'command': 'c++ -c connector.cpp'})
            selection.write_text(selection.read_text() + 'duckdb_extension_load(' + name + '_scanner SOURCE_DIR /store/' + name + ' EXTENSION_VERSION ' + receipt.sources(REPO, 'database')['wrappers'][name]['revision'] + ')\n')
        receipt.write(evidence / 'compile-commands.json', commands)
        vortex_policy = receipt.sources(REPO, 'vortex')
        receipt.write(evidence / 'vortex-link.json', {'archive': str(vortex / 'lib/libvortex_duckdb.a'), 'sha256': receipt.digest(vortex / 'lib/libvortex_duckdb.a'), 'headers': {name: receipt.digest(vortex / name) for name in vortex_policy['headers']}, 'engineHeaders': vortex_policy['engine']['selectedFiles']})
        receipt.write(evidence / 'vortex-engine-source.json', vortex_policy['engine']['selectedFiles'])
        cache.write_text(cache.read_text() + 'VORTEX_FFI_LIBRARY:FILEPATH=' + str(vortex / 'lib/libvortex_duckdb.a') + '\nVORTEX_FFI_INCLUDE_DIR:PATH=' + str(vortex / 'include') + '\n')
        selection.write_text(selection.read_text() + 'duckdb_extension_load(vortex SOURCE_DIR /store/vortex EXTENSION_VERSION ' + vortex_policy['wrapper']['revision'] + ')\n')
        commands.append({'file': '/store/vortex/src/vortex_extension.cpp', 'command': 'c++ -I' + str(vortex / 'include') + ' -c vortex.cpp'})
        receipt.write(evidence / 'delta-link.json', {'archive': str(delta / 'lib/libdelta_kernel_ffi.a'), 'sha256': receipt.digest(delta / 'lib/libdelta_kernel_ffi.a'), 'headers': receipt.delta.header_hashes(delta / 'include', receipt.read)})
        cache.write_text(cache.read_text() + 'DELTA_KERNEL_LIBRARY:FILEPATH=' + str(delta / 'lib/libdelta_kernel_ffi.a') + '\nDELTA_KERNEL_INCLUDE_DIR:PATH=' + str(delta / 'include') + '\n')
        selection.write_text(selection.read_text() + 'duckdb_extension_load(delta SOURCE_DIR /store/delta EXTENSION_VERSION ' + receipt.sources(REPO, 'delta')['wrapper']['revision'] + ')\n')
        for name in ('src/delta_extension.cpp', 'src/functions/delta_scan/delta_scan.cpp'):
            commands.append({'file': '/store/delta/' + name, 'command': 'c++ -I' + str(delta / 'include') + ' -c delta.cpp'})
        receipt.write(evidence / 'compile-commands.json', commands)
        sqlite = receipt.sources(REPO, 'duckdb')['sqlite']['amalgamation']
        receipt.write(evidence / 'sqlite-source.json', {name: sqlite[name + 'SHA256'].removeprefix('sha256:') for name in ('sqlite3.c', 'sqlite3.h')})
        (duckdb / 'share/leapview').mkdir(parents=True)
        receipt.create_component('duckdb', 'linux/amd64', REPO, evidence, duckdb, duckdb / 'share/leapview/native-build')
        selected = [str(p) for p in sorted((duckdb / 'lib').glob('*.a')) if 'dummy_' not in p.name] + [str(lance / 'lib/liblance_duckdb_ffi.a'), str(croaring / 'lib/libroaring.a')] + [str(http / name) for name in sorted(receipt.HTTP_ARCHIVES)] + [str(database / name) for name in sorted(receipt.database.ARCHIVES)] + [str(excel / name) for name in sorted(receipt.excel.ARCHIVES)] + [str(avro / name) for name in sorted(receipt.avro.ARCHIVES)] + [str(delta / 'lib/libdelta_kernel_ffi.a')] + [str(azure / name) for name in sorted(receipt.azure.ARCHIVES)] + [str(vortex / 'lib/libvortex_duckdb.a')]
        inputs = self.root / 'link-inputs'
        inputs.write_text('\n'.join(selected) + '\n')
        app_evidence = self.root / 'app-evidence'
        app_evidence.mkdir()
        (app_evidence / 'link-flags.txt').write_text(' '.join(['-Wl,--start-group', *selected, '-Wl,--end-group', '-lstdc++', '-ldl', '-lm']))
        (app_evidence / 'go.txt').write_text('go version go1.26 linux/amd64\n')
        (app_evidence / 'tags.txt').write_text('duckdb_arrow,duckdb_use_static_lib,leapview_static_lance,leapview_static_sqlite,leapview_static_ducklake,leapview_static_http,leapview_static_database,leapview_static_excel,leapview_static_avro,leapview_static_delta,leapview_static_azure,leapview_static_vortex\n')
        binaries = self.root / 'bin'
        binaries.mkdir()
        for name in ('leapview', 'leapviewctl'):
            (binaries / name).write_bytes(b'\x7fELF\x02\x01' + b'\0' * 12 + bytes([62, 0]) + name.encode())
        destination = self.root / 'application'
        receipt.compose(REPO, 'linux/amd64', 'a' * 40, duckdb, lance, binaries, inputs, app_evidence, destination, croaring, http, database, excel, avro, delta, azure, vortex)
        return destination, binaries

    def test_vortex_missing_receipt_and_generated_header_rejected(self):
        directory, binaries = self.application()
        path = directory / 'vortex/receipt.json'
        original = path.read_bytes()
        path.unlink()
        with self.assertRaises(ValueError):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)
        path.write_bytes(original)
        root = self.root / 'vortex'
        header = root / 'include/vortex.h'
        header.write_bytes(b'substituted ABI')
        with self.assertRaisesRegex(ValueError, 'substitution'):
            receipt.verify_component(directory / 'vortex', 'vortex', 'linux/amd64', REPO, root)

    def test_delta_missing_receipt_archive_and_generated_header_rejected(self):
        directory, binaries = self.application()
        path = directory / 'delta/receipt.json'
        old = path.read_bytes(); path.unlink()
        with self.assertRaises((ValueError, FileNotFoundError)):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)
        path.write_bytes(old)
        root = self.root / 'delta'
        header = root / 'include/generated_delta_kernel_ffi.hpp'
        original = header.read_bytes(); header.write_bytes(b'substituted ABI')
        with self.assertRaisesRegex(ValueError, 'headers differ'):
            receipt.verify_component(directory / 'delta', 'delta', 'linux/amd64', REPO, root)
        header.write_bytes(original)
        (root / 'lib/libdelta_kernel_ffi.a').write_bytes(b'!<arch>\nsubstituted')
        with self.assertRaisesRegex(ValueError, 'substitution'):
            receipt.verify_component(directory / 'delta', 'delta', 'linux/amd64', REPO, root)

    def test_azure_missing_receipt_archive_and_shared_http_substitution_rejected(self):
        directory, binaries = self.application()
        path = directory / 'azure/receipt.json'
        old = path.read_bytes(); path.unlink()
        with self.assertRaises((ValueError, FileNotFoundError)):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)
        path.write_bytes(old)
        root = self.root / 'azure'
        (root / 'lib/libazure-core.a').write_bytes(b'!<arch>\nsubstituted')
        with self.assertRaisesRegex(ValueError, 'substitution'):
            receipt.verify_component(directory / 'azure', 'azure', 'linux/amd64', REPO, root)
        cache = self.root / 'cmake/cmake-cache.txt'
        cache.write_text(cache.read_text().replace('AZURE_HTTP_LIBRARIES:STRING=', 'AZURE_HTTP_LIBRARIES:STRING=/other;'))
        with self.assertRaisesRegex(ValueError, 'Azure engine selected dependency'):
            receipt.check_cmake(self.root / 'cmake', REPO, 'linux/amd64')

    def test_avro_receipt_missing_and_substituted_archive_rejected(self):
        directory, binaries = self.application()
        path = directory / 'avro/receipt.json'
        old = path.read_bytes(); path.unlink()
        with self.assertRaises((ValueError, FileNotFoundError)):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)
        path.write_bytes(old)
        root = self.root / 'avro'
        (root / 'lib/libavro.a').write_bytes(b'!<arch>\nsubstituted')
        with self.assertRaisesRegex(ValueError, 'substitution'):
            receipt.verify_component(directory / 'avro', 'avro', 'linux/amd64', REPO, root)

    def test_excel_receipt_missing_and_substituted_archive_rejected(self):
        directory, binaries = self.application()
        path = directory / 'excel/receipt.json'
        old = path.read_bytes(); path.unlink()
        with self.assertRaises((ValueError, FileNotFoundError)):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)
        path.write_bytes(old)
        root = self.root / 'excel'
        (root / 'lib/libminizip-ng.a').write_bytes(b'!<arch>\nsubstituted')
        with self.assertRaisesRegex(ValueError, 'substitution'):
            receipt.verify_component(directory / 'excel', 'excel', 'linux/amd64', REPO, root)

    def test_database_receipt_missing_and_substituted_archive_rejected(self):
        directory, binaries = self.application()
        path = directory / 'database/receipt.json'
        old = path.read_bytes(); path.unlink()
        with self.assertRaises((ValueError, FileNotFoundError)):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)
        path.write_bytes(old)
        root = self.root / 'database'
        (root / 'lib/libmariadbclient.a').write_bytes(b'!<arch>\nsubstituted')
        with self.assertRaisesRegex(ValueError, 'native output substitution'):
            receipt.verify_component(directory / 'database', 'database', 'linux/amd64', REPO, root)

    def test_application_missing_http_receipt_and_transitive_substitution_rejected(self):
        directory, binaries = self.application()
        path = directory / 'http/receipt.json'
        old = path.read_bytes(); path.unlink()
        with self.assertRaises((ValueError, FileNotFoundError)):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)
        path.write_bytes(old)
        evidence = self.root / 'cmake'
        cache = evidence / 'cmake-cache.txt'
        cache.write_text(cache.read_text().replace('libnghttp2.a', 'libunexpected.a'))
        with self.assertRaisesRegex(ValueError, 'transitive archive selection'):
            receipt.check_cmake(evidence, REPO, 'linux/amd64')

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

    def test_missing_croaring_receipt_and_substituted_archive_binding_rejected(self):
        directory, binaries = self.application()
        original = receipt.load(directory / 'application.json')
        changed = json.loads(json.dumps(original))
        changed['linkInputs']['croaring']['lib/libroaring.a'] = '0' * 64
        receipt.write(directory / 'application.json', changed)
        with self.assertRaisesRegex(ValueError, 'link input substitution'):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)
        receipt.write(directory / 'application.json', original)
        (directory / 'croaring/receipt.json').unlink()
        with self.assertRaises(ValueError):
            receipt.verify_application(directory, REPO, 'linux/amd64', 'a' * 40, binaries)

    def test_portable_transformation_binds_actual_shipped_bytes(self):
        directory, binaries = self.application()
        directory, binaries = self.runtime(directory, binaries)
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

    def runtime(self, directory, binaries):
        output = self.root / 'rewritten-bin'
        output.mkdir()
        for binary in binaries.iterdir():
            (output / binary.name).write_bytes(binary.read_bytes() + b'rewritten runtime references')
        pairs = self.root / 'replacements.json'
        receipt.write(pairs, [
            {'old': '/nix/store/' + 'a' * 32 + '-glibc-2.40', 'new': '/nix/store/' + 'b' * 32 + '-glibc-2.40'},
            {'old': '/nix/store/' + 'c' * 32 + '-gcc-15-lib', 'new': '/nix/store/' + 'd' * 32 + '-gcc-15-lib'},
        ])
        destination = self.root / 'runtime-receipts'
        shutil.copytree(directory, destination)
        receipt.runtime_rewrite(directory, REPO, 'linux/amd64', 'a' * 40, binaries, output, destination, pairs)
        return destination, output

    def test_runtime_rewrite_binds_original_and_replaced_bytes(self):
        directory, binaries = self.application()
        destination, rewritten = self.runtime(directory, binaries)
        result = receipt.verify_runtime(destination, REPO, 'linux/amd64', 'a' * 40, rewritten)
        self.assertIn('runtimeReceiptSHA256', result)
        with self.assertRaisesRegex(ValueError, 'runtime output substitution'):
            receipt.verify_runtime(destination, REPO, 'linux/amd64', 'a' * 40, binaries)
        self.assertNotIn(b'/nix/store/', (destination / 'runtime-replacements.json.b64').read_bytes())
        encoded = destination / 'runtime-replacements.json.b64'
        original_mapping = encoded.read_bytes()
        encoded.write_bytes(base64.b64encode(base64.b64decode(original_mapping).replace(b'b' * 32, b'e' * 32)))
        with self.assertRaisesRegex(ValueError, 'replacement mapping substitution'):
            receipt.verify_runtime(destination, REPO, 'linux/amd64', 'a' * 40, rewritten)
        encoded.write_bytes(original_mapping)
        value = receipt.load(destination / 'runtime.json')
        value['inputOutputs']['leapview'] = '0' * 64
        receipt.write(destination / 'runtime.json', value)
        with self.assertRaisesRegex(ValueError, 'runtime transformation input substitution'):
            receipt.verify_runtime(destination, REPO, 'linux/amd64', 'a' * 40, rewritten)

    def test_consumer_cli_cannot_skip_actual_binary_verification(self):
        for command in ('verify', 'verify-runtime', 'verify-portable'):
            with self.subTest(command=command):
                result = subprocess.run([sys.executable, str(SCRIPT), command,
                    '--repo', str(REPO), '--platform', 'linux/amd64', '--revision', 'a' * 40,
                    '--destination', str(self.root)], capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('requires --binaries', result.stderr)

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

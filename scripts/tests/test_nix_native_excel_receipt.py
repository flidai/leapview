import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_native_excel_receipt as excel


def excel_fixture(policy):
    files = {}
    for name in excel.LIBRARIES:
        files[name + '-source.json'] = json.dumps(policy['libraries'][name]['selectedFiles']).encode()
        files[name + '-compiler.txt'] = b'gcc\ncompiler-target: x86_64-unknown-linux-gnu\n'
    files['consumer.txt'] = b'selected static Expat/minizip/zlib deflated worksheet roundtrip passed\n'
    files['consumer-needed.txt'] = b' (NEEDED) Shared library: [libc.so.6]\n'
    files['expat-options.txt'] = b'--enable-static --disable-shared --with-pic'
    files['expat-config.txt'] = b'#define XML_DTD 1\n#define XML_NS 1\n#define XML_GE 1\n'
    files['expat-build.txt'] = b'cc -fPIC -c lib/xmlparse.c\ncc -fPIC -c lib/xmltok.c\n'
    files['minizip-cache.txt'] = ('\n'.join(f'{key}:BOOL={"ON" if value else "OFF"}' for key, value in policy['minizipFeatures'].items()) + '\nMZ_ZLIB_FLAVOR:STRING=zlib\nZLIB_LIBRARY_RELEASE:FILEPATH=/nix/store/selected-zlib/lib/libz.a\nCMAKE_HOME_DIRECTORY:INTERNAL=/build/minizip\n').encode()
    files['minizip-commands.json'] = json.dumps([{'file': '/build/minizip/' + name, 'command': 'cc -fPIC -c ' + name} for name in ('mz_zip.c', 'mz_zip_rw.c', 'mz_strm_zlib.c')]).encode()
    files['zlib-link.json'] = json.dumps({'archive': '/nix/store/selected-zlib/lib/libz.a', 'sha256': 'a' * 64}).encode()

    return files


class ExcelLibraryProofTests(unittest.TestCase):
    def setUp(self):
        self.policy = json.loads((Path(__file__).resolve().parents[2] / 'nix/excel-source-lock.json').read_text())
        self.files = excel_fixture(self.policy)

    def check(self):
        excel.check(Path('/evidence'), self.policy, 'linux/amd64', lambda p: self.files[p.name])

    def test_accepts_selected_static_parser_and_zip_closure(self):
        self.check()

    def test_rejects_each_unrequested_codec_crypto_or_fetch_feature(self):
        original = self.files['minizip-cache.txt']
        for name, enabled in self.policy['minizipFeatures'].items():
            if not enabled:
                with self.subTest(name=name):
                    self.files['minizip-cache.txt'] = original.replace((name + ':BOOL=OFF').encode(), (name + ':BOOL=ON').encode())
                    with self.assertRaisesRegex(ValueError, 'selected feature differs'):
                        self.check()
        self.files['minizip-cache.txt'] = original

    def test_accepts_cmake_platform_disabled_internal_option(self):
        self.files['minizip-cache.txt'] = self.files['minizip-cache.txt'].replace(b'MZ_LIBCOMP:BOOL=OFF', b'MZ_LIBCOMP:INTERNAL=OFF')
        self.files['minizip-cache.txt'] = self.files['minizip-cache.txt'].replace(b'ZLIB_LIBRARY_RELEASE:FILEPATH=', b'ZLIB_LIBRARY_RELEASE:STRING=')
        self.check()
        self.files['minizip-cache.txt'] = self.files['minizip-cache.txt'].replace(b'MZ_LIBCOMP:INTERNAL=OFF', b'MZ_LIBCOMP:INTERNAL=ON')
        with self.assertRaisesRegex(ValueError, 'selected feature differs'):
            self.check()

    def test_rejects_dynamic_parser_or_missing_consumer(self):
        self.files['consumer-needed.txt'] += b' (NEEDED) Shared library: [libexpat.so.1]\n'
        with self.assertRaisesRegex(ValueError, 'dynamic library selection differs'):
            self.check()
        self.files['consumer.txt'] = b''
        with self.assertRaisesRegex(ValueError, 'consumer roundtrip missing'):
            self.check()

    def test_rejects_substituted_zlib_binding(self):
        self.files['zlib-link.json'] = self.files['zlib-link.json'].replace(b'selected-zlib', b'other-zlib')
        with self.assertRaisesRegex(ValueError, 'selected zlib archive differs'):
            self.check()

    def test_rejects_wrong_architecture(self):
        self.files['expat-compiler.txt'] = b'compiler-target: aarch64-unknown-linux-gnu\n'
        with self.assertRaisesRegex(ValueError, 'compiler target differs'):
            self.check()

    def test_rejects_uncompiled_codec_or_missing_pic(self):
        original = self.files['minizip-commands.json']
        for before, after in [(b'mz_strm_zlib.c', b'not_selected.c'), (b'-fPIC', b'')]:
            self.files['minizip-commands.json'] = original.replace(before, after)
            with self.assertRaisesRegex(ValueError, 'required PIC compilation missing'):
                self.check()

    def test_rejects_source_drift_and_duplicate_fields(self):
        self.files['expat-source.json'] = b'{"lib/xmlparse.c":"wrong"}'
        with self.assertRaisesRegex(ValueError, 'source identity differs'):
            self.check()
        with self.assertRaisesRegex(ValueError, 'duplicate Excel evidence'):
            excel.decode(b'{"archive":"a","archive":"b"}')

    def test_rejects_shared_expat_and_host_specific_instruction_selection(self):
        self.files['expat-options.txt'] += b' --enable-shared'
        with self.assertRaisesRegex(ValueError, 'static/PIC selection differs'):
            self.check()
        self.files['expat-options.txt'] = b'--enable-static --disable-shared --with-pic'
        self.files['expat-build.txt'] += b'cc -march=native -c extra.c\n'
        with self.assertRaisesRegex(ValueError, 'host-specific'):
            self.check()


class ExcelLoaderProofTests(unittest.TestCase):
    def setUp(self):
        self.policy = json.loads((Path(__file__).resolve().parents[2] / 'nix/excel-source-lock.json').read_text())
        self.files = {
            'excel-link.json': json.dumps({name: {'archive': '/store/excel-libs/' + name, 'sha256': 'a' * 64} for name in excel.ARCHIVES}).encode(),
            'http-link.json': json.dumps({'lib/libz.a': {'archive': '/store/zlib/lib/libz.a', 'sha256': 'b' * 64}}).encode(),
        }
        self.cache = 'EXPAT_LIBRARY:FILEPATH=/store/excel-libs/lib/libexpat.a\nMINIZIP_LIBRARY:FILEPATH=/store/excel-libs/lib/libminizip-ng.a\nZLIB_LIBRARY_RELEASE:FILEPATH=/store/zlib/lib/libz.a\n'
        self.selection = 'duckdb_extension_load(excel SOURCE_DIR /store/excel INCLUDE_DIR /store/excel/src/excel/include EXTENSION_VERSION ' + self.policy['wrapper']['revision'] + ')'
        self.commands = [{'file': '/store/excel/' + name, 'command': 'c++ -c source.cpp'} for name in ('src/excel/excel_extension.cpp', 'src/excel/xlsx/zip_file.cpp', 'src/excel/numformat/nf_zformat.cpp')]
        self.commands.append({'file': '/build/generated_extension_loader.cpp', 'command': 'c++ -I/store/excel/src/excel/include -c generated_extension_loader.cpp'})

    def check(self):
        excel.check_engine(Path('/evidence'), self.policy, self.cache, self.commands, self.selection, lambda p: self.files[p.name])

    def test_accepts_real_excel_header_directory(self):
        self.check()

    def test_rejects_default_or_substituted_header_directory(self):
        original = self.selection
        for include in ('', ' INCLUDE_DIR /store/excel/src/include', ' INCLUDE_DIR /store/other/src/excel/include'):
            with self.subTest(include=include):
                self.selection = original.replace(' INCLUDE_DIR /store/excel/src/excel/include', include)
                with self.assertRaisesRegex(ValueError, 'Excel loader header'):
                    self.check()

    def test_rejects_loader_compiled_without_selected_header_directory(self):
        self.commands[-1]['command'] = 'c++ -I/store/excel/src/include -c generated_extension_loader.cpp'
        with self.assertRaisesRegex(ValueError, 'Excel loader header'):
            self.check()


if __name__ == '__main__':
    unittest.main()

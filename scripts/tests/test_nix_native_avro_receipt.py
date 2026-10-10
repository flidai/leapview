import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_native_avro_receipt as avro


def avro_fixture(policy):
    files = {}
    for name in avro.LIBRARIES:
        files[name + '-source.json'] = json.dumps(policy['libraries'][name]['selectedFiles']).encode()
        files[name + '-compiler.txt'] = b'gcc\ncompiler-target: x86_64-unknown-linux-gnu\n'
    for name, features in policy['cmakeFeatures'].items():
        home = '/build/' + name
        files[name + '-cache.txt'] = ('\n'.join(f'{key}:BOOL={"ON" if value else "OFF"}' for key, value in features.items()) + '\nCMAKE_HOME_DIRECTORY:INTERNAL=' + home + '\n').encode()
        flags = '-std=gnu17 -fPIC -DDEFLATE_CODEC -DSNAPPY_CODEC -DLZMA_CODEC -DTHREADSAFE'
        if name == 'avro':
            files[name + '-cache.txt'] += b'CMAKE_C_STANDARD:STRING=17\n'
        files[name + '-commands.json'] = json.dumps([{'file': home + '/' + source, 'command': 'cc ' + flags + ' -c ' + source} for source in avro.COMPILED[name]]).encode()
    bindings = {name: {'archive': '/nix/store/selected/' + name, 'sha256': 'a' * 64} for name in avro.ARCHIVES | {'lib/libz.a'}}
    files['library-link.json'] = json.dumps(bindings).encode()
    files['avro-link.txt'] = ('cc ' + ' '.join(v['archive'] for name, v in bindings.items() if name != 'lib/libavro.a')).encode()
    files['xz-options.txt'] = b'--enable-static --disable-shared --with-pic'
    files['xz-build.txt'] = b'cc -fPIC -c lzma_encoder.c\ncc -fPIC -c stream_decoder.c\n'
    files['checks.txt'] = b'jansson upstream tests passed\nxz upstream tests passed\navro upstream tests passed\n'
    files['consumer.txt'] = avro.CONSUMER
    files['consumer-needed.txt'] = b'(NEEDED) Shared library: [libc.so.6]\n'
    return files


class AvroReceiptTests(unittest.TestCase):
    def setUp(self):
        self.policy = json.loads((Path(__file__).resolve().parents[2] / 'nix/avro-source-lock.json').read_text())
        self.files = avro_fixture(self.policy)

    def check(self):
        avro.check(Path('/evidence'), self.policy, 'linux/amd64', lambda p: self.files[p.name])

    def test_accepts_selected_fork_and_all_codecs(self):
        self.check()

    def test_rejects_uncompiled_codec_pic_and_thread_support(self):
        original = self.files['avro-commands.json']
        for token in (b'codec.c', b'-fPIC', b'-DDEFLATE_CODEC', b'-DSNAPPY_CODEC', b'-DLZMA_CODEC', b'-DTHREADSAFE', b'-std=gnu17'):
            with self.subTest(token=token):
                self.files['avro-commands.json'] = original.replace(token, b'wrong')
                with self.assertRaisesRegex(ValueError, 'compilation'):
                    self.check()

    def test_rejects_generic_upstream_source_and_wrong_target(self):
        self.files['avro-source.json'] = b'{}'
        with self.assertRaisesRegex(ValueError, 'source identity'):
            self.check()
        self.files = avro_fixture(self.policy)
        self.files['snappy-compiler.txt'] = b'compiler-target: aarch64-unknown-linux-gnu\n'
        with self.assertRaisesRegex(ValueError, 'compiler target'):
            self.check()

    def test_rejects_missing_or_substituted_archive_binding(self):
        original = self.files['library-link.json']
        for name in avro.ARCHIVES | {'lib/libz.a'}:
            bindings = json.loads(original)
            del bindings[name]
            self.files['library-link.json'] = json.dumps(bindings).encode()
            with self.assertRaisesRegex(ValueError, 'archive binding'):
                self.check()
        self.files['library-link.json'] = original
        self.files['avro-link.txt'] = self.files['avro-link.txt'].replace(b'selected/lib/libsnappy.a', b'substituted/lib/libsnappy.a')
        with self.assertRaisesRegex(ValueError, 'selected link'):
            self.check()

    def test_rejects_dynamic_or_disabled_codec_and_missing_test(self):
        for name, delta in [('consumer-needed.txt', b'(NEEDED) Shared library: [libsnappy.so.1]\n'), ('avro-cache.txt', b'BUILD_SHARED_LIBS:BOOL=ON\n')]:
            self.files = avro_fixture(self.policy)
            self.files[name] += delta
            with self.assertRaises(ValueError):
                self.check()
        self.files = avro_fixture(self.policy)
        self.files['consumer.txt'] = avro.CONSUMER.replace(b'snappy', b'')
        with self.assertRaisesRegex(ValueError, 'consumer'):
            self.check()
        self.files = avro_fixture(self.policy)
        self.files['checks.txt'] = b''
        with self.assertRaisesRegex(ValueError, 'upstream tests'):
            self.check()

    def test_rejects_host_specific_selection_and_duplicate_json(self):
        self.files['xz-build.txt'] += b'cc -march=native -c extra.c\n'
        with self.assertRaisesRegex(ValueError, 'host-specific'):
            self.check()
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            avro.decode(b'{"archive":"a","archive":"b"}')


if __name__ == '__main__':
    unittest.main()

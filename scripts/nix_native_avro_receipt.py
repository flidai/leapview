"""Selected custom Avro-C and codec build, not whole-application admission."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import shlex

LIBRARIES = ('jansson', 'snappy', 'xz', 'avro')
ARCHIVES = {'lib/libavro.a', 'lib/libjansson.a', 'lib/libsnappy.a', 'lib/liblzma.a'}
RECIPES = ('nix/check-extension-version.py', 'nix/avro.nix', 'nix/avro-source-lock.json',
           'nix/avro-static-dependencies.patch', 'nix/avro-c-static-only.patch',
           'nix/avro-c-available-tests.patch',
           'nix/check-avro-static-libraries.c', 'scripts/nix_native_avro_receipt.py')
COMPILED = {'jansson': ('src/load.c', 'src/dump.c'), 'snappy': ('snappy.cc', 'snappy-c.cc'),
            'avro': ('src/schema.c', 'src/codec.c', 'src/datafile.c')}
EVIDENCE = {name + '-' + kind for name in LIBRARIES for kind in ('source.json', 'compiler.txt')} | {
    name + '-' + kind for name in COMPILED for kind in ('cache.txt', 'commands.json')} | {
    'xz-options.txt', 'xz-build.txt', 'avro-link.txt', 'library-link.json',
    'checks.txt', 'consumer.txt', 'consumer-needed.txt'}
CONSUMER = b'selected static Avro fork null/deflate/snappy/lzma codec roundtrips and logical-schema checks passed\n'


def decode(data):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate Avro evidence JSON key')
            result[key] = value
        return result
    return json.loads(data, object_pairs_hook=pairs)


def bindings(data, expected):
    value = decode(data)
    if set(value) != expected or any(set(v) != {'archive', 'sha256'} or not v['archive'].endswith('/' + name) or not re.fullmatch('[0-9a-f]{64}', v['sha256']) for name, v in value.items()):
        raise ValueError('Avro archive binding differs')
    return value


def check(evidence, policy, platform, read):
    arch = {'linux/amd64': 'x86_64', 'linux/arm64': 'aarch64'}[platform]
    commands = ''
    for name in LIBRARIES:
        if decode(read(evidence / (name + '-source.json'))) != policy['libraries'][name]['selectedFiles']:
            raise ValueError('Avro dependency selected source identity differs: ' + name)
        compiler = read(evidence / (name + '-compiler.txt')).decode()
        if not re.search(r'^compiler-target: ' + arch + r'-[^\n]*linux[^\n]*$', compiler, re.M):
            raise ValueError('Avro dependency compiler target differs: ' + name)
    for name, features in policy['cmakeFeatures'].items():
        cache = read(evidence / (name + '-cache.txt')).decode()
        for option, enabled in features.items():
            values = re.findall(r'^' + option + r':(?:BOOL|INTERNAL)=([^\n]+)$', cache, re.M)
            if len(values) != 1 or values[0] not in (('ON', 'TRUE') if enabled else ('OFF', 'FALSE')):
                raise ValueError('Avro selected feature differs: ' + option)
        if name == 'avro' and not re.search(r'^CMAKE_C_STANDARD:STRING=' + str(policy['avroCStandard']) + r'$', cache, re.M):
            raise ValueError('Avro selected C dialect differs')
        home = re.search(r'^CMAKE_HOME_DIRECTORY:INTERNAL=([^\n]+)$', cache, re.M)
        compiled = decode(read(evidence / (name + '-commands.json')))
        for source in COMPILED[name]:
            flags = ['-fPIC'] + (['-DDEFLATE_CODEC', '-DSNAPPY_CODEC', '-DLZMA_CODEC', '-DTHREADSAFE', '-std=gnu' + str(policy['avroCStandard'])] if name == 'avro' else [])
            if not home or not any(c.get('file') == home[1] + '/' + source and all(f in shlex.split(c.get('command', '')) for f in flags) for c in compiled):
                raise ValueError('Avro required PIC/codec/thread compilation missing: ' + source)
        commands += json.dumps(compiled) + '\n'
    options = shlex.split(read(evidence / 'xz-options.txt').decode())
    if not {'--enable-static', '--disable-shared', '--with-pic'} <= set(options) or '--enable-shared' in options:
        raise ValueError('xz static/PIC selection differs')
    xz = read(evidence / 'xz-build.txt').decode()
    for source in ('lzma_encoder.c', 'stream_decoder.c'):
        if not any(source in line and ' -c ' in line and '-fPIC' in line for line in xz.splitlines()):
            raise ValueError('xz required PIC compilation missing: ' + source)
    selected = bindings(read(evidence / 'library-link.json'), ARCHIVES | {'lib/libz.a'})
    linked = shlex.split(read(evidence / 'avro-link.txt').decode())
    for name in (ARCHIVES | {'lib/libz.a'}) - {'lib/libavro.a'}:
        if selected[name]['archive'] not in linked:
            raise ValueError('Avro selected link input differs: ' + name)
    if read(evidence / 'checks.txt') != b'jansson upstream tests passed\nxz upstream tests passed\navro upstream tests passed\n':
        raise ValueError('Avro dependency upstream tests missing')
    if read(evidence / 'consumer.txt') != CONSUMER:
        raise ValueError('Avro codec/logical-schema consumer missing')
    needed = read(evidence / 'consumer-needed.txt').decode()
    if not re.search(r'NEEDED.*libc\.so', needed) or re.search(r'NEEDED.*(?:libavro|libjansson|libsnappy|liblzma|libz\.so)', needed):
        raise ValueError('Avro native consumer dynamic library selection differs')
    if '-march=native' in commands + xz or '-mcpu=native' in commands + xz:
        raise ValueError('Avro dependency uses host-specific instruction selection')


def check_engine(evidence, policy, cache, commands, selection, read):
    selected = bindings(read(evidence / 'avro-link.json'), ARCHIVES)
    for name, macro in (('lib/libavro.a', 'AVRO_LIBRARY'), ('lib/libjansson.a', 'JANSSON_LIBRARY'), ('lib/libsnappy.a', 'SNAPPY_LIBRARY'), ('lib/liblzma.a', 'LZMA_LIBRARY')):
        if not re.search(r'^' + macro + r':[^=]+=' + re.escape(selected[name]['archive']) + r'$', cache, re.M):
            raise ValueError('Avro CMake library selection differs: ' + macro)
    http = decode(read(evidence / 'http-link.json'))
    if not re.search(r'^ZLIB_LIBRARY:[^=]+=' + re.escape(http['lib/libz.a']['archive']) + r'$', cache, re.M):
        raise ValueError('Avro CMake shared zlib selection differs')
    wrapper = re.search(r'duckdb_extension_load\(avro SOURCE_DIR ([^\s()]+) EXTENSION_VERSION ' + policy['wrapper']['revision'] + r'\)', selection)
    for name in ('src/avro_extension.cpp', 'src/avro_reader.cpp', 'src/avro_copy.cpp', 'src/field_ids.cpp'):
        if not wrapper or not any(c.get('file') == wrapper[1] + '/' + name for c in commands):
            raise ValueError('compiled Avro wrapper selection missing: ' + name)


def main():
    import nix_native_build_receipt as receipt
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['source', 'libraries'])
    for name in ('repo', 'output-root', 'destination'):
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--library', choices=LIBRARIES)
    parser.add_argument('--evidence', type=Path)
    parser.add_argument('--platform', choices=receipt.PLATFORMS)
    parser.add_argument('--zlib', type=Path)
    args = parser.parse_args()
    policy = receipt.load(args.repo / 'nix/avro-source-lock.json')
    if args.command == 'source':
        expected = policy['libraries'][args.library]['selectedFiles']
        actual = {name: receipt.digest(args.output_root / name) for name in expected}
        if actual != expected:
            raise ValueError('Avro dependency selected source differs')
        receipt.write(args.destination, actual)
        return
    check(args.evidence, policy, args.platform, receipt.evidence_read)
    selected = bindings(receipt.evidence_read(args.evidence / 'library-link.json'), ARCHIVES | {'lib/libz.a'})
    for name, value in selected.items():
        archive = args.zlib if name == 'lib/libz.a' else Path(value['archive'])
        if value['sha256'] != receipt.digest(archive):
            raise ValueError('Avro linked archive bytes differ')
        if name != 'lib/libz.a' and value['sha256'] != receipt.digest(args.output_root / name):
            raise ValueError('Avro retained archive differs')
        if not receipt.read(archive).startswith(b'!<arch>\n'):
            raise ValueError('Avro native output is not an archive')
    receipt.write(args.destination, {'schemaVersion': 1, 'platform': args.platform,
        'scope': 'selected native library build; engine linkage and whole-application admission not asserted',
        'policySHA256': receipt.digest(args.repo / 'nix/avro-source-lock.json'), 'libraries': selected,
        'evidence': {name: hashlib.sha256(receipt.evidence_read(args.evidence / name)).hexdigest() for name in sorted(EVIDENCE)}})


if __name__ == '__main__':
    main()

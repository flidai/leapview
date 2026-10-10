"""Selected Excel native libraries; authenticated build context remains required."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import shlex

LIBRARIES = ('expat', 'minizip')
ARCHIVES = {'lib/libexpat.a', 'lib/libminizip-ng.a'}
RECIPES = ('nix/check-extension-version.py', 'nix/excel.nix', 'nix/excel-source-lock.json',
           'nix/excel-static-dependencies.patch', 'nix/check-excel-static-libraries.c',
           'scripts/nix_native_excel_receipt.py')
EVIDENCE = {name + '-' + kind for name in LIBRARIES for kind in ('source.json', 'compiler.txt')} | {
    'expat-build.txt', 'expat-options.txt', 'expat-config.txt',
    'minizip-cache.txt', 'minizip-commands.json', 'zlib-link.json', 'consumer.txt', 'consumer-needed.txt',
}


def decode(data):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate Excel evidence JSON key')
            result[key] = value
        return result
    return json.loads(data, object_pairs_hook=pairs)


def check(evidence, policy, platform, read):
    arch = {'linux/amd64': 'x86_64', 'linux/arm64': 'aarch64'}[platform]
    for name in LIBRARIES:
        if decode(read(evidence / (name + '-source.json'))) != policy['libraries'][name]['selectedFiles']:
            raise ValueError('Excel dependency selected source identity differs: ' + name)
        compiler = read(evidence / (name + '-compiler.txt')).decode()
        if not re.search(r'^compiler-target: ' + arch + r'-[^\n]*linux[^\n]*$', compiler, re.M):
            raise ValueError('Excel dependency compiler target differs: ' + name)
    options = shlex.split(read(evidence / 'expat-options.txt').decode())
    if not {'--enable-static', '--disable-shared', '--with-pic'} <= set(options) or '--enable-shared' in options:
        raise ValueError('Expat static/PIC selection differs')
    config = read(evidence / 'expat-config.txt').decode()
    for macro in ('XML_DTD', 'XML_NS', 'XML_GE'):
        if not re.search(r'^#define ' + macro + r' 1$', config, re.M):
            raise ValueError('Expat parser feature differs: ' + macro)
    commands = read(evidence / 'expat-build.txt').decode()
    for name in ('xmlparse.c', 'xmltok.c'):
        if not any(name in line and ' -c ' in line and '-fPIC' in line for line in commands.splitlines()):
            raise ValueError('Expat actual PIC compilation missing: ' + name)
    cache = read(evidence / 'minizip-cache.txt').decode()
    for name, enabled in policy['minizipFeatures'].items():
        selected = '(ON|TRUE)' if enabled else '(OFF|FALSE)'
        if not re.search(r'^' + name + r':(?:BOOL|INTERNAL)=' + selected + r'$', cache, re.M):
            raise ValueError('minizip selected feature differs: ' + name)
    if not re.search(r'^MZ_ZLIB_FLAVOR:STRING=zlib$', cache, re.M):
        raise ValueError('minizip zlib flavor differs')
    zlib = decode(read(evidence / 'zlib-link.json'))
    if set(zlib) != {'archive', 'sha256'} or not zlib['archive'].endswith('/lib/libz.a') or not re.fullmatch('[0-9a-f]{64}', zlib['sha256']):
        raise ValueError('Excel zlib archive binding differs')
    if not re.search(r'^ZLIB_LIBRARY_RELEASE:(?:FILEPATH|STRING)=' + re.escape(zlib['archive']) + r'$', cache, re.M):
        raise ValueError('minizip selected zlib archive differs')
    home = re.search(r'^CMAKE_HOME_DIRECTORY:INTERNAL=([^\n]+)$', cache, re.M)
    compiled = decode(read(evidence / 'minizip-commands.json'))
    for name in ('mz_zip.c', 'mz_zip_rw.c', 'mz_strm_zlib.c'):
        if not home or not any(c.get('file') == home[1] + '/' + name and '-fPIC' in c.get('command', '') for c in compiled):
            raise ValueError('minizip required PIC compilation missing: ' + name)
    if read(evidence / 'consumer.txt') != b'selected static Expat/minizip/zlib deflated worksheet roundtrip passed\n':
        raise ValueError('Excel native consumer roundtrip missing')
    needed = read(evidence / 'consumer-needed.txt').decode()
    if not re.search(r'NEEDED.*libc\.so', needed) or re.search(r'NEEDED.*(?:libexpat|libminizip|libz\.so)', needed):
        raise ValueError('Excel native consumer dynamic library selection differs')
    commands += '\n' + json.dumps(compiled)
    if '-march=native' in commands or '-mcpu=native' in commands:
        raise ValueError('Excel dependency uses host-specific instruction selection')


def check_engine(evidence, policy, cache, commands, selection, read):
    bindings = decode(read(evidence / 'excel-link.json'))
    if set(bindings) != ARCHIVES or any(set(v) != {'archive', 'sha256'} or not v['archive'].endswith('/' + name) or not re.fullmatch('[0-9a-f]{64}', v['sha256']) for name, v in bindings.items()):
        raise ValueError('Excel CMake dependency bindings differ')
    for name, macro in (('lib/libexpat.a', 'EXPAT_LIBRARY'), ('lib/libminizip-ng.a', 'MINIZIP_LIBRARY')):
        if not re.search(r'^' + macro + r':[^=]+=' + re.escape(bindings[name]['archive']) + r'$', cache, re.M):
            raise ValueError('Excel CMake library selection differs: ' + macro)
    http = decode(read(evidence / 'http-link.json'))
    if not re.search(r'^ZLIB_LIBRARY_RELEASE:[^=]+=' + re.escape(http['lib/libz.a']['archive']) + r'$', cache, re.M):
        raise ValueError('Excel CMake shared zlib selection differs')
    selected = re.search(r'duckdb_extension_load\(excel SOURCE_DIR ([^\s()]+) INCLUDE_DIR ([^\s()]+) EXTENSION_VERSION ' + policy['wrapper']['revision'] + r'\)', selection)
    if not selected or selected[2] != selected[1] + '/src/excel/include':
        raise ValueError('Excel loader header directory differs')
    if not any(Path(c.get('file', '')).name == 'generated_extension_loader.cpp' and
               '-I' + selected[2] in shlex.split(c.get('command', '')) for c in commands):
        raise ValueError('Excel loader header directory missing from compilation')
    for name in ('src/excel/excel_extension.cpp', 'src/excel/xlsx/zip_file.cpp', 'src/excel/numformat/nf_zformat.cpp'):
        if not selected or not any(c.get('file') == selected[1] + '/' + name for c in commands):
            raise ValueError('compiled Excel wrapper selection missing: ' + name)


def main():
    import nix_native_build_receipt as receipt
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['source', 'libraries'])
    parser.add_argument('--repo', type=Path, required=True)
    parser.add_argument('--output-root', type=Path, required=True)
    parser.add_argument('--destination', type=Path, required=True)
    parser.add_argument('--library', choices=LIBRARIES)
    parser.add_argument('--evidence', type=Path)
    parser.add_argument('--platform', choices=receipt.PLATFORMS)
    parser.add_argument('--zlib', type=Path)
    args = parser.parse_args()
    policy = receipt.load(args.repo / 'nix/excel-source-lock.json')
    if args.command == 'source':
        expected = policy['libraries'][args.library]['selectedFiles']
        actual = {name: receipt.digest(args.output_root / name) for name in expected}
        if actual != expected:
            raise ValueError('Excel dependency selected source differs')
        receipt.write(args.destination, actual)
        return
    check(args.evidence, policy, args.platform, receipt.evidence_read)
    binding = decode(receipt.evidence_read(args.evidence / 'zlib-link.json'))
    if binding != {'archive': str(args.zlib), 'sha256': receipt.digest(args.zlib)}:
        raise ValueError('Excel linked zlib bytes differ')
    archives = {name: receipt.digest(args.output_root / name) for name in sorted(ARCHIVES)}
    for name in ARCHIVES:
        if not receipt.read(args.output_root / name).startswith(b'!<arch>\n'):
            raise ValueError('Excel native output is not an archive')
    receipt.write(args.destination, {'schemaVersion': 1, 'platform': args.platform,
        'scope': 'selected native library build; engine linkage and whole-application admission not asserted',
        'policySHA256': receipt.digest(args.repo / 'nix/excel-source-lock.json'),
        'archives': archives, 'zlib': binding,
        'evidence': {name: hashlib.sha256(receipt.evidence_read(args.evidence / name)).hexdigest() for name in sorted(EVIDENCE)}})


if __name__ == '__main__':
    main()

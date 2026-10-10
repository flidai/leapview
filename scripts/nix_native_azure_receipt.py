"""Selected Azure SDK source evidence, not whole-application admission."""
import argparse
import hashlib
import json
import re
import shlex
from pathlib import Path


LIBRARIES = ('core', 'identity', 'storage-common', 'storage-blobs', 'storage-files-datalake', 'libxml2')
ARCHIVES = {'lib/libxml2.a'} | {'lib/libazure-' + name + '.a' for name in LIBRARIES if name != 'libxml2'}
HTTP_ARCHIVES = {'lib/libcurl.a', 'lib/libssl.a', 'lib/libcrypto.a', 'lib/libz.a', 'lib/libnghttp2.a'}
RECIPES = ('nix/check-extension-version.py', 'nix/azure.nix', 'nix/azure-source-lock.json',
           'nix/azure-static-dependencies.patch', 'nix/azure-core-selected-curl.patch',
           'nix/azure-identity-retain-fetch-option.patch', 'nix/azure-storage-common-retain-fetch-option.patch',
           'nix/check-azure-static-libraries.cpp', 'scripts/nix_native_azure_receipt.py')
EVIDENCE = {name + '-' + kind for name in LIBRARIES for kind in ('source.json', 'compiler.txt')} | {
    name + '-' + kind for name in LIBRARIES if name != 'libxml2' for kind in ('cache.txt', 'commands.json')} | {
    'libxml2-options.txt', 'libxml2-config.txt', 'libxml2-checks.txt', 'libxml2-build.txt', 'native-link.json', 'consumer.txt', 'consumer-needed.txt'}


def source(policy, name, root):
    expected = policy['libraries'][name]
    files = {name: hashlib.sha256((root / name).read_bytes()).hexdigest()
             for name in expected['selectedFiles']}
    if files != expected['selectedFiles']:
        raise ValueError('selected Azure SDK source changed: ' + name)
    return {'version': expected['version'], 'sourceNARHash': expected['sourceNARHash'],
            'patchSHA256': expected['patchSHA256'], 'selectedFiles': files}


def decode(data):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate evidence key')
            result[key] = value
        return result
    return json.loads(data, object_pairs_hook=pairs)


def check(evidence, policy, platform, read=None):
    if read is not None:
        evidence = {name: read(evidence / name) for name in EVIDENCE}
    def fail(message):
        raise ValueError('Azure receipt: ' + message)
    def content(name):
        if name not in evidence or len(evidence[name]) > 8 * 1024 * 1024:
            fail('missing or oversized evidence: ' + name)
        return evidence[name].decode()
    target = {'linux/amd64': 'x86_64', 'linux/arm64': 'aarch64'}[platform]
    bindings = decode(content('native-link.json'))
    expected_archives = {'lib/libcurl.a', 'lib/libssl.a', 'lib/libcrypto.a', 'lib/libz.a', 'lib/libnghttp2.a', 'lib/libxml2.a'}
    if set(bindings) != expected_archives:
        fail('selected native dependency binding set changed')
    for item in bindings.values():
        if not re.fullmatch(r'[0-9a-f]{64}', item['sha256']) or not item['archive'].startswith('/nix/store/'):
            fail('native dependency binding is not immutable')
    variables = {'CURL_LIBRARY_RELEASE': 'lib/libcurl.a', 'OPENSSL_SSL_LIBRARY': 'lib/libssl.a',
                 'OPENSSL_CRYPTO_LIBRARY': 'lib/libcrypto.a', 'LIBXML2_LIBRARY': 'lib/libxml2.a'}
    for name, selected in policy['libraries'].items():
        if decode(content(name + '-source.json')) != selected:
            fail('selected source changed: ' + name)
        if not re.search(r'compiler-target: ' + target + r'[^\n]*linux', content(name + '-compiler.txt')):
            fail('wrong compiler target: ' + name)
        if name == 'libxml2':
            continue
        cache = content(name + '-cache.txt')
        for key, value in policy['sdkFeatures'].items():
            if not re.search(r'^' + key + r':BOOL=' + ('(?:ON|TRUE|1)' if value else '(?:OFF|FALSE|0)') + r'$', cache, re.M):
                fail('SDK feature changed: ' + name + '/' + key)
        for key, archive in variables.items():
            if not re.search(r'^' + key + r':(?:FILEPATH|STRING)=' + re.escape(bindings[archive]['archive']) + r'$', cache, re.M):
                fail('selected archive rebound: ' + name + '/' + key)
        commands = decode(content(name + '-commands.json'))
        for path in selected['selectedFiles']:
            rows = [row for row in commands if row.get('file', '').endswith('/' + path)]
            if not rows or any('-fPIC' not in row.get('command', '') or re.search(r'-m(?:arch|cpu|tune)=native', row.get('command', '')) for row in rows):
                fail('selected SDK compilation missing PIC or has native ISA: ' + name)
    if content('libxml2-checks.txt') != 'libxml2 upstream checks passed\n':
        fail('XML upstream checks did not pass')
    options = shlex.split(content('libxml2-options.txt'))
    for name, value in policy['libxml2Features'].items():
        flag = ('--enable-' if value else '--disable-') + name if name in ('static', 'shared') else ('--with-' if value else '--without-') + name
        if flag not in options:
            fail('XML feature changed: ' + name)
    xml_config = content('libxml2-config.txt')
    if re.search(r'^#define LIBXML_(?:MODULES|ICU|ZLIB|HTTP_STUBS)_ENABLED', xml_config, re.M):
        fail('XML optional dependency enabled')
    for feature in ('TREE', 'READER'):
        if '#define LIBXML_' + feature + '_ENABLED' not in xml_config:
            fail('XML reader disabled')
    xml_build = content('libxml2-build.txt')
    if not any('parser.c' in line and '-fPIC' in line for line in xml_build.splitlines()) or re.search(r'-m(?:arch|cpu|tune)=native', xml_build):
        fail('selected XML compilation missing PIC or has native ISA')
    if content('consumer.txt') != 'selected static Azure SDK signed blob/datalake XML and identity roundtrips passed\n':
        fail('actual SDK consumer did not pass')
    needed = content('consumer-needed.txt')
    if 'libc.so' not in needed or re.search(r'NEEDED.*(?:libazure|libxml2|libcurl|libssl|libcrypto|libz\.so|libnghttp2)', needed):
        fail('consumer used dynamic selected libraries')
    return bindings


def check_engine(evidence, policy, cache, commands, selection, read):
    selected = decode(read(evidence / 'azure-link.json'))
    http = decode(read(evidence / 'http-link.json'))
    for bindings, expected, variable in ((selected, ARCHIVES, 'AZURE_LIBRARIES'), (http, HTTP_ARCHIVES, 'AZURE_HTTP_LIBRARIES')):
        if set(bindings) != expected or any(set(v) != {'archive', 'sha256'} or not v['archive'].endswith('/' + name) or not re.fullmatch('[0-9a-f]{64}', v['sha256']) for name, v in bindings.items()):
            raise ValueError('Azure engine archive binding differs')
        value = ';'.join(bindings[name]['archive'] for name in sorted(expected))
        if not re.search(r'^' + variable + r':STRING=' + re.escape(value) + r'$', cache, re.M):
            raise ValueError('Azure engine selected dependency differs: ' + variable)
    wrapper = re.search(r'duckdb_extension_load\(azure SOURCE_DIR ([^\s()]+) EXTENSION_VERSION ' + policy['wrapper']['revision'] + r'\)', selection)
    for name in ('src/azure_extension.cpp', 'src/azure_blob_filesystem.cpp', 'src/azure_dfs_filesystem.cpp', 'src/azure_secret.cpp'):
        if not wrapper or not any(c.get('file') == wrapper[1] + '/' + name for c in commands):
            raise ValueError('compiled Azure wrapper selection missing: ' + name)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['source', 'libraries'])
    parser.add_argument('--policy', type=Path, required=True)
    parser.add_argument('--library')
    parser.add_argument('--platform')
    parser.add_argument('--evidence', type=Path)
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--destination', type=Path, required=True)
    args = parser.parse_args()
    policy = decode(args.policy.read_bytes())
    if args.command == 'source':
        result = source(policy, args.library, args.root)
    else:
        evidence = {p.name: p.read_bytes() for p in args.evidence.iterdir()}
        bindings = check(evidence, policy, args.platform)
        for item in bindings.values():
            if hashlib.sha256(Path(item['archive']).read_bytes()).hexdigest() != item['sha256']:
                raise ValueError('native archive bytes differ from their selected binding')
        archives = {}
        expected = {'lib/libxml2.a'} | {'lib/libazure-' + name + '.a' for name in policy['libraries'] if name != 'libxml2'}
        for path in args.root.rglob('*.a'):
            relative = path.relative_to(args.root).as_posix()
            data = path.read_bytes()
            if not data.startswith(b'!<arch>\n'):
                raise ValueError('selected native output is not an archive')
            archives[relative] = hashlib.sha256(data).hexdigest()
        if set(archives) != expected or archives['lib/libxml2.a'] != bindings['lib/libxml2.a']['sha256']:
            raise ValueError('selected Azure native archive set differs')
        result = {'kind': 'selected-azure-native-libraries', 'platform': args.platform,
                  'policySHA256': hashlib.sha256(args.policy.read_bytes()).hexdigest(),
                  'archives': archives, 'evidence': {k: hashlib.sha256(v).hexdigest() for k,v in evidence.items()}}
    args.destination.write_text(json.dumps(result, sort_keys=True) + '\n')

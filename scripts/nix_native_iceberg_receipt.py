"""Validate selected Iceberg AWS libraries; this is not release admission."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shlex

TARGETS = {'linux/amd64': 'x86_64-unknown-linux-gnu', 'linux/arm64': 'aarch64-unknown-linux-gnu'}
HTTP_ARCHIVES = {'lib/libcurl.a', 'lib/libssl.a', 'lib/libcrypto.a', 'lib/libz.a', 'lib/libnghttp2.a'}
ARCHIVES = {'lib/libaws-cpp-sdk-core.a', 'lib/libaws-cpp-sdk-sso.a', 'lib/libaws-cpp-sdk-sts.a',
            'lib/libaws-crt-cpp.a', 'lib/libs2n.a'} | {'lib/lib' + name + '.a' for name in
            ('aws-c-auth', 'aws-c-cal', 'aws-c-common', 'aws-c-compression', 'aws-c-event-stream',
             'aws-c-http', 'aws-c-io', 'aws-c-mqtt', 'aws-c-s3', 'aws-c-sdkutils', 'aws-checksums')}
SUCCESS = 'selected static AWS SigV4 payload, HTTP client, SSO JSON and STS XML checks passed\n'

LIBRARIES = ('aws-c-auth', 'aws-c-cal', 'aws-c-common', 'aws-c-compression', 'aws-c-event-stream',
             'aws-c-http', 'aws-c-io', 'aws-c-mqtt', 'aws-c-s3', 'aws-c-sdkutils', 'aws-checksums',
             'aws-crt-cpp', 'aws-sdk-cpp', 's2n-tls')
RECIPES = ('nix/iceberg.nix', 'nix/iceberg-source-lock.json', 'nix/iceberg-selected-curl.patch',
           'nix/iceberg-static-dependencies.patch', 'nix/check-extension-version.py',
           'nix/check-iceberg-static-libraries.cpp', 'scripts/nix_native_iceberg_receipt.py')
EVIDENCE = {name + '-' + suffix for name in LIBRARIES for suffix in
            ('source.json', 'compiler.txt', 'cache.txt', 'commands.json')} | {
            'http-link.json', 'consumer.txt', 'consumer-needed.txt'}


def decode(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate Iceberg evidence field')
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=pairs)


def check(evidence, policy, platform, read=None):
    if read is not None:
        evidence = {name: read(evidence / name) for name in EVIDENCE}
    def content(name):
        if name not in evidence or len(evidence[name]) > 64 * 1024 * 1024:
            raise ValueError('missing Iceberg evidence: ' + name)
        return evidence[name].decode()
    bindings = decode(content('http-link.json'))
    if set(bindings) != HTTP_ARCHIVES or any(
            set(value) != {'archive', 'sha256'} or not value['archive'].endswith('/' + name)
            or re.fullmatch('[0-9a-f]{64}', value['sha256']) is None
            for name, value in bindings.items()):
        raise ValueError('selected Iceberg HTTP binding differs')
    for name, selected in policy['libraries'].items():
        if decode(content(name + '-source.json')) != selected['selectedFiles']:
            raise ValueError('selected AWS source differs: ' + name)
        if 'compiler-target: ' + TARGETS[platform] not in content(name + '-compiler.txt'):
            raise ValueError('AWS compiler target differs: ' + name)
        cache = content(name + '-cache.txt')
        for key, enabled in policy['features'].items():
            if not re.search(r'^' + key + r':BOOL=(?:' + ('ON|TRUE|1' if enabled else 'OFF|FALSE|0') + r')$', cache, re.M):
                raise ValueError('AWS compilation feature differs: ' + name + '/' + key)
        commands = decode(content(name + '-commands.json'))
        if (not commands or not any('-fPIC' in item.get('command', '') for item in commands)
                or any(re.search(r'-m(?:arch|cpu|tune)=native', item.get('command', '')) for item in commands)):
            raise ValueError('AWS PIC compilation or portable ISA missing: ' + name)
        if name in ('aws-crt-cpp', 'aws-sdk-cpp') and not re.search(r'^BUILD_DEPS:BOOL=OFF$', cache, re.M):
            raise ValueError('AWS unselected bundled dependencies enabled')
        if name == 'aws-sdk-cpp':
            if not re.search(r'^BUILD_ONLY:STRING=' + ';'.join(policy['sdkAPIs']) + r'$', cache, re.M):
                raise ValueError('AWS service selection differs')
            selection = re.search(r'^ICEBERG_HTTP_STATIC_LIBRARIES:[^=]+=(.*)$', cache, re.M)
            if not selection or set(selection[1].split(';')) != {v['archive'] for v in bindings.values()}:
                raise ValueError('AWS static HTTP closure differs')
            for key, archive in [('CURL_LIBRARY_RELEASE', 'libcurl.a'), ('OPENSSL_SSL_LIBRARY', 'libssl.a'),
                                 ('OPENSSL_CRYPTO_LIBRARY', 'libcrypto.a'), ('ZLIB_LIBRARY', 'libz.a')]:
                if not re.search(r'^' + key + r':[^=]+=' + re.escape(bindings['lib/' + archive]['archive']) + r'$', cache, re.M):
                    raise ValueError('AWS selected HTTP archive differs: ' + key)
    if content('consumer.txt') != SUCCESS:
        raise ValueError('actual static AWS consumer did not pass')
    needed = content('consumer-needed.txt')
    if 'libc.so' not in needed or re.search(r'NEEDED.*lib(?:aws|s2n|ssl|crypto|curl|z\.|nghttp2)', needed):
        raise ValueError('AWS consumer used a selected dynamic library')
    return bindings


def check_engine(evidence, policy, cache, commands, selection, read):
    linked = decode(read(evidence / 'iceberg-link.json'))
    if set(linked) != ARCHIVES or any(set(v) != {'archive', 'sha256'}
            or not v['archive'].endswith('/' + name) or not re.fullmatch('[0-9a-f]{64}', v['sha256'])
            for name, v in linked.items()):
        raise ValueError('Iceberg AWS archive binding differs')
    for variable, bindings in [('ICEBERG_AWS_LIBRARIES', linked),
                               ('ICEBERG_HTTP_LIBRARIES', decode(read(evidence / 'http-link.json')))]:
        expected = ';'.join(bindings[name]['archive'] for name in sorted(bindings))
        if not re.search(r'^' + variable + r':STRING=' + re.escape(expected) + r'$', cache, re.M):
            raise ValueError('Iceberg selected archive set differs: ' + variable)
    roaring = decode(read(evidence / 'croaring-link.json'))
    if not re.search(r'^ICEBERG_ROARING_LIBRARY:[^=]+=' + re.escape(roaring['archive']) + r'$', cache, re.M):
        raise ValueError('Iceberg selected CRoaring differs')
    includes = re.search(r'^ICEBERG_INCLUDE_DIRS:STRING=(.*)$', cache, re.M)
    wrapper = re.search(r'duckdb_extension_load\(iceberg SOURCE_DIR ([^\s()]+) EXTENSION_VERSION '
                        + policy['wrapper']['revision'] + r'\)', selection)
    for name in ('src/iceberg_extension.cpp', 'src/catalog/rest/storage/authorization/sigv4.cpp'):
        if not wrapper or not includes or not any(c.get('file') == wrapper[1] + '/' + name and
                all('-I' + path in shlex.split(c.get('command', '')) for path in includes[1].split(';'))
                for c in commands):
            raise ValueError('Iceberg compiled wrapper or headers differ')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['source', 'libraries'])
    parser.add_argument('--policy', type=Path, required=True)
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--destination', type=Path, required=True)
    parser.add_argument('--library')
    parser.add_argument('--evidence', type=Path)
    parser.add_argument('--platform', choices=TARGETS)
    args = parser.parse_args()
    policy = decode(args.policy.read_bytes())
    if args.command == 'source':
        expected = policy['libraries'][args.library]['selectedFiles']
        result = {name: hashlib.sha256((args.root / name).read_bytes()).hexdigest() for name in expected}
        if result != expected:
            raise ValueError('AWS source substitution')
    else:
        evidence = {path.name: path.read_bytes() for path in args.evidence.iterdir()}
        bindings = check(evidence, policy, args.platform)
        for value in bindings.values():
            if hashlib.sha256(Path(value['archive']).read_bytes()).hexdigest() != value['sha256']:
                raise ValueError('AWS selected HTTP bytes changed')
        archives = {}
        for path in args.root.rglob('*.a'):
            data = path.read_bytes()
            if not data.startswith(b'!<arch>\n'):
                raise ValueError('AWS output is not a static archive')
            archives[path.relative_to(args.root).as_posix()] = hashlib.sha256(data).hexdigest()
        if set(archives) != ARCHIVES:
            raise ValueError('AWS selected archive set differs')
        result = {'kind': 'selected-iceberg-aws-libraries', 'platform': args.platform,
                  'policySHA256': hashlib.sha256(args.policy.read_bytes()).hexdigest(), 'archives': archives,
                  'evidence': {name: hashlib.sha256(data).hexdigest() for name, data in evidence.items()}}
    args.destination.write_text(json.dumps(result, sort_keys=True) + '\n')


if __name__ == '__main__':
    main()

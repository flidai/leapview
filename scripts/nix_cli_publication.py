#!/usr/bin/env python3
"""Qualify and verify protected static CLI archive evidence without release authority."""

import argparse
import copy
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import resource
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
import uuid
from urllib.parse import unquote

import check_nix_cli_compatibility as compatibility
import nix_archive_go_evidence as go_evidence
import nix_candidate_manifest as candidate

SYFT_VERSION = '1.52.0'
HOST_FIXTURES = (
    {'id': 'debian12', 'image': ('public.ecr.aws/docker/library/debian:bookworm-slim@sha256:'
                                 '7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587'),
     'osID': 'debian', 'versionID': '12'},
    {'id': 'ubuntu2404', 'image': ('public.ecr.aws/docker/library/ubuntu:24.04@sha256:'
                                  '534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55'),
     'osID': 'ubuntu', 'versionID': '24.04'},
    {'id': 'debian13', 'image': ('public.ecr.aws/docker/library/debian:trixie-slim@sha256:'
                                 'a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a'),
     'osID': 'debian', 'versionID': '13'},
)
WORKFLOW = 'flidai/leapview/.github/workflows/nix-cli-candidate.yml'
PROVENANCE = 'https://slsa.dev/provenance/v1'
SPDX_PREDICATE = 'https://spdx.dev/Document/v2.3'
QUALIFICATION_KEY = 'nix-cli-qualification'
MAX_ARCHIVE_BYTES = 300 * 1024**2
MAX_RUNTIME_BYTES = 1024**2
MAX_GO_BUILDINFO_BYTES = candidate.MAX_JSON_BYTES
MAX_SYFT_VERSION_BYTES = 64 * 1024
MAX_ATTESTATION_BYTES = candidate.MAX_REPORT_BYTES
HOST_REPORT_SUFFIXES = ('os-release.txt', 'runtime-version.json', 'runtime-help.txt',
                        'runtime-host-help.txt')
HOST_REPORTS = tuple(f"{fixture['id']}-{suffix}" for fixture in HOST_FIXTURES
                     for suffix in HOST_REPORT_SUFFIXES)
REPORTS = ('static.json', *HOST_REPORTS, 'sbom.spdx.json')
ROOT_REPORTS = (*REPORTS, 'candidate-manifest.json', 'qualification.json', 'go')
PROBE_HOST_REPORTS = ('static.json', *HOST_REPORTS)
ARCH_MACHINE = {'amd64': 'x86_64', 'arm64': 'aarch64'}
RUNTIME_COMMANDS = (
    (('version', '--format', 'json'), 'runtime-version.json'),
    (('--help',), 'runtime-help.txt'),
    (('host', '--help'), 'runtime-host-help.txt'),
)
REVISION = re.compile(r'^[0-9a-f]{40}$')
PURL = re.compile(r'^pkg:golang/([^@?#]+)(?:@([^?#]+))?(?:\?[^#]*)?$')


def _lstat_regular(path, limit):
    """Read a bounded regular file through one no-follow descriptor."""
    path = Path(path)
    for component in (path, *path.parents):
        if component.is_symlink():
            raise ValueError('evidence cannot traverse symlinks')
    flags = os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0) | getattr(os, 'O_CLOEXEC', 0)
    descriptor = os.open(path, flags)
    try:
        before = os.fstat(descriptor)
        if not stat.S_ISREG(before.st_mode) or before.st_size > limit:
            raise ValueError('evidence must be a bounded regular file')
        chunks, remaining = [], limit + 1
        while remaining:
            chunk = os.read(descriptor, min(remaining, 1024 * 1024))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        after = os.fstat(descriptor)
        data = b''.join(chunks)
        if (len(data) > limit or before.st_dev != after.st_dev or before.st_ino != after.st_ino or
                before.st_size != after.st_size or before.st_mtime_ns != after.st_mtime_ns or
                len(data) != after.st_size):
            raise ValueError('evidence changed or exceeds its byte limit')
        current = path.lstat()
        if current.st_dev != after.st_dev or current.st_ino != after.st_ino:
            raise ValueError('evidence changed during verification')
        return data
    finally:
        os.close(descriptor)


def _write_new(path, data, mode=0o600):
    path = Path(path)
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0)
    descriptor = os.open(path, flags, mode)
    with os.fdopen(descriptor, 'wb') as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def _write_json_new(path, value):
    data = json.dumps(value, indent=2, ensure_ascii=False, allow_nan=False).encode() + b'\n'
    if len(data) > candidate.MAX_JSON_BYTES:
        raise ValueError('qualification JSON exceeds its byte limit')
    _write_new(path, data)


def _read_json(path, limit=candidate.MAX_JSON_BYTES):
    return candidate.read_json(_lstat_regular(path, limit), limit)


def _run(args, *, timeout, env=None, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL):
    options = {'check': True, 'timeout': timeout, 'stdout': stdout,
               'stderr': stderr, 'env': env}
    if len(args) >= 4 and args[0] == 'syft' and args[1] != 'version':
        options['preexec_fn'] = _file_size_limit(candidate.MAX_REPORT_BYTES)
    try:
        return subprocess.run(args, **options)
    except (OSError, subprocess.SubprocessError):
        raise ValueError('protected qualification command failed: ' + Path(args[0]).name) from None


def _trusted_source(source_root, requested_revision):
    if not REVISION.fullmatch(requested_revision):
        raise ValueError('source revision must be an exact commit')
    source_root = Path(source_root)
    source = candidate.checkout_source(source_root)
    if source['revision'] != requested_revision:
        raise ValueError('trusted source checkout differs from requested revision')
    return source


def _source_build_identity(source_root, source, archive_identity):
    version_path = Path(source_root) / 'VERSION'
    version_bytes = _lstat_regular(version_path, 4096)
    try:
        version_text = version_bytes.decode('utf-8')
    except UnicodeDecodeError:
        raise ValueError('source VERSION must be UTF-8') from None
    canonical_version = version_text.removesuffix('\n')
    if (not canonical_version or canonical_version != canonical_version.strip() or
            '\n' in canonical_version or '\r' in canonical_version):
        raise ValueError('source VERSION is missing or noncanonical')
    expected_version = canonical_version + '+nix.' + source['revision'][:12]
    if archive_identity.get('version') != expected_version:
        raise ValueError('archive version does not match the trusted source VERSION and revision')
    try:
        epoch = int(subprocess.check_output(
            ['git', '-C', str(source_root), 'show', '-s', '--format=%ct', source['revision']],
            text=True, timeout=10, stderr=subprocess.DEVNULL).strip())
    except (OSError, ValueError, subprocess.SubprocessError):
        raise ValueError('trusted source commit timestamp is unavailable') from None
    expected_build_time = datetime.fromtimestamp(epoch, timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    return expected_version, expected_build_time


def _identity_file(identity_path, source_root, source, archive, requested_revision):
    if not REVISION.fullmatch(requested_revision) or source['revision'] != requested_revision:
        raise ValueError('source revision must be an exact authorized commit')
    identity = _read_json(identity_path)
    if not isinstance(identity, dict) or set(identity) != {'platform', 'version', 'sourceRevision'}:
        raise ValueError('archive identity must contain exactly platform, version and sourceRevision')
    if identity['sourceRevision'] != requested_revision or identity['platform'] not in candidate.PLATFORMS:
        raise ValueError('archive identity differs from trusted source or supported platform')
    arch = identity['platform'].removeprefix('linux/')
    if Path(archive).name != 'leapviewctl-linux-' + arch + '.tar.gz':
        raise ValueError('archive basename does not match its declared architecture')
    expected_version, build_time = _source_build_identity(source_root, source, identity)
    if identity['version'] != expected_version:
        raise ValueError('archive version is not the canonical source build version')
    return identity, build_time


def _validate_binary_verifier(path):
    path = Path(path)
    if not path.is_absolute() or path.is_symlink():
        raise ValueError('protected Go binary verifier must be an absolute regular executable')
    try:
        info = path.stat()
    except OSError:
        raise ValueError('protected Go binary verifier is unavailable') from None
    if not stat.S_ISREG(info.st_mode) or not os.access(path, os.X_OK):
        raise ValueError('protected Go binary verifier must be an absolute regular executable')
    return path


def _build_info(binary, arch):
    try:
        raw = subprocess.check_output(['go', 'version', '-m', '-json', str(binary)],
                                      timeout=30, stderr=subprocess.DEVNULL)
    except (OSError, subprocess.SubprocessError):
        raise ValueError('trusted Go tool could not read controller build information') from None
    info = candidate.read_json(raw, MAX_GO_BUILDINFO_BYTES)
    if not isinstance(info, dict) or not isinstance(info.get('GoVersion'), str):
        raise ValueError('controller Go build information is incomplete')
    compatibility.check_build_info(info, arch)
    return info


def _static_report(binary, arch):
    data = _lstat_regular(binary, go_evidence.MAX_BINARY_BYTES)
    compatibility.check_elf(data, arch)
    info = _build_info(binary, arch)
    return ({'schemaVersion': 1, 'platform': 'linux/' + arch,
             'binarySHA256': candidate.digest_bytes(data), 'goVersion': info['GoVersion'],
             'static': True, 'cgoEnabled': False, 'releaseAdmission': False}, info)


def _native_arch():
    try:
        machine = subprocess.check_output(['uname', '-m'], text=True, timeout=10,
                                          stderr=subprocess.DEVNULL).strip()
    except (OSError, subprocess.SubprocessError):
        raise ValueError('native architecture is unavailable') from None
    reverse = {value: key for key, value in ARCH_MACHINE.items()}
    if machine not in reverse:
        raise ValueError('host qualification requires native amd64 or arm64 Linux')
    return reverse[machine], machine


def _runtime_env():
    env = {key: value for key, value in os.environ.items()
           if not key.startswith('SYFT_')}
    env.update(SYFT_CHECK_FOR_APP_UPDATE='false', DOCKER_CLI_HINTS='false')
    return env


def _file_size_limit(limit):
    def apply_limit():
        resource.setrlimit(resource.RLIMIT_FSIZE, (limit, limit))
    return apply_limit


def _run_container(args, name, *, timeout=45, limit=MAX_RUNTIME_BYTES):
    output = b''
    try:
        with tempfile.TemporaryFile(mode='w+b') as capture, tempfile.TemporaryFile(mode='w+b') as errors:
            try:
                subprocess.run(args, check=True, timeout=timeout, stdout=capture,
                               stderr=errors, env=_runtime_env(),
                               preexec_fn=_file_size_limit(limit))
            except (OSError, subprocess.SubprocessError):
                raise ValueError('protected controller host probe failed or exceeded its limits') from None
            capture.seek(0)
            output = capture.read(limit + 1)
            errors.seek(0)
            if len(errors.read(limit + 1)) > limit:
                raise ValueError('controller host-probe stderr exceeds its byte limit')
    finally:
        # Own removal explicitly: --rm races this request when an output limit
        # kills the client while the daemon is still removing the container.
        # Do not publish successful evidence if cleanup could not complete.
        try:
            removed = subprocess.run(['docker', 'rm', '--force', name], check=False, timeout=15,
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                     env=_runtime_env())
        except (OSError, subprocess.SubprocessError):
            raise ValueError('controller host-probe container cleanup failed') from None
        if removed.returncode != 0:
            raise ValueError('controller host-probe container cleanup failed')
    if not output or len(output) > limit:
        raise ValueError('controller host-probe output is empty or exceeds its byte limit')
    return output


def _validate_os_release(data, fixture):
    try:
        lines = data.decode('utf-8').splitlines()
    except UnicodeDecodeError:
        raise ValueError('host OS release report must be UTF-8') from None
    values = {}
    for line in lines:
        key, separator, raw_value = line.partition('=')
        if not separator or key not in ('ID', 'VERSION_ID'):
            continue
        if key in values:
            raise ValueError('host OS release report contains duplicate identity fields')
        match = re.fullmatch(r'(?:"([^"\\]*)"|\'([^\'\\]*)\'|([^\s#]*))(?:\s+#.*)?', raw_value)
        if match is None:
            raise ValueError('host OS release report contains a malformed identity field')
        values[key] = next(value for value in match.groups() if value is not None)
    if values != {'ID': fixture['osID'], 'VERSION_ID': fixture['versionID']}:
        raise ValueError('host OS release identity differs from pinned fixture ' + fixture['id'])


def _host_command(binary, arch, fixture, name, command):
    args = ['docker', 'run', '--name', name, '--platform', 'linux/' + arch,
            '--log-driver', 'none', '--network', 'none', '--read-only', '--user', '65534:65534',
            '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
            '--memory', '256m', '--cpus', '1', '--pids-limit', '64', '--ulimit', 'core=0']
    if binary is not None:
        binary = str(binary)
        if ',' in binary or '\n' in binary or '\r' in binary:
            raise ValueError('temporary controller path is not safe for a Docker bind mount')
        args.extend(['--mount', 'type=bind,src=' + binary +
                     ',dst=/usr/local/bin/leapviewctl,readonly'])
        entrypoint = '/usr/local/bin/leapviewctl'
    else:
        entrypoint = '/bin/cat'
    args.extend(['--entrypoint', entrypoint, fixture['image'], *command])
    return args


def _run_host(binary, arch, output_directory):
    native_arch, machine = _native_arch()
    if native_arch != arch:
        raise ValueError('host qualification must run on the candidate native architecture')
    hosts = []
    for fixture in HOST_FIXTURES:
        for attempt in range(3):
            try:
                _run(['docker', 'pull', '--platform', 'linux/' + arch, fixture['image']], timeout=300,
                     env=_runtime_env(), stdout=subprocess.DEVNULL, stderr=sys.stderr)
                break
            except ValueError:
                if attempt == 2:
                    raise
                # Public registries can throttle sequential pulls on shared runner IPs.
                time.sleep(5 * (attempt + 1))
        name = 'leapview-cli-probe-' + uuid.uuid4().hex
        output = _run_container(_host_command(None, arch, fixture, name, ('/etc/os-release',)), name,
                                timeout=45, limit=MAX_RUNTIME_BYTES)
        _validate_os_release(output, fixture)
        os_report_name = fixture['id'] + '-os-release.txt'
        _write_new(Path(output_directory) / os_report_name, output)
        runtime = None
        for command, suffix in RUNTIME_COMMANDS:
            name = 'leapview-cli-probe-' + uuid.uuid4().hex
            output = _run_container(_host_command(binary, arch, fixture, name, command), name,
                                    timeout=45, limit=MAX_RUNTIME_BYTES)
            filename = fixture['id'] + '-' + suffix
            if suffix == 'runtime-version.json':
                runtime = candidate.read_json(output, MAX_RUNTIME_BYTES)
                if not isinstance(runtime, dict):
                    raise ValueError('controller runtime identity must be a JSON object')
            _write_new(Path(output_directory) / filename, output)
        hosts.append({'id': fixture['id'], 'image': fixture['image'],
                      'platform': 'linux/' + arch, 'machine': machine,
                      'runtimeIdentity': runtime})
    return hosts, machine


def _validate_runtime_identity(runtime, identity, source, expected_build_time):
    if (not isinstance(runtime, dict) or set(runtime) != {
            'product', 'version', 'revision', 'buildTime', 'dirty', 'development'} or
            runtime.get('product') != 'leapviewctl' or
            runtime.get('version') != identity['version'] or
            runtime.get('revision') != source['revision'] or runtime.get('dirty') is not False or
            runtime.get('development') is not True or runtime.get('buildTime') != expected_build_time):
        raise ValueError('controller runtime identity differs from trusted source and archive identity')


def _syft_spdx(binary, output_path):
    version_result = _run(['syft', 'version', '-o', 'json'], timeout=30, env=_runtime_env())
    version_data = version_result.stdout if isinstance(version_result.stdout, bytes) else b''
    version = candidate.read_json(version_data, MAX_SYFT_VERSION_BYTES)
    if not isinstance(version, dict) or version.get('version') != SYFT_VERSION:
        raise ValueError('controller SPDX inventory requires pinned Syft ' + SYFT_VERSION)
    env = _runtime_env()
    args = ['syft', 'file:' + str(binary), '-o', 'spdx-json=' + str(output_path)]
    _run(args, timeout=300, env=env, stdout=subprocess.DEVNULL)
    data = _lstat_regular(output_path, candidate.MAX_REPORT_BYTES)
    return data, SYFT_VERSION


def _expected_module_inventory(info):
    modules = {}

    def add(path, version):
        if not isinstance(path, str) or not path or not isinstance(version, str) or not version:
            raise ValueError('Go build information contains an invalid module identity')
        if version == '(devel)':
            version = 'UNKNOWN'
        if path in modules:
            raise ValueError('Go build information contains duplicate module paths')
        modules[path] = version

    main = info.get('Main')
    if not isinstance(main, dict):
        raise ValueError('Go build information has no main module')
    add(main.get('Path'), main.get('Version'))
    deps = info.get('Deps')
    if not isinstance(deps, list) or not deps:
        raise ValueError('Go build information has no dependency modules')
    for dep in deps:
        if not isinstance(dep, dict):
            raise ValueError('Go build information has a malformed dependency')
        replacement = dep.get('Replace')
        version = replacement.get('Version') if isinstance(replacement, dict) else dep.get('Version')
        add(dep.get('Path'), version)
    return modules


def validate_spdx(data, info, binary_sha256):
    document = candidate.read_json(data, candidate.MAX_REPORT_BYTES)
    if (not isinstance(document, dict) or document.get('spdxVersion') != 'SPDX-2.3' or
            document.get('SPDXID') != 'SPDXRef-DOCUMENT' or
            not isinstance(document.get('documentNamespace'), str) or
            not document['documentNamespace'] or
            not isinstance(document.get('packages'), list) or not document['packages']):
        raise ValueError('Syft SPDX inventory is empty or not SPDX 2.3')
    creators = document.get('creationInfo', {}).get('creators', [])
    if 'Tool: syft-' + SYFT_VERSION not in creators:
        raise ValueError('SPDX inventory was not generated by pinned Syft ' + SYFT_VERSION)

    expected = _expected_module_inventory(info)
    found, file_packages, stdlib = {}, [], []
    for package in document['packages']:
        if not isinstance(package, dict):
            raise ValueError('SPDX package inventory is malformed')
        if package.get('primaryPackagePurpose') == 'FILE':
            file_packages.append(package)
        purls = [entry.get('referenceLocator') for entry in package.get('externalRefs', [])
                 if isinstance(entry, dict) and entry.get('referenceCategory') == 'PACKAGE-MANAGER'
                 and entry.get('referenceType') == 'purl'
                 and isinstance(entry.get('referenceLocator'), str)
                 and entry['referenceLocator'].startswith('pkg:golang/')]
        if len(purls) > 1:
            raise ValueError('SPDX package has duplicate Go module identities')
        if not purls:
            continue
        match = PURL.fullmatch(purls[0])
        if match is None:
            raise ValueError('SPDX Go module package has a malformed purl')
        path = unquote(match.group(1))
        version = unquote(match.group(2)) if match.group(2) is not None else 'UNKNOWN'
        if path == 'stdlib':
            stdlib.append(package)
            if version != info['GoVersion'].removeprefix('go') or package.get('versionInfo') != info['GoVersion']:
                raise ValueError('SPDX Go standard library version differs from exact build information')
            continue
        if path not in expected or path in found:
            raise ValueError('SPDX Go module inventory has an unexpected or duplicate module')
        if version != expected[path] or package.get('versionInfo') != expected[path]:
            raise ValueError('SPDX Go module version differs from exact build information: ' + path)
        if package.get('name') != path:
            raise ValueError('SPDX Go module name differs from its package URL')
        found[path] = version
    if found != expected or len(stdlib) != 1:
        raise ValueError('SPDX Go module inventory is incomplete or lacks the exact standard library')
    if len(file_packages) != 1:
        raise ValueError('SPDX must describe exactly one controller binary file')
    binary_package = file_packages[0]
    if (binary_package.get('name') != 'leapviewctl' or
            binary_package.get('versionInfo') != binary_sha256 or
            binary_package.get('checksums') != [{'algorithm': 'SHA256',
                                                  'checksumValue': binary_sha256.removeprefix('sha256:')}]):
        raise ValueError('SPDX controller file identity differs from exact archive binary')
    relationships = document.get('relationships')
    if not isinstance(relationships, list) or not any(
            item == {'spdxElementId': 'SPDXRef-DOCUMENT',
                     'relatedSpdxElement': binary_package.get('SPDXID'),
                     'relationshipType': 'DESCRIBES'} for item in relationships):
        raise ValueError('SPDX document does not describe the exact controller file')
    return document


def _report_limit(name):
    if name == 'static.json':
        return candidate.MAX_JSON_BYTES
    if name == 'sbom.spdx.json':
        return candidate.MAX_REPORT_BYTES
    if name.endswith('-runtime-version.json') or name.endswith('-runtime-help.txt') or name.endswith(
            '-runtime-host-help.txt') or name.endswith('-os-release.txt'):
        return MAX_RUNTIME_BYTES
    raise ValueError('unknown protected host report: ' + name)


def _report_records(evidence_dir, names=REPORTS):
    records = []
    for name in names:
        data = _lstat_regular(Path(evidence_dir) / name, _report_limit(name))
        records.append({'path': name, 'sha256': candidate.digest_bytes(data)})
    return records


def _expected_hosts(evidence_dir, arch, identity, source, expected_build_time):
    hosts = []
    machine = ARCH_MACHINE[arch]
    for fixture in HOST_FIXTURES:
        os_name = fixture['id'] + '-os-release.txt'
        os_bytes = _lstat_regular(Path(evidence_dir) / os_name, MAX_RUNTIME_BYTES)
        if not os_bytes:
            raise ValueError('host OS release report is empty')
        _validate_os_release(os_bytes, fixture)
        runtime_name = fixture['id'] + '-runtime-version.json'
        runtime = _read_json(Path(evidence_dir) / runtime_name, MAX_RUNTIME_BYTES)
        _validate_runtime_identity(runtime, identity, source, expected_build_time)
        for suffix in ('runtime-help.txt', 'runtime-host-help.txt'):
            if not _lstat_regular(Path(evidence_dir) / (fixture['id'] + '-' + suffix),
                                  MAX_RUNTIME_BYTES):
                raise ValueError('host compatibility output is empty')
        hosts.append({'id': fixture['id'], 'image': fixture['image'],
                      'platform': 'linux/' + arch, 'machine': machine,
                      'runtimeIdentity': runtime})
    return hosts


def _qualification_record(identity, artifact, source, static_report, hosts,
                           report_records, go_record, syft_version):
    return {
        'schemaVersion': 2,
        'scope': 'static-cli-archive',
        'archive': {'basename': 'leapviewctl-linux-' + identity['platform'].removeprefix('linux/') + '.tar.gz',
                    'sha256': artifact['sha256']},
        'archiveIdentity': identity,
        'source': source,
        'platform': artifact['platform'],
        'hosts': hosts,
        'syftVersion': syft_version,
        'staticReport': static_report,
        'reports': report_records,
        'goEvidence': go_record,
        'result': 'success',
        'releaseAdmission': False,
    }


def _host_compatibility_record(identity, archive_hash, source, static_report, hosts, reports):
    return {
        'schemaVersion': 1,
        'scope': 'static-cli-host-compatibility',
        'archive': {'basename': 'leapviewctl-linux-' + identity['platform'].removeprefix('linux/') + '.tar.gz',
                    'sha256': archive_hash},
        'archiveIdentity': identity,
        'source': source,
        'platform': identity['platform'],
        'staticBinarySHA256': static_report['binarySHA256'],
        'staticReport': static_report,
        'hosts': hosts,
        'reports': reports,
        'releaseAdmission': False,
    }


def _manifest_with_qualification(base, qualification):
    manifest = json.loads(candidate.canonical_bytes(base))
    manifest['evidence'][QUALIFICATION_KEY] = qualification
    manifest.pop('candidateDigest', None)
    manifest['candidateDigest'] = candidate.digest_bytes(
        b'leapview/nix-candidate-manifest/v1\n' + candidate.canonical_bytes(manifest))
    if len(candidate.canonical_bytes(manifest)) > candidate.MAX_JSON_BYTES:
        raise ValueError('candidate manifest exceeds its byte limit')
    return manifest


def _root_inventory(evidence_dir, *, qualifying):
    root = Path(evidence_dir)
    if root.is_symlink() or not root.is_dir():
        raise ValueError('evidence directory must be a regular directory')
    names = {path.name for path in root.iterdir()}
    if qualifying:
        if names:
            raise ValueError('qualification evidence directory must be empty')
    elif names != set(ROOT_REPORTS):
        raise ValueError('qualification evidence inventory is incomplete or contains extra files')


def _check_paths(archive, identity_path):
    archive_data = _lstat_regular(archive, MAX_ARCHIVE_BYTES)
    identity_data = _lstat_regular(identity_path, candidate.MAX_JSON_BYTES)
    return candidate.digest_bytes(archive_data), candidate.digest_bytes(identity_data)


def qualify(archive, archive_identity, source_root, source_revision, binary_verifier, evidence_dir):
    if os.geteuid() == 0:
        raise ValueError('CLI qualification must run as an unprivileged user')
    archive, identity_path, source_root, evidence_dir = map(Path,
        (archive, archive_identity, source_root, evidence_dir))
    verifier = _validate_binary_verifier(binary_verifier)
    source = _trusted_source(source_root, source_revision)
    archive_hash, identity_hash = _check_paths(archive, identity_path)
    identity, expected_build_time = _identity_file(identity_path, source_root, source, archive,
                                                    source_revision)
    arch = identity['platform'].removeprefix('linux/')
    machine_arch, machine = _native_arch()
    if machine_arch != arch:
        raise ValueError('CLI qualification must run on the archive native architecture')
    evidence_dir.mkdir(mode=0o700, parents=True, exist_ok=False)
    os.chmod(evidence_dir, 0o700)
    output_directory = evidence_dir.resolve()
    with tempfile.TemporaryDirectory(prefix='leapview-cli-qualify-') as temporary:
        binaries = go_evidence.extract(archive, {
            'kind': 'cli-archive', 'platform': identity['platform'], 'sha256': archive_hash,
            'version': identity['version'],
        }, Path(temporary))
        binary = binaries['leapviewctl']
        # The extractor writes a private mode-0600 copy for scanners. Docker's
        # UID 65534 probe needs read/execute bits on this temporary copy only.
        binary.chmod(0o755)
        static_report, build_info = _static_report(binary, arch)
        _write_json_new(output_directory / 'static.json', static_report)
        probed_hosts, probed_machine = _run_host(binary, arch, output_directory)
        if probed_machine != machine:
            raise ValueError('native host architecture changed during qualification')
        hosts = _expected_hosts(output_directory, arch, identity, source, expected_build_time)
        if probed_hosts != hosts:
            raise ValueError('native host probe reports differ from parsed fixture evidence')
        spdx_path = output_directory / 'sbom.spdx.json'
        spdx_bytes, syft_version = _syft_spdx(binary, spdx_path)
        validate_spdx(spdx_bytes, build_info, static_report['binarySHA256'])
        if _check_paths(archive, identity_path) != (archive_hash, identity_hash):
            raise ValueError('archive or archive identity changed during qualification')

    go_dir = output_directory / 'go'
    go_evidence.scan(archive, {'kind': 'cli-archive', 'platform': identity['platform'],
                               'sha256': archive_hash, 'version': identity['version']}, go_dir, verifier)
    base = candidate.collect(archive, 'cli-archive', source, archive_identity=identity,
                             go_dir=go_dir, binary_verifier=verifier)
    artifact = base['artifact']
    go_record = base['evidence']['go-binaries']
    if artifact['sha256'] != archive_hash or artifact['platform'] != identity['platform']:
        raise ValueError('candidate artifact identity changed during qualification')
    report_records = _report_records(output_directory)
    qualification = _qualification_record(identity, artifact, source, static_report, hosts,
                                          report_records, go_record, syft_version)
    manifest = _manifest_with_qualification(base, qualification)
    _write_json_new(output_directory / 'candidate-manifest.json', manifest)
    qualification_file = dict(qualification)
    qualification_file['candidateDigest'] = manifest['candidateDigest']
    _write_json_new(output_directory / 'qualification.json', qualification_file)
    if _check_paths(archive, identity_path) != (archive_hash, identity_hash) or candidate.canonical_bytes(
            _trusted_source(source_root, source_revision)) != candidate.canonical_bytes(source):
        raise ValueError('candidate source or archive changed during qualification')
    return manifest


def probe_hosts(archive, archive_identity, source_root, source_revision, evidence_dir):
    if os.geteuid() == 0:
        raise ValueError('CLI host probing must run as an unprivileged user')
    archive, identity_path, source_root, evidence_dir = map(Path,
        (archive, archive_identity, source_root, evidence_dir))
    source = _trusted_source(source_root, source_revision)
    archive_hash, identity_hash = _check_paths(archive, identity_path)
    identity, expected_build_time = _identity_file(identity_path, source_root, source, archive,
                                                    source_revision)
    arch = identity['platform'].removeprefix('linux/')
    machine_arch, machine = _native_arch()
    if machine_arch != arch:
        raise ValueError('CLI host probing must run on the archive native architecture')
    evidence_dir.mkdir(mode=0o700, parents=True, exist_ok=False)
    os.chmod(evidence_dir, 0o700)
    output_directory = evidence_dir.resolve()
    with tempfile.TemporaryDirectory(prefix='leapview-cli-host-probe-') as temporary:
        binaries = go_evidence.extract(archive, {
            'kind': 'cli-archive', 'platform': identity['platform'], 'sha256': archive_hash,
            'version': identity['version'],
        }, Path(temporary))
        binary = binaries['leapviewctl']
        binary.chmod(0o755)
        static_report, _ = _static_report(binary, arch)
        _write_json_new(output_directory / 'static.json', static_report)
        probed_hosts, probed_machine = _run_host(binary, arch, output_directory)
        if probed_machine != machine:
            raise ValueError('native host architecture changed during host probing')
        hosts = _expected_hosts(output_directory, arch, identity, source, expected_build_time)
        if probed_hosts != hosts:
            raise ValueError('native host probe reports differ from parsed fixture evidence')
    if _check_paths(archive, identity_path) != (archive_hash, identity_hash) or candidate.canonical_bytes(
            _trusted_source(source_root, source_revision)) != candidate.canonical_bytes(source):
        raise ValueError('candidate source or archive changed during host probing')
    receipt = _host_compatibility_record(identity, archive_hash, source, static_report, hosts,
                                         _report_records(output_directory, PROBE_HOST_REPORTS))
    _write_json_new(output_directory / 'host-compatibility.json', receipt)
    if _check_paths(archive, identity_path) != (archive_hash, identity_hash) or candidate.canonical_bytes(
            _trusted_source(source_root, source_revision)) != candidate.canonical_bytes(source):
        raise ValueError('candidate source or archive changed during host probing')
    return receipt


def _expected_candidate(archive, identity_path, source_root, source_revision, verifier, evidence_dir):
    archive, identity_path, source_root, evidence_dir = map(Path,
        (archive, identity_path, source_root, evidence_dir))
    archive_hash, identity_hash = _check_paths(archive, identity_path)
    source = _trusted_source(source_root, source_revision)
    identity, expected_build_time = _identity_file(identity_path, source_root, source, archive,
                                                    source_revision)
    arch = identity['platform'].removeprefix('linux/')
    if Path(archive).name != 'leapviewctl-linux-' + arch + '.tar.gz':
        raise ValueError('archive basename does not match its declared architecture')
    _root_inventory(evidence_dir, qualifying=False)
    verifier = _validate_binary_verifier(verifier)
    hosts = _expected_hosts(evidence_dir, arch, identity, source, expected_build_time)
    with tempfile.TemporaryDirectory(prefix='leapview-cli-verify-') as temporary:
        binaries = go_evidence.extract(archive, {
            'kind': 'cli-archive', 'platform': identity['platform'], 'sha256': archive_hash,
            'version': identity['version'],
        }, Path(temporary))
        binary = binaries['leapviewctl']
        static_report, build_info = _static_report(binary, arch)
        static_bytes = _lstat_regular(Path(evidence_dir) / 'static.json', candidate.MAX_JSON_BYTES)
        if candidate.canonical_bytes(candidate.read_json(static_bytes)) != candidate.canonical_bytes(static_report):
            raise ValueError('static report differs from exact archive binary')
        spdx_bytes = _lstat_regular(Path(evidence_dir) / 'sbom.spdx.json', candidate.MAX_REPORT_BYTES)
        validate_spdx(spdx_bytes, build_info, static_report['binarySHA256'])

    report_records = _report_records(evidence_dir)
    actual = _read_json(Path(evidence_dir) / 'candidate-manifest.json')
    if not isinstance(actual, dict) or not isinstance(actual.get('evidence'), dict):
        raise ValueError('candidate manifest is malformed')
    base_manifest = copy.deepcopy(actual)
    base_manifest['evidence'].pop(QUALIFICATION_KEY, None)
    base_manifest.pop('candidateDigest', None)
    base_manifest['candidateDigest'] = candidate.digest_bytes(
        b'leapview/nix-candidate-manifest/v1\n' + candidate.canonical_bytes(base_manifest))
    base = candidate.verify(base_manifest, archive, source, kind='cli-archive',
                            archive_identity=identity, go_dir=Path(evidence_dir) / 'go',
                            binary_verifier=verifier)
    go_record = base['evidence']['go-binaries']
    qualification = _qualification_record(identity, base['artifact'], source, static_report, hosts,
                                          report_records, go_record, SYFT_VERSION)
    expected = _manifest_with_qualification(base, qualification)
    if candidate.canonical_bytes(actual) != candidate.canonical_bytes(expected):
        raise ValueError('candidate manifest differs from exact current archive, source or evidence')
    qualification_file = dict(qualification)
    qualification_file['candidateDigest'] = expected['candidateDigest']
    actual_qualification = _read_json(Path(evidence_dir) / 'qualification.json')
    if candidate.canonical_bytes(actual_qualification) != candidate.canonical_bytes(qualification_file):
        raise ValueError('qualification report differs from exact current archive or evidence')
    if _check_paths(archive, identity_path) != (archive_hash, identity_hash) or candidate.canonical_bytes(
            _trusted_source(source_root, source_revision)) != candidate.canonical_bytes(source):
        raise ValueError('candidate source or archive changed during verification')
    return expected


def verify(archive, archive_identity, source_root, source_revision, binary_verifier, evidence_dir):
    return _expected_candidate(archive, archive_identity, source_root, source_revision,
                               binary_verifier, evidence_dir)


def _match_attestation(entries, archive, digest, predicate_type, expected_predicate=None):
    subject = [{'name': Path(archive).name, 'digest': {'sha256': digest.removeprefix('sha256:')}}]
    if not isinstance(entries, list) or not entries or len(entries) > 10:
        raise ValueError('verified attestation statements are missing or exceed the limit')
    matches = []
    for entry in entries:
        if not isinstance(entry, dict):
            continue
        result = entry.get('verificationResult')
        statement = result.get('statement') if isinstance(result, dict) else None
        if not isinstance(statement, dict):
            continue
        if (statement.get('_type') != 'https://in-toto.io/Statement/v1' or
                statement.get('subject') != subject or statement.get('predicateType') != predicate_type or
                not isinstance(statement.get('predicate'), dict)):
            continue
        if expected_predicate is not None and candidate.canonical_bytes(statement['predicate']) != candidate.canonical_bytes(expected_predicate):
            continue
        matches.append(candidate.digest_bytes(candidate.canonical_bytes(statement)))
    matches = sorted(set(matches))
    if not matches:
        raise ValueError('verified attestation does not match the exact archive subject and predicate')
    return matches[0]


def verify_attestation(archive, digest, signer_revision, predicate_type, expected_predicate=None):
    if not REVISION.fullmatch(signer_revision):
        raise ValueError('signer revision must be an exact protected commit')
    with tempfile.TemporaryFile() as output:
        args = ['gh', 'attestation', 'verify', str(archive), '--repo', 'flidai/leapview',
                '--signer-workflow', WORKFLOW, '--source-digest', signer_revision,
                '--source-ref', 'refs/heads/main', '--deny-self-hosted-runners',
                '--predicate-type', predicate_type, '--limit', '10', '--format', 'json']
        _run(args, timeout=120, env=_runtime_env(), stdout=output)
        output.seek(0)
        entries = candidate.read_json(output.read(MAX_ATTESTATION_BYTES + 1), MAX_ATTESTATION_BYTES)
    return _match_attestation(entries, archive, digest, predicate_type, expected_predicate)


def verify_signed(archive, archive_identity, source_root, source_revision, binary_verifier,
                  evidence_dir, signer_revision, output):
    manifest = verify(archive, archive_identity, source_root, source_revision,
                      binary_verifier, evidence_dir)
    manifest_snapshot = candidate.canonical_bytes(manifest)
    if not REVISION.fullmatch(signer_revision):
        raise ValueError('signer revision must be an exact protected commit')
    archive = Path(archive)
    evidence_dir = Path(evidence_dir)
    spdx_bytes = _lstat_regular(evidence_dir / 'sbom.spdx.json', candidate.MAX_REPORT_BYTES)
    spdx_document = candidate.read_json(spdx_bytes, candidate.MAX_REPORT_BYTES)
    provenance_hash = verify_attestation(archive, manifest['artifact']['sha256'],
                                         signer_revision, PROVENANCE)
    spdx_hash = verify_attestation(archive, manifest['artifact']['sha256'],
                                   signer_revision, SPDX_PREDICATE, spdx_document)
    current_manifest = verify(archive, archive_identity, source_root, source_revision,
                              binary_verifier, evidence_dir)
    current_spdx = _lstat_regular(evidence_dir / 'sbom.spdx.json', candidate.MAX_REPORT_BYTES)
    if (candidate.canonical_bytes(current_manifest) != manifest_snapshot or
            current_spdx != spdx_bytes):
        raise ValueError('archive or qualification evidence changed during signed verification')
    manifest = current_manifest
    binding = {
        'schemaVersion': 1,
        'archive': {'basename': archive.name, 'sha256': manifest['artifact']['sha256']},
        'candidateDigest': manifest['candidateDigest'],
        'signer': {'workflow': WORKFLOW, 'sourceRevision': signer_revision,
                   'sourceRef': 'refs/heads/main'},
        'provenance': {'predicateType': PROVENANCE, 'statementSHA256': provenance_hash},
        'spdx': {'predicateType': SPDX_PREDICATE,
                 'reportSHA256': candidate.digest_bytes(spdx_bytes),
                 'predicateSHA256': candidate.digest_bytes(candidate.canonical_bytes(spdx_document)),
                 'statementSHA256': spdx_hash},
        'releaseAdmission': False,
    }
    binding['signedEvidenceBindingDigest'] = candidate.digest_bytes(
        b'leapview/nix-cli-signed-evidence/v1\n' + candidate.canonical_bytes(binding))
    Path(output).parent.mkdir(parents=True, exist_ok=True)
    _write_json_new(output, binding)
    return binding


def _arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    operations = parser.add_subparsers(dest='operation', required=True)
    for operation in ('qualify', 'verify', 'verify-signed', 'probe-hosts'):
        command = operations.add_parser(operation)
        command.add_argument('--archive', type=Path, required=True)
        command.add_argument('--archive-identity', type=Path, required=True)
        command.add_argument('--source-root', type=Path, required=True)
        command.add_argument('--source-revision', required=True)
        if operation != 'probe-hosts':
            command.add_argument('--binary-verifier', type=Path, required=True)
        command.add_argument('--evidence-dir', type=Path, required=True)
        if operation == 'verify-signed':
            command.add_argument('--signer-revision', required=True)
            command.add_argument('--output', type=Path, required=True)
    return parser


def main():
    args = _arguments().parse_args()
    os.umask(0o077)
    try:
        if args.operation == 'qualify':
            manifest = qualify(args.archive, args.archive_identity, args.source_root,
                               args.source_revision, args.binary_verifier, args.evidence_dir)
            result = {'candidateDigest': manifest['candidateDigest'], 'releaseAdmission': False}
        elif args.operation == 'verify':
            manifest = verify(args.archive, args.archive_identity, args.source_root,
                              args.source_revision, args.binary_verifier, args.evidence_dir)
            result = {'candidateDigest': manifest['candidateDigest'], 'releaseAdmission': False}
        elif args.operation == 'probe-hosts':
            receipt = probe_hosts(args.archive, args.archive_identity, args.source_root,
                                  args.source_revision, args.evidence_dir)
            result = {'archiveSHA256': receipt['archive']['sha256'],
                      'staticBinarySHA256': receipt['staticBinarySHA256'],
                      'releaseAdmission': False}
        else:
            binding = verify_signed(args.archive, args.archive_identity, args.source_root,
                                    args.source_revision, args.binary_verifier, args.evidence_dir,
                                    args.signer_revision, args.output)
            result = {'signedEvidenceBindingDigest': binding['signedEvidenceBindingDigest'],
                      'releaseAdmission': False}
        print(json.dumps(result, sort_keys=True))
    except (ValueError, KeyError, TypeError, OSError, EOFError, tarfile.TarError,
            subprocess.SubprocessError) as error:
        raise SystemExit('CLI publication evidence rejected: ' + str(error)) from error


if __name__ == '__main__':
    main()

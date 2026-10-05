#!/usr/bin/env python3
"""Qualify one exact unsigned Nix-built Linux x64 Desktop Debian candidate."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
from pathlib import PurePosixPath
import re
import shutil
import shlex
import stat
import subprocess
import sys
import tarfile

sys.path.insert(0, str(Path(__file__).resolve().parent))
import nix_candidate_manifest as candidate


REVISION = re.compile(r'^[0-9a-f]{40}$')
MAX_DEB_BYTES = 1024 * 1024 * 1024
MAX_DEB_MEMBER_BYTES = 512 * 1024 * 1024
MAX_DEB_UNPACKED_BYTES = 2 * 1024 * 1024 * 1024
PACKAGE_NAME = 'leapview-desktop'
APP_DIRECTORY = Path('usr/lib/leapview-desktop')
APP_EXECUTABLE = APP_DIRECTORY / 'LeapView'
SANDBOX_EXECUTABLE = APP_DIRECTORY / 'chrome-sandbox'
OWNED_INSTALL_PATHS = [Path('/usr/lib/leapview-desktop'),
                       Path('/usr/bin/leapview-desktop'),
                       Path('/usr/share/applications/leapview-desktop.desktop'),
                       Path('/usr/share/doc/leapview-desktop/copyright'),
                       Path('/usr/share/lintian/overrides/leapview-desktop'),
                       Path('/usr/share/pixmaps/leapview-desktop.png')]
CHECKS = [
    'ubuntu-22.04-x86_64-host',
    'candidate-protected-desktop-policy-match',
    'desktop-contract-tests',
    'exact-deb-installed-for-dependencies',
    'exact-deb-control-fields-and-dependency-contract',
    'exact-deb-has-no-maintainer-scripts',
    'exact-deb-package-startup-and-content-verification',
    'exact-deb-installer-metadata-verification',
    'exact-deb-release-evidence',
    'installed-exact-deb-hostile-instance-proof',
    'apt-install-reinstall-protocol-registration-removal',
]
LIFECYCLE_TESTED = ['install', 'reinstall', 'protocol-registration', 'remove']
LIFECYCLE_PENDING = ['upgrade', 'rollback', 'recovery', 'profile-observation']
POLICY_INPUTS = ['desktop/package.json', 'desktop/bun.lock', 'desktop/release-policy.json']
VERIFIER_FILES = [
    'scripts/nix_candidate_manifest.py',
    'scripts/nix_desktop_qualification.py',
    'desktop/scripts/verify-package.mjs',
    'desktop/scripts/verify-installer.mjs',
    'desktop/scripts/release-evidence.mjs',
    'desktop/scripts/verify-release-evidence.mjs',
    'desktop/scripts/qualify-installer-linux.sh',
    'internal/app/testing/maliciousinstance/packaged_proof_test.go',
]
MAINTAINER_SCRIPTS = {'preinst', 'postinst', 'prerm', 'postrm', 'config', 'triggers'}
EXPECTED_DEB_CONTROL = {
    'section': 'utils',
    'priority': 'optional',
    'depends': ('libgtk-3-0, libnotify4, libnss3, xdg-utils, libatspi2.0-0, libdrm2, '
                'libgbm1, libxcb-dri3-0, kde-cli-tools | kde-runtime | trash-cli | '
                'libglib2.0-bin | gvfs-bin'),
    'recommends': 'pulseaudio | libasound2',
    'suggests': 'gir1.2-gnomekeyring-1.0, libgnome-keyring0, lsb-release',
    'maintainer': 'LeapView',
    'homepage': 'https://leapview.dev',
    'description': ('End-user desktop client for deployed LeapView instances.\n'
                    'Connects to deployed LeapView instances while preserving server-side\n'
                    'authentication, access, and dashboard authority.'),
}
DEB_CONTROL_FIELDS = set(EXPECTED_DEB_CONTROL) | {
    'package', 'version', 'architecture', 'installed-size',
}


def digest_file(path):
    with Path(path).open('rb') as stream:
        return 'sha256:' + hashlib.file_digest(stream, 'sha256').hexdigest()


def read_json(path, limit=candidate.MAX_JSON_BYTES):
    return candidate.read_json_file(path, limit)


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open('x', encoding='utf-8') as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write('\n')


def os_release_identity(data):
    fields = {}
    for line in data.splitlines():
        if not line or line.startswith('#') or '=' not in line:
            continue
        key, value = line.split('=', 1)
        if key in fields:
            raise ValueError('duplicate /etc/os-release field: ' + key)
        parsed = shlex.split(value, posix=True)
        if len(parsed) != 1:
            raise ValueError('malformed /etc/os-release field: ' + key)
        fields[key] = parsed[0]
    return {'id': fields.get('ID'), 'versionID': fields.get('VERSION_ID')}


def require_host(os_release_path=Path('/etc/os-release'), machine=None):
    with Path(os_release_path).open(encoding='utf-8') as stream:
        identity = os_release_identity(stream.read())
    machine = platform.machine() if machine is None else machine
    if identity != {'id': 'ubuntu', 'versionID': '22.04'} or machine != 'x86_64':
        raise ValueError('Desktop candidate execution requires native Ubuntu 22.04 x86_64')
    return {'id': identity['id'], 'versionID': identity['versionID'],
            'architecture': machine, 'nativeExecution': True}


def command_output(arguments, *, cwd=None, env=None):
    try:
        result = subprocess.run(arguments, cwd=cwd, env=env, check=True, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=600)
    except (OSError, subprocess.SubprocessError) as error:
        detail = getattr(error, 'stderr', None) or ''
        if isinstance(detail, bytes):
            detail = detail.decode(errors='replace')
        detail = detail.strip().splitlines()[-1] if detail.strip() else ''
        raise ValueError('qualification command failed: ' + Path(arguments[0]).name +
                         (': ' + detail if detail else '')) from error
    return result.stdout.strip()


def run_command(arguments, *, cwd=None, env=None):
    try:
        subprocess.run(arguments, cwd=cwd, env=env, check=True, timeout=900)
    except (OSError, subprocess.SubprocessError) as error:
        detail = getattr(error, 'stderr', None) or ''
        if isinstance(detail, bytes):
            detail = detail.decode(errors='replace')
        detail = detail.strip().splitlines()[-1] if detail.strip() else ''
        raise ValueError('qualification command failed: ' + Path(arguments[0]).name +
                         (': ' + detail if detail else '')) from error


def deb_control_fields(archive):
    control = command_output(['dpkg-deb', '--field', str(archive)])
    fields = {}
    current = None
    for line in control.splitlines():
        if line.startswith((' ', '\t')):
            if current is None:
                raise ValueError('Debian package control continuation has no field')
            fields[current] += '\n' + line[1:].rstrip()
            continue
        name, separator, value = line.partition(':')
        if (not separator or re.fullmatch(r'[A-Za-z][A-Za-z0-9-]*', name) is None
                or name.lower() in fields):
            raise ValueError('Debian package control fields are malformed or duplicated')
        current = name.lower()
        fields[current] = value.lstrip()
    return fields


def validate_deb_control(fields, expected_version):
    if set(fields) != DEB_CONTROL_FIELDS:
        raise ValueError('Debian package control field inventory differs from the protected MakerDeb contract')
    if (fields['package'] != PACKAGE_NAME or fields['version'] != expected_version
            or fields['architecture'] != 'amd64'):
        raise ValueError('Debian package control identity differs from LeapView Linux x64 source')
    for field, expected in EXPECTED_DEB_CONTROL.items():
        if fields[field] != expected:
            raise ValueError('Debian package control field differs from the protected MakerDeb contract: ' + field)
    installed_size = fields['installed-size']
    if (re.fullmatch(r'[1-9][0-9]*', installed_size) is None
            or int(installed_size) > MAX_DEB_UNPACKED_BYTES // 1024):
        raise ValueError('Debian package Installed-Size is outside the protected bound')


def aligned_policy_inputs(source_root, verifier_root):
    source_root, verifier_root = Path(source_root), Path(verifier_root)
    result = []
    for relative in POLICY_INPUTS:
        source_path, verifier_path = source_root / relative, verifier_root / relative
        if (source_path.is_symlink() or verifier_path.is_symlink()
                or not source_path.is_file() or not verifier_path.is_file()):
            raise ValueError('candidate or protected desktop policy input is missing: ' + relative)
        source_bytes, verifier_bytes = source_path.read_bytes(), verifier_path.read_bytes()
        if source_bytes != verifier_bytes:
            raise ValueError('candidate desktop policy differs from protected qualification tools: ' + relative)
        result.append({'path': relative, 'sha256': candidate.digest_bytes(source_bytes)})
    return result


def verifier_identity(verifier_root):
    verifier_root = Path(verifier_root)
    source = candidate.checkout_source(verifier_root)
    files = []
    for relative in VERIFIER_FILES:
        path = verifier_root / relative
        if path.is_symlink() or not path.is_file():
            raise ValueError('protected Desktop verifier file is missing: ' + relative)
        files.append({'path': relative, 'sha256': digest_file(path)})
    return {'revision': source['revision'], 'files': files}


def candidate_record(archive, source_root, source_revision, verifier_root):
    archive, source_root, verifier_root = Path(archive), Path(source_root), Path(verifier_root)
    if (not REVISION.fullmatch(source_revision) or archive.is_symlink()
            or not archive.is_file() or archive.stat().st_size <= 0
            or archive.stat().st_size > MAX_DEB_BYTES):
        raise ValueError('Desktop candidate requires a regular Debian archive and exact source revision')
    source = candidate.checkout_source(source_root)
    if source['revision'] != source_revision:
        raise ValueError('Desktop candidate source checkout differs from the requested revision')
    aligned_policy_inputs(source_root, verifier_root)
    package = read_json(source_root / 'desktop/package.json')
    control = deb_control_fields(archive)
    validate_deb_control(control, package.get('version'))
    identity = {'Package': control['package'], 'Version': control['version'],
                'Architecture': control['architecture']}
    artifact_identity = {'platform': 'linux/amd64', 'version': identity['Version'],
                         'sourceRevision': source_revision}
    record = candidate.collect(archive, 'desktop-archive', source, archive_identity=artifact_identity)
    return record, identity


def deb_tar_headers(archive, option, maximum_members):
    process = subprocess.Popen(['dpkg-deb', option, str(archive)], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    members = []
    try:
        with tarfile.open(fileobj=process.stdout, mode='r|*', tarinfo=DebianTarInfo) as stream:
            unpacked_bytes = 0
            for member in stream:
                if len(members) >= maximum_members:
                    raise ValueError('Debian archive member count exceeds the qualification bound')
                if type(member.size) is not int or member.size < 0 or member.size > MAX_DEB_MEMBER_BYTES:
                    raise ValueError('Debian archive member exceeds the qualification size bound')
                unpacked_bytes += member.size
                if unpacked_bytes > MAX_DEB_UNPACKED_BYTES:
                    raise ValueError('Debian archive unpacked content exceeds the qualification size bound')
                members.append(member)
        _stdout, stderr = process.communicate(timeout=90)
    except (OSError, tarfile.TarError, subprocess.SubprocessError, ValueError) as error:
        if isinstance(error, ValueError):
            raise
        raise ValueError('could not safely inspect Debian archive metadata') from error
    finally:
        if process.poll() is None:
            process.kill()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
        for pipe in (process.stdout, process.stderr):
            if pipe is not None and not pipe.closed:
                pipe.close()
    if process.returncode != 0:
        raise ValueError('dpkg-deb could not inspect Debian archive: ' + stderr.decode(errors='replace')[-512:])
    return members


class DebianTarInfo(tarfile.TarInfo):
    """Keep tarfile from consuming unbounded PAX, GNU long-name, or sparse headers."""

    def _proc_member(self, archive):
        extended_types = {tarfile.XHDTYPE, tarfile.XGLTYPE, tarfile.SOLARIS_XHDTYPE,
                          tarfile.GNUTYPE_LONGNAME, tarfile.GNUTYPE_LONGLINK, tarfile.GNUTYPE_SPARSE}
        if self.type in extended_types:
            raise ValueError('Debian payload may not use extended or sparse tar headers')
        return super()._proc_member(archive)


def validate_package_paths(archive):
    return validate_package_members(deb_tar_headers(archive, '--fsys-tarfile', 20_000))


def validate_package_members(members):
    allowed_directories = {
        '',
        'usr', 'usr/bin', 'usr/lib', 'usr/lib/leapview-desktop',
        'usr/share', 'usr/share/applications', 'usr/share/doc', 'usr/share/doc/leapview-desktop',
        'usr/share/lintian', 'usr/share/lintian/overrides', 'usr/share/pixmaps',
    }
    allowed_files = {
        'usr/share/applications/leapview-desktop.desktop',
        'usr/share/doc/leapview-desktop/copyright',
        'usr/share/lintian/overrides/leapview-desktop',
        'usr/share/pixmaps/leapview-desktop.png',
    }
    allowed_links = {'usr/bin/leapview-desktop': '../lib/leapview-desktop/LeapView'}
    inventory = set()
    regular_files = set()
    symbolic_links = set()
    for member in members:
        path = '' if member.name in {'.', './'} else member.name.removeprefix('./').rstrip('/')
        relative = PurePosixPath(path)
        if (member.name.startswith('/') or '\\' in path or '..' in relative.parts
                or (path and str(relative) != path) or path in inventory or (not path and not member.isdir())):
            raise ValueError('Debian payload path is unsafe or duplicated')
        inventory.add(path)
        if member.uid != 0 or member.gid != 0:
            raise ValueError('Debian package payload must be owned by root')
        if member.isdir():
            if path not in allowed_directories and not path.startswith('usr/lib/leapview-desktop/'):
                raise ValueError('Debian package contains an unexpected directory: ' + path)
            if member.mode != 0o755:
                raise ValueError('Debian package directory mode is not canonical: ' + path)
        elif member.isfile():
            if path in allowed_links:
                raise ValueError('Debian launcher must remain the reviewed symbolic link')
            if path == str(SANDBOX_EXECUTABLE):
                if member.mode != 0o4755:
                    raise ValueError('Debian chrome-sandbox archive mode must be root setuid 4755')
            elif member.mode not in {0o644, 0o755}:
                raise ValueError('Debian package file mode has unexpected privilege bits: ' + path)
            if (path not in allowed_files and path not in allowed_links
                    and path != str(SANDBOX_EXECUTABLE)
                    and not path.startswith('usr/lib/leapview-desktop/')):
                raise ValueError('Debian package contains an unexpected regular file: ' + path)
            regular_files.add(path)
        elif member.issym():
            if path not in allowed_links or member.linkname != allowed_links[path] or member.mode != 0o777:
                raise ValueError('Desktop launcher symlink differs from the reviewed MakerDeb contract')
            symbolic_links.add(path)
        else:
            raise ValueError('Debian package may contain only regular files, directories and the reviewed launcher link')
    required = {str(APP_EXECUTABLE), str(SANDBOX_EXECUTABLE),
                'usr/bin/leapview-desktop', 'usr/share/applications/leapview-desktop.desktop'}
    required_regular_files = {str(APP_EXECUTABLE), str(SANDBOX_EXECUTABLE)}
    required_launcher = 'usr/bin/leapview-desktop'
    if (not required.issubset(inventory) or not required_regular_files.issubset(regular_files)
            or required_launcher not in symbolic_links):
        raise ValueError('Debian package is missing a required desktop payload path or has the wrong entry type')
    return inventory


def reject_maintainer_scripts(archive, verifier_root):
    members = deb_tar_headers(archive, '--ctrl-tarfile', 128)
    names = set()
    for member in members:
        path = '' if member.name in {'.', './'} else member.name.removeprefix('./')
        if not path and member.isdir() and member.mode == 0o755 and member.uid == 0 and member.gid == 0:
            continue
        if (not path or PurePosixPath(path).name != path or path in names or not member.isfile()
                or member.uid != 0 or member.gid != 0):
            raise ValueError('Debian control archive is outside the current static MakerDeb contract')
        names.add(path)
        if path in MAINTAINER_SCRIPTS:
            raise ValueError('Debian candidate contains an executable maintainer script: ' + path)
        if path not in {'control', 'md5sums', 'conffiles'} or member.mode != 0o644:
            raise ValueError('Debian control archive contains unexpected executable or metadata content')
    if 'control' not in names:
        raise ValueError('Debian control archive is missing package identity metadata')


def refuse_preexisting_installation():
    package_is_absent()
    for path in ('/usr/lib/leapview-desktop', '/usr/bin/leapview-desktop',
                 '/usr/share/applications/leapview-desktop.desktop'):
        if Path(path).exists() or Path(path).is_symlink():
            raise ValueError('Ubuntu 22.04 qualification target already contains ' + path)


def package_is_absent():
    result = subprocess.run(['dpkg-query', '-W', '-f=${Status}', PACKAGE_NAME], text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False)
    status = result.stdout.strip()
    if result.returncode == 0 and status and status != 'deinstall ok config-files':
        raise ValueError('Ubuntu 22.04 qualification requires leapview-desktop to be absent before install')


def remove_candidate_installation(verifier_root):
    def status():
        result = subprocess.run(['dpkg-query', '-W', '-f=${Status}', PACKAGE_NAME], cwd=verifier_root,
                                text=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False)
        return result.stdout.strip() if result.returncode == 0 else None

    installed_status = status()
    if installed_status not in {None, 'deinstall ok config-files'}:
        run_command(['sudo', 'dpkg', '--remove', '--force-remove-reinstreq', PACKAGE_NAME], cwd=verifier_root)
    installed_status = status()
    if installed_status not in {None, 'deinstall ok config-files'}:
        raise ValueError('could not remove the candidate after installer qualification')
    if any(path.exists() or path.is_symlink() for path in OWNED_INSTALL_PATHS):
        raise ValueError('desktop installer lifecycle left candidate-owned payload files installed')


def payload_tree(root):
    root = Path(root)
    metadata = root.lstat()
    result = {'.': ('directory', stat.S_IMODE(metadata.st_mode), metadata.st_uid, metadata.st_gid)}

    def walk(directory):
        for path in sorted(directory.iterdir(), key=lambda item: item.name):
            metadata = path.lstat()
            relative = path.relative_to(root).as_posix()
            mode = stat.S_IMODE(metadata.st_mode)
            if stat.S_ISDIR(metadata.st_mode):
                result[relative] = ('directory', mode, metadata.st_uid, metadata.st_gid)
                walk(path)
            elif stat.S_ISREG(metadata.st_mode):
                result[relative] = ('file', mode, metadata.st_uid, metadata.st_gid, digest_file(path))
            elif stat.S_ISLNK(metadata.st_mode):
                result[relative] = ('symlink', mode, metadata.st_uid, metadata.st_gid, os.readlink(path))
            else:
                raise ValueError('Debian application contains a special filesystem entry: ' + relative)

    walk(root)
    return result


def copy_exact_payload(archive, verifier_root):
    verifier_root = Path(verifier_root)
    desktop_root = verifier_root / 'desktop'
    out = desktop_root / 'out'
    if out.is_symlink():
        raise ValueError('desktop/out may not be a symlink')
    if out.exists():
        command_output(['sudo', 'rm', '-rf', str(out)])
    out.mkdir(parents=True)
    make = out / 'make'
    make.mkdir()
    copied_archive = make / Path(archive).name
    shutil.copyfile(archive, copied_archive)
    if digest_file(copied_archive) != digest_file(archive):
        raise ValueError('copied Debian package bytes differ from the candidate')
    validate_package_paths(archive)
    reject_maintainer_scripts(archive, verifier_root)

    temporary = out / 'deb-payload'
    temporary.mkdir()
    command_output(['sudo', 'dpkg-deb', '--extract', str(Path(archive).resolve()), str(temporary)])
    desktop_entry = temporary / 'usr/share/applications/leapview-desktop.desktop'
    launcher = temporary / 'usr/bin/leapview-desktop'
    if not desktop_entry.is_file() or not launcher.is_symlink():
        raise ValueError('Debian candidate lacks the reviewed desktop entry or MakerDeb launcher')
    if os.readlink(launcher) != '../lib/leapview-desktop/LeapView':
        raise ValueError('Debian launcher target differs from its desktop entry contract')
    payload_root = temporary.resolve()
    executable = launcher.resolve(strict=True)
    if not executable.is_relative_to(payload_root) or executable.name != 'LeapView':
        raise ValueError('Debian launcher resolves outside its extracted application payload')
    extracted_app = executable.parent
    sandbox = temporary / SANDBOX_EXECUTABLE
    for path in (executable, sandbox):
        metadata = path.lstat()
        if not stat.S_ISREG(metadata.st_mode):
            raise ValueError('Debian payload is missing a regular packaged Electron executable')
    sandbox_metadata = sandbox.stat()
    if sandbox_metadata.st_uid != 0 or stat.S_IMODE(sandbox_metadata.st_mode) != 0o4755:
        raise ValueError('Debian payload chrome-sandbox must be root-owned setuid 4755 in the archive')
    staged = out / 'LeapView-linux-x64'
    command_output(['sudo', 'mv', str(extracted_app), str(staged)])
    command_output(['sudo', 'rm', '-rf', str(temporary)])
    return copied_archive, staged, executable.relative_to(payload_root)


def installed_executable(staged_app, executable_relative):
    installed_app = Path('/') / executable_relative.parent
    installed_executable = Path('/') / executable_relative
    paths = command_output(['dpkg-query', '-L', PACKAGE_NAME]).splitlines()
    if str(installed_executable) not in paths or not installed_executable.is_file():
        raise ValueError('installed Debian candidate does not contain its declared LeapView executable')
    if Path('/usr/bin/leapview-desktop').resolve(strict=True) != installed_executable.resolve(strict=True):
        raise ValueError('installed MakerDeb launcher does not resolve to its declared app executable')
    if payload_tree(installed_app) != payload_tree(staged_app):
        raise ValueError('installed Desktop payload differs from the exact extracted Debian application tree')
    return installed_executable


def evidence_files(evidence_directory, record):
    evidence_directory = Path(evidence_directory)
    verification_path = evidence_directory / 'package-verification.json'
    release_files = sorted(evidence_directory.glob('*.release.json'))
    sbom_files = sorted(evidence_directory.glob('*.spdx.json'))
    checksums_path = evidence_directory / 'checksums.txt'
    verifier_path = evidence_directory / 'verify-release-evidence.mjs'
    selected = [verification_path, *release_files, *sbom_files, checksums_path, verifier_path,
                evidence_directory / 'candidate-manifest.json']
    if (len(release_files) != 1 or len(sbom_files) != 1
            or any(path.is_symlink() or not path.is_file() for path in selected)):
        raise ValueError('desktop package and release evidence set is incomplete')
    release = read_json(release_files[0])
    if (release.get('source', {}).get('commit') != record['source']['revision']
            or release.get('source', {}).get('dirty') is not False
            or release.get('artifact', {}).get('sha256') != record['artifact']['sha256'].removeprefix('sha256:')
            or release.get('artifact', {}).get('format') != 'deb'
            or release.get('application', {}).get('packageName') != PACKAGE_NAME
            or release.get('application', {}).get('version') != record['artifact']['version']
            or release.get('support', {}).get('minimumVersion') != 'Ubuntu 22.04 LTS'
            or release.get('support', {}).get('qualification') != 'candidate'
            or release.get('signing') != {'state': 'unsigned-candidate', 'productionEligible': False, 'identity': None}):
        raise ValueError('desktop release evidence is not bound to this unsigned Nix candidate')
    verification = read_json(verification_path)
    if (verification.get('platform') != 'linux' or verification.get('architecture') != 'x64'
            or verification.get('packageFormat') != 'deb'
            or verification.get('startup') != 'trusted-shell-ready'):
        raise ValueError('desktop package startup verification does not match Linux x64')
    sbom = read_json(sbom_files[0], candidate.MAX_REPORT_BYTES)
    if sbom.get('spdxVersion') != 'SPDX-2.3' or not sbom.get('packages'):
        raise ValueError('desktop release SPDX inventory is incomplete')
    selected = [verification_path, release_files[0], sbom_files[0], checksums_path, verifier_path]
    manifest_path = evidence_directory / 'candidate-manifest.json'
    manifest = read_json(manifest_path)
    if candidate.canonical_bytes(manifest) != candidate.canonical_bytes(record):
        raise ValueError('desktop candidate manifest changed or belongs to another artifact')
    selected.append(manifest_path)
    return [{'path': path.name, 'sha256': digest_file(path)} for path in selected]


def copy_evidence_bundle(verifier_root, evidence_directory, record):
    desktop_out = Path(verifier_root) / 'desktop/out'
    paths = [desktop_out / 'package-verification.json', *sorted((desktop_out / 'evidence').glob('*.release.json')),
             *sorted((desktop_out / 'evidence').glob('*.spdx.json')),
             desktop_out / 'evidence/checksums.txt', desktop_out / 'evidence/verify-release-evidence.mjs']
    evidence_directory = Path(evidence_directory)
    evidence_directory.mkdir(parents=True, exist_ok=True)
    manifest_path = evidence_directory / 'candidate-manifest.json'
    write_json(manifest_path, record)
    for path in paths:
        shutil.copyfile(path, evidence_directory / path.name)
    return evidence_files(evidence_directory, record)


def qualification_report(record, host, verifier, policy_inputs, evidence):
    return {'schemaVersion': 1, 'result': 'success', 'sourceRevision': record['source']['revision'],
            'platform': 'linux/amd64', 'host': host, 'artifact': record['artifact'],
            'candidateDigest': record['candidateDigest'],
            'requiredReleaseEvidence': record['requiredReleaseEvidence'],
            'checks': CHECKS, 'installerLifecycleTested': LIFECYCLE_TESTED,
            'installerLifecyclePending': LIFECYCLE_PENDING,
            'policyInputs': policy_inputs, 'verifierRevision': verifier['revision'],
            'verifierFiles': verifier['files'],
            'evidence': evidence, 'releaseAdmission': False}


def verify_report(archive, source_root, verifier_root, source_revision, evidence_directory,
                  os_release_path=Path('/etc/os-release'), machine=None):
    evidence_directory = Path(evidence_directory)
    report_path = evidence_directory / 'qualification-report.json'
    report = read_json(report_path)
    record, _identity = candidate_record(archive, source_root, source_revision, verifier_root)
    host = require_host(os_release_path, machine)
    verifier = verifier_identity(verifier_root)
    policy_inputs = aligned_policy_inputs(source_root, verifier_root)
    evidence = evidence_files(evidence_directory, record)
    expected = qualification_report(record, host, verifier, policy_inputs, evidence)
    if (candidate.canonical_bytes(report) != candidate.canonical_bytes(expected)
            or report.get('result') != 'success' or report.get('releaseAdmission') is not False):
        raise ValueError('Desktop qualification report does not bind the exact candidate and host')
    return report


def run_desktop_verifiers(verifier_root, copied_archive, staged_app, executable_relative, source_revision):
    verifier_root = Path(verifier_root)
    desktop = verifier_root / 'desktop'
    env = os.environ.copy()
    env.update({'LEAPVIEW_DESKTOP_DISTRIBUTION': 'preview', 'GITHUB_SHA': source_revision,
                'GITHUB_ACTIONS': 'true', 'CI': 'true'})
    run_command(['bun', 'install', '--frozen-lockfile'], cwd=desktop, env=env)
    run_command(['bun', 'run', 'test'], cwd=desktop, env=env)

    refuse_preexisting_installation()
    try:
        run_command(['sudo', 'apt-get', 'install', '--yes', str(copied_archive)], cwd=verifier_root, env=env)
        installed_app_exec = installed_executable(staged_app, executable_relative)
        node = 'node_modules/node/bin/node'
        run_command(['xvfb-run', '--auto-servernum', node, 'scripts/verify-package.mjs'], cwd=desktop, env=env)
        run_command([node, 'scripts/verify-installer.mjs'], cwd=desktop, env=env)
        run_command(['bun', 'run', 'evidence'], cwd=desktop, env=env)

        proof_env = env.copy()
        proof_env['LEAPVIEW_PACKAGED_APP'] = str(installed_app_exec)
        run_command(['xvfb-run', '--auto-servernum', 'go', 'test', '-count=1',
                     '-run', 'TestPackagedLeapViewPreservesRemoteContentBoundary', '-v',
                     './internal/app/testing/maliciousinstance'], cwd=verifier_root, env=proof_env)
        run_command([str(desktop / 'scripts/qualify-installer-linux.sh'), str(copied_archive)],
                    cwd=verifier_root, env=env)
    finally:
        remove_candidate_installation(verifier_root)
    return env


def qualify(archive, source_root, verifier_root, source_revision, evidence_directory):
    host = require_host()
    record, _identity = candidate_record(archive, source_root, source_revision, verifier_root)
    refuse_preexisting_installation()
    verifier = verifier_identity(verifier_root)
    policy_inputs = aligned_policy_inputs(source_root, verifier_root)
    copied_archive, staged_app, _executable_relative = copy_exact_payload(archive, verifier_root)
    if digest_file(copied_archive) != record['artifact']['sha256']:
        raise ValueError('staged Debian installer no longer matches the candidate archive')
    run_desktop_verifiers(verifier_root, copied_archive, staged_app, _executable_relative, source_revision)
    if digest_file(copied_archive) != record['artifact']['sha256'] or digest_file(archive) != record['artifact']['sha256']:
        raise ValueError('exact Debian candidate bytes changed during qualification')

    evidence_directory = Path(evidence_directory)
    copy_evidence_bundle(verifier_root, evidence_directory, record)
    evidence = evidence_files(evidence_directory, record)
    report = qualification_report(record, host, verifier, policy_inputs, evidence)
    write_json(evidence_directory / 'qualification-report.json', report)
    verify_report(archive, source_root, verifier_root, source_revision, evidence_directory)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('qualify', 'verify'))
    parser.add_argument('--deb', type=Path, required=True)
    parser.add_argument('--source-root', type=Path, required=True)
    parser.add_argument('--verifier-root', type=Path, required=True)
    parser.add_argument('--source-revision', required=True)
    parser.add_argument('--evidence-dir', type=Path, required=True)
    args = parser.parse_args()
    args.deb = args.deb.absolute()
    args.source_root = args.source_root.resolve()
    args.verifier_root = args.verifier_root.resolve()
    args.evidence_dir = args.evidence_dir.resolve()
    os.umask(0o077)
    try:
        if args.operation == 'qualify':
            report = qualify(args.deb, args.source_root, args.verifier_root,
                             args.source_revision, args.evidence_dir)
        else:
            report = verify_report(args.deb, args.source_root, args.verifier_root,
                                   args.source_revision, args.evidence_dir)
        print(json.dumps({'candidateDigest': report['candidateDigest'], 'artifact': report['artifact'],
                          'result': report['result'], 'releaseAdmission': False}, sort_keys=True))
    except (OSError, ValueError, TypeError, KeyError, subprocess.CalledProcessError) as error:
        raise SystemExit('Desktop candidate qualification rejected: ' + str(error)) from error


if __name__ == '__main__':
    main()

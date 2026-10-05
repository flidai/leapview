#!/usr/bin/env python3
"""Exercise a content-verified native site image with a read-only root filesystem."""

import argparse
from datetime import datetime, timezone
import html
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
import uuid

import nix_archive_go_evidence as binary_evidence
import nix_candidate_manifest as candidate
import nix_registry_content as registry

CHECKS = ('native-platform', 'read-only-nonroot', 'health', 'readiness',
          'public-release', 'build-identity', 'installation-docs')
SITE_ENVIRONMENT = {'LEAPVIEW_SITE_BASE_URL=', 'LEAPVIEW_SITE_SHOWCASE_EMBED_URL='}
MAX_SAVED_IMAGE_BYTES = 1024 * 1024**2


def run(*args):
    return subprocess.check_output(args, text=True, timeout=180).strip()


def native_platform():
    architecture = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine())
    if platform.system() != 'Linux' or architecture is None:
        raise ValueError('site qualification requires a native supported Linux runner')
    return 'linux/' + architecture


def validate_image(image, artifact, revision):
    config = image['Config']
    labels = config['Labels']
    environment = config.get('Env')
    if (image['Os'] + '/' + image['Architecture'] != artifact['platform']
            or image['RootFS']['Layers'] != artifact['layerDiffIDs']
            or not candidate.SHA256.fullmatch(image['Id'])
            or config.get('User') != '65532:65532'
            or config.get('Entrypoint') != ['/leapview-site']
            or config.get('Cmd') != ['-addr=:8081']
            or config.get('WorkingDir') != '/'
            or config.get('ExposedPorts') != {'8081/tcp': {}}
            or config.get('Healthcheck') is not None
            or not isinstance(environment, list)
            or not all(isinstance(value, str) for value in environment)
            or set(environment) != SITE_ENVIRONMENT or len(environment) != len(SITE_ENVIRONMENT)
            or labels.get('org.opencontainers.image.revision') != revision
            or labels.get('org.opencontainers.image.version') != artifact['version']
            or labels.get('dev.leapview.build.kind') != 'site-image'):
        raise ValueError('loaded site image differs from its verified content or native runtime')


def saved_config_digest(archive_path, expected_digest):
    """Bind Docker's selected image to the exact config blob in its save archive."""
    if not candidate.SHA256.fullmatch(expected_digest):
        raise ValueError('candidate image config digest is invalid')
    archive_path = Path(archive_path)
    if archive_path.stat().st_size > MAX_SAVED_IMAGE_BYTES:
        raise ValueError('saved site image exceeds its byte limit')
    files, seen, total = {}, set(), 0
    with tarfile.open(archive_path, 'r:*') as contents:
        for index, member in enumerate(contents, start=1):
            if index > binary_evidence.MAX_ENTRIES:
                raise ValueError('saved site image exceeds its member limit')
            name = member.name.removeprefix('./')
            if member.isdir():
                name = name.rstrip('/')
                if name in {'', '.'}:
                    continue
                candidate.safe_path(name)
            else:
                candidate.safe_path(name)
            if name in seen:
                raise ValueError('duplicate saved image member: ' + name)
            seen.add(name)
            if member.isdir():
                continue
            if not member.isfile() or member.sparse is not None or member.size < 0:
                raise ValueError('saved image members must be regular files or directories')
            total += member.size
            if total > MAX_SAVED_IMAGE_BYTES:
                raise ValueError('saved site image exceeds its byte limit')
            files[name] = member
        manifest_member = files.get('manifest.json')
        if manifest_member is None or manifest_member.size > candidate.MAX_JSON_BYTES:
            raise ValueError('saved site image has no bounded manifest')
        manifest_stream = contents.extractfile(manifest_member)
        if manifest_stream is None:
            raise ValueError('saved site image manifest cannot be read')
        manifest_bytes = manifest_stream.read(candidate.MAX_JSON_BYTES + 1)
        if len(manifest_bytes) != manifest_member.size:
            raise ValueError('saved site image manifest is truncated')
        manifest = candidate.read_json(manifest_bytes)
        if not isinstance(manifest, list) or len(manifest) != 1 or not isinstance(manifest[0], dict):
            raise ValueError('saved site image must contain exactly one image')
        config_name = candidate.safe_path(manifest[0].get('Config'))
        config_member = files.get(config_name)
        if config_member is None or config_member.size > candidate.MAX_JSON_BYTES:
            raise ValueError('saved site image config blob is missing or unbounded')
        config_stream = contents.extractfile(config_member)
        if config_stream is None:
            raise ValueError('saved site image config blob cannot be read')
        config_bytes = config_stream.read(candidate.MAX_JSON_BYTES + 1)
        if len(config_bytes) != config_member.size:
            raise ValueError('saved site image config blob is truncated')
    actual_digest = candidate.digest_bytes(config_bytes)
    if actual_digest != expected_digest:
        raise ValueError('saved site image config differs from the verified candidate')
    return actual_digest


def verify_saved_image_config(image_reference, artifact):
    """Save one immutable image reference under TMPDIR and check its config blob."""
    with tempfile.TemporaryDirectory(prefix='leapview-site-image-') as directory:
        saved_image = Path(directory) / 'image.tar'
        run('docker', 'image', 'save', '--output', str(saved_image), image_reference)
        return saved_config_digest(saved_image, artifact['configDigest'])


def release_values(release):
    values = [release[key] for key in ('version', 'tag', 'revision', 'image', 'releaseUrl')]
    for artifact in release['artifacts']:
        values.extend(artifact[key] for key in ('archiveUrl', 'checksumUrl'))
    if not all(isinstance(value, str) and value for value in values):
        raise ValueError('public release metadata is incomplete')
    return values


def validate_responses(release, expected, build, installation, revision, image):
    if release != expected:
        raise ValueError('served public release differs from the source bytes')
    identity = candidate.read_json(build)
    if (type(identity.get('schemaVersion')) is not int or identity['schemaVersion'] != 1
            or identity.get('revision') != revision or identity.get('image') != image):
        raise ValueError('served build identity differs from the selected image')
    rendered = html.unescape(installation.decode('utf-8'))
    if any(value not in rendered for value in release_values(candidate.read_json(expected))):
        raise ValueError('installation documentation omits canonical release information')


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError('site qualification endpoint redirected')


def fetch(origin, path):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    with opener.open(origin + path, timeout=5) as response:
        if response.status != 200:
            raise ValueError('site qualification endpoint failed: ' + path)
        data = response.read(candidate.MAX_JSON_BYTES + 1)
        if len(data) > candidate.MAX_JSON_BYTES:
            raise ValueError('site qualification response exceeds its byte limit')
        return data


def exercise(image, artifact, revision, expected_release):
    container, network = '', ''
    try:
        network = run('docker', 'network', 'create', '--driver', 'bridge',
                      '--label', 'io.leapview.qualification=site',
                      'leapview-site-qualification-' + uuid.uuid4().hex)
        if re.fullmatch(r'[0-9a-f]{64}', network) is None:
            raise ValueError('Docker did not return a network identity')
        # Retain the ID before starting, so startup failures are cleaned up too.
        container = run('docker', 'create', '--network', network, '--read-only', '--cap-drop=ALL',
                        '--security-opt=no-new-privileges', '--pids-limit=128', '--memory=512m',
                        '--platform', artifact['platform'], '--publish', '127.0.0.1::8081',
                        '--env', 'LEAPVIEW_SITE_BASE_URL=https://leapview.dev',
                        image, '-addr=:8081', '-image-reference=' + image)
        if re.fullmatch(r'[0-9a-f]{64}', container) is None:
            raise ValueError('Docker did not return a container identity')
        run('docker', 'start', container)
        runtime = candidate.read_json(run('docker', 'inspect', container).encode())[0]
        if (runtime['HostConfig'].get('ReadonlyRootfs') is not True
                or runtime['Config'].get('User') != '65532:65532'):
            raise ValueError('site container is not read-only and nonroot')
        bindings = runtime['NetworkSettings']['Ports']['8081/tcp']
        if (len(bindings) != 1 or bindings[0].get('HostIp') != '127.0.0.1'
                or re.fullmatch(r'[0-9]{1,5}', bindings[0].get('HostPort', '')) is None
                or not 1 <= int(bindings[0]['HostPort']) <= 65535):
            raise ValueError('site port is not bound exclusively to loopback')
        origin = 'http://127.0.0.1:' + bindings[0]['HostPort']
        deadline = time.monotonic() + 30
        while True:
            try:
                fetch(origin, '/healthz')
                fetch(origin, '/readyz')
                break
            except (urllib.error.URLError, TimeoutError, ConnectionError):
                if time.monotonic() >= deadline:
                    raise ValueError('site health/readiness did not become available') from None
                time.sleep(1)
        validate_responses(fetch(origin, '/release.json'), expected_release,
                           fetch(origin, '/build.json'), fetch(origin, '/docs/installation'),
                           revision, image)
    finally:
        try:
            if re.fullmatch(r'[0-9a-f]{64}', container):
                run('docker', 'rm', '--force', container)
        finally:
            if re.fullmatch(r'[0-9a-f]{64}', network):
                run('docker', 'network', 'rm', network)


def qualify(archive, source_root, evidence_dir, image=None):
    archive, source_root, evidence_dir = Path(archive), Path(source_root), Path(evidence_dir)
    source = candidate.checkout_source(source_root)
    source_revision = source['revision']
    started = datetime.now(timezone.utc).isoformat()
    artifact = candidate.collect(archive, 'site-image', source)['artifact']
    if artifact['platform'] != native_platform():
        raise ValueError('site archive platform differs from the native runner')
    binary_evidence.check_entrypoint_config(archive, artifact, binary_evidence.SITE_ENTRYPOINTS)
    if image is not None:
        registry.image_digest(image, 'site-image')
        run('docker', 'pull', '--platform', artifact['platform'], image)
        selected = image
    else:
        with tarfile.open(archive, 'r:*') as contents:
            member = contents.getmember('manifest.json')
            manifest = candidate.read_json(contents.extractfile(member).read(candidate.MAX_JSON_BYTES + 1))
        tags = manifest[0].get('RepoTags')
        expected_tag = 'leapview-site:' + source_revision[:12]
        if tags != [expected_tag]:
            raise ValueError('site archive tag is outside its candidate namespace')
        run('docker', 'load', '--input', str(archive))
        selected = expected_tag
    loaded = candidate.read_json(run('docker', 'image', 'inspect', selected).encode())[0]
    validate_image(loaded, artifact, source_revision)
    selected = image if image is not None else loaded['Id']
    verify_saved_image_config(selected, artifact)
    release = (source_root / 'docs/public-release.json').read_bytes()
    exercise(selected, artifact, source_revision, release)
    if (candidate.digest_file(archive) != artifact['sha256']
            or candidate.checkout_source(source_root) != source):
        raise ValueError('candidate inputs changed during site qualification')
    report = {'schemaVersion': 1, 'result': 'success', 'sourceRevision': source_revision,
              'platform': artifact['platform'], 'image': selected,
              'archiveSHA256': artifact['sha256'], 'releaseAdmission': False,
              'startedAt': started, 'completedAt': datetime.now(timezone.utc).isoformat(),
              'checks': {name: True for name in CHECKS}}
    evidence_dir.mkdir(parents=True, exist_ok=True)
    output = evidence_dir / 'site-qualification-report.json'
    output.write_text(json.dumps(report, indent=2) + '\n')
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', type=Path, required=True)
    parser.add_argument('--source-root', type=Path, required=True)
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--image')
    args = parser.parse_args()
    os.umask(0o077)
    try:
        report = qualify(args.archive, args.source_root, args.evidence_dir, args.image)
        print(json.dumps(report, sort_keys=True))
    except (OSError, ValueError, KeyError, TypeError, tarfile.TarError,
            subprocess.SubprocessError) as exc:
        raise SystemExit('Nix site qualification rejected: ' + str(exc)) from exc


if __name__ == '__main__':
    main()

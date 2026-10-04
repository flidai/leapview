#!/usr/bin/env python3
"""Promote a qualified multi-platform release without moving stable aliases backward."""

import argparse
from dataclasses import dataclass
import hashlib
import json
import re
import subprocess
import sys

from qualification_release import valid_tag

IMAGE_INDEX_TYPES = {
    'application/vnd.oci.image.index.v1+json',
    'application/vnd.docker.distribution.manifest.list.v2+json',
}
IMAGE_MANIFEST_TYPES = {
    'application/vnd.oci.image.manifest.v1+json',
    'application/vnd.docker.distribution.manifest.v2+json',
}
PLATFORMS = {'linux/amd64', 'linux/arm64'}
SHA256 = re.compile(r'sha256:[0-9a-f]{64}')
REVISION = re.compile(r'[0-9a-f]{40}')
VERSION_LABEL = 'org.opencontainers.image.version'


class PromotionError(ValueError):
    """The registry state is unsafe or could not be read completely."""


@dataclass(frozen=True)
class ImageManifest:
    digest: str
    platform_digests: dict[str, str]


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise PromotionError(f'duplicate JSON key: {key}')
        result[key] = value
    return result


def _json_document(data, description):
    try:
        value = json.loads(data, object_pairs_hook=_unique_object)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise PromotionError(f'malformed {description} JSON') from error
    if not isinstance(value, dict):
        raise PromotionError(f'{description} must be a JSON object')
    return value


def _confirmed_missing(stderr, reference):
    """Recognize only Buildx's explicit missing-manifest diagnostics."""
    lines = [line.strip() for line in stderr.splitlines() if line.strip()]
    if len(lines) != 1:
        return False
    message = lines[0]
    if message.startswith('ERROR: '):
        message = message[len('ERROR: '):]
    return message in {
        f'no such manifest: {reference}',
        f'{reference}: not found',
        f'{reference}: manifest unknown',
    }


def _platform_manifests(document):
    if (type(document.get('schemaVersion')) is not int or document['schemaVersion'] != 2
            or document.get('mediaType') not in IMAGE_INDEX_TYPES):
        raise PromotionError('expected an OCI image index or Docker manifest list')
    descriptors = document.get('manifests')
    if not isinstance(descriptors, list):
        raise PromotionError('image index has no manifest list')

    platforms = {}
    for descriptor in descriptors:
        if not isinstance(descriptor, dict) or descriptor.get('mediaType') not in IMAGE_MANIFEST_TYPES:
            raise PromotionError('image index contains an invalid manifest descriptor')
        digest = descriptor.get('digest')
        if not isinstance(digest, str) or not SHA256.fullmatch(digest):
            raise PromotionError('image index contains an invalid manifest digest')
        if type(descriptor.get('size')) is not int or descriptor['size'] <= 0:
            raise PromotionError('image index contains an invalid manifest size')
        platform = descriptor.get('platform')
        if not isinstance(platform, dict):
            raise PromotionError('image index contains a manifest without a platform')
        if platform == {'os': 'unknown', 'architecture': 'unknown'}:
            annotations = descriptor.get('annotations')
            if (not isinstance(annotations, dict)
                    or annotations.get('vnd.docker.reference.type') != 'attestation-manifest'
                    or not isinstance(annotations.get('vnd.docker.reference.digest'), str)
                    or not SHA256.fullmatch(annotations['vnd.docker.reference.digest'])):
                raise PromotionError('image index contains an unrecognized unknown platform')
            continue
        required = {'os', 'architecture'}
        optional = {'variant', 'os.version', 'os.features', 'features'}
        if (not required <= set(platform) or not set(platform) <= required | optional
                or platform.get('os') != 'linux'
                or not isinstance(platform.get('architecture'), str)):
            raise PromotionError('image index contains an unsupported platform descriptor')
        for field in ('variant', 'os.version'):
            if field in platform and (not isinstance(platform[field], str) or not platform[field]):
                raise PromotionError('image index contains invalid optional platform metadata')
        for field in ('os.features', 'features'):
            if field in platform and (not isinstance(platform[field], list)
                                      or any(not isinstance(feature, str) or not feature
                                             for feature in platform[field])):
                raise PromotionError('image index contains invalid optional platform metadata')
        name = f"linux/{platform.get('architecture')}"
        if name not in PLATFORMS or name in platforms:
            raise PromotionError('image index contains an unexpected or duplicate platform')
        platforms[name] = digest
    if set(platforms) != PLATFORMS:
        raise PromotionError('image index must contain exactly linux/amd64 and linux/arm64')
    return platforms


def stable_core(version):
    if not isinstance(version, str) or not valid_tag('v' + version):
        raise PromotionError(f'invalid release version: {version!r}')
    core = version.split('+', 1)[0]
    if '-' in core:
        return None
    return tuple(int(part) for part in core.split('.'))


def plan_tags(version, revision, candidate_digest, *, version_digest,
              latest_version, major_minor_version):
    """Return only tags that can safely move to this qualified candidate."""
    core = stable_core(version)
    if not isinstance(revision, str) or not REVISION.fullmatch(revision):
        raise PromotionError('release revision must be a full lowercase git SHA')
    if not isinstance(candidate_digest, str) or not SHA256.fullmatch(candidate_digest):
        raise PromotionError('candidate digest must be a lowercase sha256 digest')

    if version_digest is not None:
        if not isinstance(version_digest, str) or not SHA256.fullmatch(version_digest):
            raise PromotionError('existing immutable version tag has an invalid digest')
        if version_digest != candidate_digest:
            raise PromotionError('existing immutable version tag points to a different digest')

    # The revision alias remains qualified and may be retargeted when the same
    # commit is released under a different version identity.
    tags = [f'sha-{revision[:7]}']
    if version_digest is None:
        tags.append(version)

    if core is None:
        return tags

    aliases = [('latest', latest_version, None),
               ('.'.join(str(part) for part in core[:2]), major_minor_version,
                tuple(core[:2]))]
    for alias, current_version, expected_prefix in aliases:
        if current_version is None:
            tags.append(alias)
            continue
        current_core = stable_core(current_version)
        if current_core is None:
            raise PromotionError(f'{alias} points to a prerelease image')
        if expected_prefix is not None and current_core[:2] != expected_prefix:
            raise PromotionError(f'{alias} points to version {current_version} from another major/minor')
        if core > current_core:
            tags.append(alias)
    return tags


class DockerRegistry:
    """Read registry manifests and configs through the authenticated Buildx client."""

    def __init__(self, run=subprocess.run):
        self.run = run

    def _command(self, command, description):
        try:
            result = self.run(command, capture_output=True)
        except OSError as error:
            raise PromotionError(f'could not {description}: {error}') from error
        return result

    def manifest(self, reference, *, missing_ok=False):
        result = self._command(['docker', 'buildx', 'imagetools', 'inspect', '--raw', reference],
                               f'inspect manifest {reference}')
        if result.returncode:
            stderr = result.stderr.decode('utf-8', errors='replace')
            if missing_ok and _confirmed_missing(stderr, reference):
                return None
            raise PromotionError(f'could not inspect manifest {reference}: {stderr.strip()}')
        document = _json_document(result.stdout, f'manifest for {reference}')
        digest = 'sha256:' + hashlib.sha256(result.stdout).hexdigest()
        return ImageManifest(digest, _platform_manifests(document))

    def image_version(self, repository, platform, digest):
        reference = f'{repository}@{digest}'
        result = self._command([
            'docker', 'buildx', 'imagetools', 'inspect',
            '--format', '{{json .Image}}', reference,
        ], f'inspect image config {reference}')
        if result.returncode:
            stderr = result.stderr.decode('utf-8', errors='replace')
            raise PromotionError(f'could not inspect image config {reference}: {stderr.strip()}')
        image = _json_document(result.stdout, f'image config for {reference}')
        expected_architecture = platform.split('/', 1)[1]
        if image.get('os') != 'linux' or image.get('architecture') != expected_architecture:
            raise PromotionError(f'image config {reference} does not match {platform}')
        config = image.get('config')
        labels = config.get('Labels') if isinstance(config, dict) else None
        version = labels.get(VERSION_LABEL) if isinstance(labels, dict) else None
        if not isinstance(version, str):
            raise PromotionError(f'image config {reference} is missing {VERSION_LABEL}')
        stable_core(version)
        return version

    def manifest_version(self, repository, manifest):
        versions = {
            platform: self.image_version(repository, platform, digest)
            for platform, digest in sorted(manifest.platform_digests.items())
        }
        if len(set(versions.values())) != 1:
            raise PromotionError('linux/amd64 and linux/arm64 image versions differ')
        return next(iter(versions.values()))

    def create(self, repository, tags, candidate_reference):
        if not tags:
            return
        command = ['docker', 'buildx', 'imagetools', 'create']
        for tag in tags:
            command.extend(['--tag', f'{repository}:{tag}'])
        command.append(candidate_reference)
        result = self._command(command, 'publish qualified image tags')
        if result.returncode:
            stderr = result.stderr.decode('utf-8', errors='replace')
            raise PromotionError(f'could not publish qualified image tags: {stderr.strip()}')


def promote(registry, repository, candidate_reference, expected_digest, version, revision):
    if not isinstance(repository, str) or not repository:
        raise PromotionError('image repository is required')
    if not isinstance(expected_digest, str) or not SHA256.fullmatch(expected_digest):
        raise PromotionError('candidate digest must be a lowercase sha256 digest')
    if not isinstance(revision, str) or not REVISION.fullmatch(revision):
        raise PromotionError('release revision must be a full lowercase git SHA')
    stable_core(version)
    if candidate_reference != f'{repository}@{expected_digest}':
        raise PromotionError('candidate reference must name the admitted immutable digest')

    candidate = registry.manifest(candidate_reference)
    if candidate.digest != expected_digest:
        raise PromotionError('admitted candidate reference resolved to a different digest')
    if registry.manifest_version(repository, candidate) != version:
        raise PromotionError('candidate platform labels do not match the release version')

    version_tag = registry.manifest(f'{repository}:{version}', missing_ok=True)
    if version_tag is not None and version_tag.digest != candidate.digest:
        raise PromotionError('existing immutable version tag points to a different digest')

    latest_version = None
    major_minor_version = None
    core = stable_core(version)
    if core is not None:
        latest = registry.manifest(f'{repository}:latest', missing_ok=True)
        major_minor = '.'.join(str(part) for part in core[:2])
        minor = registry.manifest(f'{repository}:{major_minor}', missing_ok=True)
        if latest is not None:
            latest_version = registry.manifest_version(repository, latest)
        if minor is not None:
            major_minor_version = registry.manifest_version(repository, minor)

    tags = plan_tags(version, revision, candidate.digest,
                     version_digest=version_tag.digest if version_tag else None,
                     latest_version=latest_version,
                     major_minor_version=major_minor_version)
    registry.create(repository, tags, candidate_reference)
    return tags


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repository', required=True)
    parser.add_argument('--candidate-reference', required=True)
    parser.add_argument('--candidate-digest', required=True)
    parser.add_argument('--version', required=True)
    parser.add_argument('--revision', required=True)
    args = parser.parse_args(argv)
    try:
        tags = promote(DockerRegistry(), args.repository, args.candidate_reference,
                       args.candidate_digest, args.version, args.revision)
    except PromotionError as error:
        print(f'release image promotion failed: {error}', file=sys.stderr)
        return 1
    if tags:
        print('Published release image tags: ' + ', '.join(tags))
    else:
        print('Qualified image tags already point to the admitted candidate.')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())

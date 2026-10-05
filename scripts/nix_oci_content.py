#!/usr/bin/env python3
"""Bind freshly verified Nix image candidates to a bounded OCI layout; no release authority."""

import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import shutil

import nix_candidate_manifest as candidate

MANIFEST = 'application/vnd.oci.image.manifest.v1+json'
INDEX = 'application/vnd.oci.image.index.v1+json'
CONFIG = 'application/vnd.oci.image.config.v1+json'
LAYER = 'application/vnd.oci.image.layer.v1.tar'
GZIP = LAYER + '+gzip'
MAX_LAYER_BYTES = 8 * 1024**3
MAX_LAYOUT_BYTES = 16 * 1024**3


def export_layout(archive, record, root):
    """Export verified Docker archive bytes without config normalization or rebuilding."""
    if root.exists():
        raise ValueError('refusing to overwrite an OCI layout')
    root.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive, 'r|*') as stream:
        for entry in stream:
            if entry.name == 'manifest.json':
                manifest = candidate.read_json(stream.extractfile(entry).read(candidate.MAX_JSON_BYTES + 1))[0]
                break
        else:
            raise ValueError('missing Docker archive manifest')
    wanted = {manifest['Config']: (CONFIG, record['artifact']['configDigest'])}
    wanted.update({name: (LAYER, digest) for name, digest in
                   zip(manifest['Layers'], record['artifact']['layerDiffIDs'])})
    with tempfile.TemporaryDirectory(prefix='.oci-export-', dir=root.parent) as temporary:
        staging = Path(temporary) / 'layout'
        blobs = staging / 'blobs/sha256'
        blobs.mkdir(parents=True)
        descriptors, total = {}, 0
        with tarfile.open(archive, 'r|*') as stream:
            for entry in stream:
                if entry.name not in wanted:
                    continue
                media, expected = wanted[entry.name]
                limit = candidate.MAX_JSON_BYTES if media == CONFIG else MAX_LAYER_BYTES
                if not entry.isfile() or entry.size > limit:
                    raise ValueError('invalid Docker archive content for OCI export')
                temporary_blob = staging / 'pending-blob'
                with stream.extractfile(entry) as source, temporary_blob.open('xb') as target:
                    shutil.copyfileobj(source, target, length=1024 * 1024)
                actual = candidate.digest_file(temporary_blob)
                total += entry.size
                if actual != expected or total > MAX_LAYOUT_BYTES or entry.name in descriptors:
                    raise ValueError('Docker archive differs from verified candidate during OCI export')
                temporary_blob.replace(blobs / expected[7:])
                descriptors[entry.name] = {'mediaType': media, 'digest': actual, 'size': entry.size}
        if set(descriptors) != set(wanted) or candidate.digest_file(archive) != record['artifact']['sha256']:
            raise ValueError('Docker archive differs from verified candidate during OCI export')
        image = {'schemaVersion': 2, 'mediaType': MANIFEST,
                 'config': descriptors[manifest['Config']],
                 'layers': [descriptors[name] for name in manifest['Layers']]}
        content = candidate.canonical_bytes(image)
        if len(content) > candidate.MAX_JSON_BYTES:
            raise ValueError('exported OCI manifest exceeds JSON byte limit')
        digest = candidate.digest_bytes(content)
        (blobs / digest[7:]).write_bytes(content)
        (staging / 'oci-layout').write_text('{"imageLayoutVersion":"1.0.0"}\n')
        (staging / 'index.json').write_bytes(candidate.canonical_bytes({
            'schemaVersion': 2, 'manifests': [{'mediaType': MANIFEST, 'digest': digest, 'size': len(content),
                                            'annotations': {'org.opencontainers.image.ref.name': 'candidate'}}]}))
        staging.rename(root)
    return digest


def local_file(root, relative):
    path = root
    for part in Path(relative).parts:
        path /= part
        if path.is_symlink():
            raise ValueError('OCI content must not use symlinks')
    if not path.is_file():
        raise ValueError('missing regular OCI file: ' + relative)
    return path


def stream_digest(stream, limit):
    size, digest = 0, hashlib.sha256()
    while chunk := stream.read(1024 * 1024):
        size += len(chunk)
        if size > limit:
            raise ValueError('OCI content exceeds qualification byte limit')
        digest.update(chunk)
    return 'sha256:' + digest.hexdigest(), size


class Layout:
    def __init__(self, root):
        self.root = root
        self.bytes = 0
        layout = candidate.read_json_file(local_file(root, 'oci-layout'))
        if layout != {'imageLayoutVersion': '1.0.0'}:
            raise ValueError('unsupported OCI layout version')

    def blob(self, descriptor, media_types, *, limit=candidate.MAX_JSON_BYTES):
        if (not isinstance(descriptor, dict) or descriptor.get('mediaType') not in media_types
                or not isinstance(descriptor.get('digest'), str)
                or not candidate.SHA256.fullmatch(descriptor['digest'])
                or type(descriptor.get('size')) is not int or not 0 <= descriptor['size'] <= limit
                or 'urls' in descriptor or 'data' in descriptor):
            raise ValueError('invalid or unsupported OCI descriptor')
        path = local_file(self.root, 'blobs/sha256/' + descriptor['digest'][7:])
        with path.open('rb') as stream:
            digest, size = stream_digest(stream, limit)
        self.bytes += size
        if self.bytes > MAX_LAYOUT_BYTES:
            raise ValueError('OCI layout exceeds qualification byte limit')
        if (digest, size) != (descriptor['digest'], descriptor['size']):
            raise ValueError('OCI descriptor hash or size mismatch')
        return path

    def document(self, descriptor, media_types):
        value = candidate.read_json_file(self.blob(descriptor, media_types))
        if (not isinstance(value, dict) or type(value.get('schemaVersion')) is not int or value['schemaVersion'] != 2
                or value.get('mediaType') != descriptor['mediaType']
                or 'subject' in value or 'artifactType' in value):
            raise ValueError('expected an OCI image document')
        return value

    def image(self, descriptor, record):
        manifest = self.document(descriptor, {MANIFEST})
        artifact = record['artifact']
        config_descriptor = manifest['config']
        config = candidate.read_json_file(self.blob(config_descriptor, {CONFIG}))
        if config_descriptor['digest'] != artifact['configDigest']:
            raise ValueError('OCI config differs from verified candidate')
        platform = config['os'] + '/' + config['architecture']
        if (platform != artifact['platform'] or config.get('variant')
                or config['rootfs'] != {'type': 'layers', 'diff_ids': artifact['layerDiffIDs']}):
            raise ValueError('OCI config differs from verified candidate')
        if 'platform' in descriptor and descriptor['platform'] != {
                'os': config['os'], 'architecture': config['architecture']}:
            raise ValueError('OCI descriptor platform differs from image config')
        layers = manifest['layers']
        if not isinstance(layers, list) or len(layers) != len(artifact['layerDiffIDs']):
            raise ValueError('OCI layer count differs from verified candidate')
        bound = []
        for layer, expected in zip(layers, artifact['layerDiffIDs']):
            path = self.blob(layer, {LAYER, GZIP}, limit=MAX_LAYER_BYTES)
            opener = gzip.open if layer['mediaType'] == GZIP else Path.open
            with opener(path, 'rb') as stream:
                diff_id, size = stream_digest(stream, MAX_LAYER_BYTES)
            self.bytes += size
            if self.bytes > MAX_LAYOUT_BYTES:
                raise ValueError('OCI layout exceeds qualification byte limit')
            if diff_id != expected:
                raise ValueError('OCI ordered layer content differs from verified candidate')
            bound.append({'digest': layer['digest'], 'size': layer['size'],
                          'mediaType': layer['mediaType'], 'diffID': diff_id})
        return {'platform': platform, 'manifestDigest': descriptor['digest'],
                'configDigest': config_descriptor['digest'], 'layers': bound,
                'candidateDigest': record['candidateDigest']}


def bind(root, manifest_digest, records, platforms):
    """Internal adapter: records must first be recomputed by candidate.verify."""
    expected = set(platforms)
    if not expected or len(expected) != len(platforms) or not expected <= candidate.PLATFORMS:
        raise ValueError('declare a unique supported platform set')
    by_platform = {record['artifact']['platform']: record for record in records}
    if len(by_platform) != len(records) or set(by_platform) != expected:
        raise ValueError('candidate platform set differs from declared platform set')
    first = records[0]
    identity = {'kind': first['artifact']['kind'], 'version': first['artifact']['version'],
                'source': first['source']}
    for record in records:
        actual = {'kind': record['artifact']['kind'], 'version': record['artifact']['version'],
                  'source': record['source']}
        if (actual['kind'] not in {'application-image', 'site-image'}
                or candidate.canonical_bytes(actual) != candidate.canonical_bytes(identity)
                or record['releaseAdmission'] is not False):
            raise ValueError('candidate source, output kind or version differs across platforms')
    layout = Layout(root)
    catalog = candidate.read_json_file(local_file(root, 'index.json'))
    if (not isinstance(catalog, dict) or type(catalog.get('schemaVersion')) is not int or catalog['schemaVersion'] != 2
            or not isinstance(catalog.get('manifests'), list) or len(catalog['manifests']) != 1):
        raise ValueError('OCI layout must select exactly one root descriptor')
    descriptor = catalog['manifests'][0]
    if not isinstance(descriptor, dict) or descriptor.get('digest') != manifest_digest:
        raise ValueError('OCI root differs from requested manifest digest')
    root_document = layout.document(descriptor, {MANIFEST, INDEX})
    images = []
    if descriptor['mediaType'] == MANIFEST:
        if len(records) != 1:
            raise ValueError('single OCI manifest cannot satisfy a platform matrix')
        images.append(layout.image(descriptor, first))
    else:
        manifests = root_document['manifests']
        if not isinstance(manifests, list) or len(manifests) != len(expected):
            raise ValueError('OCI index differs from declared platform set')
        seen = set()
        for image in manifests:
            platform = image['platform']
            name = platform['os'] + '/' + platform['architecture']
            if (set(platform) != {'os', 'architecture'} or name not in expected or name in seen):
                raise ValueError('OCI index platform is duplicated, unexpected or qualified')
            seen.add(name)
            images.append(layout.image(image, by_platform[name]))
    result = {'schemaVersion': 1, **identity,
              'oci': {'digest': manifest_digest, 'mediaType': descriptor['mediaType']},
              'platforms': sorted(images, key=lambda image: image['platform']),
              'requiredReleaseEvidence': sorted(set().union(*(r['requiredReleaseEvidence'] for r in records))),
              'releaseAdmission': False}
    result['bindingDigest'] = candidate.digest_bytes(
        b'leapview/nix-oci-content/v1\n' + candidate.canonical_bytes(result))
    if len(json.dumps(result, indent=2).encode()) + 1 > candidate.MAX_JSON_BYTES:
        raise ValueError('OCI binding exceeds JSON byte limit')
    return result


def verify(record, expected):
    if candidate.canonical_bytes(record) != candidate.canonical_bytes(expected):
        raise ValueError('OCI binding differs from current candidate and layout content')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=['export', 'bind'])
    parser.add_argument('--layout', type=Path, required=True)
    parser.add_argument('--manifest-digest')
    parser.add_argument('--platform', action='append', required=True)
    parser.add_argument('--binary-verifier', type=Path)
    parser.add_argument('--kind', choices=['application-image', 'site-image'], required=True)
    parser.add_argument('--candidate', nargs=3, action='append', required=True,
                        metavar=('ARCHIVE', 'MANIFEST', 'RUNTIME_EVIDENCE'))
    output = parser.add_mutually_exclusive_group()
    output.add_argument('--output', type=Path)
    output.add_argument('--verify', type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        source = candidate.checkout_source(candidate.ROOT)
        records = [candidate.verify(candidate.read_json_file(Path(manifest)), Path(archive), source,
                                    kind=args.kind, runtime_dir=Path(runtime),
                                    go_dir=Path(runtime) / 'go' if args.binary_verifier is not None else None,
                                    binary_verifier=args.binary_verifier)
                   for archive, manifest, runtime in args.candidate]
        if args.operation == 'export':
            if len(records) != 1 or args.platform != [records[0]['artifact']['platform']] or args.output or args.verify or args.manifest_digest:
                raise ValueError('export requires one matching candidate/platform and no binding output or digest')
            digest = export_layout(Path(args.candidate[0][0]), records[0], args.layout)
            print(json.dumps({'manifestDigest': digest, 'releaseAdmission': False}))
            return
        if not args.manifest_digest or not (args.output or args.verify):
            raise ValueError('bind requires a manifest digest and output or verify record')
        result = bind(args.layout, args.manifest_digest, records, args.platform)
        if args.verify:
            verify(candidate.read_json_file(args.verify), result)
        else:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            with args.output.open('x') as stream:
                stream.write(json.dumps(result, indent=2) + '\n')
        print(json.dumps({'bindingDigest': result['bindingDigest'], 'oci': result['oci'],
                          'releaseAdmission': False}))
    except (ValueError, KeyError, TypeError, OSError, EOFError, tarfile.TarError,
            subprocess.CalledProcessError) as error:
        raise SystemExit('OCI content binding rejected: ' + str(error)) from error


if __name__ == '__main__':
    main()

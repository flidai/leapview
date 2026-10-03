#!/usr/bin/env python3
"""Bind Go reports to exact application-image and CLI-archive entrypoints without execution."""

import argparse
import hashlib
import os
from pathlib import Path, PurePosixPath
import subprocess
import tarfile
import tempfile

import nix_candidate_manifest as candidate

ENTRYPOINTS = [
    {'id': 'leapview', 'path': 'usr/local/bin/leapview',
     'package': 'github.com/flidai/leapview/cmd/leapview'},
    {'id': 'leapviewctl', 'path': 'usr/local/libexec/leapviewctl',
     'package': 'github.com/flidai/leapview/cmd/leapviewctl'},
    {'id': 'deployment-leapviewctl', 'path': 'usr/local/share/leapview/deployment/leapviewctl',
     'package': 'github.com/flidai/leapview/cmd/leapviewctl'},
]
CLI_ENTRYPOINTS = [{'id': 'leapviewctl', 'path': 'leapviewctl',
                    'package': 'github.com/flidai/leapview/cmd/leapviewctl'}]


def entrypoints(artifact):
    if artifact['kind'] == 'application-image':
        return ENTRYPOINTS
    if artifact['kind'] == 'cli-archive':
        return CLI_ENTRYPOINTS
    raise ValueError('unsupported Go archive kind')


MAX_BINARY_BYTES = 256 * 1024**2
MAX_REPORT_BYTES = 32 * 1024**2
MAX_LAYER_BYTES = 8 * 1024**3
MAX_ENTRIES = 200_000
MAX_EXTENDED_HEADER_BYTES = 1024**2


class LayerTarInfo(tarfile.TarInfo):
    """Reject metadata that Python and Docker's Go reader interpret differently."""

    def _apply_pax_info(self, headers, encoding, errors):
        # Bounded layer entries fit the base tar size field. In particular, Go
        # ignores an empty PAX size while Python uses zero and reads file payload
        # as headers. Python also applies sparse names without a sparse map.
        if 'size' in headers or any(key.startswith('GNU.sparse.') for key in headers):
            raise ValueError('unsupported size or sparse image layer extended header')
        super()._apply_pax_info(headers, encoding, errors)

    def _proc_member(self, archive):
        # Inspect raw metadata headers before tarfile recursively consumes them.
        # Docker ignores global PAX headers; Python applies them to later files.
        if self.type in (tarfile.XGLTYPE, tarfile.SOLARIS_XHDTYPE, tarfile.GNUTYPE_SPARSE):
            raise ValueError('unsupported image layer extended header')
        extensions = getattr(archive, '_leapview_extensions', ())
        if self.type in (tarfile.XHDTYPE, tarfile.GNUTYPE_LONGNAME, tarfile.GNUTYPE_LONGLINK):
            # Python applies outer headers last; Go gives GNU names precedence
            # over PAX and applies repeated headers in the opposite order.
            if (self.type in extensions or (extensions and
                    (self.type == tarfile.XHDTYPE or tarfile.XHDTYPE in extensions))):
                raise ValueError('ambiguous image layer extended headers')
            if not 0 < self.size <= MAX_EXTENDED_HEADER_BYTES:
                raise ValueError('image layer extended header exceeds byte limit')
            archive._leapview_extensions = (*extensions, self.type)
            try:
                result = super()._proc_member(archive)
            finally:
                archive._leapview_extensions = extensions
        else:
            result = super()._proc_member(archive)
        if result.sparse is not None:
            raise ValueError('unsupported sparse image layer extended header')
        return result


class HashedLayer:
    def __init__(self, source):
        self.source = source
        self.digest = hashlib.sha256()
        self.size = 0

    def read(self, size=-1):
        data = self.source.read(size)
        self.size += len(data)
        if self.size > MAX_LAYER_BYTES:
            raise ValueError('image layer exceeds extraction byte limit')
        self.digest.update(data)
        return data


def member_path(member):
    # dockerTools emits absolute container-root paths for Nix store layers.
    # Canonicalize that one root marker; never extract it as a host path.
    name = member.name.removeprefix('./').removeprefix('/')
    if member.isdir():
        name = name.rstrip('/')
        if name in {'', '.'}:
            return None
    return candidate.safe_path(name)


def affects_entrypoint(name):
    return any(entry['path'] == name or entry['path'].startswith(name + '/') for entry in ENTRYPOINTS)


def layer_names(archive, artifact):
    with tarfile.open(archive, 'r|*') as outer:
        for member in outer:
            if member.name.removeprefix('./') != 'manifest.json':
                continue
            if not member.isfile() or member.size > candidate.MAX_JSON_BYTES:
                raise ValueError('invalid Docker archive manifest')
            manifest = candidate.read_json(outer.extractfile(member).read(candidate.MAX_JSON_BYTES + 1))
            if not isinstance(manifest, list) or len(manifest) != 1:
                raise ValueError('expected exactly one application image')
            image = manifest[0]
            names = [candidate.safe_path(name) for name in image['Layers']]
            if (image['Config'] != artifact['configDigest'][7:] + '.json'
                    or len(names) != len(artifact['layerDiffIDs']) or not 0 < len(names) <= 256
                    or len(names) != len(set(names))):
                raise ValueError('archive layer identity differs from candidate')
            return names
    raise ValueError('missing Docker archive manifest')


def check_entrypoint_config(archive, artifact):
    expected = artifact['configDigest'][7:] + '.json'
    with tarfile.open(archive, 'r|*') as outer:
        for member in outer:
            if member.name.removeprefix('./') != expected:
                continue
            if not member.isfile() or not 0 <= member.size <= candidate.MAX_JSON_BYTES:
                raise ValueError('invalid image entrypoint configuration')
            data = outer.extractfile(member).read(candidate.MAX_JSON_BYTES + 1)
            if candidate.digest_bytes(data) != artifact['configDigest']:
                raise ValueError('entrypoint configuration differs from candidate')
            config = candidate.read_json(data)['config']
            if not isinstance(config, dict):
                raise ValueError('image entrypoint configuration is malformed')
            environment = config.get('Env')
            if not isinstance(environment, list) or not all(isinstance(value, str) for value in environment):
                raise ValueError('image environment is missing or malformed')
            paths = [value[5:] for value in environment if value.startswith('PATH=')]
            healthcheck = config.get('Healthcheck')
            executable = '/' + ENTRYPOINTS[0]['path']
            if (config.get('Entrypoint') != [executable]
                    or len(paths) != 1 or paths[0].split(':')[0] != '/usr/local/bin'
                    or not isinstance(healthcheck, dict)
                    or healthcheck.get('Test') != ['CMD', executable, 'healthcheck']):
                raise ValueError('image redirects a declared Go entrypoint')
            return
    raise ValueError('missing image entrypoint configuration')


def extract_image(archive, artifact, destination):
    """Read nested tars; write only fixed filenames, never tar paths or links.

    Regular overlays use manifest order even if outer members are reordered.
    Any link/redirection or whiteout affecting an entrypoint/ancestor is rejected,
    including one superseded by a later layer. Unrelated Nix store links are allowed.
    """
    archive, destination = Path(archive), Path(destination)
    if (archive.is_symlink() or not archive.is_file() or artifact['kind'] != 'application-image'
            or candidate.digest_file(archive) != artifact['sha256']):
        raise ValueError('Go extraction requires the exact regular application archive')
    names = layer_names(archive, artifact)
    check_entrypoint_config(archive, artifact)
    wanted = {entry['path']: entry for entry in ENTRYPOINTS}
    indexes = {name: index for index, name in enumerate(names)}
    selected, seen_layers, count, total = {}, set(), 0, 0
    with tarfile.open(archive, 'r|*') as outer:
        for member in outer:
            name = member.name.removeprefix('./')
            if name not in indexes:
                continue
            if name in seen_layers or not member.isfile() or not 0 <= member.size <= MAX_LAYER_BYTES:
                raise ValueError('duplicate, redirected or oversized image layer')
            seen_layers.add(name)
            index = indexes[name]
            source = HashedLayer(outer.extractfile(member))
            seen_paths = set()
            with tarfile.open(fileobj=source, mode='r|', tarinfo=LayerTarInfo) as layer:
                for entry in layer:
                    count += 1
                    total += entry.size
                    if entry.size < 0 or count > MAX_ENTRIES or total > MAX_LAYER_BYTES:
                        raise ValueError('image content exceeds extraction limits')
                    path = member_path(entry)
                    if path is None:
                        continue
                    if path in seen_paths:
                        raise ValueError('duplicate image layer path: ' + path)
                    seen_paths.add(path)
                    leaf = PurePosixPath(path).name
                    if leaf.startswith('.wh.'):
                        parent = str(PurePosixPath(path).parent)
                        deleted = parent if leaf == '.wh..wh..opq' else str(
                            PurePosixPath(path).with_name(leaf[4:]))
                        if deleted == '.' or affects_entrypoint(deleted):
                            raise ValueError('whiteout affects a declared Go entrypoint')
                    if affects_entrypoint(path) and path not in wanted and not entry.isdir():
                        raise ValueError('entrypoint ancestor is not a directory: ' + path)
                    if path not in wanted:
                        continue
                    if (not entry.isfile() or not entry.mode & 0o111 or entry.mode & 0o6000
                            or not 0 < entry.size <= MAX_BINARY_BYTES):
                        raise ValueError('Go entrypoint must be a bounded regular executable: ' + path)
                    data = layer.extractfile(entry).read(MAX_BINARY_BYTES + 1)
                    if len(data) != entry.size:
                        raise ValueError('truncated Go entrypoint')
                    if index >= selected.get(path, -1):
                        target = destination / wanted[path]['id']
                        with target.open('wb') as output:
                            output.write(data)
                        target.chmod(0o600)
                        selected[path] = index
            while source.read(1024 * 1024):
                pass
            if 'sha256:' + source.digest.hexdigest() != artifact['layerDiffIDs'][index]:
                raise ValueError('extracted layer differs from candidate content')
    if seen_layers != set(names) or set(selected) != set(wanted):
        raise ValueError('declared Go entrypoint or image layer is missing')
    result = {entry['id']: destination / entry['id'] for entry in ENTRYPOINTS}
    if candidate.digest_file(result['leapviewctl']) != candidate.digest_file(result['deployment-leapviewctl']):
        raise ValueError('shipped controller copies differ')
    if candidate.digest_file(archive) != artifact['sha256']:
        raise ValueError('archive changed during extraction')
    return result


def extract(archive, artifact, destination):
    entries = entrypoints(artifact)
    if artifact['kind'] == 'application-image':
        return extract_image(archive, artifact, destination)
    archive, destination = Path(archive), Path(destination)
    if (archive.is_symlink() or not archive.is_file()
            or candidate.digest_file(archive) != artifact['sha256']):
        raise ValueError('Go extraction requires the exact regular CLI archive')
    # The standalone controller export contains exactly one executable at the
    # archive root. Never materialize archive names or links on the host.
    target = destination / entries[0]['id']
    count = 0
    with tarfile.open(archive, 'r|*', tarinfo=LayerTarInfo) as content:
        for member in content:
            count += 1
            if (count != 1 or member.name != entries[0]['path'] or not member.isfile()
                    or not member.mode & 0o111 or member.mode & 0o6000
                    or not 0 < member.size <= MAX_BINARY_BYTES):
                raise ValueError('CLI archive must contain exactly one bounded regular executable')
            data = content.extractfile(member).read(MAX_BINARY_BYTES + 1)
            if len(data) != member.size:
                raise ValueError('truncated CLI executable')
            target.write_bytes(data)
            target.chmod(0o600)
    if count != 1 or candidate.digest_file(archive) != artifact['sha256']:
        raise ValueError('CLI archive is empty or changed during extraction')
    return {entries[0]['id']: target}


def run_verifier(verifier, binary, entry, platform, directory, *, offline):
    if not Path(verifier).is_absolute():
        raise ValueError('protected verifier requires an absolute executable path')
    args = [str(verifier), '-root', str(candidate.ROOT), '-binary', str(binary),
            '-binary-package', entry['package'], '-binary-platform', platform,
            '-binary-evidence', str(directory)]
    if offline:
        args.append('-verify-binary-evidence')
    try:
        subprocess.run(args, check=True, timeout=120 if offline else 900,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except (OSError, subprocess.SubprocessError):
        raise ValueError('protected Go binary ' + ('verification' if offline else 'scan') +
                         ' failed for ' + entry['path']) from None


def scan(archive, artifact, directory, verifier):
    directory = Path(directory)
    directory.mkdir(mode=0o700)
    with tempfile.TemporaryDirectory(prefix='leapview-archive-go-') as temporary:
        binaries = extract(archive, artifact, Path(temporary))
        for entry in entrypoints(artifact):
            run_verifier(verifier, binaries[entry['id']], entry, artifact['platform'],
                         directory / entry['id'], offline=False)


def regular_report(root, name, limit):
    path = Path(root) / name
    for component in [path, *path.parents]:
        if component.is_symlink():
            raise ValueError('Go evidence cannot traverse symlinks')
    if not path.is_file() or path.stat().st_size > limit:
        raise ValueError('Go report must be a bounded regular file')
    with path.open('rb') as source:
        data = source.read(limit + 1)
    if len(data) > limit:
        raise ValueError('Go report exceeds byte limit')
    return data


def verify(archive, artifact, directory, verifier):
    directory = Path(directory)
    if directory.is_symlink() or {path.name for path in directory.iterdir()} != {entry['id'] for entry in entrypoints(artifact)}:
        raise ValueError('Go evidence must cover exactly the declared entrypoints')
    records = []
    with tempfile.TemporaryDirectory(prefix='leapview-archive-go-verify-') as temporary:
        staging = Path(temporary)
        binaries = extract(archive, artifact, staging)
        for entry in entrypoints(artifact):
            original = directory / entry['id']
            if original.is_symlink() or {path.name for path in original.iterdir()} != {'summary.json', 'govulncheck.json'}:
                raise ValueError('Go report directory is incomplete or contains diagnostics')
            data = {name: regular_report(original, name, limit) for name, limit in
                    [('summary.json', candidate.MAX_JSON_BYTES), ('govulncheck.json', MAX_REPORT_BYTES)]}
            snapshot = staging / (entry['id'] + '-reports')
            snapshot.mkdir()
            for name, content in data.items():
                (snapshot / name).write_bytes(content)
            run_verifier(verifier, binaries[entry['id']], entry, artifact['platform'], snapshot, offline=True)
            receipt = candidate.read_json(data['summary.json'])
            binary_digest = candidate.digest_file(binaries[entry['id']])
            report_digest = candidate.digest_bytes(data['govulncheck.json'])
            if receipt['binarySHA256'] != binary_digest or receipt['reportSHA256'] != report_digest:
                raise ValueError('Go reports differ from exact archive binary')
            for name, content in data.items():
                if candidate.digest_file(original / name) != candidate.digest_bytes(content):
                    raise ValueError('Go evidence changed during verification')
            records.append({**entry, 'binarySHA256': binary_digest, 'reportSHA256': report_digest,
                            'summarySHA256': candidate.digest_bytes(data['summary.json']),
                            'buildInfo': receipt['buildInfo'], 'scanner': receipt['scanner'],
                            'scannedAt': receipt['scannedAt']})
    return {'scope': 'go-binary-only', 'archiveSHA256': artifact['sha256'], 'binaries': records}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path)
    parser.add_argument('--source-root', type=Path, required=True)
    parser.add_argument('--source-revision', required=True)
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--binary-verifier', type=Path, required=True)
    parser.add_argument('--kind', choices=['application-image', 'cli-archive'], required=True)
    parser.add_argument('--archive-identity', type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        source = candidate.checkout_source(args.source_root)
        if source['revision'] != args.source_revision or not candidate.REVISION.fullmatch(args.source_revision):
            raise ValueError('source differs from authorized candidate revision')
        identity = candidate.read_json_file(args.archive_identity) if args.archive_identity else None
        artifact = candidate.collect(args.archive, args.kind, source, archive_identity=identity)['artifact']
        scan(args.archive, artifact, args.evidence_dir, args.binary_verifier)
        verify(args.archive, artifact, args.evidence_dir, args.binary_verifier)
        if candidate.canonical_bytes(candidate.checkout_source(args.source_root)) != candidate.canonical_bytes(source):
            raise ValueError('candidate source changed during scan')
    except (ValueError, KeyError, TypeError, OSError, EOFError, tarfile.TarError,
            subprocess.SubprocessError) as error:
        raise SystemExit('archive Go evidence rejected: ' + str(error)) from error


if __name__ == '__main__':
    main()

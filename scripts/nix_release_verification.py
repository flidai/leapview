#!/usr/bin/env python3
"""Verify authenticated original Nix outputs plus fresh exact-artifact scans.

This produces bounded evidence for the protected canonical admission command.
It does not publish, install or grant authority to candidate JSON flags.
"""

import argparse
from pathlib import Path
import subprocess

import nix_archive_go_evidence as go
import nix_candidate_manifest as candidate
import nix_candidate_publication as publication
import nix_release_inputs as inputs


def command(arguments, timeout):
    try:
        subprocess.run(arguments, check=True, timeout=timeout, stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL)
    except (OSError, subprocess.SubprocessError):
        raise ValueError('fresh protected exact-artifact verification failed') from None


def inventory(root):
    root = Path(root)
    result = {}
    for path in sorted(root.rglob('*')):
        if path.is_symlink():
            raise ValueError('fresh evidence cannot contain symlinks')
        if path.is_file():
            relative = path.relative_to(root).as_posix()
            result[relative] = candidate.digest_bytes(inputs._regular(path, candidate.MAX_REPORT_BYTES))
    if not result or len(result) > inputs.MAX_ENTRIES:
        raise ValueError('fresh evidence inventory is empty or exceeds bound')
    return result


def verify(directory, source_root, kind, run_id, attempt, arch, verifier, output, *, native_directory=None):
    directory, output, source_root = Path(directory), Path(output), Path(source_root)
    if (kind == 'application-image') != (native_directory is not None):
        raise ValueError('exact output kind requires applicable embedded native evidence')
    selection = inputs.verify(directory, kind, run_id, attempt, arch)
    source = candidate.checkout_source(source_root)
    archive, runtime = directory / 'qualified/image.tar', directory / 'qualified/runtime'
    paths = (archive, runtime / 'candidate-manifest.json', runtime)
    original = publication.verified_record(source_root, paths, verifier, kind)
    if original['source'] != source or original['artifact']['platform'] != 'linux/' + arch:
        raise ValueError('authenticated Nix output differs from exact source/platform selection')
    prefix = 'nix' if kind == 'application-image' else 'nix-site'
    signed_path = directory / f'binding/{prefix}-signed.json'
    signed = candidate.read_json_file(signed_path)
    report_name = 'image-qualification-report.json' if kind == 'application-image' else 'site-qualification-report.json'
    qualified = publication.bind_qualified(source_root, paths, signed['image'], selection['signerRevision'],
                                          verifier, signed_path, directory / ('published/' + report_name), kind)
    retained = candidate.read_json_file(directory / f'published/{prefix}-qualified.json')
    if qualified != retained or signed['kind'] != kind or signed['source'] != source:
        raise ValueError('original live signed qualification differs from retained exact output')
    output.mkdir(mode=0o700)
    # All scanners come from the current protected checkout/toolchain. The source
    # checkout is read only for immutable input identity, never for executable tools.
    go.scan(archive, original['artifact'], output / 'go', verifier)
    fresh_go = go.verify(archive, original['artifact'], output / 'go', verifier)
    command(['python3', str(candidate.ROOT / 'scripts/check_nix_runtime_security.py'), str(archive),
             '--kind', kind, '--source-revision', source['revision'], '--evidence-dir', str(output / 'runtime')], 1200)
    fresh_runtime = (candidate.site_runtime_evidence if kind == 'site-image' else candidate.runtime_evidence)(
        output / 'runtime', original['artifact'], source['revision'])
    native = None
    if kind == 'application-image':
        if native_directory is None:
            raise ValueError('application admission requires complete embedded native evidence')
        # The native verifier owns complete graph/scan semantics. Absence, partial
        # coverage or guessed component identities cannot be replaced by a flag.
        command(['python3', str(candidate.ROOT / 'scripts/nix_native_inventory.py'), 'verify', str(native_directory),
                 '--artifact', str(archive), '--kind', kind, '--platform', original['artifact']['platform'],
                 '--source-revision', source['revision'], '--output', str(output / 'native.json')], 1200)
        native = candidate.read_json_file(output / 'native.json')
        if not candidate.SHA256.fullmatch(native.get('verifiedDigest', '')) or not isinstance(native.get('fileHashes'), dict) or not native['fileHashes']:
            raise ValueError('embedded native verifier did not produce complete bound evidence')
        for name, digest in native['fileHashes'].items():
            relative = candidate.safe_path(name)
            source_path = Path(native_directory) / relative
            if any(parent.is_symlink() for parent in source_path.parents):
                raise ValueError('embedded native reports are redirected')
            content = inputs._regular(source_path, candidate.MAX_REPORT_BYTES)
            if candidate.digest_bytes(content) != digest:
                raise ValueError('embedded native raw reports differ from verified inventory')
            target = output / 'native-evidence' / relative
            target.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
            with target.open('xb') as stream:
                stream.write(content)
    elif native_directory is not None:
        raise ValueError('site admission cannot borrow application native evidence')
    # Repeat byte/API proof after scanning so changed evidence cannot silently
    # preserve the first verification's successful result.
    inputs.verify(directory, kind, run_id, attempt, arch)
    if publication.verified_record(source_root, paths, verifier, kind) != original:
        raise ValueError('original Nix output changed while verifying fresh evidence')
    # Preserve original attested SPDX bytes separately from fresh scan SPDX.
    # Their creation timestamps may differ; the original signature proves only
    # the original SPDX, while the fresh inventory/scans prove current policy.
    for name, path in {'original-sbom.spdx.json': runtime / 'sbom.spdx.json',
                       'original-signed.json': signed_path,
                       'original-qualified.json': directory / f'published/{prefix}-qualified.json',
                       'runtime-security-policy.json': candidate.ROOT / ('nix/site-runtime-security-policy.json' if kind == 'site-image' else 'nix/runtime-security-policy.json'),
                       'security-exceptions.yaml': candidate.ROOT / '.security/exceptions.yaml',
                       'security-coverage.yaml': candidate.ROOT / '.security/coverage.yaml'}.items():
        with (output / name).open('xb') as stream:
            stream.write(inputs._regular(path, candidate.MAX_REPORT_BYTES))
    files = inventory(output)
    result = {'schemaVersion': 1, 'kind': kind, 'sourceRevision': source['revision'],
              'signerRevision': selection['signerRevision'], 'image': signed['image'],
              'platform': original['artifact']['platform'], 'version': original['artifact']['version'],
              'archiveSHA256': original['artifact']['sha256'], 'candidateDigest': original['candidateDigest'],
              'registryBindingDigest': signed['registryBindingDigest'],
              'signedEvidenceBindingDigest': signed['signedEvidenceBindingDigest'],
              'qualifiedEvidenceBindingDigest': qualified['qualifiedEvidenceBindingDigest'],
              'runtimeEvidence': fresh_runtime, 'goEvidence': fresh_go, 'nativeEvidence': native,
              'originalProducer': selection, 'fileHashes': files}
    result['verificationDigest'] = candidate.digest_bytes(b'leapview/nix-release-verification/v1\n' + candidate.canonical_bytes(result))
    with (output / 'verification.json').open('xb') as target:
        target.write(candidate.canonical_bytes(result))
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--source-root', type=Path, required=True)
    parser.add_argument('--kind', choices=tuple(inputs.PRODUCERS), required=True)
    parser.add_argument('--run-id', type=int, required=True)
    parser.add_argument('--attempt', type=int, required=True)
    parser.add_argument('--architecture', choices=('amd64', 'arm64'), required=True)
    parser.add_argument('--binary-verifier', type=Path, required=True)
    parser.add_argument('--native-directory', type=Path)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    verify(args.directory, args.source_root, args.kind, args.run_id, args.attempt, args.architecture,
           args.binary_verifier, args.output, native_directory=args.native_directory)


if __name__ == '__main__':
    main()

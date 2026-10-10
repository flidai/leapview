"""One-time, exact-package archive and retirement; never used by production."""
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile

REPO = 'flidai/leapview'
PACKAGE = 'leapview-site-kamal-trial'
ENDPOINT = 'orgs/flidai/packages/container/' + PACKAGE
TAG = 'archive-kamal-trial-20261010'
REQUIRED = {'sha256:8e653a743d65a8d90b2bbf65a620524c4d378f936f7754407c0a9fa09d3d8339',
            'sha256:b60e9a9c1054b4d8071d655f03852d8032df038be3fe0204a53130377563de0c',
            'sha256:c10c14ca8cb0f9122b1f67fa30715bea7eb2deccadbe0c45ae570d9219a29121'}


def gh(*args):
    return subprocess.check_output(['gh', *args])


def api(path):
    return json.loads(gh('api', path))


def pages(path, key=None):
    result = json.loads(gh('api', '--paginate', '--slurp', path))
    return [item for page in result for item in (page[key] if key else page)]


def sha(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def dump(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def preflight():
    assert os.environ['GITHUB_REPOSITORY'] == REPO
    workflow = api('repos/' + REPO + '/actions/workflows/site-kamal-trial.yml')
    assert workflow['state'] == 'disabled_manually', workflow
    assert not api('repos/' + REPO + '/pulls?state=open&head=flidai:ganesh/site-kamal-trial')
    runs = pages('repos/' + REPO + '/actions/workflows/site-kamal-trial.yml/runs?per_page=100', 'workflow_runs')
    assert all(run['status'] == 'completed' for run in runs)
    package = api(ENDPOINT)
    assert package['name'] == PACKAGE and package['package_type'] == 'container'
    assert package['repository']['full_name'] == REPO
    versions = pages(ENDPOINT + '/versions?per_page=100')
    assert versions and REQUIRED <= {version['name'] for version in versions}
    assert all(re.fullmatch(r'sha256:[a-f0-9]{64}', version['name']) for version in versions)
    return package, versions, runs


def archive():
    package, versions, runs = preflight()
    root = Path('retirement-archive')
    root.mkdir()
    layout = root / 'oci'
    blobs = layout / 'blobs' / 'sha256'
    blobs.mkdir(parents=True)
    dump(layout / 'oci-layout', {'imageLayoutVersion': '1.0.0'})
    dump(root / 'package.json', package)
    dump(root / 'versions.json', versions)
    dump(root / 'workflow-runs.json', runs)
    evidence = root / 'admission-artifacts'
    evidence.mkdir()
    saved = []
    for run in runs:
        artifacts = pages('repos/' + REPO + '/actions/runs/' + str(run['id']) + '/artifacts?per_page=100', 'artifacts')
        for artifact in artifacts:
            if not artifact['name'].startswith('kamal-trial-'):
                continue
            assert not artifact['expired'], artifact
            filename = str(artifact['id']) + '-' + artifact['name'] + '.zip'
            target = evidence / filename
            target.write_bytes(gh('api', 'repos/' + REPO + '/actions/artifacts/' + str(artifact['id']) + '/zip'))
            saved.append({'run': run['id'], 'artifact': artifact, 'file': filename, 'sha256': sha(target)})
    assert any(item['run'] == run_id and item['artifact']['name'].startswith('kamal-trial-image-')
               for run_id in (36228605549, 36230525487, 36543690675) for item in saved)
    for run_id in (36228605549, 36230525487, 36543690675):
        assert any(item['run'] == run_id and item['artifact']['name'].startswith('kamal-trial-image-') for item in saved)
    dump(root / 'admission-artifacts.json', saved)
    scope = 'repository:flidai/' + PACKAGE + ':pull'
    url = 'https://ghcr.io/token?' + urllib.parse.urlencode({'service': 'ghcr.io', 'scope': scope})
    token = json.load(urllib.request.urlopen(url, timeout=60))['token']
    base = 'https://ghcr.io/v2/flidai/' + PACKAGE + '/'
    headers = {'Authorization': 'Bearer ' + token,
               'Accept': ','.join(['application/vnd.oci.image.index.v1+json', 'application/vnd.oci.image.manifest.v1+json',
                                  'application/vnd.docker.distribution.manifest.list.v2+json', 'application/vnd.docker.distribution.manifest.v2+json'])}
    manifests, pending_blobs = {}, set()

    def fetch(kind, digest):
        assert re.fullmatch(r'sha256:[a-f0-9]{64}', digest)
        target = blobs / digest.split(':')[1]
        for attempt in range(4):
            try:
                request = urllib.request.Request(base + kind + '/' + digest, headers=headers)
                with urllib.request.urlopen(request, timeout=120) as response, target.open('wb') as output:
                    while chunk := response.read(1024 * 1024):
                        output.write(chunk)
                assert 'sha256:' + sha(target) == digest, digest
                return target
            except (OSError, urllib.error.URLError):
                if attempt == 3:
                    raise
                time.sleep(2 ** attempt)

    def manifest(digest):
        if digest in manifests:
            return manifests[digest]
        target = fetch('manifests', digest)
        value = json.loads(target.read_bytes())
        descriptor = {'mediaType': value['mediaType'], 'digest': digest, 'size': target.stat().st_size}
        manifests[digest] = descriptor
        for child in value.get('manifests', []):
            selected = manifest(child['digest'])
            assert selected['size'] == child['size']
        for blob in value.get('layers', []) + value.get('blobs', []) + ([value['config']] if 'config' in value else []):
            pending_blobs.add(blob['digest'])
        return descriptor

    roots = []
    for version in versions:
        selected = dict(manifest(version['name']))
        tags = version['metadata']['container']['tags']
        selected['annotations'] = {'org.opencontainers.image.ref.name': tags[0] if tags else 'version-' + str(version['id'])}
        roots.append(selected)
    with concurrent.futures.ThreadPoolExecutor(max_workers=6) as executor:
        list(executor.map(lambda digest: fetch('blobs', digest), pending_blobs - set(manifests)))
    dump(layout / 'index.json', {'schemaVersion': 2, 'mediaType': 'application/vnd.oci.image.index.v1+json', 'manifests': roots})
    print('Archived', len(versions), 'package versions,', len(manifests), 'manifests,', len(pending_blobs), 'blobs and', len(saved), 'admission artifacts', flush=True)
    output = Path('trial-package-oci.tar.gz')
    with tarfile.open(output, 'w:gz', compresslevel=1) as tar:
        tar.add(root, arcname='trial-package')
    receipt = {'schemaVersion': 1, 'package': PACKAGE, 'repository': REPO, 'versionCount': len(versions),
               'versions': sorted(version['name'] for version in versions), 'manifestCount': len(manifests),
               'blobCount': len(pending_blobs), 'admissionArtifactCount': len(saved), 'releaseTag': TAG,
               'archive': output.name, 'archiveSha256': sha(output), 'archiveBytes': output.stat().st_size,
               'sourceRevision': os.environ['GITHUB_SHA'], 'archiveRunId': int(os.environ['GITHUB_RUN_ID'])}
    parts = []
    with output.open('rb') as original:
        number = 1
        while chunk := original.read(1024 * 1024):
            part = Path(output.name + '.part-' + str(number).zfill(3))
            with part.open('wb') as selected:
                selected.write(chunk)
                size = len(chunk)
                while size < 1_000_000_000:
                    chunk = original.read(min(1024 * 1024, 1_000_000_000 - size))
                    if not chunk:
                        break
                    selected.write(chunk)
                    size += len(chunk)
            parts.append({'file': part.name, 'bytes': part.stat().st_size, 'sha256': sha(part)})
            number += 1
    receipt['archiveParts'] = parts
    output.unlink()
    dump(Path('retirement-manifest.json'), receipt)
    Path('SHA256SUMS').write_text(''.join(part['sha256'] + '  ' + part['file'] + '\n' for part in parts)
                                + sha(Path('retirement-manifest.json')) + '  retirement-manifest.json\n')
    Path('archive-notes.md').write_text('Historical deployment qualification archive for the retired `leapview-site-kamal-trial` package. This is not an application release.\n\n'
        'The archive preserves all package versions (including untagged versions and attestation images), their OCI manifests/configs/layers, original tags, and every available trial admission artifact. All OCI content digests were checked before publication.\n\n'
        'Verify `SHA256SUMS`, concatenate the numbered `trial-package-oci.tar.gz.part-*` files in order, then verify the complete archive against `archiveSha256` in `retirement-manifest.json` before extraction. The OCI layout is under `trial-package/oci/`; `versions.json` maps historical tags/digests to package version IDs. This archive is not production admission.\n')
    subprocess.run(['gh', 'release', 'create', TAG, *[part['file'] for part in parts], 'retirement-manifest.json', 'SHA256SUMS', '--repo', REPO,
                    '--target', os.environ['GITHUB_SHA'], '--title', 'Historical Kamal trial archive (not an application release)',
                    '--notes-file', 'archive-notes.md', '--prerelease', '--latest=false'], check=True)


def delete():
    package, versions, runs = preflight()
    directory = Path('verified-retirement')
    directory.mkdir()
    subprocess.run(['gh', 'release', 'download', TAG, '--repo', REPO, '--dir', str(directory),
                    '--pattern', 'trial-package-oci.tar.gz.part-*', '--pattern', 'retirement-manifest.json', '--pattern', 'SHA256SUMS'], check=True)
    receipt = json.loads((directory / 'retirement-manifest.json').read_text())
    assert receipt['package'] == PACKAGE and receipt['repository'] == REPO
    assert sorted(version['name'] for version in versions) == receipt['versions'], 'package changed since archive'
    subprocess.run(['sha256sum', '--check', 'SHA256SUMS'], cwd=directory, check=True)
    with (directory / receipt['archive']).open('wb') as archive:
        for part in receipt['archiveParts']:
            selected = directory / part['file']
            assert sha(selected) == part['sha256'] and selected.stat().st_size == part['bytes']
            with selected.open('rb') as fragment:
                shutil.copyfileobj(fragment, archive)
            selected.unlink()
    assert sha(directory / receipt['archive']) == receipt['archiveSha256']
    with tarfile.open(directory / receipt['archive'], 'r:gz') as tar:
        members = {member.name: member for member in tar.getmembers()}
        archived = json.load(tar.extractfile('trial-package/versions.json'))
        assert sorted(version['name'] for version in archived) == receipt['versions']
        for digest in receipt['versions']:
            assert 'trial-package/oci/blobs/sha256/' + digest.split(':')[1] in members
        tar.extractall(directory, filter='data')
    (directory / receipt['archive']).unlink()
    root = directory / 'trial-package'
    for blob in (root / 'oci' / 'blobs' / 'sha256').iterdir():
        assert sha(blob) == blob.name, blob.name
    artifacts = json.loads((root / 'admission-artifacts.json').read_text())
    recovered = []
    for run_id in (36228605549, 36230525487, 36543690675):
        selected = [item for item in artifacts if item['run'] == run_id and item['artifact']['name'].startswith('kamal-trial-image-')]
        assert len(selected) == 1
        artifact = root / 'admission-artifacts' / selected[0]['file']
        assert sha(artifact) == selected[0]['sha256']
        with zipfile.ZipFile(artifact) as original:
            names = [name for name in original.namelist() if name.endswith('/oci-admission.json') or name == 'oci-admission.json']
            assert len(names) == 1
            admission = json.loads(original.read(names[0]))
        assert admission['digest'] in REQUIRED
        admission_path = directory / ('admission-' + str(run_id) + '.json')
        dump(admission_path, admission)
        prepared = directory / ('prepared-' + str(run_id))
        subprocess.run(['python3', 'deploy/kamal-trial/prepare_site_image.py', '--admission', str(admission_path),
                        '--output', str(prepared), '--archive-layout', str(root / 'oci')], check=True)
        record = json.loads((prepared / 'site-record.json').read_text())
        assert record['admission']['digest'] == admission['digest']
        recovered.append({'runId': run_id, 'image': admission['image'], 'platformDigest': record['platform_digest'],
                          'configDigest': record['config_digest'], 'archiveSha256': record['archive_sha256']})
        (prepared / 'site.oci.tar').unlink()
    before = {name: api('orgs/flidai/packages/container/' + name)['id'] for name in ('leapview', 'leapview-site')}
    subprocess.run(['gh', 'api', '--method', 'DELETE', ENDPOINT], check=True)
    result = subprocess.run(['gh', 'api', ENDPOINT], capture_output=True, text=True)
    assert result.returncode and '(HTTP 404)' in result.stderr, result.stderr
    after = {name: api('orgs/flidai/packages/container/' + name)['id'] for name in before}
    assert after == before
    receipt['deletedAtUTC'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
    receipt['deleteRunId'] = int(os.environ['GITHUB_RUN_ID'])
    receipt['packageGetAfterDelete'] = 404
    receipt['productionPackagesPreserved'] = after
    receipt['offlinePreparationVerified'] = recovered
    dump(Path('deletion-receipt.json'), receipt)
    subprocess.run(['gh', 'release', 'upload', TAG, 'deletion-receipt.json', '--repo', REPO], check=True)
    print('Deleted only', PACKAGE, '; both production package IDs are unchanged', flush=True)


if __name__ == '__main__':
    {'archive': archive, 'delete': delete}[sys.argv[1]]()

#!/usr/bin/env python3
"""Run an already-admitted real site image through the isolated Kamal topology.

Input record/OCI archive must be prepared from a successful trial Actions run.
This consumes admission evidence; it does not replace or forge admission.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import threading
import time

from lifecycle import Trial, HTTP, run, wait_until


def main():
    parser = argparse.ArgumentParser()
    for name in ('state', 'artifacts', 'gems', 'registry', 'site_record', 'site_archive'):
        parser.add_argument('--' + name.replace('_', '-'), type=Path, required=True)
    args = parser.parse_args()
    args.mitigate, args.snapshotter, args.disk_mib = True, 'overlayfs', 0
    site = json.loads(args.site_record.read_text())
    if 'archive_sha256' in site:
        with args.site_archive.open('rb') as archive:
            if hashlib.file_digest(archive, 'sha256').hexdigest() != site['archive_sha256']:
                raise SystemExit('OCI archive checksum mismatch')
    admission = site['admission']
    reference = admission['image']
    if not re.fullmatch(r'ghcr\.io/flidai/leapview-site-kamal-trial@sha256:[a-f0-9]{64}', reference):
        raise SystemExit('not an immutable trial image')
    if (admission['digest'] != reference.split('@')[1]
            or admission['registryDigest'] != admission['digest']
            or not admission['attestation']['verified']
            or admission['attestation']['repository'] != 'flidai/leapview'
            or admission['attestation']['workflow'] != 'flidai/leapview/.github/workflows/site-kamal-trial.yml'
            or not admission['sbom']['discoverable']
            or not admission['vulnerabilityPolicy']['passed']):
        raise SystemExit('successful matching trial admission evidence required')
    revision = admission['attestation']['sourceRevision']
    if not re.fullmatch(r'[a-f0-9]{40}', revision):
        raise SystemExit('invalid source revision')
    trial = Trial(args)
    try:
        trial.setup()
        trial.report['synthetic'] = False
        trial.report['source_admission'] = admission
        trial.report['expected_platform_digest'] = site['platform_digest']
        trial.report['expected_config_digest'] = site['config_digest']
        trial.docker('load', '-i', str(args.site_archive.resolve()))
        version = 'site-' + revision[:12]
        tag = trial.repo + ':' + version
        trial.docker('tag', admission['digest'], tag)
        trial.docker('push', tag)
        image = trial.inspect(tag)
        expected_local = trial.repo + '@' + admission['digest']
        if expected_local not in image['RepoDigests']:
            raise RuntimeError('archive/registry round trip changed admitted index digest')
        # Verify the original signed index -> platform -> config chain in the private registry.
        for digest in (admission['digest'], site['platform_digest']):
            from urllib.request import Request
            req = Request('http://127.0.0.1:5000/v2/site/manifests/' + digest,
                          headers={'Accept': 'application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json'})
            raw = HTTP.open(req).read()
            if 'sha256:' + hashlib.sha256(raw).hexdigest() != digest:
                raise RuntimeError('manifest bytes differ from recorded digest')
            parsed = json.loads(raw)
            if digest == admission['digest']:
                selected = [m['digest'] for m in parsed['manifests'] if m.get('platform') == {'architecture': 'amd64', 'os': 'linux'}]
                if selected != [site['platform_digest']]:
                    raise RuntimeError('unexpected selected amd64 manifest')
            elif parsed['config']['digest'] != site['config_digest']:
                raise RuntimeError('wrong image config digest')
        # Remove the seed copy so the measured deployment pulls only this host's
        # platform. The private registry retains the admitted index and all blobs.
        trial.docker('image', 'rm', '--force', image['Id'])
        content = trial.root / 'containerd/io.containerd.content.v1.content/blobs/sha256'
        wait_until(lambda: not (content / site['config_digest'].split(':')[1]).exists())
        time.sleep(1)
        trial.report['before_real_pull'] = trial.snapshot()
        trial.sampler = threading.Thread(target=trial.sample, daemon=True)
        trial.sampler.start()
        config = trial.root / 'deploy.yml'
        config.write_text(config.read_text().replace('    options:',
            f'    cmd: -addr=:8081 -image-reference={reference}\n    options:', 1)
            .replace('  clear:\n', '  clear:\n    LEAPVIEW_SITE_BASE_URL: https://leapview.test\n', 1))
        (trial.root / 'records').mkdir(exist_ok=True)
        trial.save_record(version, {'schema': 1, 'version': version, 'service': 'leapview-site-trial',
                                   'status': 'candidate', 'image_id': image['Id'], 'reference': expected_local})
        trial.kamal('deploy', '--skip-push', '--version', version)
        actual = trial.inspect('leapview-site-trial-web-' + version)
        if actual['Image'] != image['Id']:
            raise RuntimeError('running container has a different image identity')
        descriptor = actual.get('ImageManifestDescriptor', {})
        if descriptor.get('digest') != site['platform_digest']:
            raise RuntimeError('running container platform manifest differs from admission: ' + json.dumps(descriptor))
        caddy_ip = trial.setup_caddy(check_fixture=False)
        def get(path):
            return run(['curl', '--noproxy', '*', '--fail', '--silent', '--show-error',
                        '--cacert', str(trial.root / 'ca.crt'), '--resolve', f'leapview.test:443:{caddy_ip}',
                        'https://leapview.test' + path])[1].encode()
        for path in ('/healthz', '/readyz', '/', '/docs/installation'):
            if not get(path):
                raise RuntimeError('empty site response: ' + path)
        build = json.loads(get('/build.json'))
        if build['revision'] != revision or build['image'] != reference:
            raise RuntimeError('public build identity differs from admitted image')
        release = json.loads(get('/release.json'))
        if release != json.loads((Path(__file__).resolve().parents[2] / 'docs/public-release.json').read_text()):
            raise RuntimeError('release documentation differs from selected source')
        html = get('/').decode()
        assets = set(re.findall(r'(?:src|href)="(/[^"?]+\.(?:css|js))(?:\?[^" ]*)?"', html))
        if not assets:
            raise RuntimeError('no representative CSS/JS assets found')
        for asset in sorted(assets):
            get(asset)
        trial.report['after_real_acceptance'] = trial.snapshot()
        trial.report['real_site_proxy_qualified'] = True
        trial.report['actual_container_platform_digest'] = descriptor['digest']
        trial.report['build'] = build
        trial.report['assets_checked'] = sorted(assets)
        print('PASS admitted real site through Kamal proxy: actual platform identity, health, docs and assets', flush=True)
    except BaseException as exc:
        trial.report['error'] = str(exc)
        raise
    finally:
        trial.close()


if __name__ == '__main__':
    main()

"""Public-site deployment contracts. No network or filesystem effects."""
import re

REPOSITORY = 'ghcr.io/flidai/leapview-site'
SERVICE = 'leapview-site'
RUNTIME = {'port': 8081, 'user': '65532:65532', 'base_url': 'https://leapview.dev',
           'tmpfs_mib': 64, 'log_size': '10m', 'log_files': 3}


def activation_allowed(mode, repository, ref):
    return mode == 'kamal' and repository == 'flidai/leapview' and ref == 'refs/heads/main'


def validate_record(record):
    if record.get('schema') != 1 or record.get('kamal') != '2.12.0':
        raise ValueError('unsupported record/tooling version')
    if not re.fullmatch(r'[a-f0-9]{40}', record.get('revision', '')):
        raise ValueError('invalid source revision')
    if not re.fullmatch(re.escape(REPOSITORY) + r'@sha256:[a-f0-9]{64}', record.get('image', '')):
        raise ValueError('immutable canonical image required')
    if record.get('version') != 'k' + record['image'].split(':')[-1]:
        raise ValueError('version is not bound to the complete image digest')
    for field in ('platform', 'config'):
        if not re.fullmatch(r'sha256:[a-f0-9]{64}', record.get(field, '')):
            raise ValueError('invalid platform/config identity')
    if record.get('runtime') != RUNTIME:
        raise ValueError('unsupported runtime contract; retain matching tooling for rollback')
    return record


def validate_scope(image):
    if image.get('Config', {}).get('Labels', {}).get('service') != SERVICE:
        return
    # Docker 29's containerd store can also list a RepoDigest in RepoTags.
    digests = image.get('RepoDigests', [])
    canonical = re.compile(re.escape(REPOSITORY) + r'@sha256:[a-f0-9]{64}').fullmatch
    if any(not (tag.startswith(REPOSITORY + ':') or (tag in digests and canonical(tag)))
           for tag in image.get('RepoTags', [])):
        raise ValueError('service image has an unrelated repository alias')
    if any(not canonical(ref) for ref in digests):
        raise ValueError('service image has an unrelated repository digest')


def validate_image(record, image):
    validate_record(record)
    validate_scope(image)
    labels = image.get('Config', {}).get('Labels', {})
    if (record['image'] not in image.get('RepoDigests', []) or image.get('Os') != 'linux'
            or image.get('Architecture') != 'amd64' or labels.get('service') != SERVICE
            or labels.get('org.opencontainers.image.revision') != record['revision']):
        raise ValueError('local image differs from admitted source/platform/service')


def validate_container(record, container):
    config, host = container['Config'], container['HostConfig']
    if (not container['State']['Running']
            or container.get('ImageManifestDescriptor', {}).get('digest') != record['platform']
            or config.get('Cmd') != ['-addr=:8081', '-image-reference=' + record['image']]
            or config.get('Entrypoint') != ['/leapview-site']
            or config.get('User') != record['runtime']['user']
            or 'LEAPVIEW_SITE_BASE_URL=https://leapview.dev' not in config.get('Env', [])
            or not host.get('ReadonlyRootfs') or 'ALL' not in host.get('CapDrop', [])
            or 'no-new-privileges=true' not in host.get('SecurityOpt', [])
            or host.get('Tmpfs', {}).get('/tmp') != 'rw,noexec,nosuid,size=64m'):
        raise ValueError('running container differs from admitted identity/runtime contract')
    if host.get('LogConfig') != {'Type': 'json-file', 'Config': {'max-file': '3', 'max-size': '10m'}}:
        raise ValueError('unexpected container logging contract')


def cleanup_candidates(containers, records, active, prior):
    """All-or-nothing plan; keep current and a distinct, recorded prior container."""
    matches = {}
    for c in containers:
        if c['Config'].get('Labels', {}).get('service') != SERVICE:
            raise ValueError('unexpected container ownership')
        name = c['Name'].lstrip('/')
        versions = [v for v, r in records.items() if c['Image'] == r.get('local_id')
                    and (name == SERVICE + '-web-' + v or name.startswith(SERVICE + '-web-' + v + '_'))]
        if len(versions) != 1:
            raise ValueError('unrecorded or ambiguous container; inspect before cleanup')
        version = versions[0]
        if c['State']['Running'] and version != active:
            raise ValueError('unexpected live version; inspect before cleanup')
        matches.setdefault(version, []).append(c)
    live = [c for c in matches.get(active, []) if c['State']['Running']]
    if len(live) != 1 or (prior and prior == active):
        raise ValueError('exactly one live and a distinct prior are required')
    keep = {live[0]['Id']}
    if prior:
        options = [c for c in matches.get(prior, []) if not c['State']['Running']]
        if not options:
            raise ValueError('recorded prior container missing')
        keep.add(max(options, key=lambda c: c['Created'])['Id'])
    return sorted(c['Id'] for c in containers if c['Id'] not in keep)

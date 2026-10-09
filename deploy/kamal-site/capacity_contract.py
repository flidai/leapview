"""Exact cold measurement proof and monotonic, supervised capacity policy CAS."""
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import tarfile

from contract import validate_record

RUNTIME_ARTIFACTS = {
    'docker-29.1.3.tgz': 'c019c608ba2bb009dd673f3230e4d743f36a78d36166c6c2444c05d0aa9ff0d9',
    'containerd-2.2.1-linux-amd64.tar.gz': 'f5d8e90ecb6c1c7e33ecddf8cc268a93b9e5b54e0e850320d765511d76624f41',
}
ROUTES = ['/healthz', '/readyz', '/', '/docs', '/sitemap.xml']
LIFECYCLE = ['cold-pull-candidate-active-prior', 'boot-active', 'boot-candidate',
             'rollback-active', 'restart-candidate', 'remove-private-prior']


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def validate_inputs(candidate, baseline):
    validate_record(candidate)
    state = baseline['state']
    require(not state.get('pending') and not state.get('maintenance_pending'), 'unfinished deployment')
    active, prior = (validate_record(state['records'][state[k]]) for k in ['active', 'prior'])
    records = [candidate, active, prior]
    require(len({r['image'] for r in records}) == 3, 'three distinct exact images required')
    require(all(type(r.get('compressed_bytes')) is int and r['compressed_bytes'] > 0 for r in records),
            'compressed artifact envelopes required')
    return records


def verify_runtime(root):
    """Authenticate cached archives and every used extracted executable byte."""
    for name, expected in RUNTIME_ARTIFACTS.items():
        archive = root / name
        require(not archive.is_symlink() and archive.is_file(), 'regular runtime archive required')
        require(digest(archive.read_bytes()) == expected, 'runtime archive differs from reviewed digest')
        prefix = 'docker/' if name.startswith('docker') else 'containerd/'
        with tarfile.open(archive, 'r:gz') as packed:
            for member in packed.getmembers():
                if not member.isfile():
                    continue
                path = Path(member.name)
                require(not path.is_absolute() and '..' not in path.parts, 'unsafe runtime archive member')
                selected = root / 'runtime' / (path if prefix == 'docker/' else Path(prefix) / path)
                require(selected.is_file() and not selected.is_symlink(), 'regular cached runtime executable required')
                require(digest(packed.extractfile(member).read()) == digest(selected.read_bytes()),
                        'extracted runtime byte mismatch')
    return dict(RUNTIME_ARTIFACTS)


def registration_payload(candidate_raw, baseline_raw, report_raw):
    candidate, baseline, report = (json.loads(raw) for raw in [candidate_raw, baseline_raw, report_raw])
    records = validate_inputs(candidate, baseline)
    require(report.get('schema') == 1 and report.get('passed') is True and
            report.get('production_changed') is False, 'successful isolated measurement required')
    require(report.get('candidate') == candidate['image'] and
            report.get('candidate_revision') == candidate['revision'] and
            report.get('candidate_record_sha256') == digest(candidate_raw) and
            report.get('baseline_sha256') == digest(baseline_raw), 'measurement input identity differs')
    require(report.get('runtime_artifacts') == RUNTIME_ARTIFACTS and
            report['runtime'].get('docker') == '29.1.3' and report['runtime'].get('driver') == 'overlayfs' and
            report['runtime'].get('containerd', '').startswith('containerd github.com/containerd/containerd/v2 v2.2.1 '), 'unqualified runtime profile')
    require(report.get('qualified_images') == [r['image'] for r in records] and
            report.get('lifecycle') == LIFECYCLE, 'cold lifecycle or retained identity differs')
    expected_checks = [{'image': r['image'], 'selected_config_verified': True} for r in records] + [
        {'image': r['image'], 'build_identity': True, 'health': True, 'routes': ROUTES}
        for r in [records[1], candidate, records[1], candidate]]
    require(report.get('checks') == expected_checks, 'actual identity and lifecycle checks required')
    samples = report.get('samples')
    require(isinstance(samples, list) and len(samples) >= 2, 'cold baseline and continuous samples required')
    for sample in samples:
        require(all(type(sample.get(k)) is int and sample[k] > 0
                    for k in ['available_bytes', 'available_inodes']), 'invalid filesystem sample')
        require(type(sample.get('time')) in (int, float), 'invalid sample clock')
    require(all(b['time'] >= a['time'] for a, b in zip(samples, samples[1:])), 'sample clock regressed')
    measured = {key: max(1, samples[0][field] - min(s[field] for s in samples))
                for key, field in [('bytes', 'available_bytes'), ('inodes', 'available_inodes')]}
    for key, value in measured.items():
        require(type(report.get('measured_peak_' + key)) is int and report['measured_peak_' + key] == value,
                'reported peak differs from raw samples')
    compressed = max(r['compressed_bytes'] for r in records)
    require(report.get('qualified_compressed_bytes') == compressed, 'compressed envelope differs')
    return {'candidate': candidate['image'], **measured, 'compressed': compressed,
            'receipt_sha256': digest(report_raw), 'active': baseline['state']['active'],
            'prior': baseline['state']['prior'],
            'ready_sha256': digest(json.dumps(baseline['handover'], sort_keys=True).encode())}


def require_host_runtime():
    info = json.loads(subprocess.run(['docker', 'info', '--format', '{{json .}}'],
        capture_output=True, text=True, check=True, timeout=10).stdout)
    components = json.loads(subprocess.run(['docker', 'version', '--format', '{{json .Server.Components}}'],
        capture_output=True, text=True, check=True, timeout=10).stdout)
    require(info.get('ServerVersion') == '29.1.3' and info.get('Driver') == 'overlayfs' and
            isinstance(components, list) and
            [component.get('Version') for component in components if component.get('Name') == 'containerd'] == ['2.2.1'],
            'host runtime differs from measured Docker/containerd profile')


def register(root, payload):
    """Caller must retain the normal site supervisor owner for this entire CAS."""
    target = root / 'ready.json'
    for path in [root, target, root / 'state.json']:
        meta = path.lstat()
        require(not stat.S_ISLNK(meta.st_mode) and meta.st_uid == os.geteuid() == 0 and
                not meta.st_mode & 0o022, 'unsafe capacity policy ownership')
    require_host_runtime()
    before = target.read_bytes()
    ready = json.loads(before)
    require(digest(json.dumps(ready, sort_keys=True).encode()) == payload['ready_sha256'],
            'capacity policy changed during qualification')
    state = json.loads((root / 'state.json').read_bytes())
    require(state['active'] == payload['active'] and state['prior'] == payload['prior'] and
            not state.get('pending') and not state.get('maintenance_pending'), 'deployment state changed')
    require(len(ready['capacity']) == 1, 'measurement covers one shared backing filesystem')
    require(all(type(payload[k]) is int and payload[k] > 0 for k in ['bytes', 'inodes', 'compressed']),
            'invalid measurement bounds')
    budget = next(iter(ready['capacity'].values()))
    require(payload['candidate'] not in budget['qualified_images'], 'candidate already qualified')
    budget['measured_peak_bytes'] = max(budget['measured_peak_bytes'], payload['bytes'])
    budget['measured_peak_inodes'] = max(budget['measured_peak_inodes'], payload['inodes'])
    budget['candidate_headroom_bytes'] = max(budget['candidate_headroom_bytes'],
                                            (3 * budget['measured_peak_bytes'] + 1) // 2)
    budget['reserve_inodes'] = max(budget['reserve_inodes'], 10000, 2 * budget['measured_peak_inodes'])
    budget['qualified_compressed_bytes'] = max(budget['qualified_compressed_bytes'], payload['compressed'])
    budget['qualified_images'].append(payload['candidate'])
    for path in budget['paths']:
        filesystem = os.statvfs(path)
        require(str(os.stat(path).st_dev) in ready['capacity'], 'backing filesystem changed')
        budget['reserve_bytes'] = max(budget['reserve_bytes'], 2 * 1024**3,
                                      (filesystem.f_blocks * filesystem.f_frsize + 9) // 10)
        require(filesystem.f_bavail * filesystem.f_frsize >=
                budget['candidate_headroom_bytes'] + budget['reserve_bytes'], 'byte reserve insufficient')
        require(filesystem.f_favail >= budget['measured_peak_inodes'] + budget['reserve_inodes'],
                'inode reserve insufficient')
    ready.setdefault('capacity_measurement_receipts', []).append({
        'image': payload['candidate'], 'sha256': payload['receipt_sha256'],
        'measured_peak_bytes': payload['bytes'], 'measured_peak_inodes': payload['inodes'],
        'qualified_compressed_bytes': payload['compressed'],
        'scope': 'disposable cold ext4 lifecycle; retained active/prior plus candidate'})
    after = json.dumps(ready, indent=2).encode() + b'\n'
    backup = root / ('ready-capacity-before-' + digest(before) + '.json')
    temporary = root / ('ready-capacity-' + payload['receipt_sha256'] + '.next')
    for path, raw in [(backup, before), (temporary, after)]:
        fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
    os.replace(temporary, target)
    fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)
    return {'candidate': payload['candidate'], 'receipt_sha256': payload['receipt_sha256'],
            'before_sha256': digest(before), 'after_sha256': digest(after), 'state_unchanged': True}

import ast
import copy
import errno
import io
import json
import os
from pathlib import Path
import shutil
import tempfile
import threading
import time
import unittest
import subprocess
from contextlib import redirect_stderr, redirect_stdout
from unittest.mock import patch

import observe


ACTIVE_VERSION = 'k' + 'b' * 64
PRIOR_VERSION = 'k' + 'a' * 64
ACTIVE_ID = 'sha256:' + '1' * 64
PRIOR_ID = 'sha256:' + '2' * 64
REVISION = 'b' * 40
ACTIVE_IMAGE = 'ghcr.io/flidai/leapview-site@sha256:' + 'b' * 64


def config(duration=240, probe=60, host=120):
    return {'identity': {'version': ACTIVE_VERSION, 'revision': REVISION, 'image': ACTIVE_IMAGE},
            'prior_version': PRIOR_VERSION, 'active_image_id': ACTIVE_ID, 'prior_image_id': PRIOR_ID,
            'min_free_bytes': 500, 'min_free_inodes': 50, 'duration_seconds': duration,
            'probe_interval_seconds': probe, 'host_interval_seconds': host, 'ssh_config': 'fixture',
            'target':'127.0.0.1','base_url':'https://leapview.dev'}


def app(version, image_id, *, running, revision=None, restarts=0):
    return {'id': ('a' if running else 'b') * 64, 'name': '/leapview-site-web-' + version,
            'image_id': image_id, 'config_image': 'localhost:5555/leapview-site:' + version,
            'service': 'leapview-site', 'role': 'web', 'version': version, 'destination': '',
            'revision': revision, 'compose_project': None, 'compose_service': None,
            'status': 'running' if running else 'exited', 'running': running,
            'started_at': '2026-09-29T00:00:00Z' if running else '2026-09-28T00:00:00Z',
            'health': 'none', 'restart_count': restarts, 'restart_policy': 'unless-stopped',
            'ports': None, 'mounts': [], 'networks': ['kamal']}


def host_sample():
    caddy_image = 'caddy@sha256:' + 'c' * 64
    return {'boot_id': '01234567-89ab-cdef-0123-456789abcdef',
        'filesystems': [{'device': '2049', 'capacity_bytes': 100000,
            'paths': [{'path': '/var/lib/docker', 'device': '2049', 'capacity_bytes': 100000,
                       'mountpoint': '/', 'major_minor': '8:1', 'fstype': 'ext4', 'source': '/dev/sda1'}],
            'available_bytes': 10000, 'available_inodes': 1000}],
        'state': {'active': ACTIVE_VERSION, 'prior': PRIOR_VERSION, 'pending': None,
                  'maintenance_pending': False, 'active_local_id': ACTIVE_ID, 'prior_local_id': PRIOR_ID},
        'updater': {'timer': {'active': 'inactive', 'enabled': 'disabled'},
                    'service': {'active': 'inactive', 'enabled': 'static'}},
        'owner_journal': False, 'operator_socket': False,
        'owned_image_ids': sorted([ACTIVE_ID, PRIOR_ID]),
        'locks': {'reconcile': {'exists': True, 'held': False}, 'deploy': {'exists': True, 'held': False}},
        'apps': [app(ACTIVE_VERSION, ACTIVE_ID, running=True, revision=REVISION),
                 app(PRIOR_VERSION, PRIOR_ID, running=False)],
        'proxy': {'id': 'd' * 64, 'name': '/kamal-proxy', 'image_id': 'e' * 64,
            'config_image': 'basecamp/kamal-proxy@' + observe.PROXY_DIGEST,
            'image_repo_digests': ['basecamp/kamal-proxy@' + observe.PROXY_DIGEST],
            'service': None, 'role': None, 'version': None, 'destination': None, 'revision': None,
            'compose_project': None, 'compose_service': None, 'status': 'running', 'running': True,
            'started_at': '2026-09-29T00:00:00Z', 'health': 'none', 'restart_count': 0,
            'restart_policy': 'unless-stopped', 'ports': None, 'mounts': [], 'networks': ['kamal']},
        'caddy': {'id': 'f' * 64, 'name': '/leapview-site-caddy-1', 'image_id': 'g' * 64,
            'config_image': caddy_image, 'service': None, 'role': None, 'version': None, 'destination': None,
            'revision': None, 'compose_project': 'leapview-site', 'compose_service': 'caddy',
            'status': 'running', 'running': True, 'started_at': '2026-09-29T00:00:00Z',
            'health': 'none', 'restart_count': 0, 'restart_policy': 'unless-stopped', 'networks': ['kamal'],
            'mounts': [{'destination':'/etc/caddy/Caddyfile','source':'/opt/leapview-site/Caddyfile','type':'bind','rw':False},
                       {'destination':'/data','source':'/var/lib/leapview-site/caddy-data','type':'bind','rw':True},
                       {'destination':'/config','source':'/var/lib/leapview-site/caddy-config','type':'bind','rw':True}],
            'ports': {'80/tcp':[{'HostIp':'0.0.0.0','HostPort':'80'}],
                      '443/tcp':[{'HostIp':'0.0.0.0','HostPort':'443'}]}}}


def http_good():
    return {'healthz': {'ok': True, 'status': 200}, 'readyz': {'ok': True, 'status': 200},
            'build': {'ok': True, 'status': 200, 'revision': REVISION,
                      'image': ACTIVE_IMAGE}}


class FakeClock:
    def __init__(self):
        self.mono = 1000.0
        self.wall_base = 1790640000.0
        self.offset = 0.0
        self.sleeps = 0
        self.oversleep = 0.0
        self.jump_after_sleep = 0.0

    def monotonic(self): return self.mono
    def wall(self): return self.wall_base + self.mono - 1000.0 + self.offset
    def sleep(self, seconds):
        self.sleeps += 1
        self.mono += seconds + (self.oversleep if self.sleeps == 1 else 0)
        if self.sleeps == 1: self.offset += self.jump_after_sleep


class ObserverTests(unittest.TestCase):
    def run_fixture(self, cfg=None, *, clock=None, http=None, host=None, clock_tolerance=5):
        cfg = cfg or config(); clock = clock or FakeClock()
        tmp = tempfile.TemporaryDirectory()
        out = Path(tmp.name) / 'run'
        with patch.multiple(observe,DURATION=cfg['duration_seconds'],
                            PROBE_INTERVAL=cfg['probe_interval_seconds'],
                            HOST_INTERVAL=cfg['host_interval_seconds'],GAP_GRACE=5):
            result = observe.run_observation(cfg, out,
                http_probe=http or http_good,
                host_sample=host or host_sample,
                monotonic=clock.monotonic, wall_time=clock.wall, sleep=clock.sleep,
                gap_grace=5, clock_tolerance=clock_tolerance)
        events = [json.loads(line) for line in (out / 'samples.jsonl').read_text().splitlines()]
        return tmp, out, result, events

    def test_full_short_fixture_passes_only_after_all_required_samples(self):
        tmp, out, result, events = self.run_fixture()
        try:
            self.assertEqual(result['status'], 'health_storage_passed')
            self.assertEqual(result['rollout_acceptance'], 'incomplete_public_adoption_smoke_required')
            self.assertEqual(result['samples'], 5)
            self.assertEqual(result['host_samples'], 3)
            self.assertEqual(len([e for e in events if e['type'] == 'sample']), 5)
            process=json.loads((out/'process.json').read_text())
            self.assertEqual(process['observer_sha256'],observe.observer_source_sha256())
            self.assertEqual(result['observer_sha256'],process['observer_sha256'])
            self.assertEqual(result['target'],'127.0.0.1')
            self.assertEqual(result['base_url'],'https://leapview.dev')
            self.assertEqual((out / 'summary.json').stat().st_mode & 0o777, 0o600)
            self.assertEqual((out / 'process.json').stat().st_mode & 0o777, 0o600)
            self.assertEqual(out.stat().st_mode & 0o777, 0o700)
        finally: tmp.cleanup()

    def test_bad_build_stops_without_recording_a_pass(self):
        bad = http_good(); bad['build']['revision'] = '0' * 40; bad['build']['ok'] = False
        tmp, out, result, events = self.run_fixture(http=lambda: bad)
        try:
            self.assertEqual(result['status'], 'failed')
            self.assertIn('public health/readiness/build', result['failure'])
            self.assertEqual(result['samples'], 1)
            self.assertEqual(json.loads((out / 'summary.json').read_text())['status'], 'failed')
        finally: tmp.cleanup()

    def test_sample_append_enospc_is_reported_and_best_effort_failure_is_durable(self):
        original_append=observe._append_jsonl
        def fail_sample(path,value):
            if value.get('type')=='sample': raise OSError(errno.ENOSPC,'private /operator/path secret')
            return original_append(path,value)
        stderr=io.StringIO()
        with patch.object(observe,'_append_jsonl',side_effect=fail_sample), redirect_stderr(stderr):
            tmp,out,result,events=self.run_fixture()
        try:
            expected='observer I/O failure during sample append (errno 28 ENOSPC)'
            self.assertEqual(result['status'],'failed')
            self.assertEqual(result['failure'],expected)
            self.assertEqual(json.loads((out/'summary.json').read_text())['failure'],expected)
            self.assertEqual([event['type'] for event in events],['rejected'])
            self.assertIn(expected,stderr.getvalue())
            self.assertNotIn('/operator/path',stderr.getvalue())
            self.assertNotIn('private',stderr.getvalue())
        finally: tmp.cleanup()

    def test_sample_summary_eio_keeps_recorded_sample_but_fails_interval(self):
        original_write=observe._write_json_atomic
        raised=False
        def fail_sample_summary(path,value):
            nonlocal raised
            if Path(path).name=='summary.json' and value.get('samples')==1 and value.get('status')=='running' and not raised:
                raised=True
                raise OSError(errno.EIO,'secret /operator/ssh/config')
            return original_write(path,value)
        stderr=io.StringIO()
        with patch.object(observe,'_write_json_atomic',side_effect=fail_sample_summary), redirect_stderr(stderr):
            tmp,out,result,events=self.run_fixture()
        try:
            expected='observer I/O failure during sample summary write (errno 5 EIO)'
            summary=json.loads((out/'summary.json').read_text())
            self.assertTrue(raised)
            self.assertEqual(result['status'],'failed')
            self.assertEqual(summary['status'],'failed')
            self.assertEqual(summary['samples'],1)
            self.assertEqual(summary['failure'],expected)
            self.assertEqual([event['type'] for event in events],['sample','rejected'])
            self.assertIn(expected,stderr.getvalue())
            self.assertNotIn('/operator/ssh/config',stderr.getvalue())
        finally: tmp.cleanup()

    def test_failed_final_summary_write_cannot_leave_a_pass(self):
        original_write=observe._write_json_atomic
        raised=False
        def fail_final_pass(path,value):
            nonlocal raised
            if Path(path).name=='summary.json' and value.get('status')=='health_storage_passed' and not raised:
                raised=True
                original_write(path,value)
                raise OSError(errno.EIO,'sensitive path /operator/state')
            return original_write(path,value)
        stderr=io.StringIO()
        with patch.object(observe,'_write_json_atomic',side_effect=fail_final_pass), redirect_stderr(stderr):
            tmp,out,result,events=self.run_fixture()
        try:
            expected='observer I/O failure during final summary write (errno 5 EIO)'
            summary=json.loads((out/'summary.json').read_text())
            self.assertTrue(raised)
            self.assertEqual(result['status'],'failed')
            self.assertEqual(summary['status'],'failed')
            self.assertEqual(summary['failure'],expected)
            self.assertIn(expected,stderr.getvalue())
            self.assertNotIn('/operator/state',stderr.getvalue())
            self.assertEqual(events[-1]['type'],'rejected')
        finally: tmp.cleanup()

    def test_secondary_failure_persistence_errors_do_not_mask_primary_errno(self):
        original_append=observe._append_jsonl
        original_write=observe._write_json_atomic
        def fail_sample(path,value):
            if value.get('type')=='sample': raise OSError(errno.ENOSPC,'primary /secret')
            if value.get('type')=='rejected':
                original_append(path,value)
                raise OSError(errno.EIO,'secondary /secret')
            return original_append(path,value)
        def fail_failed_summary(path,value):
            if Path(path).name=='summary.json' and value.get('status')=='failed':
                raise OSError(errno.EIO,'secondary /secret')
            return original_write(path,value)
        stderr=io.StringIO()
        with patch.object(observe,'_append_jsonl',side_effect=fail_sample), \
                patch.object(observe,'_write_json_atomic',side_effect=fail_failed_summary), \
                redirect_stderr(stderr):
            tmp,out,result,events=self.run_fixture()
        try:
            self.assertEqual(result['status'],'failed')
            self.assertEqual(result['failure'],'observer I/O failure during sample append (errno 28 ENOSPC)')
            self.assertIn('sample append (errno 28 ENOSPC)',stderr.getvalue())
            self.assertNotIn('EIO',stderr.getvalue())
            self.assertNotIn('/secret',stderr.getvalue())
        finally: tmp.cleanup()

    def test_unexpected_loop_oserror_keeps_errno_and_sanitizes_exception_text(self):
        def fail_probe(): raise OSError(errno.EIO,'private /operator/ssh/id_ed25519')
        stderr=io.StringIO()
        with redirect_stderr(stderr):
            tmp,out,result,events=self.run_fixture(http=fail_probe)
        try:
            expected='observer I/O failure during observation loop (errno 5 EIO)'
            self.assertEqual(result['status'],'failed')
            self.assertEqual(result['failure'],expected)
            self.assertEqual(json.loads((out/'summary.json').read_text())['failure'],expected)
            self.assertEqual(events[-1]['type'],'rejected')
            self.assertIn(expected,stderr.getvalue())
            self.assertNotIn('/operator/ssh/id_ed25519',stderr.getvalue())
        finally: tmp.cleanup()

    def test_failed_observation_maps_to_nonzero_command_status(self):
        args=['run','--record','record','--prior-version',PRIOR_VERSION,
              '--active-image-id',ACTIVE_ID,'--prior-image-id',PRIOR_ID,
              '--min-free-bytes','1','--min-free-inodes','1','--ssh-config','ssh',
              '--fingerprint-file','fingerprint','--target','127.0.0.1','--output-dir','run']
        with patch.object(observe,'prepare_config',return_value=config()), \
                patch.object(observe,'run_supervised',return_value=1):
            self.assertEqual(observe.main(args),1)

    def test_supervisor_receipt_prevents_pass_after_final_fsync_and_recovery_failures(self):
        original_write=observe._write_json_atomic
        original_append=observe._append_jsonl
        original_identity=observe.proc_identity
        child_pid=987654321
        child_start='12345'
        child_boot=observe.proc_boot_id()

        class FakeChild:
            pid=child_pid
            returncode=None
            def communicate(self,payload):
                worker_config=json.loads(payload)
                clock=FakeClock()
                def fail_summary(path,value):
                    if Path(path).name=='summary.json' and value.get('status')=='health_storage_passed':
                        original_write(path,value)
                        raise OSError(errno.EIO,'sensitive /operator/state')
                    if Path(path).name=='summary.json' and value.get('status')=='failed':
                        raise OSError(errno.ENOSPC,'sensitive /operator/state')
                    return original_write(path,value)
                def fail_rejection(path,value):
                    if value.get('type')=='rejected': raise OSError(errno.ENOSPC,'sensitive /operator/state')
                    return original_append(path,value)
                with patch.multiple(observe,DURATION=240,PROBE_INTERVAL=60,HOST_INTERVAL=120,GAP_GRACE=5), \
                        patch.object(observe.os,'getpid',return_value=child_pid), \
                        patch.object(observe,'proc_identity',return_value={'state':'S','start_ticks':child_start}), \
                        patch.object(observe,'proc_boot_id',return_value=child_boot), \
                        patch.object(observe,'_write_json_atomic',side_effect=fail_summary), \
                        patch.object(observe,'_append_jsonl',side_effect=fail_rejection):
                    result=observe.run_observation(worker_config,run_dir,http_probe=http_good,host_sample=host_sample,
                        monotonic=clock.monotonic,wall_time=clock.wall,sleep=clock.sleep,gap_grace=5,
                        run_id=worker_config['run_id'],output_dir_exists=True)
                self.returncode=0 if result['status']=='health_storage_passed' else 1

        with tempfile.TemporaryDirectory() as directory:
            run_dir=Path(directory)/'run'
            cfg=config()
            stderr=io.StringIO(); stdout=io.StringIO()
            def identity(pid):
                return {'state':'S','start_ticks':child_start} if pid==child_pid else original_identity(pid)
            with patch.object(observe.subprocess,'Popen',return_value=FakeChild()), \
                    patch.object(observe,'proc_identity',side_effect=identity), \
                    redirect_stderr(stderr),redirect_stdout(stdout):
                result=observe.run_supervised(cfg,run_dir)
            self.assertEqual(result,1)
            self.assertEqual(json.loads((run_dir/'summary.json').read_text())['status'],'health_storage_passed')
            receipt=json.loads((run_dir/'exit.json').read_text())
            self.assertEqual(receipt['returncode'],1)
            self.assertEqual(receipt['child'],{'pid':child_pid,'start_ticks':child_start,'boot_id':child_boot})
            summary,status=observe.status(run_dir)
            self.assertEqual(status,'failed')
            self.assertEqual(summary['status'],'failed')
            self.assertIn('supervised observer exited unsuccessfully',summary['failure'])
            self.assertNotIn('/operator/state',stderr.getvalue())

    def test_failed_exit_receipt_commit_is_removed_and_never_reports_pass(self):
        original_write=observe._write_json_atomic
        original_identity=observe.proc_identity
        child_pid=987654320
        child_start='12346'
        child_boot=observe.proc_boot_id()
        class FakeChild:
            pid=child_pid
            returncode=0
            def communicate(self,payload):
                worker_config=json.loads(payload)
                now=time.time()
                summary=observe._initial_summary(worker_config,now,observe.CLOCK_TOLERANCE)
                summary.update(status='health_storage_passed',ended_at=observe.utc(now),
                               ended_wall_unix_seconds=now)
                process={'pid':child_pid,'start_ticks':child_start,'boot_id':child_boot,
                         'observer_sha256':worker_config['observer_sha256'],'run_id':worker_config['run_id']}
                original_write(run_dir/'summary.json',summary)
                original_write(run_dir/'process.json',process)
        with tempfile.TemporaryDirectory() as directory:
            run_dir=Path(directory)/'run'
            def identity(pid):
                return {'state':'S','start_ticks':child_start} if pid==child_pid else original_identity(pid)
            def fail_exit_commit(path,value):
                if Path(path).name=='exit.json':
                    original_write(path,value)
                    raise OSError(errno.EIO,'private /operator/state')
                return original_write(path,value)
            stderr=io.StringIO(); stdout=io.StringIO()
            cfg=config(); cfg['observer_sha256']=observe.observer_source_sha256()
            with patch.object(observe.subprocess,'Popen',return_value=FakeChild()), \
                    patch.object(observe,'proc_identity',side_effect=identity), \
                    patch.object(observe,'_write_json_atomic',side_effect=fail_exit_commit), \
                    redirect_stderr(stderr),redirect_stdout(stdout):
                result=observe.run_supervised(cfg,run_dir)
            self.assertEqual(result,2)
            self.assertFalse((run_dir/'exit.json').exists())
            self.assertEqual(observe.status(run_dir)[1],'interrupted')
            self.assertIn('exit receipt write (errno 5 EIO)',stderr.getvalue())
            self.assertNotIn('/operator/state',stderr.getvalue())

    def test_missing_public_endpoint_fails_sample(self):
        def missing_readyz():
            result=http_good(); result.pop('readyz'); return result
        tmp, _, result, events=self.run_fixture(http=missing_readyz)
        try:
            self.assertEqual(result['status'],'failed')
            self.assertIn('public health/readiness/build',result['failure'])
            sample=next(item for item in events if item['type']=='sample')
            self.assertTrue(any(not sample['http'].get(name,{}).get('ok')
                                for name in ('healthz','readyz','build')))
        finally: tmp.cleanup()

    def test_http_endpoint_records_duration_on_success_and_failure(self):
        class Response:
            status = 200
            def __enter__(self): return self
            def __exit__(self, *args): return False
            def read(self, limit): return b'ok'
            def geturl(self): return observe.DEFAULT_BASE_URL + '/healthz'
        with patch.object(observe.urllib.request, 'urlopen', return_value=Response()), \
                patch.object(observe.time, 'monotonic', side_effect=[10.0, 10.125]):
            good = observe._read_http(observe.DEFAULT_BASE_URL + '/healthz', expected_body=b'ok')
        with patch.object(observe.urllib.request, 'urlopen', side_effect=OSError()), \
                patch.object(observe.time, 'monotonic', side_effect=[20.0, 20.375]):
            bad = observe._read_http(observe.DEFAULT_BASE_URL + '/healthz', expected_body=b'ok')
        self.assertTrue(good['ok'])
        self.assertEqual(good['endpoint_duration_ms'], 125)
        self.assertFalse(bad['ok'])
        self.assertEqual(bad['endpoint_duration_ms'], 375)

    def test_public_endpoints_start_together(self):
        barrier = threading.Barrier(3)
        seen=[]
        def read(url, **kwargs):
            seen.append(url)
            try:
                barrier.wait(timeout=1)
                started_together = True
            except threading.BrokenBarrierError:
                started_together = False
            return {'status': 200, 'ok': True, 'started_together': started_together}
        with patch.object(observe, '_read_http', side_effect=read):
            result = observe.probe_public(config()['identity'],'https://observe.example')
        self.assertEqual(set(result), {'healthz', 'readyz', 'build'})
        self.assertTrue(all(value['started_together'] for value in result.values()))
        self.assertEqual(set(seen),{'https://observe.example/healthz','https://observe.example/readyz',
                                    'https://observe.example/build.json'})

    def test_collect_host_uses_configured_target_without_network(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); cfg=root/'ssh_config'; pin=root/'fingerprint'
            cfg.write_text('fixture config'); pin.write_text('fixture pin')
            args_seen=[]
            def fake_run(args,**kwargs):
                args_seen.append((args,kwargs))
                return subprocess.CompletedProcess(args,0,json.dumps(host_sample()),'')
            with patch.object(observe,'verify_ssh_config',return_value=str(cfg)) as verify, \
                    patch.object(observe.subprocess,'run',side_effect=fake_run):
                result=observe.collect_host(str(cfg),str(pin),observe.file_sha256(cfg),observe.file_sha256(pin),
                    config()['identity'],PRIOR_VERSION,ACTIVE_ID,PRIOR_ID,500,50,'fixture.example')
            self.assertEqual(result['boot_id'],host_sample()['boot_id'])
            verify.assert_called_once_with(str(cfg),str(pin),'fixture.example')
            self.assertIn('fixture.example',args_seen[0][0])
            self.assertNotIn('SSH_AUTH_SOCK',args_seen[0][1]['env'])

    def test_failed_public_endpoint_is_recorded_and_fails_sample_with_timings(self):
        def read(url, **kwargs):
            failed = url.endswith('/readyz')
            return {'status': 503 if failed else 200, 'ok': not failed,
                    'endpoint_duration_ms': 12}
        clock = FakeClock()
        with patch.object(observe, '_read_http', side_effect=read):
            tmp, out, result, events = self.run_fixture(clock=clock,
                http=lambda: observe.probe_public(config()['identity'],config()['base_url']))
        try:
            self.assertEqual(result['status'], 'failed')
            self.assertIn('public health/readiness/build', result['failure'])
            event = next(item for item in events if item['type'] == 'sample')
            self.assertFalse(event['http']['readyz']['ok'])
            self.assertEqual(event['http']['readyz']['status'], 503)
            self.assertEqual(event['http']['readyz']['endpoint_duration_ms'], 12)
            self.assertIn('public_probe_duration_ms', event)
            self.assertIn('sample_execution_ms', event)
        finally:
            tmp.cleanup()

    def test_slow_public_probe_still_fails_when_sample_exceeds_gap_grace(self):
        clock = FakeClock()
        def delayed_http():
            clock.mono += 6
            return http_good()
        tmp, _, result, events = self.run_fixture(clock=clock, http=delayed_http)
        try:
            self.assertEqual(result['status'], 'failed')
            self.assertIn('sample execution caused a monitoring gap', result['failure'])
            sample = next(item for item in events if item['type'] == 'sample')
            self.assertIn('sample execution caused a monitoring gap', sample['failures'])
            self.assertEqual(sample['sample_execution_ms'], 6000)
        finally:
            tmp.cleanup()

    def test_free_space_or_inode_reserve_breach_fails_on_first_host_sample(self):
        bad = host_sample(); bad['filesystems'][0]['available_inodes'] = 49
        tmp, _, result, _ = self.run_fixture(host=lambda: bad)
        try:
            self.assertEqual(result['status'], 'failed')
            self.assertIn('reserve breached', result['failure'])
        finally: tmp.cleanup()

    def test_unexpected_or_missing_owned_image_fails_with_healthy_containers(self):
        for images in ([ACTIVE_ID], [ACTIVE_ID, PRIOR_ID, 'sha256:' + '3' * 64], None):
            with self.subTest(images=images):
                bad = host_sample(); bad['owned_image_ids'] = images
                tmp, _, result, _ = self.run_fixture(host=lambda: bad)
                try:
                    self.assertEqual(result['status'], 'failed')
                    self.assertIn('owned application image set', result['failure'])
                finally: tmp.cleanup()

    def test_proxy_identity_uses_verified_image_digest_with_supported_kamal_tag(self):
        value = host_sample()
        value['proxy']['config_image'] = 'basecamp/kamal-proxy:v0.9.2'
        errors, _ = observe.validate_host(value, config())
        self.assertEqual(errors, [])
        value['proxy']['image_repo_digests'] = ['basecamp/kamal-proxy@sha256:' + '0' * 64]
        errors, _ = observe.validate_host(value, config())
        self.assertTrue(any('proxy identity' in error for error in errors))

    def test_application_container_name_supplies_identity_without_version_label(self):
        value=host_sample()
        for container in value['apps']:
            container['version']=None
        errors,_=observe.validate_host(value,config())
        self.assertEqual(errors,[])

        value['apps'][0]['version']='wrong-version'
        errors,_=observe.validate_host(value,config())
        self.assertTrue(any('version label conflicts' in error for error in errors))

    def test_container_restart_or_identity_change_fails(self):
        count = 0
        def changing_host():
            nonlocal count
            count += 1; value=host_sample()
            if count == 2: value['apps'][0]['restart_count'] += 1
            return value
        tmp, _, result, _ = self.run_fixture(host=changing_host)
        try:
            self.assertEqual(result['status'], 'failed')
            self.assertIn('container identity', result['failure'])
        finally: tmp.cleanup()

    def test_updater_reenable_or_unresolved_lock_fails(self):
        bad=host_sample(); bad['updater']['timer']['enabled']='enabled'
        tmp,_,result,_=self.run_fixture(host=lambda: bad)
        try:
            self.assertEqual(result['status'],'failed')
            self.assertIn('updater timer',result['failure'])
        finally: tmp.cleanup()

    def test_embedded_filesystem_and_systemd_collector_functions(self):
        source=observe.remote_script({'min_free_bytes':1,'min_free_inodes':1})
        source=source[:source.index("\ntry:\n    print(json.dumps(inspect_all()")]
        namespace={'__name__':'fixture'}
        exec(compile(source,'<remote-collector-fixture>','exec'),namespace)
        with tempfile.TemporaryDirectory() as directory:
            device=str(os.stat(directory).st_dev)
            ready={'capacity':{device:{'paths':[directory],'reserve_bytes':1,'reserve_inodes':1}}}
            result=namespace['filesystems'](ready)
            self.assertEqual(result[0]['device'],device)
            self.assertEqual(result[0]['paths'][0]['path'],directory)
            self.assertGreaterEqual(result[0]['available_bytes'],1)
            self.assertGreaterEqual(result[0]['available_inodes'],1)
            namespace['EXPECTED']['min_free_bytes']=0
            with self.assertRaisesRegex(RuntimeError,'threshold is below'):
                namespace['filesystems'](ready)
        namespace['run']=lambda args:'UnitFileState=static\nActiveState=inactive'
        self.assertEqual(namespace['unit']('fixture.service'),{'active':'inactive','enabled':'static'})

    def test_embedded_docker_go_templates_render_selected_fields_and_networks(self):
        remote=observe.remote_script({'min_free_bytes':1,'min_free_inodes':1})
        function=next(node for node in ast.parse(remote).body
                      if isinstance(node,ast.FunctionDef) and node.name=='selected_container')
        templates={node.targets[0].id:ast.literal_eval(node.value)
                   for node in function.body if isinstance(node,ast.Assign)
                   and isinstance(node.targets[0],ast.Name)
                   and node.targets[0].id in ('template','network_template')}
        self.assertIn('"mounts":[{{range $i,$m := .Mounts}}',templates['template'])
        self.assertIn('{{range $name,$network := .NetworkSettings.Networks}}',templates['network_template'])
        go_source=Path(__file__).parent/'testdata'/'observe_template.go.txt'
        with tempfile.TemporaryDirectory() as directory:
            go_file=Path(directory)/'observe_template.go'
            go_file.write_bytes(go_source.read_bytes())
            binary=Path(directory)/'render-template'
            built=subprocess.run(['go','build','-o',str(binary),str(go_file)],capture_output=True,
                                 text=True,env={**os.environ,'GOWORK':'off','GO111MODULE':'off'},timeout=60)
            self.assertEqual(built.returncode,0,built.stderr)
            fixture={'Id':'a'*64,'Name':'/fixture','Image':'sha256:'+'b'*64,
              'Config':{'Image':'localhost:5555/leapview-site:kfixture','Labels':{'service':'leapview-site',
                  'role':'web','version':'kfixture','destination':'','org.opencontainers.image.revision':REVISION,
                  'com.docker.compose.project':'leapview-site','com.docker.compose.service':'caddy'},
                  'Env':['SECRET=must-not-be-selected']},
              'State':{'Status':'running','Running':True,'StartedAt':'2026-09-29T00:00:00Z','Health':None},
              'RestartCount':0,'HostConfig':{'RestartPolicy':{'Name':'unless-stopped'},'PortBindings':None},
              'Mounts':[{'Source':'/srv/Caddyfile','Destination':'/etc/caddy/Caddyfile','Type':'bind','RW':False}],
              'NetworkSettings':{'Networks':{'kamal':{'IPAddress':'10.0.0.2'},
                                              'loopback':{'IPAddress':'127.0.0.1'}}}}
            def render(template, data=fixture):
                result=subprocess.run([str(binary)],input=json.dumps({'template':template,'data':data}),
                                      capture_output=True,text=True,timeout=10)
                self.assertEqual(result.returncode,0,result.stderr)
                return result.stdout
            selected=json.loads(render(templates['template']))
            without_health=copy.deepcopy(fixture)
            without_health['State'].pop('Health')
            selected_without_health=json.loads(render(templates['template'],without_health))
            self.assertEqual(selected_without_health['health'],'none')
            selected['networks']=sorted(render(templates['network_template']).splitlines())
            self.assertEqual(selected['networks'],['kamal','loopback'])
            self.assertEqual(selected['service'],'leapview-site')
            self.assertEqual(selected['mounts'][0]['destination'],'/etc/caddy/Caddyfile')
            self.assertNotIn('Env',selected)

        bad=host_sample(); bad['locks']['deploy']['held']=True
        tmp,_,result,_=self.run_fixture(host=lambda: bad)
        try:
            self.assertEqual(result['status'],'failed')
            self.assertIn('lock state',result['failure'])
        finally: tmp.cleanup()

    def test_backing_filesystem_identity_change_fails(self):
        count=0
        def changing_host():
            nonlocal count
            count+=1; value=host_sample()
            if count==2: value['filesystems'][0]['paths'][0]['source']='/dev/sdb1'
            return value
        tmp, _, result, _ = self.run_fixture(host=changing_host)
        try:
            self.assertEqual(result['status'],'failed')
            self.assertIn('filesystem identity changed',result['failure'])
        finally: tmp.cleanup()

    def test_monitoring_gap_is_rejected_and_never_counted_as_a_probe(self):
        clock=FakeClock(); clock.oversleep=6
        tmp,out,result,events=self.run_fixture(clock=clock)
        try:
            self.assertEqual(result['status'],'failed')
            self.assertIn('monitoring gap',result['failure'])
            self.assertEqual(result['samples'],1)
            self.assertEqual(events[-1]['type'],'rejected')
        finally: tmp.cleanup()

    def test_wall_clock_jump_is_rejected(self):
        clock=FakeClock(); clock.jump_after_sleep=6
        tmp,_,result,events=self.run_fixture(clock=clock)
        try:
            self.assertEqual(result['status'],'failed')
            self.assertIn('clocks diverged',result['failure'])
            self.assertEqual(events[-1]['type'],'rejected')
        finally: tmp.cleanup()

    def test_permitted_positive_and_negative_wall_clock_corrections_pass(self):
        for correction in (-4, 4):
            with self.subTest(correction=correction):
                clock=FakeClock(); clock.jump_after_sleep=correction
                tmp,out,result,events=self.run_fixture(clock=clock)
                try:
                    self.assertEqual(result['status'],'health_storage_passed')
                    shifted=events[1]
                    self.assertEqual(shifted['elapsed_seconds'],60)
                    self.assertAlmostEqual(shifted['observed_wall_unix_seconds'] -
                                           result['clock_origin_wall_unix_seconds'],60 + correction,places=3)
                    self.assertEqual(shifted['observed_at'] < shifted['scheduled_at'],correction < 0)
                    self.assertAlmostEqual(result['ended_wall_unix_seconds'] -
                                           result['clock_origin_wall_unix_seconds'],
                                           result['elapsed_seconds'] + correction,places=3)
                finally: tmp.cleanup()

    def test_interrupted_process_status_is_not_success_or_resumable(self):
        with tempfile.TemporaryDirectory() as tmp:
            out=Path(tmp)
            sha=observe.observer_source_sha256()
            run_id='c'*32
            (out/'summary.json').write_text(json.dumps({'status':'running','samples':5,'observer_sha256':sha,'run_id':run_id})); os.chmod(out/'summary.json',0o600)
            (out/'process.json').write_text(json.dumps({'pid':987654321,'start_ticks':'1','boot_id':'old','observer_sha256':sha,'run_id':run_id})); os.chmod(out/'process.json',0o600)
            (out/'supervisor.json').write_text(json.dumps({'pid':987654321,'start_ticks':'1','boot_id':'old','observer_sha256':sha,'run_id':run_id})); os.chmod(out/'supervisor.json',0o600)
            with patch.object(observe,'proc_identity',side_effect=FileNotFoundError):
                summary,result=observe.status(out)
            self.assertEqual(summary['status'],'interrupted')
            self.assertIn('dead or its recorded identity/source is stale',summary['failure'])
            self.assertEqual(result,'interrupted')
            with patch.object(observe,'proc_identity',return_value={'state':'S','start_ticks':'1'}), \
                 patch.object(observe,'proc_boot_id',return_value='old'):
                summary,result=observe.status(out)
            self.assertEqual(result,'running')
            with patch.object(observe,'proc_identity',return_value={'state':'Z','start_ticks':'1'}), \
                 patch.object(observe,'proc_boot_id',return_value='old'):
                summary,result=observe.status(out)
            self.assertEqual(result,'interrupted')
            (out/'process.json').write_text(json.dumps({'pid':987654321,'start_ticks':'1','boot_id':'old',
                                                        'observer_sha256':'b'*64})); os.chmod(out/'process.json',0o600)
            with patch.object(observe,'proc_identity',return_value={'state':'S','start_ticks':'1'}), \
                 patch.object(observe,'proc_boot_id',return_value='old'):
                summary,result=observe.status(out)
            self.assertEqual(result,'interrupted')

    def test_ssh_config_uses_operator_fixture_without_connecting(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); os.chmod(root,0o700)
            cfg=root/'ssh_config'; pin=root/'host-fingerprint'; known=root/'known_hosts'; identity=root/'identity'
            for path,contents in ((cfg,'Host *\n IdentityFile '+str(identity)+'\n UserKnownHostsFile '+str(known)+'\n'),
                                  (pin,'SHA256:'+'A'*43+'\n'),(known,'fixture ssh-ed25519 AAAA\n'),
                                  (identity,'fixture-only-key')):
                path.write_text(contents); os.chmod(path,0o600)
            def fake_run(args,**kwargs):
                if args[0]=='ssh':
                    self.assertNotIn('SSH_AUTH_SOCK',kwargs['env'])
                    stdout='\n'.join((
                        'hostname fixture.example','user root','stricthostkeychecking yes',
                        'identitiesonly yes','batchmode yes','port 22','proxycommand none',
                        'proxyjump none','userknownhostsfile '+str(known),'identityfile '+str(identity)))+'\n'
                elif args[1:3]==['-F','fixture.example']:
                    stdout='fixture.example ssh-ed25519 AAAA\n'
                else:
                    stdout='256 SHA256:'+'A'*43+' fixture (ED25519)\n'
                return subprocess.CompletedProcess(args,0,stdout,'')
            with patch.object(observe.subprocess,'run',side_effect=fake_run):
                resolved=observe.verify_ssh_config(cfg,pin,'fixture.example')
            self.assertEqual(resolved,str(cfg.resolve()))
            cfg.write_text('Host another.example\n IdentityFile '+str(identity)+'\n'); os.chmod(cfg,0o600)
            with self.assertRaisesRegex(ValueError,'explicitly provide an IdentityFile'):
                observe.explicit_ssh_identity_paths(cfg,'fixture.example')

    def test_relative_ssh_identity_and_known_hosts_paths_fail_before_ssh_g(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); os.chmod(root,0o700)
            cfg=root/'ssh_config'; pin=root/'host-fingerprint'; known=root/'known_hosts'; identity=root/'identity'
            for path,contents in ((pin,'SHA256:'+'A'*43+'\n'),(known,'fixture ssh-ed25519 AAAA\n'),
                                  (identity,'fixture-only-key')):
                path.write_text(contents); os.chmod(path,0o600)
            cases=(('IdentityFile relative-key\n UserKnownHostsFile '+str(known)+'\n','IdentityFile'),
                   ('IdentityFile '+str(identity)+'\n UserKnownHostsFile relative-known-hosts\n','UserKnownHostsFile'))
            for directives, label in cases:
                cfg.write_text('Host fixture.example\n '+directives); os.chmod(cfg,0o600)
                with self.subTest(label=label), patch.object(observe.subprocess,'run') as command:
                    with self.assertRaisesRegex(ValueError,'explicit absolute path'):
                        observe.verify_ssh_config(cfg,pin,'fixture.example')
                    command.assert_not_called()

    def test_real_ssh_g_resolves_absolute_local_fixture_without_connecting(self):
        if not shutil.which('ssh') or not shutil.which('ssh-keygen'):
            self.skipTest('OpenSSH tools are unavailable')
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); os.chmod(root,0o700)
            identity=root/'fixture-identity'; known=root/'known_hosts'
            generated=subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f',str(identity)],
                                     capture_output=True,text=True,timeout=10)
            self.assertEqual(generated.returncode,0,generated.stderr)
            public=(root/'fixture-identity.pub').read_text().split()
            fingerprint=subprocess.run(['ssh-keygen','-lf',str(root/'fixture-identity.pub')],
                                        capture_output=True,text=True,timeout=10)
            self.assertEqual(fingerprint.returncode,0,fingerprint.stderr)
            pin=fingerprint.stdout.split()[1]
            target='fixture.example'
            known.write_text(target+' '+public[0]+' '+public[1]+'\n'); os.chmod(known,0o600)
            fingerprint_pin=root/'host-fingerprint'; fingerprint_pin.write_text(pin+'\n'); os.chmod(fingerprint_pin,0o600)
            cfg=root/'ssh_config'
            content=('Host '+target+'\n'
                     '  HostName '+target+'\n'
                     '  User root\n'
                     '  Port 22\n'
                     '  BatchMode yes\n'
                     '  IdentitiesOnly yes\n'
                     '  StrictHostKeyChecking yes\n'
                     '  IdentityFile '+str(identity)+'\n'
                     '  UserKnownHostsFile '+str(known)+'\n'
                     '  GlobalKnownHostsFile none\n')
            cfg.write_text(content); os.chmod(cfg,0o600)
            self.assertEqual(observe.verify_ssh_config(cfg,fingerprint_pin,target),str(cfg.resolve()))

    def test_record_validation_rejects_wrong_release_or_runtime(self):
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'record.json'
            record={'schema':1,'kamal':'2.12.0','revision':REVISION,
                    'image':ACTIVE_IMAGE,'version':ACTIVE_VERSION,
                    'platform':'sha256:'+'3'*64,'config':'sha256:'+'4'*64,
                    'runtime':{'port':8081,'user':'65532:65532','base_url':'https://leapview.dev',
                               'tmpfs_mib':64,'log_size':'10m','log_files':3}}
            path.write_text(json.dumps(record)); os.chmod(path,0o600)
            self.assertEqual(observe.read_record(path)['version'],ACTIVE_VERSION)
            record['runtime']['port']=8080; path.write_text(json.dumps(record))
            with self.assertRaises(ValueError): observe.read_record(path)

    def test_target_and_base_url_validation(self):
        self.assertEqual(observe.validate_target('203.0.113.8'),'203.0.113.8')
        self.assertEqual(observe.validate_target('SITE.example'),'site.example')
        self.assertEqual(observe.validate_target('2001:db8::1'),'2001:db8::1')
        for bad in ('-oProxyCommand=touch','root@site.example','bad host','https://site.example'):
            with self.subTest(target=bad), self.assertRaises(ValueError): observe.validate_target(bad)
        self.assertEqual(observe.validate_base_url('https://site.example/'),'https://site.example')
        self.assertEqual(observe.validate_base_url('https://[2001:db8::2]:8443'),'https://[2001:db8::2]:8443')
        for bad in ('http://site.example','https://user:pass@site.example','https://site.example/api',
                    'https://site.example/?x=1','https://site.example:bad'):
            with self.subTest(base_url=bad), self.assertRaises(ValueError): observe.validate_base_url(bad)

    def test_schedule_and_thresholds_cannot_override_production_contract(self):
        with tempfile.TemporaryDirectory() as tmp:
            cfg=config(duration=120)
            with self.assertRaisesRegex(ValueError,'fixed at 86400/60/900/15'):
                observe.run_observation(cfg,Path(tmp)/'not-created')
            self.assertFalse((Path(tmp)/'not-created').exists())
            cfg=config(duration=observe.DURATION,probe=observe.PROBE_INTERVAL,host=observe.HOST_INTERVAL)
            cfg['min_free_inodes']=0
            with self.assertRaisesRegex(ValueError,'thresholds must be positive'):
                observe.run_observation(cfg,Path(tmp)/'not-created')
            self.assertFalse((Path(tmp)/'not-created').exists())

    def test_prepare_config_pins_operator_inputs_and_fixed_source_contract(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); os.chmod(root,0o700)
            record=root/'record.json'; ssh=root/'ssh_config'; pin=root/'fingerprint'
            value={'schema':1,'kamal':'2.12.0','revision':REVISION,'image':ACTIVE_IMAGE,
                   'version':ACTIVE_VERSION,'platform':'sha256:'+'3'*64,'config':'sha256:'+'4'*64,
                   'runtime':{'port':8081,'user':'65532:65532','base_url':'https://leapview.dev',
                              'tmpfs_mib':64,'log_size':'10m','log_files':3}}
            for path,contents in ((record,json.dumps(value)),(ssh,'fixture'),(pin,'fingerprint')):
                path.write_text(contents); os.chmod(path,0o600)
            with patch.object(observe,'verify_ssh_config',return_value=str(ssh)):
                cfg=observe.prepare_config(record,PRIOR_VERSION,ACTIVE_ID,PRIOR_ID,500,50,ssh,pin,
                                           'fixture.example','https://site.example')
            self.assertEqual(cfg['duration_seconds'],86400)
            self.assertEqual(cfg['probe_interval_seconds'],60)
            self.assertEqual(cfg['host_interval_seconds'],900)
            self.assertEqual(cfg['target'],'fixture.example')
            self.assertEqual(cfg['base_url'],'https://site.example')
            self.assertEqual(cfg['observer_sha256'],observe.observer_source_sha256())
            self.assertEqual(cfg['input_files']['record']['path'],str(record.resolve()))
            self.assertRegex(cfg['input_files']['record']['sha256'],r'^[0-9a-f]{64}$')


if __name__ == '__main__': unittest.main()

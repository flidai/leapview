"""Cold capacity measurement for an exact admitted public-site image.

Run only via sudo unshare --mount --propagation private. No production access.
Three retained/candidate images exercise cold pulls, boot, rollback and cleanup.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import threading
import time
import urllib.error
import urllib.request

from contract import validate_record
from capacity_contract import validate_inputs, verify_runtime, require


def cleanup_runtime(runtime, socket, environment, daemon, containerd, mount, root, logs, mounted):
    failures = []
    for process, name in [(daemon, 'docker'), (containerd, 'containerd')]:
        if process is None or process.poll() is not None:
            continue
        if name == 'docker':
            try:
                subprocess.run([str(runtime/'docker/docker'), '--host=unix://'+str(socket),
                                'container', 'rm', '--force', 'candidate', 'active'],
                               env=environment, capture_output=True, timeout=30)
            except Exception as error:
                failures.append('private container cleanup: '+str(error))
        try:
            process.terminate()
            try:
                process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)
        except Exception as error:
            failures.append('private '+name+' termination: '+str(error))
    for stream, name in logs:
        try:
            if stream:
                stream.close()
            if (mount/name).exists():
                (root/name).write_bytes((mount/name).read_bytes())
        except Exception as error:
            failures.append('private log retention: '+str(error))
    if mounted:
        try:
            subprocess.run(['umount', str(mount)], check=True, capture_output=True)
        except Exception as error:
            failures.append('private filesystem unmount: '+str(error))
    return failures

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--candidate', type=Path, required=True)
    parser.add_argument('--baseline', type=Path, required=True)
    parser.add_argument('--runtime', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    runtime_identity = verify_runtime(args.runtime)
    RUNTIME = args.runtime / 'runtime'
    candidate_bytes = args.candidate.read_bytes()
    baseline_bytes = args.baseline.read_bytes()
    candidate = validate_record(json.loads(candidate_bytes))
    status = json.loads(baseline_bytes)
    records = validate_inputs(candidate, status)
    require(os.geteuid() == 0, 'Root required for disposable mount')
    require(os.readlink('/proc/self/ns/mnt') != os.readlink('/proc/1/ns/mnt'), 'Private mount namespace required')
    root = args.output.resolve()
    root.mkdir(mode=0o700)  # Refuse a reused measurement directory.
    mount = root / 'storage'
    mount.mkdir()
    volume = root / 'storage.ext4'
    outer = os.statvfs(root)
    if outer.f_bavail * outer.f_frsize < 11 * 1024**3:
        raise RuntimeError('Disposable volume plus 3 GiB outer reserve required')
    report = {'schema':1, 'candidate':candidate['image'], 'candidate_record_sha256':hashlib.sha256(candidate_bytes).hexdigest(), 'baseline_sha256':hashlib.sha256(baseline_bytes).hexdigest(), 'runtime_artifacts':runtime_identity, 'candidate_revision':candidate['revision'], 'qualified_images':[r['image'] for r in records], 'production_changed':False, 'scope':'cold disposable ext4 filesystem; Docker 29.1.3 / containerd 2.2.1 overlayfs; pull, boot, switch, rollback, retained-image cleanup', 'commands':[], 'samples':[], 'checks':[]}
    stop = threading.Event()
    sampler = None
    daemon = containerd = daemon_log = containerd_log = None
    environment, sock = {}, root / 'docker.sock'
    mounted = False
    try:
        with volume.open('xb') as stream:
            stream.truncate(8 * 1024**3)
        subprocess.run(['mkfs.ext4', '-F', '-q', str(volume)], check=True)
        subprocess.run(['mount', '-o', 'loop,noatime', str(volume), str(mount)], check=True)
        mounted = True
        active, prior = records[1:]
        environment = os.environ.copy()
        for key in ['DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH']:
            environment.pop(key, None)
        anonymous_config=root/'docker-client'
        anonymous_config.mkdir(mode=0o700)
        (anonymous_config/'config.json').write_text('{}\n')
        environment['DOCKER_CONFIG']=str(anonymous_config)
        environment['PATH'] = str(RUNTIME / 'containerd/bin') + ':' + str(RUNTIME / 'docker') + ':' + environment['PATH']
        sock = root / 'docker.sock'
        daemon_config = root / 'daemon.json'
        daemon_config.write_text(json.dumps({'features':{'containerd-snapshotter':True}}))
        containerd_config=root/'containerd.toml'
        containerd_config.write_text('version = 3\ndisabled_plugins = ["io.containerd.cri.v1.runtime", "io.containerd.cri.v1.images"]\n')
        containerd_socket=root/'containerd.sock'
        containerd_log=(mount/'containerd.log').open('w')
        containerd=subprocess.Popen([str(RUNTIME/'containerd/bin/containerd'),'--config',str(containerd_config),'--root',str(mount/'containerd'),'--state',str(mount/'containerd-run'),'--address',str(containerd_socket)],env=environment,stdout=containerd_log,stderr=containerd_log)
        daemon_log = (mount / 'daemon.log').open('w')
        daemon = subprocess.Popen([str(RUNTIME/'docker/dockerd'), '--host=unix://'+str(sock), '--data-root='+str(mount/'docker'), '--exec-root='+str(mount/'run'), '--pidfile='+str(root/'dockerd.pid'), '--config-file='+str(daemon_config), '--containerd='+str(containerd_socket), '--exec-opt=native.cgroupdriver=cgroupfs', '--cgroup-parent=/leapview-site-capacity-'+hashlib.sha256(str(root).encode()).hexdigest()[:16], '--bridge=none', '--iptables=false', '--ip6tables=false', '--ip-forward=false', '--ip-masq=false', '--userland-proxy=false'], env=environment, stdout=daemon_log, stderr=daemon_log)
        def docker(*argv, timeout=120):
            command = [str(RUNTIME/'docker/docker'), '--host=unix://'+str(sock), *argv]
            result = subprocess.run(command, env=environment, capture_output=True, text=True, timeout=timeout)
            report['commands'].append({'argv':list(argv), 'exit_code':result.returncode, 'stdout_sha256':hashlib.sha256(result.stdout.encode()).hexdigest()})
            if result.returncode:
                raise RuntimeError('Private Docker command failed: '+repr(argv)+' '+result.stderr[-2000:])
            return result.stdout
        sampling_failures = []
        def sample():
            outer = os.statvfs(root)
            if outer.f_bavail * outer.f_frsize < 3 * 1024**3:
                raise RuntimeError('Disposable backing filesystem reserve below 3 GiB')
            st = os.statvfs(mount)
            report['samples'].append({'time':time.time(), 'available_bytes':st.f_bavail*st.f_frsize, 'available_inodes':st.f_favail})
        def sampling():
            while not stop.wait(0.1):
                try:
                    sample()
                except Exception as error:
                    sampling_failures.append(str(error))
                    stop.set()
        def check(record):
            port=19042
            url='http://127.0.0.1:'+str(port)
            for attempt in range(60):
                try:
                    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(url+'/build.json', timeout=2) as response:
                        build=json.load(response)
                    require(build['revision']==record['revision'] and build['image']==record['image'], 'private build identity differs')
                    for path in ['/healthz','/readyz','/','/docs','/sitemap.xml']:
                        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(url+path, timeout=2) as response:
                            body=response.read().decode()
                            require(response.status==200, 'private route unhealthy')
                    report['checks'].append({'image':record['image'],'build_identity':True,'health':True,'routes':['/healthz','/readyz','/','/docs','/sitemap.xml']})
                    return
                except (urllib.error.URLError, TimeoutError, ConnectionError):
                    time.sleep(0.5)
            raise RuntimeError('Private site failed to become ready')
        def boot(record,name,port=19042):
            docker('run','-d','--pull=never','--name',name,'--network=host','--user','65532:65532','--read-only','--cap-drop=ALL','--security-opt','no-new-privileges','--tmpfs','/tmp:rw,noexec,nosuid,size=64m','--log-driver=json-file','--log-opt','max-size=10m','--log-opt','max-file=3','-e','LEAPVIEW_SITE_BASE_URL=https://leapview.dev',record['image'],'-addr=127.0.0.1:'+str(port),'-image-reference='+record['image'])
            container=json.loads(docker('container','inspect',name))[0]
            require(container['ImageManifestDescriptor']['digest']==record['platform'], 'private container manifest differs')
            require(container['Config']['Image']==record['image'], 'private container image differs')
        for attempt in range(90):
            if daemon.poll() is not None:
                raise RuntimeError('Private daemon exited; inspect daemon.log')
            try:
                info = json.loads(docker('info','--format','{{json .}}',timeout=5))
                break
            except (RuntimeError,subprocess.TimeoutExpired):
                time.sleep(0.5)
        else:
            raise RuntimeError('Private daemon startup deadline expired')
        require(info['ServerVersion']=='29.1.3' and info['Driver']=='overlayfs', 'private runtime differs')
        require(not docker('image','ls','--quiet').strip(), 'Cold image store required')
        report['runtime']={'docker':info['ServerVersion'],'driver':info['Driver'],'containerd':subprocess.check_output([str(RUNTIME/'containerd/bin/containerd'),'--version'],text=True).strip(),'kernel':os.uname().release}
        sample()
        baseline = report['samples'][0]
        sampler=threading.Thread(target=sampling,daemon=True)
        sampler.start()
        for record in records:
            docker('pull','--platform=linux/amd64',record['image'],timeout=900)
            image=json.loads(docker('image','inspect',record['image']))[0]
            require(record['image'] in image['RepoDigests'], 'private pulled digest differs')
            require(image['Architecture']=='amd64' and image['Os']=='linux', 'private pulled platform differs')
            require(image['Config']['Labels']['org.opencontainers.image.revision']==record['revision'], 'private pulled revision differs')
            report['checks'].append({'image':record['image'],'selected_config_verified':True})
            sample()
        boot(active,'active')
        check(active)
        docker('stop','active')
        boot(candidate,'candidate')
        check(candidate)
        docker('stop','candidate')
        docker('start','active')
        check(active)
        docker('stop','active')
        docker('start','candidate')
        check(candidate)
        sample()
        # Only this private daemon's exact old-prior image is removed.
        docker('image','rm',prior['image'])
        require(len(set(docker('image','ls','--quiet').splitlines()))==2, 'private cleanup retained image count differs')
        sample()
        stop.set()
        sampler.join()
        if sampling_failures:
            raise RuntimeError('; '.join(sampling_failures))
        report['measured_peak_bytes']=max(1,baseline['available_bytes']-min(s['available_bytes'] for s in report['samples']))
        report['measured_peak_inodes']=max(1,baseline['available_inodes']-min(s['available_inodes'] for s in report['samples']))
        report['qualified_compressed_bytes']=max(r['compressed_bytes'] for r in records)
        report['lifecycle']=['cold-pull-candidate-active-prior','boot-active','boot-candidate','rollback-active','restart-candidate','remove-private-prior']
        report['passed']=True
    except Exception as error:
        report['passed']=False
        report['failure']=repr(error)
        raise
    finally:
        stop.set()
        if sampler:
            sampler.join(timeout=5)
        cleanup_failures = cleanup_runtime(RUNTIME, sock, environment, daemon, containerd,
            mount, root, [(containerd_log, 'containerd.log'), (daemon_log, 'daemon.log')], mounted)
        if cleanup_failures:
            report['passed'] = False
            report['cleanup_failures'] = cleanup_failures
        receipt=root/'measurement.json'
        receipt.write_text(json.dumps(report,indent=2)+'\n')
        receipt.chmod(0o600)
        if os.environ.get('SUDO_UID'):
            uid,gid=int(os.environ['SUDO_UID']),int(os.environ['SUDO_GID'])
            os.chown(root,uid,gid)
            os.chown(receipt,uid,gid)
        if cleanup_failures:
            raise RuntimeError('; '.join(cleanup_failures))
    print(json.dumps({key:report[key] for key in ['passed','candidate','measured_peak_bytes','measured_peak_inodes','qualified_compressed_bytes']}))

if __name__ == '__main__':
    main()

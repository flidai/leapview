#!/usr/bin/env python3
"""Read-only, fail-closed 24-hour observation of the LeapView public site."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import datetime as dt
import fnmatch
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import signal
import shlex
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_BASE_URL = 'https://leapview.dev'
SERVICE = 'leapview-site'
PROXY_DIGEST = 'sha256:826a6f66c6ba26ac26197ac8755804403c9bb617b90cfac25c7972154c5328ab'
DURATION = 24 * 60 * 60
PROBE_INTERVAL = 60
HOST_INTERVAL = 15 * 60
GAP_GRACE = 15
CLOCK_TOLERANCE = 5


class ObservationFailure(Exception):
    pass


def utc(ts):
    return dt.datetime.fromtimestamp(ts, dt.timezone.utc).isoformat(timespec='seconds').replace('+00:00', 'Z')


def require_sha(value, label):
    if not isinstance(value, str) or not re.fullmatch(r'sha256:[a-f0-9]{64}', value):
        raise ValueError(label + ' must be a full sha256 identity')


def read_record(path):
    p = Path(path)
    st = p.lstat()
    if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
        raise ValueError('record must be a regular operator-owned file not writable by other users')
    d = json.loads(p.read_text())
    image = d.get('image', '')
    match = re.fullmatch(r'ghcr\.io/flidai/leapview-site@(sha256:[a-f0-9]{64})', image)
    if (d.get('schema') != 1 or d.get('kamal') != '2.12.0'
            or not re.fullmatch(r'[a-f0-9]{40}', d.get('revision', ''))
            or not match or d.get('version') != 'k' + match.group(1).split(':', 1)[1]
            or not re.fullmatch(r'sha256:[a-f0-9]{64}', d.get('platform', ''))
            or not re.fullmatch(r'sha256:[a-f0-9]{64}', d.get('config', ''))
            or d.get('runtime') != {'port':8081,'user':'65532:65532','base_url':'https://leapview.dev',
                                    'tmpfs_mib':64,'log_size':'10m','log_files':3}):
        raise ValueError('admitted site record has an invalid identity')
    return {'version': d['version'], 'revision': d['revision'], 'image': image}


def proc_identity(pid):
    raw = Path('/proc/' + str(pid) + '/stat').read_text()
    tail = raw[raw.rfind(')') + 2:].split()
    if len(tail) <= 19:
        raise ValueError('invalid process stat')
    return {'state':tail[0], 'start_ticks':tail[19]}


def proc_boot_id():
    return Path('/proc/sys/kernel/random/boot_id').read_text().strip()


def file_sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def validate_target(value):
    if not isinstance(value, str) or not value or value.startswith('-') or any(c.isspace() for c in value):
        raise ValueError('target must be a hostname or IP address')
    try:
        return str(ipaddress.ip_address(value))
    except ValueError:
        if (len(value) > 253 or value.endswith('.')
                or not re.fullmatch(r'(?=.{1,253}$)[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*', value)):
            raise ValueError('target must be a hostname or IP address')
        return value.lower()


def validate_base_url(value):
    if not isinstance(value, str):
        raise ValueError('base URL must be an HTTPS origin')
    try:
        parts=urllib.parse.urlsplit(value)
        _=parts.port
    except ValueError as exc:
        raise ValueError('base URL must be a valid HTTPS origin') from exc
    if (parts.scheme.lower()!='https' or not parts.hostname or parts.username is not None
            or parts.password is not None or parts.path not in ('','/') or parts.query or parts.fragment):
        raise ValueError('base URL must be an HTTPS origin without credentials, path, query, or fragment')
    hostname=validate_target(parts.hostname)
    if parts.port==0:
        raise ValueError('base URL port must be between 1 and 65535')
    authority=('['+hostname+']' if ':' in hostname else hostname)
    if parts.port is not None: authority+=':'+str(parts.port)
    return 'https://' + authority


def observer_source_sha256():
    return file_sha256(__file__)


def ssh_environment():
    allowed={'PATH','HOME','USER','LOGNAME','LANG','LC_ALL'}
    env={name:value for name,value in os.environ.items() if name in allowed}
    env['LC_ALL']='C'
    return env


def explicit_ssh_identity_paths(config_path, target):
    paths=[]
    target=validate_target(target)
    host_patterns=None
    active=True
    for line in Path(config_path).read_text().splitlines():
        try: fields=shlex.split(line,comments=True,posix=True)
        except ValueError as exc: raise ValueError('SSH config contains invalid quoting') from exc
        if not fields: continue
        option=fields[0].lower()
        if '=' in option:
            option,first=option.split('=',1)
            fields=[option,first,*fields[1:]]
        if option=='host':
            if len(fields)<2: raise ValueError('SSH Host directive requires a pattern')
            host_patterns=[x.lower() for x in fields[1:]]
            active=not any(pat.startswith('!') and fnmatch.fnmatchcase(target,pat[1:])
                           for pat in host_patterns)
            positives=[pat for pat in host_patterns if not pat.startswith('!')]
            active=active and (not positives or any(fnmatch.fnmatchcase(target,pat) for pat in positives))
            continue
        if option=='include':
            raise ValueError('SSH config must be self-contained; Include is not allowed')
        if option=='match': raise ValueError('SSH Match blocks are not allowed in the pinned observer config')
        if option!='identityfile' or not active: continue
        if len(fields)!=2 or '$' in fields[1] or '%' in fields[1]:
            raise ValueError('SSH IdentityFile must be an explicit literal path')
        path=Path(fields[1]).expanduser()
        if not path.is_absolute(): path=Path.cwd()/path
        paths.append(str(path.resolve()))
    if not paths:
        raise ValueError('SSH config must explicitly provide an IdentityFile')
    return set(paths)


def read_json_file(path):
    p = Path(path)
    st = p.lstat()
    if not stat.S_ISREG(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022:
        raise RuntimeError('protected host state is unavailable')
    return json.loads(p.read_text())


REMOTE_COLLECTOR = r'''import hashlib,json,os,re,stat,subprocess,sys
EXPECTED=json.loads(__EXPECTED_JSON__)
ROOT='/var/lib/leapview-site/kamal'
SITE='/opt/leapview-site'

def run(args, timeout=15):
    p=subprocess.run(args,capture_output=True,text=True,timeout=timeout)
    if p.returncode: raise RuntimeError('read-only host command failed')
    return p.stdout.strip()

def protected(path):
    st=os.lstat(path)
    if not stat.S_ISREG(st.st_mode) or st.st_uid!=0 or st.st_mode&0o022:
        raise RuntimeError('protected host state invalid')
    with open(path,encoding='utf-8') as f: return json.load(f)

def selected_container(name):
    template=r''' + "'''" + r'''{"id":{{json .Id}},"name":{{json .Name}},"image_id":{{json .Image}},"config_image":{{json .Config.Image}},"service":{{json (index .Config.Labels "service")}},"role":{{json (index .Config.Labels "role")}},"version":{{json (index .Config.Labels "version")}},"destination":{{json (index .Config.Labels "destination")}},"revision":{{json (index .Config.Labels "org.opencontainers.image.revision")}},"compose_project":{{json (index .Config.Labels "com.docker.compose.project")}},"compose_service":{{json (index .Config.Labels "com.docker.compose.service")}},"status":{{json .State.Status}},"running":{{json .State.Running}},"started_at":{{json .State.StartedAt}},"health":{{with index .State "Health"}}{{json .Status}}{{else}}"none"{{end}},"restart_count":{{json .RestartCount}},"restart_policy":{{json .HostConfig.RestartPolicy.Name}},"ports":{{json .HostConfig.PortBindings}},"mounts":[{{range $i,$m := .Mounts}}{{if $i}},{{end}}{"source":{{json $m.Source}},"destination":{{json $m.Destination}},"type":{{json $m.Type}},"rw":{{json $m.RW}}}{{end}}]}''' + "'''" + r'''
    network_template=r''' + "'''" + r'''{{range $name,$network := .NetworkSettings.Networks}}{{printf "%s\n" $name}}{{end}}''' + "'''" + r'''
    result=json.loads(run(['docker','inspect','--format',template,name]))
    result['networks']=sorted(run(['docker','inspect','--format',network_template,name]).splitlines())
    return result

def labels(c): return c.get('service'),c.get('role'),c.get('version'),c.get('destination')

def mount_identity(path):
    target=os.path.realpath(path)
    best=None
    with open('/proc/self/mountinfo',encoding='utf-8') as f:
        for line in f:
            left,right=line.rstrip('\n').split(' - ',1)
            cols=left.split(); tail=right.split()
            mp=re.sub(r'\\([0-7]{3})',lambda m:chr(int(m.group(1),8)),cols[4])
            if target==mp or target.startswith(mp.rstrip('/')+'/'):
                if best is None or len(mp)>len(best[0]): best=(mp,cols[2],tail[0],tail[1])
    if best is None: raise RuntimeError('backing filesystem mount not found')
    return {'mountpoint':best[0],'major_minor':best[1],'fstype':best[2],'source':best[3]}

def filesystems(ready):
    budgets=ready.get('capacity')
    if not isinstance(budgets,dict) or not budgets: raise RuntimeError('qualified capacity policy missing')
    grouped={}
    for device,budget in budgets.items():
        if not isinstance(budget,dict) or not isinstance(budget.get('paths'),list) or not budget['paths']:
            raise RuntimeError('qualified capacity paths missing')
        for path in budget['paths']:
            st=os.stat(path); sv=os.statvfs(path); key=str(st.st_dev)
            item={'path':path,'device':key,'capacity_bytes':sv.f_blocks*sv.f_frsize,**mount_identity(path)}
            group=grouped.setdefault(key,{'device':key,'capacity_bytes':item['capacity_bytes'],'paths':[],'available_bytes':None,'available_inodes':None})
            if str(device)!=key or group['capacity_bytes']!=item['capacity_bytes']:
                raise RuntimeError('qualified filesystem identity changed')
            group['paths'].append(item)
            avail=sv.f_bavail*sv.f_frsize; inos=sv.f_favail
            group['available_bytes']=avail if group['available_bytes'] is None else min(group['available_bytes'],avail)
            group['available_inodes']=inos if group['available_inodes'] is None else min(group['available_inodes'],inos)
        if type(budget.get('reserve_bytes')) is not int or type(budget.get('reserve_inodes')) is not int:
            raise RuntimeError('qualified capacity reserves missing')
        if EXPECTED['min_free_bytes']<budget['reserve_bytes'] or EXPECTED['min_free_inodes']<budget['reserve_inodes']:
            raise RuntimeError('observer threshold is below the admitted reserve')
    return sorted(grouped.values(),key=lambda x:x['device'])

def unit(name):
    values={}
    for line in run(['systemctl','show','--property=ActiveState','--property=UnitFileState',name]).splitlines():
        if '=' in line:
            key,value=line.split('=',1); values[key]=value
    if not values.get('ActiveState') or not values.get('UnitFileState'):
        raise RuntimeError('updater systemd state unavailable')
    return {'active':values['ActiveState'],'enabled':values['UnitFileState']}

def held(path):
    try: st=os.stat(path)
    except FileNotFoundError: return {'exists':False,'held':True}
    wanted=(os.major(st.st_dev),os.minor(st.st_dev),st.st_ino)
    with open('/proc/locks',encoding='utf-8') as f:
        for line in f:
            cols=line.split()
            if len(cols)>6 and cols[1] in ('FLOCK','POSIX'):
                try:
                    major_s,minor_s,inode_s=cols[5].split(':')
                    major,minor,inode=int(major_s,16),int(minor_s,16),int(inode_s)
                except Exception: continue
                if (major,minor,inode)==wanted: return {'exists':True,'held':True}
    return {'exists':True,'held':False}

def inspect_all():
    root_st=os.lstat(ROOT)
    if not stat.S_ISDIR(root_st.st_mode) or root_st.st_uid!=0 or root_st.st_mode&0o077:
        raise RuntimeError('protected handover directory invalid')
    state=protected(ROOT+'/state.json'); ready=protected(ROOT+'/ready.json')
    if ready.get('schema')!=1 or ready.get('controller')!='kamal' or ready.get('handover_verified') is not True:
        raise RuntimeError('handover readiness invalid')
    if state.get('schema')!=1 or state.get('active')!=EXPECTED['active_version'] or state.get('prior')!=EXPECTED['prior_version']:
        raise RuntimeError('active/prior state identity changed')
    if state.get('pending') or state.get('maintenance_pending'):
        raise RuntimeError('unresolved deployment or maintenance state')
    records=state.get('records',{})
    active=records.get(EXPECTED['active_version'],{}); prior=records.get(EXPECTED['prior_version'],{})
    if (active.get('version')!=EXPECTED['active_version'] or active.get('verified') is not True
            or active.get('local_id')!=EXPECTED['active_image_id']
            or active.get('image')!=EXPECTED['active_image'] or active.get('revision')!=EXPECTED['revision']
            or prior.get('version')!=EXPECTED['prior_version'] or prior.get('verified') is not True
            or prior.get('local_id')!=EXPECTED['prior_image_id']):
        raise RuntimeError('protected admitted image records changed')
    for filename,key in (('compose.yaml','compose_sha256'),('Caddyfile','caddy_sha256')):
        actual=hashlib.sha256(open(SITE+'/'+filename,'rb').read()).hexdigest()
        if actual!=ready.get('topology',{}).get(key): raise RuntimeError('persistent topology hash changed')
    timer=unit('leapview-site-reconcile.timer'); service=unit('leapview-site-reconcile.service')
    if timer['active']!='inactive' or timer['enabled'] not in ('disabled','masked'):
        raise RuntimeError('legacy updater timer is not disabled')
    if service['active']!='inactive' or service['enabled'] not in ('static','disabled','masked','indirect'):
        raise RuntimeError('legacy updater service is not inactive')
    owner=os.path.lexists(ROOT+'/owner.json')
    sock=os.path.lexists(ROOT+'/operator.sock')
    locks={'reconcile':held(SITE+'/reconcile.lock'),'deploy':held(SITE+'/deploy.lock')}
    if owner or sock or any(x['held'] for x in locks.values()): raise RuntimeError('unresolved operator work or lock detected')
    ids=run(['docker','ps','--all','--quiet','--no-trunc','--filter','label=service=leapview-site']).split()
    if not ids or any(not re.fullmatch(r'[a-f0-9]{64}',cid) for cid in ids): raise RuntimeError('site container inventory invalid')
    apps=[selected_container(cid) for cid in sorted(ids)]
    owned_image_ids=sorted(set(run(['docker','image','ls','--quiet','--no-trunc','--filter','label=service=leapview-site']).split()))
    if owned_image_ids!=sorted({EXPECTED['active_image_id'],EXPECTED['prior_image_id']}):
        raise RuntimeError('owned application image set differs from retained A/B')
    caddy_ids=run(['docker','ps','--all','--quiet','--no-trunc','--filter','label=com.docker.compose.project=leapview-site','--filter','label=com.docker.compose.service=caddy']).split()
    if len(caddy_ids)!=1 or not re.fullmatch(r'[a-f0-9]{64}',caddy_ids[0]): raise RuntimeError('Caddy inventory invalid')
    proxy=selected_container('kamal-proxy'); caddy=selected_container(caddy_ids[0])
    proxy['image_repo_digests']=json.loads(run(['docker','image','inspect','--format','{{json .RepoDigests}}',proxy['image_id']])) or []
    fs=filesystems(ready)
    return {'boot_id':open('/proc/sys/kernel/random/boot_id').read().strip(),'filesystems':fs,
      'state':{'active':state['active'],'prior':state['prior'],'pending':state.get('pending'),
        'maintenance_pending':bool(state.get('maintenance_pending',False)),
        'active_local_id':active.get('local_id'),'prior_local_id':prior.get('local_id')},
      'updater':{'timer':timer,'service':service},'owner_journal':owner,'operator_socket':sock,'locks':locks,
      'apps':apps,'owned_image_ids':owned_image_ids,'proxy':proxy,'caddy':caddy}

try:
    print(json.dumps(inspect_all(),sort_keys=True,separators=(',',':')))
except Exception:
    sys.stderr.write('read-only host sample rejected\n'); sys.exit(2)
'''


def verify_ssh_config(config_path, fingerprint_path, target):
    target=validate_target(target)
    cfg_input=Path(config_path); fp_input=Path(fingerprint_path)
    if cfg_input.is_symlink() or fp_input.is_symlink():
        raise ValueError('SSH configuration and fingerprint pin may not be symlinks')
    cfg = cfg_input.resolve(strict=True)
    fp = fp_input.resolve(strict=True)
    for p in (cfg, fp):
        st = p.stat()
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
            raise ValueError('SSH configuration and fingerprint pin must be operator-owned protected regular files')
    pinned = fp.read_text().strip()
    if not re.fullmatch(r'SHA256:[A-Za-z0-9+/]{43}', pinned):
        raise ValueError('invalid pinned host fingerprint')
    explicit_identities=explicit_ssh_identity_paths(cfg,target)
    p = subprocess.run(['ssh', '-G', '-F', str(cfg), target], capture_output=True, text=True,
                       timeout=10,env=ssh_environment())
    if p.returncode:
        raise ValueError('pinned SSH configuration could not be resolved')
    resolved = {}
    for line in p.stdout.splitlines():
        fields = line.split(None, 1)
        if len(fields) == 2: resolved.setdefault(fields[0].lower(), []).append(fields[1].strip())
    one = lambda key: resolved.get(key, [''])[0].lower()
    if (one('hostname') != target.lower() or one('user') != 'root' or one('stricthostkeychecking') not in ('yes','true')
            or one('identitiesonly') != 'yes' or one('batchmode') != 'yes'
            or one('port') != '22'
            or one('proxycommand') not in ('', 'none') or one('proxyjump') not in ('', 'none')
            or one('identityagent') not in ('', 'none')
            or one('knownhostscommand') not in ('','none')
            or one('hostkeyalias') not in ('','none',target.lower())
            or one('remotecommand') not in ('','none')
            or one('permitlocalcommand') in ('yes','true')
            or one('forwardagent') in ('yes','true') or one('forwardx11') in ('yes','true')
            or any(resolved.get(key) for key in ('localforward','remoteforward','dynamicforward'))):
        raise ValueError('SSH config must pin direct root access without a proxy')
    kh_values = resolved.get('userknownhostsfile', [])
    if not kh_values: raise ValueError('pinned known-hosts file missing from SSH config')
    matched = False; host_fingerprints=set()
    for raw in kh_values:
        for hostfile in raw.split():
            if hostfile.lower() == 'none': continue
            kh = Path(hostfile).expanduser()
            if not kh.is_file(): continue
            found = subprocess.run(['ssh-keygen', '-F', target, '-f', str(kh)], capture_output=True, text=True, timeout=10)
            if found.returncode: continue
            fingerprints = subprocess.run(['ssh-keygen', '-lf', '-'], input=found.stdout,
                                          capture_output=True, text=True, timeout=10)
            seen = {line.split()[1] for line in fingerprints.stdout.splitlines() if len(line.split()) > 1}
            if seen:
                matched=True; host_fingerprints.update(seen)
    if not matched or host_fingerprints != {pinned}:
        raise ValueError('known-host entry does not match the reviewed fingerprint')
    identity_paths={str(Path(value).expanduser().resolve()) for value in resolved.get('identityfile', [])}
    if not identity_paths or not identity_paths.issubset(explicit_identities):
        raise ValueError('resolved SSH identities must all be explicit in the supplied SSH config')
    for value in identity_paths:
        key = Path(value)
        if not key.is_file(): raise ValueError('SSH identity file is unavailable')
        st = key.stat()
        if st.st_uid != os.getuid() or st.st_mode & 0o077:
            raise ValueError('SSH identity file must be operator-owned and inaccessible to group/other')
    return str(cfg)


def _read_http(url, expected_body=None, expected_build=None):
    started = time.monotonic()
    result = {'status': None, 'ok': False}
    try:
        request = urllib.request.Request(url, headers={'User-Agent': 'leapview-readonly-observer/1'})
        with urllib.request.urlopen(request, timeout=10) as response:
            body = response.read(8193)
            result['status'] = response.status
            result['canonical_url'] = response.geturl() == url
        if len(body) > 8192:
            return result
        if expected_build is not None:
            value = json.loads(body)
            result['revision'] = value.get('revision') if isinstance(value, dict) else None
            result['image'] = value.get('image') if isinstance(value, dict) else None
            result['ok'] = (result['status'] == 200 and result['canonical_url']
                            and isinstance(value, dict) and value.get('schemaVersion') == 1
                            and result['revision'] == expected_build['revision']
                            and result['image'] == expected_build['image'])
        else:
            result['ok'] = result['status'] == 200 and result['canonical_url'] and body.strip() == expected_body
    except urllib.error.HTTPError as exc:
        result['status'] = exc.code
    except Exception:
        result['error'] = 'request or response invalid'
    finally:
        result['endpoint_duration_ms'] = int(round(max(0.0, time.monotonic() - started) * 1000))
    return result


def probe_public(identity, base_url=DEFAULT_BASE_URL):
    base_url=validate_base_url(base_url)
    endpoints = {
        'healthz': (base_url + '/healthz', {'expected_body': b'ok'}),
        'readyz': (base_url + '/readyz', {'expected_body': b'ok'}),
        'build': (base_url + '/build.json', {'expected_build': identity}),
    }
    # Three workers bound concurrency to one request per required endpoint. Each
    # request retains _read_http's 10-second timeout and every result is required.
    with ThreadPoolExecutor(max_workers=3, thread_name_prefix='leapview-probe') as pool:
        futures = {name: pool.submit(_read_http, url, **kwargs)
                   for name, (url, kwargs) in endpoints.items()}
        results = {}
        for name, future in futures.items():
            try:
                results[name] = future.result()
            except Exception:
                results[name] = {'status': None, 'ok': False,
                                 'error': 'probe worker failed', 'endpoint_duration_ms': None}
    return results


def remote_script(expected):
    # JSON is embedded as a quoted Python literal in stdin, not shell arguments.
    import json as _json
    return REMOTE_COLLECTOR.replace('__EXPECTED_JSON__', repr(_json.dumps(expected)))


def collect_host(ssh_config, fingerprint_file, ssh_config_sha256, fingerprint_sha256,
                 identity, prior_version, active_id, prior_id, min_bytes, min_inodes, target):
    if file_sha256(ssh_config)!=ssh_config_sha256 or file_sha256(fingerprint_file)!=fingerprint_sha256:
        raise ObservationFailure('pinned SSH input changed during observation')
    verify_ssh_config(ssh_config,fingerprint_file,target)
    expected = {'active_version': identity['version'], 'revision': identity['revision'], 'active_image': identity['image'],
                'active_image_id': active_id, 'prior_version': prior_version, 'prior_image_id': prior_id,
                'min_free_bytes': min_bytes, 'min_free_inodes': min_inodes}
    p = subprocess.run(['ssh', '-F', ssh_config, '-T', '-o', 'IdentityAgent=none',
                        '-o', 'UpdateHostKeys=no', '-o', 'ControlMaster=no',
                        '-o', 'ControlPath=none', validate_target(target), 'python3', '-'],
                       env=ssh_environment(),
                       input=remote_script(expected), capture_output=True, text=True, timeout=35)
    if p.returncode:
        raise ObservationFailure('remote read-only host sample failed')
    if len(p.stdout) > 200000:
        raise ObservationFailure('remote host sample exceeded its size limit')
    try: return json.loads(p.stdout)
    except Exception as exc: raise ObservationFailure('remote host sample was not valid JSON') from exc


def _container_key(c):
    fields = ('id','name','image_id','config_image','service','role','version','destination','revision',
              'compose_project','compose_service','status','running','started_at','health','restart_count',
              'restart_policy','ports','mounts','networks')
    result={key:c.get(key) for key in fields}
    result['networks']=sorted(result.get('networks') or [])
    result['mounts']=sorted(result.get('mounts') or [],key=lambda m:(m.get('destination',''),m.get('source','')))
    return result


def _fs_key(fs):
    return {'device':fs.get('device'),'capacity_bytes':fs.get('capacity_bytes'),
            'paths':sorted(fs.get('paths',[]),key=lambda x:x.get('path',''))}


def validate_host(obs, config, baseline=None):
    errors=[]
    identity=config['identity']
    if not isinstance(obs,dict): return ['host sample is not an object'], baseline
    if obs.get('owned_image_ids')!=sorted({config['active_image_id'],config['prior_image_id']}):
        errors.append('owned application image set differs from retained A/B')
    if not re.fullmatch(r'[0-9a-f-]{36}',str(obs.get('boot_id',''))): errors.append('host boot identity invalid')
    state=obs.get('state',{})
    if (state.get('active')!=identity['version'] or state.get('prior')!=config['prior_version']
            or state.get('pending') or state.get('maintenance_pending')
            or state.get('active_local_id')!=config['active_image_id']
            or state.get('prior_local_id')!=config['prior_image_id']):
        errors.append('active/prior or unresolved state changed')
    updater=obs.get('updater',{})
    timer=updater.get('timer',{}); service=updater.get('service',{})
    if timer.get('active')!='inactive' or timer.get('enabled') not in ('disabled','masked'):
        errors.append('legacy updater timer is not disabled')
    if service.get('active')!='inactive' or service.get('enabled') not in ('static','disabled','masked','indirect'):
        errors.append('legacy updater service is not inactive')
    if obs.get('owner_journal') or obs.get('operator_socket'):
        errors.append('unresolved operator work is present')
    locks=obs.get('locks',{})
    if any(not locks.get(name,{}).get('exists') or locks.get(name,{}).get('held') for name in ('reconcile','deploy')):
        errors.append('deployment/reconciliation lock state changed')
    filesystems=obs.get('filesystems',[])
    if not filesystems: errors.append('qualified backing filesystems are missing')
    for fs in filesystems:
        if (not isinstance(fs.get('available_bytes'),int) or fs['available_bytes']<config['min_free_bytes']
                or not isinstance(fs.get('available_inodes'),int) or fs['available_inodes']<config['min_free_inodes']):
            errors.append('qualified filesystem reserve breached')
    apps=obs.get('apps',[])
    expected_names={SERVICE+'-web-'+identity['version']:identity['version'],
                    SERVICE+'-web-'+config['prior_version']:config['prior_version']}
    by_version={}
    for c in apps:
        if not isinstance(c,dict) or not isinstance(c.get('name'),str):
            errors.append('application container identity is invalid')
            continue
        version=expected_names.get(c['name'].lstrip('/'))
        if version is None:
            errors.append('application container set differs from retained A/B')
            continue
        if c.get('version') is not None and c.get('version')!=version:
            errors.append('application version label conflicts with container identity')
            continue
        by_version.setdefault(version,[]).append(c)
    active=by_version.get(identity['version'],[]); prior=by_version.get(config['prior_version'],[])
    if len(active)!=1 or len(prior)!=1 or len(apps)!=2:
        errors.append('application container set differs from retained A/B')
    else:
        a,b=active[0],prior[0]
        if (a.get('image_id')!=config['active_image_id'] or a.get('revision')!=identity['revision']
                or a.get('name','').lstrip('/')!=SERVICE+'-web-'+identity['version']
                or a.get('config_image')!='localhost:5555/leapview-site:'+identity['version']
                or a.get('service')!=SERVICE or a.get('role')!='web' or a.get('running') is not True
                or a.get('status')!='running' or a.get('restart_policy')!='unless-stopped'
                or a.get('health') not in ('healthy','none') or 'kamal' not in a.get('networks',[])):
            errors.append('active application identity or health is invalid')
        if (b.get('image_id')!=config['prior_image_id']
                or b.get('name','').lstrip('/')!=SERVICE+'-web-'+config['prior_version']
                or b.get('config_image')!='localhost:5555/leapview-site:'+config['prior_version']
                or b.get('service')!=SERVICE or b.get('role')!='web' or b.get('running') is not False
                or b.get('status') not in ('exited','created') or b.get('restart_policy')!='unless-stopped'
                or 'kamal' not in b.get('networks',[])):
            errors.append('retained prior application identity is invalid')
    proxy=obs.get('proxy',{})
    if (proxy.get('name','').lstrip('/')!='kamal-proxy' or not proxy.get('running')
            or proxy.get('status')!='running' or proxy.get('health') not in ('healthy','none')
            or proxy.get('restart_policy')!='unless-stopped'
            or 'basecamp/kamal-proxy@'+PROXY_DIGEST not in (proxy.get('image_repo_digests') or [])
            or proxy.get('ports') not in ({},None) or 'kamal' not in proxy.get('networks',[])):
        errors.append('private Kamal proxy identity or health is invalid')
    caddy=obs.get('caddy',{})
    if (caddy.get('compose_project')!='leapview-site' or caddy.get('compose_service')!='caddy'
            or not caddy.get('running') or caddy.get('status')!='running'
            or caddy.get('health') not in ('healthy','none') or caddy.get('restart_policy')!='unless-stopped'
            or not re.fullmatch(r'[^@\s]+@sha256:[a-f0-9]{64}',str(caddy.get('config_image','')))
            or 'kamal' not in caddy.get('networks',[])):
        errors.append('Caddy identity or health is invalid')
    caddy_mounts={(m.get('destination'),m.get('source'),m.get('type'),m.get('rw')) for m in caddy.get('mounts',[])}
    required_mounts={
        ('/etc/caddy/Caddyfile','/opt/leapview-site/Caddyfile','bind',False),
        ('/data','/var/lib/leapview-site/caddy-data','bind',True),
        ('/config','/var/lib/leapview-site/caddy-config','bind',True),
    }
    if not required_mounts.issubset(caddy_mounts):
        errors.append('Caddy route/certificate mounts are invalid')
    ports=caddy.get('ports') or {}
    for port in ('80','443'):
        bindings=ports.get(port+'/tcp') or []
        if not any(str(b.get('HostPort'))==port and b.get('HostIp','') in ('','0.0.0.0','::') for b in bindings):
            errors.append('Caddy public port binding is invalid')
    if baseline is None and not errors:
        baseline={'boot_id':obs['boot_id'],'filesystems':[_fs_key(x) for x in filesystems],
                  'containers':{'apps':sorted([_container_key(c) for c in apps],key=lambda x:x['id']),
                                'proxy':_container_key(proxy),'caddy':_container_key(caddy)},
                  'updater':updater,'locks':locks}
    elif baseline is not None:
        if obs.get('boot_id')!=baseline['boot_id']: errors.append('host boot identity changed')
        if [_fs_key(x) for x in filesystems]!=baseline['filesystems']:
            errors.append('backing filesystem identity changed')
        current={'apps':sorted([_container_key(c) for c in apps],key=lambda x:x['id']),
                 'proxy':_container_key(proxy),'caddy':_container_key(caddy)}
        if current!=baseline['containers']: errors.append('container identity, health, or restart count changed')
        if updater!=baseline['updater']: errors.append('legacy updater state changed')
        if locks!=baseline['locks']: errors.append('deployment lock state changed')
    return errors,baseline


def _ensure_protected_dir(path):
    p=Path(path)
    if p.exists() or p.is_symlink(): raise ValueError('output directory already exists; observer never resumes or resets')
    parent=p.parent
    if not parent.is_dir(): raise ValueError('protected observer parent directory must already exist')
    st=parent.stat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid!=os.getuid() or st.st_mode&0o077:
        raise ValueError('observer parent directory must be operator-owned and mode 0700')
    os.mkdir(p,0o700)
    os.chmod(p,0o700)
    return p


def _write_json_atomic(path, value):
    path=Path(path); tmp=path.with_name(path.name+'.tmp')
    fd=os.open(str(tmp),os.O_WRONLY|os.O_CREAT|os.O_TRUNC|getattr(os,'O_NOFOLLOW',0),0o600)
    with os.fdopen(fd,'w',encoding='utf-8') as f:
        json.dump(value,f,sort_keys=True,separators=(',',':')); f.write('\n'); f.flush(); os.fsync(f.fileno())
    os.replace(tmp,path); os.chmod(path,0o600)
    dfd=os.open(str(path.parent),os.O_RDONLY|os.O_DIRECTORY)
    try: os.fsync(dfd)
    finally: os.close(dfd)


def _append_jsonl(path,value):
    fd=os.open(str(path),os.O_WRONLY|os.O_APPEND|os.O_CREAT|getattr(os,'O_NOFOLLOW',0),0o600)
    try:
        st=os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_uid!=os.getuid(): raise ValueError('unsafe observer log')
        os.fchmod(fd,0o600)
        data=(json.dumps(value,sort_keys=True,separators=(',',':'))+'\n').encode()
        view=memoryview(data)
        while view:
            view=view[os.write(fd,view):]
        os.fsync(fd)
    finally: os.close(fd)


def _initial_summary(config, start_wall):
    return {'schema':1,'status':'running','scope':'health_and_storage_observation_only',
            'rollout_acceptance':'incomplete_public_adoption_smoke_required',
            'required_external_gates':{'public_adoption_smoke_at_start':'required_not_recorded_by_observer',
                                       'public_adoption_smoke_at_end':'required_not_recorded_by_observer'},
            'started_at':utc(start_wall),'duration_seconds':config['duration_seconds'],
            'probe_interval_seconds':config['probe_interval_seconds'],'host_interval_seconds':config['host_interval_seconds'],
            'target':config['target'],'base_url':config['base_url'],
            'identity':config['identity'],'prior_version':config['prior_version'],
            'active_image_id':config['active_image_id'],'prior_image_id':config['prior_image_id'],
            'min_free_bytes':config['min_free_bytes'],'min_free_inodes':config['min_free_inodes'],
            'observer_sha256':config['observer_sha256'],'input_files':config.get('input_files',{}),
            'samples':0,'host_samples':0,'last_sample_at':None,'elapsed_seconds':0,'failure':None}


def run_observation(config, output_dir, *, http_probe=None, host_sample=None, monotonic=time.monotonic,
                    wall_time=time.time, sleep=time.sleep, gap_grace=GAP_GRACE, clock_tolerance=CLOCK_TOLERANCE):
    if not 0 < gap_grace <= GAP_GRACE or not 0 < clock_tolerance <= CLOCK_TOLERANCE:
        raise ValueError('observation timing tolerances may not be loosened')
    if (config.get('duration_seconds')!=DURATION or config.get('probe_interval_seconds')!=PROBE_INTERVAL
            or config.get('host_interval_seconds')!=HOST_INTERVAL or gap_grace!=GAP_GRACE):
        raise ValueError('observation schedule and gap grace are fixed at 86400/60/900/15 seconds')
    if any(type(config.get(key)) is not int or config[key]<=0
           for key in ('min_free_bytes','min_free_inodes')):
        raise ValueError('qualified free-space and inode thresholds must be positive integers')
    if config.get('target')!=validate_target(config.get('target')):
        raise ValueError('target must be normalized before observation')
    if config.get('base_url')!=validate_base_url(config.get('base_url')):
        raise ValueError('base URL must be normalized before observation')
    source_sha=observer_source_sha256()
    if config.get('observer_sha256',source_sha)!=source_sha:
        raise ValueError('observer source changed after run configuration was prepared')
    config['observer_sha256']=source_sha
    out=_ensure_protected_dir(output_dir)
    event_path=out/'samples.jsonl'; summary_path=out/'summary.json'
    start_m=monotonic(); start_w=wall_time(); wall_mono_offset=start_w-start_m
    summary=_initial_summary(config,start_w)
    _write_json_atomic(summary_path,summary)
    _write_json_atomic(out/'process.json',{'pid':os.getpid(),'start_ticks':proc_identity(os.getpid())['start_ticks'],
                                           'boot_id':proc_boot_id(),'observer_sha256':source_sha})
    if http_probe is None: http_probe=lambda:probe_public(config['identity'],config['base_url'])
    baseline=None; failure=None; index=0; next_deadline=start_m
    def reject(reason, observed_m, observed_w):
        _append_jsonl(event_path,{'type':'rejected','observed_at':utc(observed_w),
                                  'elapsed_seconds':round(observed_m-start_m,3),'failure':reason})
    def stop_handler(signum, frame):
        raise ObservationFailure('received stop signal ' + str(signum))
    old_term=signal.signal(signal.SIGTERM,stop_handler)
    old_int=signal.signal(signal.SIGINT,stop_handler)
    try:
        while True:
            now=monotonic()
            if now<next_deadline: sleep(next_deadline-now)
            sample_m=monotonic(); sample_w=wall_time()
            if abs((sample_w-sample_m)-wall_mono_offset)>clock_tolerance:
                failure='wall and monotonic clocks diverged'; reject(failure,sample_m,sample_w); break
            if sample_m>next_deadline+gap_grace:
                failure='monitoring gap exceeded grace'; reject(failure,sample_m,sample_w); break
            elapsed=max(0,sample_m-start_m)
            host_due=(index*config['probe_interval_seconds'])%config['host_interval_seconds']==0
            http_started_m=monotonic()
            http=http_probe()
            http_duration_ms=int(round(max(0.0,monotonic()-http_started_m)*1000))
            host=None; host_error=None; host_errors=[]
            if host_due:
                try:
                    host=host_sample() if host_sample is not None else collect_host(
                        config['ssh_config'],config['fingerprint_file'],config['ssh_config_sha256'],
                        config['fingerprint_sha256'],config['identity'],config['prior_version'],config['active_image_id'],
                        config['prior_image_id'],config['min_free_bytes'],config['min_free_inodes'],config['target'])
                    host_errors,baseline=validate_host(host,config,baseline)
                except Exception as exc:
                    host_error=type(exc).__name__; host_errors=['remote host sample failed']
            failures=[]
            if not isinstance(http,dict) or any(not isinstance(http.get(k),dict) or not http[k].get('ok') for k in ('healthz','readyz','build')):
                failures.append('public health/readiness/build check failed')
            failures.extend(host_errors)
            end_m=monotonic(); end_w=wall_time()
            if abs((end_w-end_m)-wall_mono_offset)>clock_tolerance: failures.append('wall and monotonic clocks diverged')
            if end_m>next_deadline+gap_grace: failures.append('sample execution caused a monitoring gap')
            event={'type':'sample','index':index,'scheduled_at':utc(start_w+index*config['probe_interval_seconds']),
                   'observed_at':utc(sample_w),'elapsed_seconds':round(elapsed,3),
                   'public_probe_duration_ms':http_duration_ms,
                   'sample_execution_ms':int(round(max(0.0,end_m-sample_m)*1000)),'http':http,
                   'host':host,'host_error':host_error,'failures':failures}
            _append_jsonl(event_path,event)
            summary['samples']+=1; summary['host_samples']+=1 if host_due else 0
            summary['last_sample_at']=event['observed_at']; summary['elapsed_seconds']=round(end_m-start_m,3)
            _write_json_atomic(summary_path,summary)
            if failures:
                failure='; '.join(dict.fromkeys(failures)); break
            if elapsed>=config['duration_seconds']:
                expected_probe_count=config['duration_seconds']//config['probe_interval_seconds']+1
                expected_host_count=config['duration_seconds']//config['host_interval_seconds']+1
                if summary['samples']<expected_probe_count or summary['host_samples']<expected_host_count:
                    failure='required full-duration samples are incomplete'; break
                summary['status']='health_storage_passed'; summary['ended_at']=utc(end_w); summary['elapsed_seconds']=round(end_m-start_m,3)
                break
            index+=1; next_deadline=start_m+index*config['probe_interval_seconds']
        if failure:
            summary['status']='failed'; summary['failure']=failure; summary['ended_at']=utc(wall_time())
    except BaseException as exc:
        summary['status']='failed'; summary['failure']='observer stopped: '+type(exc).__name__
        summary['ended_at']=utc(wall_time())
        try: reject(summary['failure'],monotonic(),wall_time())
        except Exception: pass
    finally:
        signal.signal(signal.SIGTERM,old_term); signal.signal(signal.SIGINT,old_int)
    _write_json_atomic(summary_path,summary)
    return summary


def status(output_dir):
    out=Path(output_dir)
    if out.is_symlink() or not out.is_dir(): raise ValueError('observer output directory missing')
    out_st=out.stat()
    if out_st.st_uid!=os.getuid() or out_st.st_mode&0o077: raise ValueError('observer output directory is not protected')
    def local_json(name):
        path=out/name; st=path.lstat()
        if not stat.S_ISREG(st.st_mode) or st.st_uid!=os.getuid() or st.st_mode&0o077:
            raise ValueError('observer metadata is not protected')
        return json.loads(path.read_text())
    summary=local_json('summary.json')
    if summary.get('status')=='running':
        process=local_json('process.json')
        try:
            proc=proc_identity(process['pid'])
            current_source_sha=observer_source_sha256()
            alive=(proc['state'] not in ('Z','X') and proc['start_ticks']==process['start_ticks']
                   and proc_boot_id()==process['boot_id']
                   and re.fullmatch(r'[a-f0-9]{64}',summary.get('observer_sha256','')) is not None
                   and summary.get('observer_sha256')==current_source_sha
                   and process.get('observer_sha256')==summary.get('observer_sha256'))
        except Exception: alive=False
        return summary, ('running' if alive else 'interrupted')
    return summary,summary.get('status','invalid')


def prepare_config(record_path, prior_version, active_image_id, prior_image_id,
                   min_free_bytes, min_free_inodes, ssh_config_path, fingerprint_path,
                   target, base_url=DEFAULT_BASE_URL):
    """Validate operator-supplied inputs and pin the exact read-only run contract."""
    target=validate_target(target)
    base_url=validate_base_url(base_url)
    if any(type(value) is not int or value<=0 for value in (min_free_bytes,min_free_inodes)):
        raise ValueError('qualified free-space and inode thresholds must be positive integers')
    record_input=Path(record_path)
    if record_input.is_symlink(): raise ValueError('record may not be a symlink')
    record_file=str(record_input.resolve(strict=True))
    record_sha=file_sha256(record_file)
    identity=read_record(record_input)
    require_sha(active_image_id,'active image ID')
    require_sha(prior_image_id,'prior image ID')
    if not re.fullmatch(r'k[a-f0-9]{64}',prior_version) or prior_version==identity['version']:
        raise ValueError('prior version must be a distinct version identity')
    ssh_input_sha=file_sha256(Path(ssh_config_path).resolve(strict=True))
    fingerprint_sha=file_sha256(Path(fingerprint_path).resolve(strict=True))
    ssh_config=verify_ssh_config(ssh_config_path,fingerprint_path,target)
    fingerprint_file=str(Path(fingerprint_path).resolve(strict=True))
    if (file_sha256(record_file)!=record_sha or file_sha256(ssh_config)!=ssh_input_sha
            or file_sha256(fingerprint_file)!=fingerprint_sha):
        raise ValueError('operator input changed while preparing the observation')
    input_files={
        'record':{'path':record_file,'sha256':record_sha},
        'ssh_config':{'path':ssh_config,'sha256':ssh_input_sha},
        'fingerprint_pin':{'path':fingerprint_file,'sha256':fingerprint_sha},
    }
    return {'identity':identity,'prior_version':prior_version,'active_image_id':active_image_id,
            'prior_image_id':prior_image_id,'min_free_bytes':min_free_bytes,'min_free_inodes':min_free_inodes,
            'duration_seconds':DURATION,'probe_interval_seconds':PROBE_INTERVAL,'host_interval_seconds':HOST_INTERVAL,
            'target':target,'base_url':base_url,'ssh_config':ssh_config,'fingerprint_file':fingerprint_file,
            'ssh_config_sha256':input_files['ssh_config']['sha256'],
            'fingerprint_sha256':input_files['fingerprint_pin']['sha256'],
            'input_files':input_files,'observer_sha256':observer_source_sha256()}


def _positive_int(value):
    result=int(value)
    if result<=0: raise argparse.ArgumentTypeError('must be positive')
    return result


def main(argv=None):
    parser=argparse.ArgumentParser(description='Run or inspect the read-only production observer.')
    subs=parser.add_subparsers(dest='command',required=True)
    run=subs.add_parser('run')
    run.add_argument('--record',required=True)
    run.add_argument('--prior-version',required=True)
    run.add_argument('--active-image-id',required=True)
    run.add_argument('--prior-image-id',required=True)
    run.add_argument('--min-free-bytes',required=True,type=_positive_int)
    run.add_argument('--min-free-inodes',required=True,type=_positive_int)
    run.add_argument('--ssh-config',required=True)
    run.add_argument('--fingerprint-file',required=True)
    run.add_argument('--target',required=True,help='validated host name or IP address for pinned SSH checks')
    run.add_argument('--base-url',default=DEFAULT_BASE_URL,help='HTTPS origin for public read-only probes')
    run.add_argument('--output-dir',required=True)
    check=subs.add_parser('status'); check.add_argument('--output-dir',required=True)
    args=parser.parse_args(argv)
    try:
        if args.command=='status':
            summary,result=status(args.output_dir); print(json.dumps({'result':result,'summary':summary},sort_keys=True))
            return 0 if result in ('running','health_storage_passed') else 1
        config=prepare_config(args.record,args.prior_version,args.active_image_id,args.prior_image_id,
                              args.min_free_bytes,args.min_free_inodes,args.ssh_config,args.fingerprint_file,
                              args.target,args.base_url)
        summary=run_observation(config,args.output_dir)
        print(json.dumps(summary,sort_keys=True))
        return 0 if summary['status']=='health_storage_passed' else 1
    except Exception as exc:
        print('observer rejected: '+str(exc),file=sys.stderr)
        return 2


if __name__=='__main__':
    sys.exit(main())

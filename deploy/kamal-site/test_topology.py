import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import host


class TopologyReadinessTest(unittest.TestCase):
    def test_public_ports_and_certificate_storage_are_required(self):
        with tempfile.TemporaryDirectory() as tmp:
            site = Path(tmp)
            (site / 'compose.yaml').write_text('qualified compose')
            (site / 'Caddyfile').write_text('admin off\nreverse_proxy kamal-proxy:80\n')
            ready = {'topology': {field: hashlib.sha256((site / name).read_bytes()).hexdigest()
                                 for name, field in (('compose.yaml', 'compose_sha256'), ('Caddyfile', 'caddy_sha256'))}}
            proxy = {'HostConfig': {'RestartPolicy': {'Name': 'unless-stopped'}},
                     'NetworkSettings': {'Networks': {'kamal': {}}}}
            caddy = {'State': {'Running': True}, 'Config': {'Image': 'caddy@sha256:fixture'},
                     'NetworkSettings': {'Networks': {'kamal': {}}},
                     'HostConfig': {'RestartPolicy': {'Name': 'unless-stopped'}, 'PortBindings': {
                         port + '/tcp': [{'HostIp': '0.0.0.0', 'HostPort': port}] for port in ('80', '443')}},
                     'Mounts': [
                         {'Destination': '/etc/caddy/Caddyfile', 'Source': str(site / 'Caddyfile'), 'Type': 'bind', 'RW': False},
                         {'Destination': '/data', 'Source': '/var/lib/leapview-site/caddy-data', 'Type': 'bind', 'RW': True},
                         {'Destination': '/config', 'Source': '/var/lib/leapview-site/caddy-config', 'Type': 'bind', 'RW': True}]}
            compose = {'services': {'caddy': {'image': 'caddy@sha256:fixture'}}, 'networks': {'kamal': {'external': True}}}
            cases = [('valid', caddy)]
            for name, change in (
                ('unpublished ports', lambda c: c['HostConfig'].update(PortBindings={})),
                ('loopback HTTPS', lambda c: c['HostConfig']['PortBindings']['443/tcp'][0].update(HostIp='127.0.0.1')),
                ('wrong HTTPS port', lambda c: c['HostConfig']['PortBindings']['443/tcp'][0].update(HostPort='8443')),
                ('ephemeral certificates', lambda c: c['Mounts'][1].update(Source='/tmp/certificates')),
                ('missing config storage', lambda c: c['Mounts'].pop()),
                ('writable route', lambda c: c['Mounts'][0].update(RW=True)),
            ):
                altered = copy.deepcopy(caddy); change(altered); cases.append((name, altered))
            for name, actual in cases:
                with self.subTest(name=name), patch.object(host, 'Path', return_value=site), \
                        patch.object(host, 'command', side_effect=[json.dumps(compose), 'caddy-id']), \
                        patch.object(host, 'inspect', return_value=actual):
                    if name == 'valid': host.topology_ready(ready, proxy)
                    else:
                        with self.assertRaises(ValueError): host.topology_ready(ready, proxy)


if __name__ == '__main__': unittest.main()

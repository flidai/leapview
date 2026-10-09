"""Register an exact cold measurement under the existing site supervisor owner.

Only capacity policy changes; admission is repeated before ownership and the
host CAS repeats retained state/policy/free-space proof before atomic replacement.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import shlex
import tempfile

import deploy
from capacity_contract import registration_payload

HERE = Path(__file__).resolve().parent


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--candidate', type=Path, required=True)
    parser.add_argument('--baseline', type=Path, required=True)
    parser.add_argument('--measurement', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    candidate_raw = args.candidate.read_bytes()
    record = deploy.read_record(args.candidate)
    payload = registration_payload(candidate_raw, args.baseline.read_bytes(), args.measurement.read_bytes())
    fd = os.open(args.output, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as retained:
        # Capacity is separate from release admission. Never authorize even a
        # private candidate registration with stale or unauthenticated image proof.
        with tempfile.TemporaryDirectory(prefix='site-capacity-registration-') as temporary:
            directory = Path(temporary)
            if 'nixAdmission' in record:
                selection = {k: record['nixAdmission'][k] for k in
                             ['runId', 'runAttempt', 'artifactId', 'sourceRevision', 'producerRevision']}
                verified = deploy.prepare_nix(directory, record['image'], selection)
            else:
                verified = deploy.prepare(directory, record['image'])
            if verified != record:
                raise ValueError('prepared record differs from live admission')
            deploy.connect(directory)
            with deploy.ownership():
                deploy.remote('preflight')
                source = (HERE / 'contract.py').read_text() + '\n' + '\n'.join(
                    line for line in (HERE / 'capacity_contract.py').read_text().splitlines()
                    if not line.startswith('from contract import '))
                source += '\nimport base64\npayload=json.loads(base64.b64decode(' + repr(
                    base64.b64encode(json.dumps(payload).encode()).decode()) + '))\n'
                source += "print(json.dumps(register(Path('/var/lib/leapview-site/kamal'),payload)))\n"
                command = shlex.join(['python3', '-c', source])
                supervised = shlex.join(['python3', '-c', (HERE / 'supervisor.py').read_text(),
                                         'exec', os.environ['SITE_ATTEMPT'], command])
                result = deploy.run(['ssh', '-F', os.environ['SITE_SSH_CONFIG'], 'root@' + deploy.HOST, supervised])
                retained.write(result)
                retained.flush()
                os.fsync(retained.fileno())
                deploy.remote('preflight', record=record)
    print('Registered measured capacity for ' + record['image'])


if __name__ == '__main__':
    main()

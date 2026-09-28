import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import unittest

import supervisor


class SupervisorTest(unittest.TestCase):
    def test_lost_lock_connection_does_not_release_executing_work(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            locks = [root / 'reconcile.lock', root / 'deploy.lock']
            source = ('import sys; sys.path.insert(0, sys.argv[1]); '
                      'from pathlib import Path; import supervisor; '
                      'supervisor.serve(Path(sys.argv[2]), sys.argv[3], '
                      '[Path(p) for p in sys.argv[4:]])')
            args = [sys.executable, '-B', '-c', source, str(Path(supervisor.__file__).parent),
                    tmp, 'a' * 32, *map(str, locks)]
            owner = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.assertEqual(json.loads(owner.stdout.readline())['attempt'], 'a' * 32)
            work = socket.socket(socket.AF_UNIX)
            work.connect(str(root / 'operator.sock'))
            work.sendall(json.dumps({'attempt': 'a' * 32, 'command': 'sleep 2; touch ' + tmp + '/finished'}).encode() + b'\n')
            deadline = time.monotonic() + 3
            while not json.loads((root / 'owner.json').read_text()).get('work'):
                if time.monotonic() > deadline: self.fail('remote work did not start')
                time.sleep(.01)
            owner.stdin.close()  # Drop only the lock transport, not the work connection.
            time.sleep(.1)
            contender = subprocess.run(args, input=b'finish\n', capture_output=True)
            self.assertNotEqual(contender.returncode, 0)
            self.assertFalse((root / 'finished').exists())
            work.recv(65536)
            work.close()
            owner.wait(timeout=5)
            self.assertTrue((root / 'finished').exists())
            journal = json.loads((root / 'owner.json').read_text())
            self.assertEqual(journal['status'], 'unresolved')
            self.assertTrue(all(w['exit_code'] == 0 for w in journal['work']))
            contender = subprocess.run(args, input=b'finish\n', capture_output=True)
            self.assertNotEqual(contender.returncode, 0)
            self.assertIn(b'explicit recovery', contender.stderr)
            owner.stdout.close(); owner.stderr.close()


if __name__ == '__main__': unittest.main()

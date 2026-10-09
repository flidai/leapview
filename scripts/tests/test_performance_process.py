import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("performance_process", SCRIPTS / "performance_process.py")
processes = importlib.util.module_from_spec(spec)
spec.loader.exec_module(processes)


class OwnedProcessCleanupTest(unittest.TestCase):
    def test_timeout_cleans_detached_grandchild_that_escapes_parent_group(self):
        with tempfile.TemporaryDirectory() as directory:
            pidfile = Path(directory) / 'child.json'
            child = "import time; time.sleep(60)"
            parent = ("import json,subprocess,sys,time; "
                      f"p=subprocess.Popen([sys.executable,'-c',{child!r}],start_new_session=True); "
                      f"open({str(pidfile)!r},'w').write(json.dumps({{'pid':p.pid}})); time.sleep(60)")
            with (Path(directory) / 'raw.log').open('wb') as output:
                receipt = processes.run_owned([sys.executable, '-c', parent], stdout=output, timeout=0.5)
            pid = json.loads(pidfile.read_text())['pid']
            self.assertEqual(receipt['terminationReason'], 'wall_timeout')
            self.assertTrue(receipt['cleanupComplete'])
            row = processes.process_snapshot().get(pid)
            self.assertTrue(row is None or row['state'] == 'Z', 'detached child remained alive')

    def test_rss_stop_is_enforced_and_normal_exit_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            with (Path(directory) / 'stop.log').open('wb') as output:
                receipt = processes.run_owned([sys.executable, '-c', 'import time; data=bytearray(16*1024*1024);time.sleep(60)'],
                                              stdout=output, timeout=2, peak_rss_limit_kib=1)
            self.assertEqual(receipt['terminationReason'], 'process_tree_rss_stop')
            self.assertTrue(receipt['cleanupComplete'])
            with (Path(directory) / 'normal.log').open('wb') as output:
                normal = processes.run_owned([sys.executable, '-c', 'print("complete")'], stdout=output)
            self.assertEqual(normal['exitCode'], 0)
            self.assertIsNone(normal['terminationReason'])


if __name__ == '__main__':
    unittest.main()

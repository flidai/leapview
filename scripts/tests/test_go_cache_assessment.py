import json, os
from pathlib import Path
import subprocess, tempfile, unittest
SCRIPT=str(Path(__file__).parents[1] / 'measure_go_cache.py')
class AuditTest(unittest.TestCase):
 def execute(self, fail=False):
  tmp=tempfile.TemporaryDirectory(); self.addCleanup(tmp.cleanup); root=Path(tmp.name)
  for name in ['flake.lock','nix/toolchain.nix','go.mod','go.sum','Taskfile.yml','.github/actions/setup-ci/action.yml']:
   path=root/name; path.parent.mkdir(parents=True,exist_ok=True); path.write_text(name)
  for name,data in [('modules/a','module'),('build/a','compiler')]:
   path=root/name; path.parent.mkdir(parents=True,exist_ok=True); path.write_text(data)
  fake=root/'go'; fake.write_text('''#!/usr/bin/env python3
import os, sys, json
from pathlib import Path
root=Path.cwd(); args=sys.argv[1:]
with (root/'calls.jsonl').open('a') as f: f.write(json.dumps({'args':args,'cache':os.environ.get('GOCACHE'),'transport':os.environ.get('GODEBUG')})+'\\n')
if args[:2]==['env','-json']: print(json.dumps({'GOVERSION':'go1.26.7','GOOS':'linux','GOARCH':'amd64','CGO_ENABLED':'1','CC':'gcc'}))
elif args==['env','GOMODCACHE']: print(root/'modules')
elif args==['env','GOCACHE']: print(root/'build')
elif args[:1]==['test']:
 print('/tool/compile -o x'); print('# /tool/compile ignored'); print('/tool/cgo -objdir y')
 Path(os.environ['GOCACHE'],'new').write_text('new')
 if os.environ.get('FAIL_AUDIT')=='true': sys.exit(2)
'''); fake.chmod(0o755)
  env=dict(os.environ,PATH=str(root)+':'+os.environ['PATH'],RUNNER_TEMP=str(root),GITHUB_RUN_ID='123',GITHUB_RUN_ATTEMPT='1',CACHE_WORKLOAD='full-validation',GODEBUG='http2client=1',FAIL_AUDIT=str(fail).lower())
  subprocess.run(['git','init','-q'],cwd=root,check=True)
  subprocess.run(['git','-c','user.name=Test','-c','user.email=test@example.com','commit','--allow-empty','-qm','fixture'],cwd=root,check=True)
  result=subprocess.run(['python3',SCRIPT],cwd=root,env=env,stdout=subprocess.PIPE,text=True)
  return result,json.loads((root/'go-cache-assessment/assessment.json').read_text()),[json.loads(x) for x in (root/'calls.jsonl').read_text().splitlines()]
 def test_before_execution_composition_and_call_environment(self):
  result,report,calls=self.execute()
  self.assertEqual(result.returncode,0)
  self.assertEqual(report['restored']['modules'],{'files':1,'logicalBytes':6})
  self.assertEqual(report['restored']['build'],{'files':1,'logicalBytes':8})
  download=[c for c in calls if c['args']==['mod','download']]
  self.assertEqual(len(download),1); self.assertEqual(download[0]['transport'],'http2client=0')
  trials=[c for c in calls if c['args'][0]=='test']
  self.assertEqual(len(trials),2); self.assertNotEqual(trials[0]['cache'],trials[1]['cache'])
  self.assertTrue(all(c['transport']=='http2client=1' for c in trials))
  self.assertEqual(trials[0]['args'],trials[1]['args'])
 def test_compiler_count_excludes_comments(self):
  result,report,_=self.execute()
  for label in ['restoredTrial','empty-build-cacheTrial']:
   self.assertEqual(report[label]['compileInvocations'],1); self.assertEqual(report[label]['cgoInvocations'],1)
 def test_compile_failure_is_recorded_and_stops_comparison(self):
  result,report,calls=self.execute(True)
  self.assertEqual(result.returncode,2); self.assertEqual(report['restoredTrial']['exitCode'],2)
  self.assertNotIn('empty-build-cacheTrial',report)
  self.assertEqual(len([c for c in calls if c['args'][0]=='test']),1)
if __name__ == '__main__':
 unittest.main()

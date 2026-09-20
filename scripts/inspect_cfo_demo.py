import hashlib, json, os, subprocess, tempfile, time
from pathlib import Path
root=Path('/tmp/leapview-demo-host-state/managed-data/objects')
revision='sha256:dd21088c521f9b0900d9eeceff5859598d2cb1ce511832fe972ce6f87f28e530'
digest='131af03496fc84d351bffcf4a170f5da0f25204e51ce33d5d41589b4e0191d14'
manifest={'files':[{'path':'financial-sample.csv','size':75083,'sha256':digest}]}
assert 'sha256:'+hashlib.sha256(json.dumps(manifest,separators=(',',':')).encode()).hexdigest()==revision
blob=root/'blobs/sha256'/digest[:2]/digest
assert blob.is_file() and not blob.is_symlink()
assert blob.stat().st_size==75083 and hashlib.sha256(blob.read_bytes()).hexdigest()==digest
container=root/'revisions'/revision
view=container/'data'/'financial-sample.csv'
if view.exists():
 assert view.stat().st_size==75083 and hashlib.sha256(view.read_bytes()).hexdigest()==digest
 print('Verified existing CFO runtime view')
else:
 assert not container.is_symlink()
 print('Recovering incomplete CFO runtime view',list(str(p.relative_to(container)) for p in container.rglob('*')) if container.exists() else 'absent')
 staging=Path(tempfile.mkdtemp(prefix='.cfo-recovery-',dir=root/'revisions'))
 (staging/'data').mkdir(mode=0o700)
 os.link(blob,staging/'data'/'financial-sample.csv')
 (staging/'data').chmod(0o500)
 if container.exists():
  backup=root/('cfo-incomplete-backup-'+time.strftime('%Y%m%dT%H%M%SZ',time.gmtime()))
  container.rename(backup)
  print('Preserved incomplete view at',backup)
 staging.rename(container)
 print('Restored verified CFO runtime view',revision)

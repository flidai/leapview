import hashlib, json, os, subprocess
from pathlib import Path
pid=subprocess.check_output(['systemctl','show','leapview-demo-current.service','--property=MainPID','--value'],text=True).strip()
e=dict(item.decode().split('=',1) for item in Path('/proc',pid,'environ').read_bytes().split(b'\0') if b'=' in item)
for k,v in e.items():
 if k=='LEAPVIEW_HOME' or ('MANAGED' in k and any(x in k for x in ('DIR','ROOT','BACKEND'))): print(k,v)
print('runtime uid',Path('/proc',pid).stat().st_uid)
container='leapview-postgres-3948794932-demo-current-postgres-1'
query="select json_agg(row_to_json(x)) from (select c.connection_id,r.digest,r.manifest from managed_data.revision r join managed_data.collection c using(collection_id) where c.project_id='lvproject_fI7xfxRH2qubXpUe5KcU7o6T5MOt7tHz' and r.status='ready') x"
s=subprocess.check_output(['docker','exec',container,'sh','-c','exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d leapview_control -Atc "$1"','query',query],text=True)
root=Path('/tmp/leapview-demo-host-state/managed-data/objects')
for item in json.loads(s):
 print('REVISION',item['connection_id'],item['digest'])
 for f in item['manifest']['files']:
  p=root/'blobs/sha256'/f['sha256'][:2]/f['sha256']; view=root/'revisions'/item['digest']/'data'/f['path']
  print(f['path'],'expected',f['sha256'],'size',f['size'],'blob',p.exists(),'view',view.exists())
  for x in (p,view):
   if x.exists(): print(str(x),'uid',x.stat().st_uid,'mode',oct(x.stat().st_mode),'size',x.stat().st_size,'digest',hashlib.sha256(x.read_bytes()).hexdigest())

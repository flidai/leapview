import subprocess
print(subprocess.check_output(['journalctl','-u','leapview-demo-current.service','--since','-15 minutes','-n','180','--no-pager'],text=True))
print(subprocess.check_output(['df','-h','/opt','/tmp'],text=True))

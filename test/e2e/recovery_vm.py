#!/usr/bin/env python3
"""Explicit backup/restore rehearsal inside owned disposable Ubuntu VMs only."""
import os,json,subprocess,pty,select,time,tarfile,sys,hashlib,textwrap
from pathlib import Path
R=Path('/root/hostlier-recovery-fixture');R.mkdir(mode=0o700,exist_ok=True);os.chdir(R);os.umask(0o077)
B=['/usr/local/bin/hostlier'];S=['--socket','/run/hostlier/controller.sock']
def approved(*args):
 m,s=pty.openpty();p=subprocess.Popen(B+list(args)+S,stdin=s,stdout=s,stderr=s);os.close(s);out=b'';sent=False;end=time.time()+600
 while time.time()<end:
  if select.select([m],[],[],.1)[0]:
   try: chunk=os.read(m,65536)
   except OSError: break
   out+=chunk
   if b'[y/N]' in out and not sent: os.write(m,b'y\n');sent=True
  if p.poll() is not None: break
 if p.poll() is None:p.kill()
 assert p.wait()==0,out.decode();os.close(m)
 print('approved '+str(args[:3]))

def run(*args, **kwargs):
 return subprocess.run(args,check=True,timeout=300,**kwargs)

def setup():
 secret=os.urandom(32).hex().encode()
 (R/'secret-sha256').write_text(hashlib.sha256(secret).hexdigest())
 subprocess.run(B+['secret','set','recovery-secret','--stdin']+S,input=secret,check=True,stdout=subprocess.DEVNULL)
 approved('resource','create','redis','recovery-cache','--storage','1Gi')
 code=textwrap.dedent('''
 import os,socket,urllib.parse,http.server,pathlib
 u=urllib.parse.urlparse(os.environ['REDIS_URL'])
 def query(write=False):
  s=socket.create_connection((u.hostname,u.port)); f=s.makefile('rwb',buffering=0)
  def cmd(*a):
   f.write(('*%d\\r\\n'%len(a)).encode()+b''.join(('$%d\\r\\n'%len(x.encode())).encode()+x.encode()+b'\\r\\n' for x in a)); line=f.readline(); return line
  cmd('AUTH',urllib.parse.unquote(u.username or 'default'),urllib.parse.unquote(u.password))
  if write: cmd('SET','recovery','logical-data-survived')
  cmd('GET','recovery'); result=f.readline().strip();s.close();return result.decode()
 p=pathlib.Path('/data/marker')
 if not p.exists():p.write_text('volume-data-survived');query(True)
 class Handler(http.server.BaseHTTPRequestHandler):
  def do_GET(self):
   b=(p.read_text()+'|'+query()+'|'+str(bool(os.environ.get('FIXTURE_SECRET')))).encode();self.send_response(200);self.end_headers();self.wfile.write(b)
 http.server.HTTPServer(('0.0.0.0',8080),Handler).serve_forever()
 ''')
 project={'version':2,'endpoint':{'exposure':'public','port':8080,'route':'app.nginx.example.test'},'name':'nginx-gate','source':{'image':'python:3.12-alpine'},'port':8080,'route':'app.nginx.example.test','command':['python','-u','-c',code],'volumes':{'data':{'mount':'/data','size':'1Gi'}},'secrets':{'FIXTURE_SECRET':'recovery-secret'},'dependencies':{'cache':{'type':'redis','source':'existing','instance':'recovery-cache'}},'bindings':{'REDIS_URL':'cache.url'}}
 (R/'project.yaml').write_text(json.dumps(project));approved('deploy','--file',str(R/'project.yaml'))
 print(subprocess.check_output(['curl','--fail','--silent','https://app.nginx.example.test']).decode())

def backup():
 S=['/usr/local/bin/hostlier','--socket','/run/hostlier/controller.sock']
 run(*S,'stop','nginx-gate',stdout=subprocess.DEVNULL)
 result=subprocess.check_output(S+['resource','backup','recovery-cache','--output',str(R/'redis.rdb')])
 (R/'backup-result.json').write_bytes(result)
 vols=json.loads(subprocess.check_output(['docker','volume','ls','--filter','label=io.hostlier.app=nginx-gate','--format','json']).decode().splitlines()[0])
 volume=json.loads(subprocess.check_output(['docker','volume','inspect',vols['Name']]))[0]
 (R/'volume.json').write_text(json.dumps(volume))
 run('tar','-C',volume['Mountpoint'],'-cf',str(R/'app-volume.tar'),'.')
 run('systemctl','stop','hostlier','hostlier-nginx-renew.timer','hostlier-nginx')
 run('/usr/local/bin/hostlier-controller','backup','--state-dir','/var/lib/hostlier','--output',str(R/'controller.tar'))
 for name in ['hostlier','hostlier-nginx','hostlier-nginx-renew']:
  source=Path('/etc/systemd/system/'+name+'.service')
  (R/source.name).write_bytes(source.read_bytes())
 (R/'hostlier-nginx-renew.timer').write_bytes(Path('/etc/systemd/system/hostlier-nginx-renew.timer').read_bytes())
 run('tar','-C','/usr/local/share/ca-certificates','-cf',str(R/'trust.tar'),'hostlier-nginx-app.crt','hostlier-nginx-controller.crt')
 run('tar','-C',str(R),'-cf','/tmp/hostlier-clean-recovery.tar','.')
 run('chmod','600','/tmp/hostlier-clean-recovery.tar')
 print('source controller stopped; independent metadata, logical Redis and application-volume archives ready')

def restore():
 assert not Path('/var/lib/hostlier').exists(),'destination must be clean'
 assert not subprocess.check_output(['docker','ps','-aq']).strip(),'destination must have no containers'
 with tarfile.open('/tmp/hostlier-clean-recovery.tar') as t:t.extractall(R,filter='data')
 run('install','-m755','/tmp/hostlier-recovery','/usr/local/bin/hostlier')
 run('install','-m755','/tmp/hostlier-controller-recovery','/usr/local/bin/hostlier-controller')
 run('/usr/local/bin/hostlier-controller','restore','--source-stopped','--input',str(R/'controller.tar'),'--state-dir','/var/lib/hostlier')
 run('cp','-a','/var/lib/hostlier/recovery-config/etc/hostlier','/etc/hostlier')
 with tarfile.open(R/'trust.tar') as t:t.extractall('/usr/local/share/ca-certificates',filter='data')
 run('update-ca-certificates',stdout=subprocess.DEVNULL)
 with open('/etc/hosts','a') as f:f.write('\n127.0.0.1 hostlier.nginx.example.test app.nginx.example.test\n')
 for name in ['hostlier.service','hostlier-nginx.service','hostlier-nginx-renew.service','hostlier-nginx-renew.timer']:
  run('install','-m644',str(R/name),'/etc/systemd/system/'+name)
 run('mkdir','-p','/var/lib/hostlier-acme/.well-known/acme-challenge')
 run('chmod','755','/var/lib/hostlier-acme')
 run('systemctl','disable','--now','nginx')
 run('systemctl','daemon-reload')
 volume=json.loads((R/'volume.json').read_text())
 args=['docker','volume','create']
 for key,value in volume['Labels'].items():args+=['--label',key+'='+value]
 args+=[volume['Name']];run(*args,stdout=subprocess.DEVNULL)
 mount=json.loads(subprocess.check_output(['docker','volume','inspect',volume['Name']]))[0]['Mountpoint']
 with tarfile.open(R/'app-volume.tar') as t:t.extractall(mount,filter='data')
 run('systemctl','enable','--now','docker','hostlier-nginx','hostlier','hostlier-nginx-renew.timer',stdout=subprocess.DEVNULL)
 for attempt in range(100):
  p=subprocess.run(['curl','--fail','--silent','https://hostlier.nginx.example.test/health'],stdout=subprocess.PIPE)
  if p.returncode==0:break
  time.sleep(.2)
 assert p.returncode==0
 print('clean controller restored at original absolute state path; services, CA trust, exact volume labels and native volume data reinstalled')

def activate():
 backup=json.loads((R/'backup-result.json').read_text())
 approved('resource','restore','redis','recovery-cache-restored','--storage','1Gi','--backup',backup['backupId'])
 project=json.loads((R/'project.yaml').read_text())
 project['dependencies']['cache']['instance']='recovery-cache-restored'
 (R/'restored-project.yaml').write_text(json.dumps(project))
 approved('deploy','--file',str(R/'restored-project.yaml'))

def check():
 expected=b'volume-data-survived|logical-data-survived|True'
 body=subprocess.check_output(['curl','--fail','--silent','https://app.nginx.example.test'])
 assert body==expected,body
 volume=json.loads((R/'volume.json').read_text())
 live=json.loads(subprocess.check_output(['docker','volume','inspect',volume['Name']]))[0]
 assert live['Labels']==volume['Labels']
 assert Path('/var/lib/hostlier/secret.key').stat().st_mode & 0o077 == 0
 assert os.access('/var/lib/hostlier/packages/redis',os.X_OK)
 logs=Path('/var/log/hostlier/hostlier.log').read_bytes()
 containers=json.loads(subprocess.check_output(['docker','inspect',subprocess.check_output(['docker','ps','-q','--filter','label=io.hostlier.app=nginx-gate']).decode().splitlines()[0]]))
 secret=next(value.split('=',1)[1].encode() for value in containers[0]['Config']['Env'] if value.startswith('FIXTURE_SECRET='))
 expected=(R/'secret-sha256').read_text() if (R/'secret-sha256').exists() else hashlib.sha256(b'recovery-private-value').hexdigest()
 assert hashlib.sha256(secret).hexdigest()==expected
 assert secret not in logs
 print('PASS fresh-server recovery: trusted HTTPS, independent native Redis logical restore, retained app volume, encrypted secret binding, exact original ownership labels, protected key and logs')

if __name__ == '__main__':
 modes={'setup':setup,'backup':backup,'restore':restore,'activate':activate,'check':check}
 if len(sys.argv)!=2 or sys.argv[1] not in modes: raise SystemExit('Use setup|backup on source; restore|activate|check on clean destination')
 if os.geteuid()!=0: raise SystemExit('Requires root inside the disposable fixture VM')
 if sys.argv[1] in ('setup','backup') and 'nginx.example.test' not in Path('/etc/hostlier/config.yaml').read_text(): raise SystemExit('Expected disposable nginx.example.test installation')
 modes[sys.argv[1]]()

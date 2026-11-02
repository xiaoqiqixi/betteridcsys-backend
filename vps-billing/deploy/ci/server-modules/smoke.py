#!/usr/bin/env python3
"""Disposable Linux CI: actual cloud main controller + fake node runner + finance."""
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys
import time

REPO = Path(__file__).resolve().parents[4]
ROOT = Path('/run/aster-module-smoke')
PRIVATE = ROOT / 'private'
SOCKET = Path('/run/aster-server/mod-smoke')
STATE = Path('/var/lib/aster-server/mod-smoke')
UNIT = 'aster-module-smoke-worker'
USERS = ('aster-mod-api', 'aster-mod-worker', 'aster-mod-other')
PROCESSES = []
LOGS = []
OWNED_PATHS = []
OWNED_USERS = []
UNIT_STARTED = False

def safe_log(raw):
    text = raw.decode('utf-8', errors='replace')[-16000:]
    # Fixture-owned credentials are never published, including in failure output.
    for value in ['module_test_only', 'CatalogFixture!2026'] + [p.read_text().strip() for p in PRIVATE.glob('*.token')]:
        if value: text = text.replace(value, '[redacted]')
    return text

def run(args, **kw):
    result = subprocess.run([str(a) for a in args], stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kw)
    if result.returncode:
        print(safe_log(result.stdout + result.stderr), file=sys.stderr, flush=True)
        raise RuntimeError(f'{Path(str(args[0])).name} failed with exit code {result.returncode}')
    return result

def write(path, value, mode=0o600):
    path.write_bytes((json.dumps(value) if isinstance(value, (dict, list)) else value).encode() if not isinstance(value, bytes) else value)
    path.chmod(mode)

def wait(predicate):
    end = time.monotonic() + 45
    while time.monotonic() < end:
        if predicate(): return
        if any(p.poll() is not None for p in PROCESSES): raise RuntimeError('fixture process exited')
        time.sleep(.15)
    raise RuntimeError('fixture readiness timed out')

def spawn(name, args, env):
    log = (PRIVATE / (name + '.log')).open('wb'); LOGS.append(log)
    p = subprocess.Popen([str(a) for a in args], env=env, stdout=log, stderr=subprocess.STDOUT)
    PROCESSES.append(p); return p

def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()
def cert_sha(path): return hashlib.sha256(run(['openssl','x509','-in',path,'-outform','DER']).stdout).hexdigest()

def main():
    if os.geteuid() != 0 or sys.platform != 'linux' or os.environ.get('GITHUB_ACTIONS') != 'true': raise RuntimeError('disposable Linux Actions root required')
    if ROOT.exists() or SOCKET.exists() or STATE.exists(): raise RuntimeError('refusing to reuse fixture state')
    ROOT.mkdir(mode=0o755); OWNED_PATHS.append(ROOT); PRIVATE.mkdir(mode=0o700)
    output = REPO / 'artifacts/mod-smoke'; output.mkdir(exist_ok=True)
    env = os.environ | {'ASTER_MODULE_SMOKE_ROOT':str(ROOT)}
    run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-subj','/CN=module-smoke','-keyout',PRIVATE/'ca.key','-out',ROOT/'ca.crt'])
    for name, usage in [('node','serverAuth'),('node-client','clientAuth'),('controller','serverAuth')]:
        run(['openssl','req','-newkey','rsa:2048','-nodes','-subj','/CN=localhost','-keyout',PRIVATE/(name+'.key'),'-out',PRIVATE/(name+'.csr')])
        write(PRIVATE/'extensions',f'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage={usage}\n')
        run(['openssl','x509','-req','-days','1','-in',PRIVATE/(name+'.csr'),'-CA',ROOT/'ca.crt','-CAkey',PRIVATE/'ca.key','-CAcreateserial','-extfile',PRIVATE/'extensions','-out',ROOT/(name+'.crt')])
    binary = ROOT/'bin'; binary.mkdir(mode=0o755)
    for source in (REPO/'artifacts/mod-bin').iterdir(): shutil.copyfile(source,binary/source.name); (binary/source.name).chmod(0o555)
    node = spawn('node',[binary/'node.test','-test.run=^TestServerModuleFakeNode$','-test.v','-test.timeout=5m'],env)
    wait(lambda:(ROOT/'node-ready.json').exists())
    meta=json.loads((ROOT/'node-ready.json').read_text())
    mysql_socket='/var/run/aster-module-mysql/mysqld.sock'
    run(['mysql','--socket='+mysql_socket,'-uroot','-e','CREATE DATABASE aster_controller_module_test; CREATE DATABASE aster_mod_finance_test;'],env=env|{'MYSQL_PWD':'module_test_only'})
    write(PRIVATE/'cloud.dsn',f'root:module_test_only@unix({mysql_socket})/aster_controller_module_test?parseTime=true')
    for name in ['billing','admin-read','admin-write']: write(PRIVATE/(name+'.token'),secrets.token_hex(32))
    profile={'id':meta['node_id'],'datacenter_id':meta['datacenter_id'],'group_id':meta['group_id'],'endpoint':'https://127.0.0.1:20444','allowed_cidrs':['127.0.0.0/8'],'ca_file':str(ROOT/'ca.crt'),'client_cert_file':str(ROOT/'node-client.crt'),'client_key_file':str(PRIVATE/'node-client.key'),'server_certificate_sha256':cert_sha(ROOT/'node.crt')}
    config={'schema':1,'controller_id':'smoke-cloud','listen':'127.0.0.1:20443','tls_cert_file':str(ROOT/'controller.crt'),'tls_key_file':str(PRIVATE/'controller.key'),'dsn_file':str(PRIVATE/'cloud.dsn'),'billing_token_file':str(PRIVATE/'billing.token'),'admin_read_token_file':str(PRIVATE/'admin-read.token'),'admin_write_token_file':str(PRIVATE/'admin-write.token'),'heartbeat_ttl_seconds':300,'nodes':[profile],'presets':[{'id':'smoke','node_id':meta['node_id'],'resources':meta['resources']}],'dispatcher':{'enabled':True},'service':{'snapshot_limit':0,'images':[]}}
    write(PRIVATE/'controller.json',config)
    for command in ['migrate','register-nodes']:run([binary/'controller',command,'--config',PRIVATE/'controller.json'])
    run([binary/'controller','heartbeat','--config',PRIVATE/'controller.json','--node',meta['node_id']])
    spawn('controller',[binary/'controller','serve','--config',PRIVATE/'controller.json'],env)
    # Wait for the verified HTTPS controller before launching its consumer.
    import ssl, urllib.request
    tls=ssl.create_default_context(cafile=ROOT/'ca.crt')
    def controller_ready():
        try:
            request=urllib.request.Request('https://127.0.0.1:20443/v1/health',headers={'Authorization':'Bearer '+(PRIVATE/'admin-read.token').read_text()})
            with urllib.request.urlopen(request,context=tls,timeout=1) as response:return response.status==200
        except Exception:return False
    wait(controller_ready)
    for user in USERS:
        run(['useradd','--system','--no-create-home','--shell','/usr/sbin/nologin',user]); OWNED_USERS.append(user)
    api_uid=int(run(['id','-u',USERS[0]]).stdout);api_gid=int(run(['id','-g',USERS[0]]).stdout);worker_uid=int(run(['id','-u',USERS[1]]).stdout)
    for path in [SOCKET,STATE]:path.parent.mkdir(exist_ok=True);path.mkdir(mode=0o750 if path==SOCKET else 0o700);OWNED_PATHS.append(path);os.chown(path,worker_uid,api_gid)
    plugin=ROOT/'plugins/server.aster-cloud/1.0.0';plugin.mkdir(parents=True,mode=0o755)
    shutil.copyfile(binary/'server-module',plugin/'plugin');(plugin/'plugin').chmod(0o555)
    manifest={'schema':1,'id':'server.aster-cloud','version':'1.0.0','protocol':'aster.server/1','core_min':'0.6.0','core_max_exclusive':'0.7.0','capabilities':['server.call'],'executable':'plugin','sha256':sha(plugin/'plugin')}
    write(plugin/'manifest.json',manifest,0o644)
    write(ROOT/'plugins.json',{'schema':1,'revision':1,'servers':[{'slot':'mod-smoke','enabled':True,'id':'server.aster-cloud','version':'1.0.0','manifest_sha256':sha(plugin/'manifest.json'),'worker_uid':worker_uid,'client_uid':api_uid,'socket':str(SOCKET/'plugin.sock'),'allowed_targets':[{'host':'127.0.0.1','port':20443}]}]},0o644)
    write(PRIVATE/'server-config.json',{'schema':1,'controller_id':'smoke-cloud','endpoint':'https://127.0.0.1:20443/v1/provisioning','allowed_cidrs':['127.0.0.0/8'],'custom_ca':True})
    run(['systemd-run','--unit='+UNIT,'--collect','--property=User='+USERS[1],'--property=Group='+str(api_gid),'--property=NoNewPrivileges=true','--property=ProtectSystem=strict','--property=PrivateTmp=true','--property=ProtectHome=true','--property=ReadWritePaths='+str(SOCKET)+' '+str(STATE),'--property=InaccessiblePaths='+str(PRIVATE),'--property=LoadCredential=server_config.json:'+str(PRIVATE/'server-config.json'),'--property=LoadCredential=server_token:'+str(PRIVATE/'billing.token'),'--property=LoadCredential=server_ca.pem:'+str(ROOT/'ca.crt'),'--property=StandardOutput=null','--property=StandardError=null',binary/'aster-plugin-host','--kind=server','--slot=mod-smoke','--root='+str(ROOT/'plugins'),'--policy='+str(ROOT/'plugins.json')])
    global UNIT_STARTED
    UNIT_STARTED = True
    wait(lambda:(SOCKET/'plugin.sock').exists())
    for user in [USERS[0],USERS[2]]:
        result=subprocess.run(['runuser','-u',user,'--','test','-r','/run/credentials/'+UNIT+'.service/server_token'])
        if result.returncode==0:raise RuntimeError('worker credential escaped its UID')
    api_dir=ROOT/'api';api_dir.mkdir(mode=0o700);os.chown(api_dir,api_uid,api_gid)
    write(api_dir/'private-canary','api-private-canary');os.chown(api_dir/'private-canary',api_uid,api_gid)
    if subprocess.run(['runuser','-u',USERS[1],'--','test','-r',str(api_dir/'private-canary')]).returncode==0:raise RuntimeError('plugin read finance private data')
    driver_env=env|{'PLUGIN_DIRECTORY':str(ROOT/'plugins'),'PLUGIN_POLICY_FILE':str(ROOT/'plugins.json'),'ASTER_MYSQL_TEST_DSN':f'root:module_test_only@unix({mysql_socket})/aster_mod_finance_test?parseTime=true','ASTER_MODULE_SMOKE_EVIDENCE':str(api_dir/'evidence.json')}
    result=run(['setpriv','--reuid='+str(api_uid),'--regid='+str(api_gid),'--init-groups',binary/'finance.test','-test.run=^TestMySQLCommerceCloudModuleSmoke$','-test.v','-test.timeout=4m'],env=driver_env,timeout=250)
    write(PRIVATE/'finance.log',result.stdout)
    write(ROOT/'stop-node','done');node.wait(timeout=10)
    if node.returncode!=0:raise RuntimeError('node operation counts failed')
    evidence=json.loads((api_dir/'evidence.json').read_text())|json.loads((ROOT/'node-evidence.json').read_text())|{'cloud_commit':(REPO/'artifacts/mod-cloud-commit.txt').read_text().strip(),'credential_isolation':True}
    write(output/'result.json',evidence,0o644)
    print(json.dumps(evidence),flush=True)

try:
    main()
except Exception:
    for path in PRIVATE.glob('*.log'):
        print(path.name + ':\n' + safe_log(path.read_bytes()), file=sys.stderr, flush=True)
    raise
finally:
    # Private logs are not uploaded: keep only bounded nonsecret outcome evidence.
    if UNIT_STARTED:subprocess.run(['systemctl','stop',UNIT],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    for process in reversed(PROCESSES):
        if process.poll() is None:process.terminate()
        try:process.wait(timeout=10)
        except subprocess.TimeoutExpired:process.kill();process.wait()
    for stream in LOGS:stream.close()
    for path in reversed(OWNED_PATHS):
        if path.exists():shutil.rmtree(path)
    for user in reversed(OWNED_USERS):subprocess.run(['userdel',user],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)

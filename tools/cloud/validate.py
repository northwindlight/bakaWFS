#!/usr/bin/env python3
"""Explicit isolated live acceptance. Credential file must contain test users only."""
import argparse,base64,concurrent.futures,hashlib,json,secrets,time,urllib.error,urllib.parse,urllib.request
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--credentials',required=True);p.add_argument('--output',required=True);a=p.parse_args()
c=json.loads(Path(a.credentials).read_text());base=c['url'];ua='bakaWFS-cloud-acceptance';checks=[]
class Checks(list):
 def append(self,value):
  super().append(value);print('PASS:',value,flush=True)
checks=Checks()
class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,*args):return None
opener=urllib.request.build_opener(NoRedirect)
def network_open(req,timeout=90,open_fn=None):
 for attempt in range(4):
  try:return (open_fn or urllib.request.urlopen)(req,timeout=timeout)
  except urllib.error.HTTPError:raise
  except (urllib.error.URLError,TimeoutError):
   if attempt==3:raise
   time.sleep(attempt+1)

def request(route,body=None,token=None,method=None):
 headers={'User-Agent':ua,'Origin':'https://cloud.northwind.site'}
 if token:headers['Authorization']='Bearer '+token
 if body is not None:headers['Content-Type']='application/json'
 req=urllib.request.Request(base+route,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method or ('POST' if body is not None else 'GET'))
 try:r=network_open(req,timeout=65,open_fn=opener.open)
 except urllib.error.HTTPError as e:r=e
 with r:
  raw=r.read()
  try:v=json.loads(raw) if raw else None
  except ValueError:v=None
  return r.status,r.headers,v
def ok(route,body=None,token=None,status=200):
 for attempt in range(4):
  code,headers,v=request(route,body,token)
  if code<500 or attempt==3:break
  time.sleep(attempt+1)
 assert code==status,(route,code,v)
 return v
login=ok('/login',{'username':c['username'],'password':c['password']});token=login['token'];assert login['role']=='admin'
guest=ok('/login',{'username':'baka','password':'baka'})['token']
assert request('/list')[0]==401
for route in ['/mkdir','/rename','/copy','/delete','/cloud/upload/prepare','/cloud/upload/complete','/remote-upload','/cancel']:
 assert request(route,{},guest)[0]==403,route
checks.append('authentication and guest write denial')
config=ok('/api/config');assert config['storage_mode']=='kodo' and config['manga_mode'];ok('/readyz')
root=ok('/tree',token=token);assert any(n['name']=='样本书' for n in root['children'])
assert request('/list?path=../private',token=token)[0]==404
checks.append('private catalog, tree and traversal denial')
uid=secrets.token_hex(8);directory='验收 # % '+uid
q={'path':directory,'operation_id':uid+'mkdir'}
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:results=list(pool.map(lambda _:ok('/mkdir',q,token),range(8)))
assert len({r['node_id'] for r in results})==1
assert request('/mkdir',{'path':directory+'different','operation_id':q['operation_id']},token)[0]==409
checks.append('8 concurrent identical commits, one durable receipt, parameter binding')
def etag(data):
 blocks=[hashlib.sha1(data[i:i+4194304]).digest() for i in range(0,len(data),4194304)] or [hashlib.sha1(b'').digest()]
 return base64.urlsafe_b64encode((b'\x16'+blocks[0]) if len(blocks)==1 else b'\x96'+hashlib.sha1(b''.join(blocks)).digest()).decode().rstrip('=')
def wire_etag(data):
 if len(data)<=10*1024*1024:return etag(data)
 size=max(8*1024*1024,((len(data)+9999)//10000+1024*1024-1)//(1024*1024)*(1024*1024))
 parts=[base64.urlsafe_b64decode(etag(data[i:i+size]))[1:] for i in range(0,len(data),size)]
 return base64.urlsafe_b64encode(b'\x9e'+hashlib.sha1(b''.join(parts)).digest()).decode().rstrip('=')
def upload(name,data):
 req={'filename':directory+'/'+name,'size':len(data),'etag':wire_etag(data),'operation_id':secrets.token_hex(16)}
 prep=ok('/cloud/upload/prepare',req,token);again=ok('/cloud/upload/prepare',req,token);assert prep['job_id']==again['job_id'] and prep['upload_id']==again['upload_id']
 parts=[]
 if prep['upload_id']:
  for i,start in enumerate(range(0,len(data),prep['part_size'])):
   reqpart=urllib.request.Request(prep['multipart_url']+'/'+str(i+1),data=data[start:start+prep['part_size']],headers={'Authorization':'UpToken '+prep['token'],'Content-Type':'application/octet-stream','Origin':'https://cloud.northwind.site'},method='PUT')
   with network_open(reqpart,timeout=90) as r:part=json.load(r);assert r.headers.get('Access-Control-Allow-Origin')=='*'
   parts.append({'partNumber':i+1,'etag':part['etag']})
   if i==0:
    resumed=ok('/cloud/upload/prepare',req,token);assert any(p['partNumber']==1 and p['etag']==part['etag'] for p in resumed['parts']);prep=resumed
 else:
  boundary='bakawfs'+secrets.token_hex(12);raw=b''
  for key,value in [('token',prep['token']),('key',prep['key'])]:raw+=f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode()
  raw+=f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="fixture.bin"\r\nContent-Type: application/octet-stream\r\n\r\n'.encode()+data+f'\r\n--{boundary}--\r\n'.encode()
  reqform=urllib.request.Request(prep['upload_url'],data=raw,headers={'Content-Type':'multipart/form-data; boundary='+boundary,'Origin':'https://cloud.northwind.site'})
  try:
   with network_open(reqform,timeout=90) as r:assert r.status==200;assert r.headers.get('Access-Control-Allow-Origin')=='*'
  except urllib.error.HTTPError as e:
   if e.code!=614:raise
 complete={'job_id':prep['job_id'],'parts':parts};result=ok('/cloud/upload/complete',complete,token);assert ok('/cloud/upload/complete',complete,token)['node_id']==result['node_id']
 listing=ok('/list?path='+urllib.parse.quote(directory),token=token);node=next(n for n in listing['children'] if n['name']==name)
 with network_open(urllib.request.Request(node['url'],headers=({'Range':'bytes=0-31'} if data else {}) | {'Origin':'https://cloud.northwind.site'}),timeout=40) as r:
  got=r.read();assert got==data[:32];assert r.status==(206 if data else 200);assert r.headers.get('Access-Control-Allow-Origin')=='*'
 unsigned=node['url'].split('?',1)[0]
 try:network_open(unsigned,timeout=20);raise AssertionError('private object publicly readable')
 except urllib.error.HTTPError as e:assert e.code in (401,403)
 return node
small=upload('中文 # % +.txt','云上只读数据'.encode()*9);empty=upload('empty.txt',b'');large=upload('multipart.bin',bytes(range(256))*(11*1024*1024//256))
checks.append('direct form, empty and 11 MiB multipart uploads, exact ETag/size, Range, CORS, private ACL')
rename={'path':directory+'/中文 # % +.txt','dst':directory+'/改名.txt','operation_id':uid+'rename'}
renamed=ok('/rename',rename,token);assert ok('/rename',rename,token)==renamed
ok('/copy',{'path':directory+'/改名.txt','dst':directory+'/copy.txt','operation_id':uid+'copy'},token)
checks.append('rename replay after old path disappeared, immutable copy')
# Delete the parent before a delayed upload completion. The object may arrive,
# but a directory with the same visible name must never inherit its job.
late={'filename':directory+'/late.txt','size':1,'etag':etag(b'x'),'operation_id':uid+'late'}
prep=ok('/cloud/upload/prepare',late,token)
ok('/delete',{'path':directory,'operation_id':uid+'delete'},token)
ok('/mkdir',{'path':directory,'operation_id':uid+'replacement'},token)
# Invalid callback cannot advance state.
assert request('/cloud/callback?job='+prep['job_id']+'&mac=invalid',{})[0]==401
ok('/cancel',{'job_id':prep['job_id'],'filename':late['filename']},token)
assert request('/cloud/upload/complete',{'job_id':prep['job_id'],'parts':[]},token)[0]==409
assert not ok('/list?path='+urllib.parse.quote(directory),token=token).get('children')
checks.append('cancelled delayed commit cannot recreate deleted/replaced directory, callback authenticity')
# Async fetch uses provider work and callback; do not poll progress to finish it.
remote={'filename':directory+'/remote.txt','url':small['url'],'operation_id':uid+'remote'}
ok('/remote-upload',remote,token,202)
for _ in range(30):
 time.sleep(2)
 children=ok('/list?path='+urllib.parse.quote(directory),token=token).get('children',[])
 if any(n['name']=='remote.txt' for n in children):break
else:raise AssertionError('asynchronous callback did not commit without progress polling')
checks.append('remote transfer completes through authenticated provider callback with browser absent')
ok('/delete',{'path':directory,'operation_id':uid+'cleanup'},token)
report={'passed':True,'checks':checks,'tested_at':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'catalog':'isolated_validation'}
Path(a.output).write_text(json.dumps(report,ensure_ascii=False,indent=2));print(json.dumps(report,ensure_ascii=False))

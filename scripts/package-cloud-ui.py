#!/usr/bin/env python3
"""Build immutable CDN assets from the shared UI. Does not upload anything."""
import argparse,hashlib,json,re,shutil
from pathlib import Path,PurePosixPath
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[1]
def package(source,output,api):
 source=source.resolve();output=output.resolve()
 if output==source or source in output.parents:raise ValueError('output must be outside source')
 if api and (urlsplit(api).scheme!='https' or urlsplit(api).query or urlsplit(api).fragment):raise ValueError('API must be an HTTPS origin')
 files={p.relative_to(source).as_posix():p.read_bytes() for p in source.rglob('*') if p.is_file()}
 files['runtime-config.js']=('globalThis.BAKAWFS_CONFIG = '+json.dumps({'apiBase':api.rstrip('/')})+';\n').encode()
 patterns={'.html':re.compile(r'(?:src|href)\s*=\s*[\'"]([^\'"]+)[\'"]'),'.js':re.compile(r'(?:from|import)\s*\(?\s*[\'"]([^\'"]+)[\'"]'),'.css':re.compile(r'url\(\s*[\'"]?([^\'"\)]+)[\'"]?\s*\)')}
 built={};visiting=set()
 def build(key):
  if key in built:return built[key]
  if key in visiting:raise ValueError('cyclic static dependency')
  visiting.add(key);data=files[key];p=PurePosixPath(key);pattern=patterns.get(p.suffix)
  if pattern:
   text=data.decode()
   for ref in dict.fromkeys(pattern.findall(text)):
    if urlsplit(ref).scheme or ref.startswith(('/', '#')):continue
    target=(p.parent/ref).as_posix()
    # PurePosixPath does not normalize '..'; resolve lexically within source.
    target=__import__('posixpath').normpath(target)
    if target not in files:continue
    renamed,_=build(target);newref=__import__('posixpath').relpath(renamed,str(p.parent))
    if ref.startswith('./') and not newref.startswith('.'):newref='./'+newref
    text=text.replace(ref,newref)
   data=text.encode()
  newkey=key if key=='index.html' else str(p.with_name(p.stem+'.'+hashlib.sha256(data).hexdigest()[:12]+p.suffix))
  built[key]=(newkey,data);visiting.remove(key);return built[key]
 for key in files:build(key)
 if output.exists():shutil.rmtree(output)
 output.mkdir(parents=True)
 for key,data in built.values():
  dest=output/key;dest.parent.mkdir(parents=True,exist_ok=True);dest.write_bytes(data)
 # Manifest stays beside the published directory, not in the bucket.
 manifest={'api':api,'assets':[{'key':key,'sha256':hashlib.sha256(data).hexdigest(),'bytes':len(data)} for key,data in built.values()]}
 output.with_suffix('.manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
 print(json.dumps({'assets':len(built),'output':str(output),'api':api}))
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--source',type=Path,default=ROOT/'html');p.add_argument('--output',type=Path,default=ROOT/'dist/cloud-ui');p.add_argument('--api',required=True);a=p.parse_args();package(a.source,a.output,a.api)

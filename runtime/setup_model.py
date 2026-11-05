#!/usr/bin/env python3
"""Explicit online installation step; the analyzer never calls this script."""
import argparse,json,pathlib,urllib.request
from profiles import PROFILES,lock_for,sha256_file
p=argparse.ArgumentParser();p.add_argument('--output',required=True);p.add_argument('--profile',choices=PROFILES,default='e5');a=p.parse_args();profile=PROFILES[a.profile];out=pathlib.Path(a.output).resolve();out.mkdir(parents=True,exist_ok=True);expected=lock_for(profile);files={}
for remote,name in profile['artifacts'].items():
 target=out/name
 if not target.exists():
  print('Downloading',remote,flush=True);temp=target.with_suffix('.download');urllib.request.urlretrieve(f"https://huggingface.co/{profile['model']}/resolve/{profile['revision']}/{remote}",temp);temp.replace(target)
 files[name]=sha256_file(target)
 if files[name]!=expected[name]:raise ValueError(f'Unexpected SHA256 for {target}; remove that file and rerun setup')
manifest={k:v for k,v in profile.items() if k not in ['artifacts','lock']};manifest.update(profile=a.profile,files=files);(out/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n');print(out/'manifest.json')

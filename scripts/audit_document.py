#!/usr/bin/env python3
"""Read-only Markdown audit through MCP, with snapshot-bound pagination."""
import argparse,json,pathlib
from evaluate import Client
p=argparse.ArgumentParser();p.add_argument('--binary',required=True);p.add_argument('--config',required=True);p.add_argument('--document',required=True);p.add_argument('--output',required=True);a=p.parse_args();c=Client(pathlib.Path(a.binary).resolve(),pathlib.Path(a.config).resolve());results=[];metadata=None
try:
    for _ in range(600):
        st=c.call('index_status')
        if st['fresh'] and not st['loading']:break
        if st['state']=='failed':raise RuntimeError(st)
        import time;time.sleep(.05)
    args={'document':a.document,'limit':100,'index_snapshot':st['index_snapshot']}
    while True:
        r=c.call('audit_document',**args);results.extend(r['items']);metadata=r['meta']
        if not r['truncated']:break
        args['cursor']=r['next_cursor']
finally:c.close()
pathlib.Path(a.output).write_text(json.dumps({'index_snapshot':st['index_snapshot'],'meta':metadata,'items':results},ensure_ascii=False,indent=2)+'\n');print(a.output)

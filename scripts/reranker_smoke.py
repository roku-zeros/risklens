#!/usr/bin/env python3
"""Read-only real reranker check, including a repeated cached query."""
import argparse,json,pathlib,tempfile,time
from evaluate import Client
p=argparse.ArgumentParser();p.add_argument('--binary',required=True);p.add_argument('--config',required=True);p.add_argument('--output',required=True);p.add_argument('--candidates',type=int,default=5);a=p.parse_args()
cfg=json.loads(pathlib.Path(a.config).read_text());cfg['rerank_candidates']=a.candidates
with tempfile.TemporaryDirectory(prefix='risklens-reranker-smoke-') as td:
 config=pathlib.Path(td)/'config.json';config.write_text(json.dumps(cfg));c=Client(pathlib.Path(a.binary).resolve(),config)
 try:
  c.ready();times=[];responses=[]
  for _ in range(2):
   start=time.perf_counter();r=c.call('search_code',query='льготный период обучения',mode='hybrid',limit=5);times.append((time.perf_counter()-start)*1000);responses.append(r)
  assert all('rerank_score' in hit for r in responses for hit in r['items'])
  assert all(not any('reranker unavailable' in x for x in r.get('limitations',[])) for r in responses)
  assert [h['chunk']['chunk_id'] for h in responses[0]['items']]==[h['chunk']['chunk_id'] for h in responses[1]['items']]
  pathlib.Path(a.output).write_text(json.dumps({'real_reranker':True,'rerank_candidates':a.candidates,'timings_ms':times,'responses':responses},ensure_ascii=False,indent=2)+'\n');print(times)
 finally:c.close()

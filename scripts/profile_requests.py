#!/usr/bin/env python3
"""Record repeated calls in one MCP process, with server phases and raw responses."""
import argparse,json,pathlib,statistics,time
from evaluate import Client
p=argparse.ArgumentParser();p.add_argument('--binary',required=True);p.add_argument('--config',required=True);p.add_argument('--file',default='main.go');p.add_argument('--query',default='limit');p.add_argument('--repeats',type=int,default=5);p.add_argument('--wait-vectors',action='store_true');p.add_argument('--output',required=True);a=p.parse_args()
if not 1<=a.repeats<=100:p.error('--repeats must be 1..100')
out=pathlib.Path(a.output);out.parent.mkdir(parents=True,exist_ok=True);rows=[]
c=Client(pathlib.Path(a.binary).resolve(),pathlib.Path(a.config).resolve());startup=time.perf_counter()
try:
 if a.wait_vectors:status=c.ready()
 else:
  deadline=time.perf_counter()+600
  while True:
   status=c.call('index_status')
   if status['state']=='failed':raise RuntimeError(status)
   if status['fresh'] and not status['loading']:break
   if time.perf_counter()>deadline:raise TimeoutError('index not ready')
   time.sleep(.05)
 ready_seconds=time.perf_counter()-startup
 cases=[('status','index_status',{}),('source_1','get_source',{'file':a.file,'start_line':1,'end_line':1}),('source_40','get_source',{'file':a.file,'start_line':1,'end_line':40}),('symbols','search_symbols',{'query':a.query,'limit':1})]
 for mode in ['lexical','vector','hybrid']:cases.append((mode,'search_code',{'query':a.query,'mode':mode,'rerank':False,'limit':5}))
 for label,tool,args in cases:
  for i in range(a.repeats):
   start=time.perf_counter();result=c.rpc('tools/call',{'name':tool,'arguments':args});elapsed=(time.perf_counter()-start)*1000
   data=result.get('structuredContent',{});rows.append({'case':label,'repeat':i,'tool':tool,'arguments':args,'duration_ms':elapsed,'response_bytes':len(json.dumps(result,ensure_ascii=False).encode()),'is_error':result.get('isError',False),'server_meta':data.get('meta'),'result':result})
   out.with_suffix('.partial.json').write_text(json.dumps({'complete':False,'calls':rows},ensure_ascii=False,indent=2)+'\n')
 summary={}
 for label,_,_ in cases:
  selected=[r for r in rows if r['case']==label];values=[r['duration_ms'] for r in selected];summary[label]={'calls':len(selected),'errors':sum(r['is_error'] for r in selected),'median_ms':statistics.median(values),'min_ms':min(values),'max_ms':max(values)}
 report={'complete':True,'readiness_seconds_excluding_launch_handshake':ready_seconds,'initial_status':status,'summary':summary,'calls':rows,'limitations':['One persistent MCP process; startup/readiness excluded from request latency.','A no-error search can still use fallback: inspect limitations/channels/vector status.','response_bytes is reserialized JSON size, not exact wire size or model token count.','Small repeat counts do not establish p95 or production performance.','Without --wait-vectors background inference may contend with requests.']}
 out.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');out.with_suffix('.partial.json').unlink(missing_ok=True);print(json.dumps(summary,indent=2))
finally:c.close()

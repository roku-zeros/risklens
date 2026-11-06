#!/usr/bin/env python3
"""Compare sequential read requests and strict-snapshot batches in one live server."""
import argparse,json,pathlib,statistics,time
from evaluate import Client
p=argparse.ArgumentParser();p.add_argument('--binary',required=True);p.add_argument('--config',required=True);p.add_argument('--file',default='main.go');p.add_argument('--calls',type=int,default=10);p.add_argument('--repeats',type=int,default=3);p.add_argument('--output',required=True);a=p.parse_args()
if not 1<=a.calls<=20 or not 1<=a.repeats<=100:p.error('calls 1..20, repeats 1..100')
c=Client(pathlib.Path(a.binary).resolve(),pathlib.Path(a.config).resolve());rows=[]
try:
 status=c.ready()
 for _ in range(a.repeats):
  args={'file':a.file,'start_line':1,'end_line':1}
  start=time.perf_counter();single=[c.call('get_source',**args) for _ in range(a.calls)];single_ms=(time.perf_counter()-start)*1000
  start=time.perf_counter();batch=c.call('batch_tools',calls=[{'tool':'get_source','arguments':args} for _ in range(a.calls)]);batch_ms=(time.perf_counter()-start)*1000
  assert batch['fresh'] and not batch['truncated'] and not any(item['is_error'] for item in batch['items'])
  assert [item['result']['items'] for item in batch['items']]==[item['items'] for item in single]
  rows.append({'single_ms':single_ms,'batch_ms':batch_ms,'single_freshness_scans':sum(r['meta']['freshness_scans'] for r in single),'batch_freshness_scans':batch['meta']['freshness_scans'],'batch_meta':batch['meta']})
 report={'calls_per_group':a.calls,'repeats':a.repeats,'initial_status':status,'rows':rows,'summary':{'single_median_ms':statistics.median(r['single_ms'] for r in rows),'batch_median_ms':statistics.median(r['batch_ms'] for r in rows)},'limitations':['Local warm persistent server; not production latency evidence.','Batch items are fresh only under the outer snapshot validation; byte budget and post-change failure apply.']}
 pathlib.Path(a.output).write_text(json.dumps(report,indent=2,ensure_ascii=False)+'\n');print(report['summary'])
finally:c.close()

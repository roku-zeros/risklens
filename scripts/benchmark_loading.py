#!/usr/bin/env python3
"""Compare technical-index loading with identical sources; no embeddings or app execution.
Each sample uses a fresh measurement process so child RSS maxima do not carry over.
"""
import argparse, json, pathlib, platform, subprocess, sys, tempfile, time, statistics
p=argparse.ArgumentParser();p.add_argument('--binary',required=True);p.add_argument('--baseline');p.add_argument('--root',required=True);p.add_argument('--output');p.add_argument('--repeats',type=int,default=3);p.add_argument('--worker',action='store_true');a=p.parse_args()
if a.worker:
    with tempfile.TemporaryDirectory(prefix='risklens-load-') as cache:
        start=time.perf_counter();r=subprocess.run([str(pathlib.Path(a.binary).resolve()),'--root',str(pathlib.Path(a.root).resolve()),'--cache',cache,'--tool','project_status'],capture_output=True,text=True,check=True);elapsed=time.perf_counter()-start
        status=json.loads(r.stdout);rss=None
        try:
            import resource
            rss=resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss
            if sys.platform!='darwin':rss*=1024
        except (ImportError,AttributeError):pass
        print(json.dumps({'seconds':elapsed,'max_child_rss_bytes':rss,'status':status}))
else:
    if not a.output:p.error('--output is required')
    binaries={'current':a.binary}
    if a.baseline:binaries={'baseline':a.baseline,**binaries}
    samples={k:[] for k in binaries}
    for _ in range(a.repeats):
        for name,binary in binaries.items():
            r=subprocess.run([sys.executable,__file__,'--worker','--binary',binary,'--root',a.root],capture_output=True,text=True,check=True);samples[name].append(json.loads(r.stdout))
    source=pathlib.Path(a.root);files=list(source.rglob('*.go'))
    report={'hardware':platform.platform(),'corpus':{'go_files':len(files),'lines':sum(len(f.read_bytes().splitlines()) for f in files)},'summary':{},'samples':samples,'limitations':['Technical-index loading only; embeddings are not configured.','New process/empty RiskLens cache; OS/Go build caches are warm.','max_child_rss is the OS child maximum, not summed process-tree RSS.','heap is status-time Go heap; current version collects the disposable loader graph before publication.','Synthetic module, not the user production repository.']}
    for name,rs in samples.items():report['summary'][name]={'median_seconds':statistics.median(r['seconds'] for r in rs),'median_go_heap_bytes':statistics.median(r['status']['go_heap_bytes'] for r in rs),'max_child_rss_bytes':max((r['max_child_rss_bytes'] or 0) for r in rs) or None}
    pathlib.Path(a.output).write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report['summary'],indent=2))

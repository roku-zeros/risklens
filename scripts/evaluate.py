#!/usr/bin/env python3
"""Reproducible real-model MCP benchmark. Copies input module; never edits original.
For large corpora allow enough free disk for a source copy. No deterministic provider.
"""
import argparse, json, pathlib, platform, queue, shutil, statistics, subprocess, tempfile, threading, time

class Client:
    def __init__(self,binary,config):
        self.errors=[]
        self.proc=subprocess.Popen([str(binary),'--config',str(config)],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,bufsize=1)
        self.q=queue.Queue();self.i=0
        def read():
            for line in self.proc.stdout:
                try:self.q.put(json.loads(line))
                except Exception as e:self.q.put({'invalid_stdout':line,'error':str(e)})
        def logs():
            for line in self.proc.stderr:self.errors.append(line.rstrip())
        threading.Thread(target=read,daemon=True).start();threading.Thread(target=logs,daemon=True).start()
        self.rpc('initialize',{'protocolVersion':'2025-11-25','capabilities':{},'clientInfo':{'name':'risklens-evaluate','version':'1'}})
        self.proc.stdin.write(json.dumps({'jsonrpc':'2.0','method':'notifications/initialized'})+'\n');self.proc.stdin.flush()
    def rpc(self,method,params):
        self.i+=1;self.proc.stdin.write(json.dumps({'jsonrpc':'2.0','id':self.i,'method':method,'params':params},ensure_ascii=False)+'\n');self.proc.stdin.flush()
        r=self.q.get(timeout=600)
        if r.get('id')!=self.i or 'error' in r:raise RuntimeError(r)
        return r['result']
    def call(self,name,**arguments):
        r=self.rpc('tools/call',{'name':name,'arguments':arguments})
        if r.get('isError'):raise RuntimeError(r)
        return r.get('structuredContent') or json.loads(r['content'][0]['text'])
    def ready(self):
        deadline=time.perf_counter()+600
        while time.perf_counter()<deadline:
            s=self.call('index_status')
            if s['state']=='failed' or s['vectors']=='degraded':raise RuntimeError(s)
            if s['fresh'] and not s['loading'] and s['vectors']=='ready':return s
            time.sleep(.05)
        raise TimeoutError('index not ready')
    def close(self):
        self.proc.stdin.close()
        try:self.proc.wait(timeout=30)
        except subprocess.TimeoutExpired:self.proc.kill();self.proc.wait()
        if self.proc.returncode:raise RuntimeError(self.errors)

class Memory:
    def __init__(self,pid):self.pid=pid;self.peak=None;self.stop=False;self.thread=threading.Thread(target=self.run,daemon=True);self.thread.start()
    def run(self):
        while not self.stop:
            try:
                rows=subprocess.check_output(['ps','-axo','pid=,ppid=,rss='],text=True,stderr=subprocess.DEVNULL)
                data=[tuple(map(int,r.split())) for r in rows.splitlines() if len(r.split())==3]
                pids={self.pid}
                for _ in range(5):pids.update(pid for pid,ppid,rss in data if ppid in pids)
                self.peak=max(self.peak or 0,sum(rss*1024 for pid,ppid,rss in data if pid in pids))
            except Exception:pass
            time.sleep(.1)
    def close(self):self.stop=True;self.thread.join(timeout=2)

def main():
    p=argparse.ArgumentParser()
    p.add_argument('--binary',required=True);p.add_argument('--config',required=True);p.add_argument('--queries',required=True);p.add_argument('--output',required=True);p.add_argument('--repeats',type=int,default=3);p.add_argument('--require-reranker',action='store_true')
    a=p.parse_args();binary=pathlib.Path(a.binary).resolve();cfg=json.loads(pathlib.Path(a.config).read_text());queries=json.loads(pathlib.Path(a.queries).read_text());out=pathlib.Path(a.output).resolve();out.parent.mkdir(parents=True,exist_ok=True)
    if a.require_reranker and not cfg.get('reranker_command'):p.error('--require-reranker needs a configured local reranker')
    with tempfile.TemporaryDirectory(prefix='risklens-eval-') as td:
        td=pathlib.Path(td);source=pathlib.Path(cfg['root']);root=td/'module';shutil.copytree(source,root,ignore=shutil.ignore_patterns('.git','.risklens','node_modules','.venv','models','bin'))
        cfg['root']=str(root);cfg['cache']=str(td/'cache');config=td/'config.json';config.write_text(json.dumps(cfg))
        files=list(root.rglob('*.go'));corpus={'go_files':len(files),'lines':sum(len(f.read_text().splitlines()) for f in files),'source_bytes':sum(f.stat().st_size for f in files)}
        start=time.perf_counter();client=Client(binary,config);mem=Memory(client.proc.pid);status=client.ready();cold=time.perf_counter()-start
        rows=[]
        for mode in ['lexical','vector','hybrid']:
            for q in queries:
                durations=[];result=None
                for _ in range(a.repeats):
                    start=time.perf_counter();result=client.call('search_code',query=q['query'],mode=mode,limit=10,**({'rerank':True} if mode=='hybrid' and a.require_reranker else {}));durations.append((time.perf_counter()-start)*1000)
                stage=result.get('meta',{}).get('reranker',{})
                if a.require_reranker and mode=='hybrid' and stage.get('status') not in ['completed','cache_hit']:
                    mem.close();client.close();raise RuntimeError('reranker experiment not executed: '+json.dumps(stage))
                ids=[x['chunk']['symbol_id'] for x in result['items']]
                rows.append({'mode':mode,'query':q['query'],'expected':q['expected'],'top10':ids,'hit5':bool(set(ids[:5])&set(q['expected'])),'hit10':bool(set(ids[:10])&set(q['expected'])),'cold_query_ms':durations[0],'warm_median_ms':statistics.median(durations[1:] or durations),'durations_ms':durations,'reranker_stage':stage,'reranked_items':sum('rerank_score' in hit for hit in result['items']),'limitations':result.get('limitations',[])})
                out.with_suffix('.partial.json').write_text(json.dumps({'complete':False,'rows':rows},indent=2,ensure_ascii=False)+'\n')
                print(f"{mode}: {q['query']} ({durations[0]:.1f} ms)",flush=True)
        cache_bytes=sum(x.stat().st_size for x in pathlib.Path(cfg['cache']).rglob('*') if x.is_file())
        # Append one comment inside one existing function so embedding content changes.
        target=None
        for f in files:
            text=f.read_text()
            marker=text.find('func ')
            brace=text.find('{',marker) if marker>=0 else -1
            if brace>=0:target=f;before=text;f.write_text(text[:brace+1]+'\n// benchmark: changed one function\n'+text[brace+1:]);break
        if target is None:raise RuntimeError('no function to mutate in corpus copy')
        start=time.perf_counter();client.call('refresh_index');updated=client.ready();update_seconds=time.perf_counter()-start
        mem.close();peak=mem.peak;client.close()
        start=time.perf_counter();client=Client(binary,config);cached_status=client.ready();cached_start=time.perf_counter()-start
        start=time.perf_counter();client.call('search_code',query=queries[0]['query'],mode='vector',limit=10);runtime_cold_query=(time.perf_counter()-start)*1000
        client.close()
        summary={}
        for mode in ['lexical','vector','hybrid']:
            rs=[r for r in rows if r['mode']==mode];summary[mode]={'queries':len(rs),'hit5':sum(r['hit5'] for r in rs),'hit10':sum(r['hit10'] for r in rs),'median_warm_ms':statistics.median(r['warm_median_ms'] for r in rs),'median_first_ms':statistics.median(r['cold_query_ms'] for r in rs)}
        hardware={'platform':platform.platform(),'machine':platform.machine(),'python':platform.python_version()}
        for k,command in [('cpu',['sysctl','-n','machdep.cpu.brand_string']),('ram_bytes',['sysctl','-n','hw.memsize'])]:
            try:hardware[k]=subprocess.check_output(command,text=True,stderr=subprocess.DEVNULL).strip()
            except Exception:pass
        report={'hardware':hardware,'corpus':corpus,'model_key':cfg.get('model_key','intfloat/multilingual-e5-small@614241f622f53c4eeff9890bdc4f31cfecc418b3:fp32:384:mask-mean:l2:e5-prefix:window510:v2'),'reranker_model_key':cfg.get('reranker_model_key'),'repeats':a.repeats,'threads':2,'batch_size':cfg.get('batch_size',8),'first_index_seconds':cold,'cached_start_seconds':cached_start,'update_one_function_seconds':update_seconds,'runtime_cold_vector_query_ms':runtime_cold_query,'sampled_process_tree_peak_rss_bytes':peak,'vector_index_bytes':cache_bytes,'initial_status':status,'updated_status':updated,'cached_status':cached_status,'summary':summary,'rows':rows,'limitations':['Small bilingual synthetic corpus; not evidence of performance on production strategy.','Top-k counts distinct parent symbols by default in 0.2.0; raw chunk mode requires distinct_symbols=false. Hit@k is at least one expected symbol.','First query per case may be warm in OS/model cache; runtime cold startup is reported separately.','RSS samples process tree at 100ms intervals; not a precise allocator maximum.','Update includes full Go reload and changed-chunk inference.']}
        out.write_text(json.dumps(report,indent=2,ensure_ascii=False)+'\n');out.with_suffix('.partial.json').unlink(missing_ok=True);print(json.dumps(summary,indent=2));print(out)
if __name__=='__main__':main()

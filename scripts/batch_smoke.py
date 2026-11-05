#!/usr/bin/env python3
"""Compare real-model embeddings at runtime batch sizes 1 and 4."""
import argparse,json,pathlib,subprocess,math
p=argparse.ArgumentParser();p.add_argument('--config',required=True);p.add_argument('--output',required=True);a=p.parse_args();c=json.loads(pathlib.Path(a.config).read_text());texts=['Ограничение по доходу','func Update(p *State){p.Limit++}', 'address taking pointer alias field writes '+('x += 1; '*300)]
results=[]
for batch in [1,4]:
    proc=subprocess.Popen(c['embedding_command']+['--batch-size',str(batch)],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
    try:
        ready=json.loads(proc.stdout.readline());proc.stdin.write(json.dumps({'model_key':ready['model_key'],'mode':'document','texts':texts},ensure_ascii=False)+'\n');proc.stdin.flush();r=json.loads(proc.stdout.readline());assert 'error' not in r,r;results.append(r)
    finally:proc.stdin.close();proc.wait(timeout=20)
cos=[]
for x,y in zip(results[0]['vectors'],results[1]['vectors']):cos.append(sum(a*b for a,b in zip(x,y))/math.sqrt(sum(v*v for v in x)*sum(v*v for v in y)))
assert min(cos)>.995,cos
assert results[0]['token_counts']==results[1]['token_counts'] and results[0]['windows']==results[1]['windows']
pathlib.Path(a.output).write_text(json.dumps({'model_key':ready['model_key'],'batch_cosines':cos,'token_counts':results[0]['token_counts'],'windows':results[0]['windows']},indent=2)+'\n');print(a.output)

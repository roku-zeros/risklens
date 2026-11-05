#!/usr/bin/env python3
"""Run the configured real worker offline; verify normalization and complete token windows."""
import argparse, json, math, pathlib, subprocess
p=argparse.ArgumentParser();p.add_argument('--config',required=True);a=p.parse_args();c=json.loads(pathlib.Path(a.config).read_text())
proc=subprocess.Popen(c['embedding_command'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
try:
    ready=json.loads(proc.stdout.readline());assert ready['ready']
    texts=['ограничение лимита по доходу', 'func Limit(income int) int { return income * 4 }', 'длинная функция '+('if income > 10 { limit += 42 }; '*250)]
    proc.stdin.write(json.dumps({'model_key':ready['model_key'],'mode':'document','texts':texts},ensure_ascii=False)+'\n');proc.stdin.flush()
    result=json.loads(proc.stdout.readline());assert 'error' not in result,result
    norms=[math.sqrt(sum(x*x for x in v)) for v in result['vectors']]
    assert all(abs(v-1)<1e-5 for v in norms)
    assert result['token_counts'][2]>512 and result['windows'][2]>1
    print(json.dumps({'model_key':ready['model_key'],'dimension':ready['dimension'],'token_counts':result['token_counts'],'windows':result['windows'],'norms':norms,'real_model':True},indent=2))
finally:
    proc.stdin.close()
    try:proc.wait(timeout=5)
    except subprocess.TimeoutExpired:proc.kill();proc.wait()

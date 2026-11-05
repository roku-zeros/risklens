#!/usr/bin/env python3
"""Read-only demonstration against examples/strategy with real configured embeddings."""
import argparse, hashlib, json, pathlib
from evaluate import Client
p=argparse.ArgumentParser();p.add_argument('--binary',required=True);p.add_argument('--config',required=True);p.add_argument('--output',required=True);a=p.parse_args();cfg=json.loads(pathlib.Path(a.config).read_text());root=pathlib.Path(cfg['root'])
def hashes():return {str(f.relative_to(root)):hashlib.sha256(f.read_bytes()).hexdigest() for f in root.rglob('*') if f.is_file() and (f.suffix=='.go' or f.name in ['go.mod','go.sum'])}
before=hashes();c=Client(pathlib.Path(a.binary).resolve(),pathlib.Path(a.config).resolve());transcript=[]
def call(name,**args):
    r=c.call(name,**args);transcript.append({'tool':name,'arguments':args,'result':r});return r
try:
    c.ready();call('project_status');call('index_status')
    funcs=call('search_symbols',query='Calculate',kind='function',package='example.com/strategy/consumer')['items'];fn=funcs[0]['id']
    field=call('search_symbols',query='Limit',kind='field',package='example.com/strategy/state')['items'][0]['id']
    final=call('search_symbols',query='Finalize',kind='function',package='example.com/strategy/state')['items'][0]['id']
    call('get_symbol',symbol_id=fn);call('find_references',symbol_id=fn,limit=5);call('get_callers',symbol_id=fn,limit=5);call('get_callees',symbol_id=final,limit=10)
    call('find_field_accesses',symbol_id=field,limit=8);call('get_source',file='main.go',start_line=1,end_line=10)
    call('search_code',query='льготный период пока клиент учится',mode='hybrid',limit=5)
    call('build_context',symbol_ids=[field],depth=1,budget_bytes=6000,limit=6)
    entry=call('search_symbols',query='Evaluate',kind='function',package='example.com/strategy')['items'][0]['id']
    definition=call('get_symbol',symbol_id=entry)
    symbol=definition['items']
    call('trace_field',symbol_id=field,entry_symbol_id=entry,include_candidates=True,depth=5,budget_bytes=12000,limit=8)
    call('verify_evidence',claims=[{'symbol_id':entry,'file':symbol['position']['file'],'start_line':symbol['position']['line'],'end_line':symbol['end_line'],'expected_text':'func Evaluate','source_hash':definition['meta']['source_hash']}])
    call('audit_document',document='notes.md',limit=20)
    call('refresh_index');c.ready();call('index_status')
finally:c.close()
assert before==hashes(),'demonstration must not change Go sources or module files'
out=pathlib.Path(a.output);out.write_text(json.dumps({'real_embeddings':True,'sources_unchanged':True,'calls':transcript},indent=2,ensure_ascii=False)+'\n');print(out)

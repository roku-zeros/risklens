#!/usr/bin/env python3
"""Offline cross-encoder scores. No text generation and no silent truncation."""
import argparse,json,pathlib,sys
from profiles import PROFILES,sha256_file,lock_for

def main():
 p=argparse.ArgumentParser();p.add_argument('--model-dir',required=True);p.add_argument('--threads',type=int,default=2);p.add_argument('--batch-size',type=int,default=4);a=p.parse_args()
 import numpy as np
 import onnxruntime as ort
 from tokenizers import Tokenizer
 root=pathlib.Path(a.model_dir);manifest=json.loads((root/'manifest.json').read_text());profile=PROFILES['bge-reranker'];key=profile['model_key']
 if manifest.get('model_key')!=key:raise ValueError('incompatible reranker manifest')
 for name,expected in lock_for(profile).items():
  if manifest['files'].get(name)!=expected or sha256_file(root/name)!=expected:raise ValueError('incompatible reranker artifact '+name)
 tok=Tokenizer.from_file(str(root/'tokenizer.json'));tok.no_truncation();tok.no_padding();options=ort.SessionOptions();options.intra_op_num_threads=max(1,min(a.threads,16));options.inter_op_num_threads=1
 session=ort.InferenceSession(str(root/profile['model_file']),sess_options=options,providers=['CPUExecutionProvider']);output=session.get_outputs()[0].name
 def send(x):print(json.dumps(x),flush=True)
 send(dict(ready=True,model_key=key,dimension=1,role='reranker'))
 for line in sys.stdin:
  try:
   r=json.loads(line)
   if r.get('model_key')!=key:raise ValueError('incompatible reranker key')
   docs=r['documents'];query=r['query']
   if not 1<=len(docs)<=100 or len(query.encode())>16000:raise ValueError('invalid rerank request')
   pairs=[tok.encode(query,doc,add_special_tokens=True).ids for doc in docs]
   if any(len(ids)>profile['max_tokens'] for ids in pairs):raise ValueError('pair exceeds 2048 tokens; no truncation performed')
   scores=[];batch=max(1,min(a.batch_size,16))
   for i in range(0,len(pairs),batch):
    group=pairs[i:i+batch];length=max(len(v) for v in group);ids=np.full((len(group),length),1,dtype=np.int64);mask=np.zeros_like(ids)
    for row,tokens in enumerate(group):ids[row,:len(tokens)]=tokens;mask[row,:len(tokens)]=1
    feed={v.name:({'input_ids':ids,'attention_mask':mask,'token_type_ids':np.zeros_like(ids)})[v.name] for v in session.get_inputs()};values=session.run([output],feed)[0].reshape(-1)
    if len(values)!=len(group) or not np.isfinite(values).all():raise ValueError('invalid reranker output')
    scores.extend(values.astype(float).tolist())
   send(dict(model_key=key,scores=scores))
  except Exception as e:send(dict(error=str(e)))
if __name__=='__main__':
 try:main()
 except Exception as e:print('RiskLens reranker startup: '+str(e),file=sys.stderr);sys.exit(1)

#!/usr/bin/env python3
"""Offline batched ONNX worker. Fixed profiles; no HTTP, remote code, or generation."""
import argparse,json,pathlib,sys,os
os.environ['HF_HUB_OFFLINE']='1';os.environ['TOKENIZERS_PARALLELISM']='false'
from profiles import PROFILES,lock_for,sha256_file

def main():
 p=argparse.ArgumentParser();p.add_argument('--model-dir',required=True);p.add_argument('--threads',type=int,default=2);p.add_argument('--batch-size',type=int,default=4);a=p.parse_args()
 import numpy as np
 import onnxruntime as ort
 from tokenizers import Tokenizer
 root=pathlib.Path(a.model_dir);manifest=json.loads((root/'manifest.json').read_text());name=manifest.get('profile','e5');profile=PROFILES[name];key=profile['model_key'];dimension=profile['dimension']
 if manifest['revision']!=profile['revision'] or manifest['dimension']!=dimension:raise ValueError('incompatible model manifest')
 for artifact,expected in lock_for(profile).items():
  if artifact not in profile['artifacts'].values():continue
  if manifest['files'].get(artifact)!=expected or sha256_file(root/artifact)!=expected:raise ValueError('incompatible or corrupt pinned artifact: '+artifact)
 tokenizer=Tokenizer.from_file(str(root/'tokenizer.json'));tokenizer.no_truncation();tokenizer.no_padding();options=ort.SessionOptions();options.intra_op_num_threads=max(1,min(a.threads,16));options.inter_op_num_threads=1
 session=ort.InferenceSession(str(root/profile['model_file']),sess_options=options,providers=['CPUExecutionProvider']);inputs=session.get_inputs();output=session.get_outputs()[0].name
 def send(v):print(json.dumps(v,ensure_ascii=False),flush=True)
 send(dict(ready=True,model_key=key,dimension=dimension,max_tokens=profile['max_tokens'],profile=name))
 for line in sys.stdin:
  try:
   r=json.loads(line)
   if r.get('model_key')!=key:raise ValueError('incompatible model_key')
   texts=r['texts'];mode=r['mode']
   if mode not in ['query','document'] or not 1<=len(texts)<=128:raise ValueError('invalid request')
   if any(len(t.encode())>2_000_000 for t in texts) or sum(len(t.encode()) for t in texts)>8_000_000:raise ValueError('embedding input exceeds resource limit')
   jobs=[];counts=[];windows=[]
   for index,text in enumerate(texts):
    prefix=profile['query_prefix'] if mode=='query' else profile['document_prefix']
    prefix_ids=tokenizer.encode(prefix,add_special_tokens=False).ids
    if profile['pooling']=='mean':
     ids=tokenizer.encode(text,add_special_tokens=False).ids;width=510-len(prefix_ids);counts.append(len(ids)+len(prefix_ids)+2);pieces=[ids[i:i+width] for i in range(0,max(1,len(ids)),width)]
     for piece in pieces:jobs.append((index,[0]+prefix_ids+piece+[2],max(1,len(piece))))
    else:
     ids=tokenizer.encode(prefix+text,add_special_tokens=False).ids;counts.append(len(ids));pieces=[];offset=0
     while offset<len(ids) or not pieces:
      lead=[] if offset==0 else tokenizer.encode(prefix.rstrip(),add_special_tokens=False).ids
      width=profile['max_tokens']-len(lead);piece=ids[offset:offset+width];pieces.append(piece);jobs.append((index,lead+piece or [151643],max(1,len(piece))));offset+=max(1,len(piece))
    windows.append(len(pieces))
   pooled=[[] for _ in texts];weights=[[] for _ in texts];batch_size=1 if profile["pooling"]=="last" else max(1,min(a.batch_size,16))
   # Pack similar token lengths together and keep original window order for pooling.
   order=list(range(len(jobs)))
   if batch_size>1:
    order.sort(key=lambda i:len(jobs[i][1]))
   window_vectors=[None]*len(jobs)
   for offset in range(0,len(jobs),batch_size):
    indices=order[offset:offset+batch_size];group=[jobs[i] for i in indices];length=max(len(ids) for _,ids,_ in group);pad=1 if profile['pooling']=='mean' else 151643
    token_ids=np.full((len(group),length),pad,dtype=np.int64);mask=np.zeros_like(token_ids)
    for row,(_,ids,_) in enumerate(group):
     start=0 if profile['pooling']=='mean' else length-len(ids);token_ids[row,start:start+len(ids)]=ids;mask[row,start:start+len(ids)]=1
    feed={}
    for item in inputs:
     if item.name=='input_ids':feed[item.name]=token_ids
     elif item.name=='attention_mask':feed[item.name]=mask
     elif item.name=='token_type_ids':feed[item.name]=np.zeros_like(token_ids)
     elif item.name=='position_ids':feed[item.name]=np.maximum(np.cumsum(mask,axis=1)-1,0)
     elif item.name.startswith('past_key_values.'):
      feed[item.name]=np.empty((len(group),int(item.shape[1]),0,int(item.shape[3])),dtype=np.float32)
     else:raise ValueError('unsupported ONNX input '+item.name)
    hidden=session.run([output],feed)[0]
    if hidden.ndim!=3:raise ValueError('expected token embeddings')
    if profile['pooling']=='mean':vectors=(hidden*mask[:,:,None]).sum(axis=1)/mask.sum(axis=1)[:,None]
    else:vectors=hidden[:,-1,:]
    for row,i in enumerate(indices):window_vectors[i]=vectors[row]
   for (index,_,weight),vector in zip(jobs,window_vectors):
    pooled[index].append(vector);weights[index].append(weight)
   vectors=[]
   for rows,ws in zip(pooled,weights):
    v=np.average(rows,axis=0,weights=ws);v=v/max(float(np.linalg.norm(v)),1e-12)
    if len(v)!=dimension or not np.isfinite(v).all():raise ValueError('invalid model output')
    vectors.append(v.tolist())
   send(dict(model_key=key,dimension=dimension,vectors=vectors,token_counts=counts,windows=windows))
  except Exception as e:send(dict(error=str(e)))
if __name__=='__main__':
 try:main()
 except Exception as e:print('RiskLens embedding startup: '+str(e),file=sys.stderr);sys.exit(1)

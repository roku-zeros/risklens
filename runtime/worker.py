#!/usr/bin/env python3
"""Offline JSONL embedding worker. No HTTP listener, network, or remote code."""
import argparse, hashlib, json, pathlib, sys, os
os.environ['HF_HUB_OFFLINE']='1'
os.environ['TOKENIZERS_PARALLELISM']='false'
REV='614241f622f53c4eeff9890bdc4f31cfecc418b3'
KEY=f'intfloat/multilingual-e5-small@{REV}:fp32:384:mask-mean:l2:e5-prefix:window510:v2'

def main():
    p=argparse.ArgumentParser()
    p.add_argument('--model-dir',required=True)
    p.add_argument('--threads',type=int,default=2)
    a=p.parse_args()
    import numpy as np
    import onnxruntime as ort
    from tokenizers import Tokenizer
    root=pathlib.Path(a.model_dir)
    manifest=json.loads((root/'manifest.json').read_text())
    if manifest['revision']!=REV or manifest['dimension']!=384:
        raise ValueError('incompatible model manifest; run setup_model.py in a new directory')
    expected=json.loads((pathlib.Path(__file__).resolve().parent/'model-lock.json').read_text())['files']
    for name in ['model.onnx','tokenizer.json']:
        if manifest['files'][name]!=expected[name]:raise ValueError(f'incompatible pinned artifact: {name}')
        if hashlib.sha256((root/name).read_bytes()).hexdigest()!=manifest['files'][name]:
            raise ValueError(f'corrupt model artifact: {name}')
    tokenizer=Tokenizer.from_file(str(root/'tokenizer.json'))
    tokenizer.no_truncation(); tokenizer.no_padding()
    options=ort.SessionOptions();options.intra_op_num_threads=max(1,min(a.threads,16));options.inter_op_num_threads=1
    session=ort.InferenceSession(str(root/'model.onnx'),sess_options=options,providers=['CPUExecutionProvider'])
    def send(v): print(json.dumps(v,ensure_ascii=False),flush=True)
    send(dict(ready=True,model_key=KEY,dimension=384,max_tokens=512))
    for line in sys.stdin:
        try:
            r=json.loads(line)
            if r.get('model_key')!=KEY: raise ValueError('incompatible model_key')
            texts=r['texts']; mode=r['mode']
            if mode not in ['query','document'] or not 1<=len(texts)<=128: raise ValueError('invalid request')
            vectors=[];counts=[];windows=[]
            for text in texts:
                if len(text.encode())>2_000_000: raise ValueError('embedding input exceeds 2 MB')
                prefix='query: ' if mode=='query' else 'passage: '
                prefix_ids=tokenizer.encode(prefix,add_special_tokens=False).ids
                ids=tokenizer.encode(text,add_special_tokens=False).ids
                counts.append(len(ids)+len(prefix_ids)+2)
                # Cover every token. Oversized indivisible AST units use windows, never silent truncation.
                width=510-len(prefix_ids)
                pieces=[ids[i:i+width] for i in range(0,max(1,len(ids)),width)]
                windows.append(len(pieces)); pooled=[];weights=[]
                for piece in pieces:
                    enc=np.array([[0]+prefix_ids+piece+[2]],dtype=np.int64)
                    feed={'input_ids':enc,'attention_mask':np.ones_like(enc),'token_type_ids':np.zeros_like(enc)}
                    output=session.run(None,{v.name:feed[v.name] for v in session.get_inputs()})[0]
                    v=output.mean(axis=1)[0] if output.ndim==3 else output[0]
                    pooled.append(v);weights.append(max(1,len(piece)))
                v=np.average(pooled,axis=0,weights=weights);v=v/max(float(np.linalg.norm(v)),1e-12)
                if len(v)!=384 or not np.isfinite(v).all(): raise ValueError('invalid model output')
                vectors.append(v.tolist())
            send(dict(model_key=KEY,dimension=384,vectors=vectors,token_counts=counts,windows=windows))
        except Exception as e:
            send(dict(error=str(e)))
if __name__=='__main__':
    try: main()
    except Exception as e:
        print('RiskLens embedding startup: '+str(e),file=sys.stderr)
        sys.exit(1)

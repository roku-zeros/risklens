#!/usr/bin/env python3
"""Explicit online installation step. Never called by the analyzer."""
import argparse, hashlib, json, pathlib, urllib.request
REV = '614241f622f53c4eeff9890bdc4f31cfecc418b3'
MODEL = 'intfloat/multilingual-e5-small'
p = argparse.ArgumentParser()
p.add_argument('--output', required=True)
a = p.parse_args()
out = pathlib.Path(a.output).resolve()
out.mkdir(parents=True, exist_ok=True)
expected=json.loads((pathlib.Path(__file__).resolve().parent/"model-lock.json").read_text())["files"]
files = {}
for remote, local in [('onnx/model.onnx','model.onnx'), ('onnx/tokenizer.json','tokenizer.json'), ('README.md','MODEL_CARD.md')]:
    target = out / local
    if not target.exists():
        print('Downloading', remote, flush=True)
        temp = target.with_suffix('.download')
        urllib.request.urlretrieve(f'https://huggingface.co/{MODEL}/resolve/{REV}/{remote}', temp)
        temp.replace(target)
    files[local] = hashlib.sha256(target.read_bytes()).hexdigest()
    if local in ['model.onnx','tokenizer.json'] and files[local]!=expected[local]:
        raise ValueError(f'Unexpected SHA256 for {target}; remove the file and rerun setup')
manifest = dict(model=MODEL, revision=REV, dimension=384, max_tokens=512,
                pooling='attention-mask-mean+l2', query_prefix='query: ', document_prefix='passage: ',
                files=files)
(out/'manifest.json').write_text(json.dumps(manifest, indent=2)+'\n')
print(out/'manifest.json')

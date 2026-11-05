"""Pinned local inference profiles. No network access in this module."""
import pathlib, json
BASE=pathlib.Path(__file__).resolve().parent
E5_REV='614241f622f53c4eeff9890bdc4f31cfecc418b3'
QWEN_REV='c25a394dd583836952667c12f008335071b3f43d'
PROFILES={
'e5':dict(model='intfloat/multilingual-e5-small',revision=E5_REV,dimension=384,max_tokens=512,pooling='mean',model_file='model.onnx',query_prefix='query: ',document_prefix='passage: ',model_key=f'intfloat/multilingual-e5-small@{E5_REV}:fp32:384:mask-mean:l2:e5-prefix:window510:v2',artifacts={'onnx/model.onnx':'model.onnx','onnx/tokenizer.json':'tokenizer.json'},lock='model-lock.json'),
'qwen3':dict(model='onnx-community/Qwen3-Embedding-0.6B-ONNX',base_model='Qwen/Qwen3-Embedding-0.6B',revision=QWEN_REV,dimension=1024,max_tokens=2048,pooling='last',model_file='model_quantized.onnx',query_prefix='Instruct: Given a code search query, retrieve relevant Go source code implementing the described behavior.\nQuery: ',document_prefix='',model_key=f'onnx-community/Qwen3-Embedding-0.6B-ONNX@{QWEN_REV}:q8:1024:last:l2:go-instruct:windows2048:batch1:v2',artifacts={'onnx/model_quantized.onnx':'model_quantized.onnx','tokenizer.json':'tokenizer.json','config.json':'config.json'},lock='qwen3-model-lock.json')}
def sha256_file(path):
 import hashlib
 h=hashlib.sha256()
 with open(path,'rb') as f:
  for piece in iter(lambda:f.read(1024*1024),b''):h.update(piece)
 return h.hexdigest()
def lock_for(profile):return json.loads((BASE/profile['lock']).read_text())['files']
RERANK_REV='6f5ff65298512715a1e669753bc754d2bc8f367b'
PROFILES['bge-reranker']=dict(model='onnx-community/bge-reranker-v2-m3-ONNX',base_model='BAAI/bge-reranker-v2-m3',revision=RERANK_REV,dimension=1,max_tokens=2048,pooling='pair-logit',role='reranker',model_file='model_quantized.onnx',model_key=f'onnx-community/bge-reranker-v2-m3-ONNX@{RERANK_REV}:q8:pair:max2048:logit:v1',artifacts={'onnx/model_quantized.onnx':'model_quantized.onnx','tokenizer.json':'tokenizer.json','config.json':'config.json'},lock='reranker-model-lock.json')

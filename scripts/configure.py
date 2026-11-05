#!/usr/bin/env python3
"""Create an absolute-path configuration; no installation or process launch."""
import argparse, json, pathlib, os
p=argparse.ArgumentParser();p.add_argument('--root',required=True);p.add_argument('--python',required=True);p.add_argument('--model-dir',required=True);p.add_argument('--cache');p.add_argument('--threads',type=int,default=2);p.add_argument('--output',default='config.local.json');a=p.parse_args()
root=pathlib.Path(a.root).resolve();python=pathlib.Path(os.path.abspath(os.path.expanduser(a.python)));model=pathlib.Path(a.model_dir).resolve();worker=pathlib.Path(__file__).resolve().parents[1]/'runtime'/'worker.py'
if not (root/'go.mod').is_file():p.error('--root must contain go.mod')
if not python.is_file():p.error('--python must be an executable file path')
if not (model/'manifest.json').is_file():p.error('install the model with runtime/setup_model.py first')
c={'root':str(root),'cache':str(pathlib.Path(a.cache).resolve()) if a.cache else str(root/'.risklens'),'embedding_command':[str(python),str(worker),'--model-dir',str(model),'--threads',str(a.threads)],'batch_size':8,'chunk_bytes':1200,'max_results':100,'max_source_bytes':16000,'max_context_bytes':48000,'max_files':30000,'max_project_bytes':134217728,'operation_seconds':300,'build_flags':[]}
out=pathlib.Path(a.output).resolve();out.write_text(json.dumps(c,indent=2,ensure_ascii=False)+'\n');print(out)

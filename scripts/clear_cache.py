#!/usr/bin/env python3
"""Clear only RiskLens-owned vector files. Refuse while server.lock exists."""
import argparse, json, pathlib
p=argparse.ArgumentParser();p.add_argument('--config',required=True);a=p.parse_args();c=json.loads(pathlib.Path(a.config).read_text());cache=pathlib.Path(c.get('cache') or pathlib.Path(c['root'])/'.risklens').resolve()
if (cache/'server.lock').exists():p.error('stop the server first; inspect lock PID before removing a stale server.lock manually')
for name in ['vectors.json']:
    f=cache/name
    if f.is_file():f.unlink();print('Removed',f)
for f in cache.glob('vectors-*.tmp'):
    if f.is_file():f.unlink();print('Removed',f)

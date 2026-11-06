#!/usr/bin/env python3
"""Clear owned vector files through the same OS lock protocol as the server."""
import argparse,pathlib,subprocess,shutil
p=argparse.ArgumentParser();p.add_argument('--config',required=True);p.add_argument('--binary');a=p.parse_args()
if a.binary:cmd=[str(pathlib.Path(a.binary).resolve())]
else:
 go=shutil.which('go')
 if not go:p.error('Go not found; set PATH or pass --binary for RiskLens 0.5+')
 cmd=[go,'-C',str(pathlib.Path(__file__).resolve().parents[1]),'run','./cmd/risklens-mcp']
subprocess.run(cmd+['--config',str(pathlib.Path(a.config).resolve()),'--clear-cache'],check=True)

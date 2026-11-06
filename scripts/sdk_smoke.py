#!/usr/bin/env python3
"""Optional interoperability check: pip install mcp==1.26.0 (Python >=3.10)."""
import argparse, asyncio, json, os, time, shutil, pathlib
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
async def run(a):
    if a.go_run:
        command=shutil.which(a.go)
        if not command:raise RuntimeError('Go executable not found: '+a.go)
        args=['-C',str(pathlib.Path(__file__).resolve().parents[1]),'run','./cmd/risklens-mcp','--config',os.path.abspath(a.config)]
    else:
        command=os.path.abspath(a.binary);args=['--config',os.path.abspath(a.config)]
    params=StdioServerParameters(command=command,args=args,env=dict(os.environ))
    async with stdio_client(params) as (read,write):
        async with ClientSession(read,write) as session:
            init=await session.initialize()
            tools=await session.list_tools()
            assert len(tools.tools)==15
            for _ in range(600):
                result=await session.call_tool('index_status',{})
                st=result.structuredContent
                if st['state']=='failed':raise RuntimeError(st)
                if st['fresh'] and not st['loading']:break
                await asyncio.sleep(.05)
            result=await session.call_tool('search_symbols',{'query':'Calculate','limit':5})
            assert not result.isError and result.structuredContent['items']
            result=await session.call_tool('search_code',{'query':'льготный период обучения','mode':'hybrid','limit':5})
            assert not result.isError
            types=await session.call_tool('search_symbols',{'query':'Application','kind':'type','package':'example.com/strategy/state'})
            owner=types.structuredContent['items'][0]['id']
            definition=await session.call_tool('get_symbol',{'symbol_id':owner})
            field=next(f for f in definition.structuredContent['meta']['fields'] if f['name']=='Limit')
            assert field['promoted'] and field['definition_indexed']
            found=await session.call_tool('search_symbols',{'owner_id':owner,'kind':'field','query':'Limit'})
            assert found.structuredContent['items'][0]['id']==field['symbol_id']
            accesses=await session.call_tool('find_field_accesses',{'symbol_id':field['symbol_id']})
            assert not accesses.isError and accesses.structuredContent['items']
            wrong=await session.call_tool('find_field_accesses',{'symbol_id':'example.com/strategy/state#field::Application.Limit'})
            assert wrong.isError and wrong.structuredContent['details']['code']=='unknown_symbol_id'
            assert any(v['id']==field['symbol_id'] for v in wrong.structuredContent['details']['candidates'])
            entry=await session.call_tool('search_symbols',{'query':'Evaluate','kind':'function','package':'example.com/strategy'})
            entry_id=entry.structuredContent['items'][0]['id']
            trace=await session.call_tool('trace_field',{'symbol_id':field['symbol_id'],'entry_symbol_id':entry_id,'include_candidates':True,'depth':5,'limit':10})
            assert not trace.isError and trace.structuredContent['items']
            definition=await session.call_tool('get_symbol',{'symbol_id':entry_id})
            symbol=definition.structuredContent['items']
            verified=await session.call_tool('verify_evidence',{'claims':[{'symbol_id':entry_id,'file':symbol['position']['file'],'start_line':symbol['position']['line'],'end_line':symbol['end_line'],'expected_text':'func Evaluate','source_hash':definition.structuredContent['meta']['source_hash']}]})
            assert verified.structuredContent['items'][0]['status']=='source_verified'
            audit=await session.call_tool('audit_document',{'document':'notes.md','limit':20})
            assert not audit.isError and any(c['status']=='missing_file' for c in audit.structuredContent['items']) and any(c['status']=='missing_symbol' for c in audit.structuredContent['items'])
            print(json.dumps({'launch':'go_run' if a.go_run else 'binary','field_trace':True,'evidence_verification':True,'document_audit':True,'canonical_field_navigation':True,'structured_recovery':True,'sdk':'mcp==1.26.0','protocol':init.protocolVersion,'server':init.serverInfo.model_dump(),'tools':len(tools.tools),'snapshot':st['index_snapshot'],'search_items':len(result.structuredContent['items'])},indent=2))
if __name__=='__main__':
    p=argparse.ArgumentParser();launch=p.add_mutually_exclusive_group(required=True);launch.add_argument('--binary');launch.add_argument('--go-run',action='store_true');p.add_argument('--go',default='go');p.add_argument('--config',required=True);asyncio.run(run(p.parse_args()))

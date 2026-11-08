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
            assert len(tools.tools)==20
            for _ in range(600):
                result=await session.call_tool('index_status',{})
                st=result.structuredContent
                if st['state']=='failed':raise RuntimeError(st)
                if st['fresh'] and not st['loading']:break
                await asyncio.sleep(.05)
            packages=await session.call_tool('project_map',{'limit':2})
            page=packages.structuredContent
            assert not packages.isError and not page['fresh'] and page['meta']['freshness_scans']==0
            subgraph=await session.call_tool('project_map',{'node_id':page['items'][0]['id'],'edge_kind':'imports','index_snapshot':page['index_snapshot']})
            assert not subgraph.isError and subgraph.structuredContent['meta']['edges']
            cached=await session.call_tool('get_source',{'file':'main.go','index_snapshot':page['index_snapshot'],'freshness':'snapshot'})
            assert not cached.isError and not cached.structuredContent['fresh'] and cached.structuredContent['meta']['freshness_scans']==0
            package_search=await session.call_tool('search_code',{'query':'shared mutable Application','package':'example.com/strategy/state','mode':'lexical','index_snapshot':page['index_snapshot'],'freshness':'snapshot'})
            scoped=package_search.structuredContent
            assert not package_search.isError and scoped['meta']['freshness_scans']==0
            assert scoped['meta']['search_scope']['candidate_chunks'] < scoped['meta']['search_scope']['index_chunks']
            assert any('package_context' in item['channels'] for item in scoped['items'])
            inventory=await session.call_tool('list_files',{'limit':2})
            inv=inventory.structuredContent
            assert inv['truncated'] and inv['next_cursor'] and inv['meta']['total_results']>2
            source=await session.call_tool('get_source',{'file':'main.go','end_line':-1})
            src=source.structuredContent
            assert src['items']['eof'] and src['items']['total_lines']>0
            assert src['meta']['freshness_scans']==2 and src['meta']['timings_ms']['freshness']>=0
            result=await session.call_tool('search_symbols',{'query':'Calculate','limit':5})
            assert not result.isError and result.structuredContent['items']
            result=await session.call_tool('search_code',{'query':'льготный период обучения','mode':'hybrid','limit':5})
            assert not result.isError
            types=await session.call_tool('search_symbols',{'query':'Application','kind':'type','package':'example.com/strategy/state'})
            owner=types.structuredContent['items'][0]['id']
            resolved=await session.call_tool('resolve_reference',{'query':'Application.Limit','namespace':'go','owner_id':owner})
            assert resolved.structuredContent['items']
            batched=await session.call_tool('batch_tools',{'calls':[{'tool':'get_source','arguments':{'file':'main.go','end_line':1}},{'tool':'list_files','arguments':{'limit':2}},{'tool':'get_source','arguments':{'file':'missing-file.go'}}]})
            batch=batched.structuredContent
            assert batch['fresh'] and batch['meta']['freshness_scans']==2 and len(batch['items'])==3
            assert batch['items'][2]['is_error'] and not batch['items'][0]['is_error']
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
            print(json.dumps({'batch_strict_snapshot':True,'typed_field_resolution':True,'launch':'go_run' if a.go_run else 'binary','field_trace':True,'evidence_verification':True,'document_audit':True,'canonical_field_navigation':True,'structured_recovery':True,'sdk':'mcp==1.26.0','protocol':init.protocolVersion,'server':init.serverInfo.model_dump(),'tools':len(tools.tools),'snapshot':st['index_snapshot'],'search_items':len(result.structuredContent['items'])},indent=2))
if __name__=='__main__':
    p=argparse.ArgumentParser();launch=p.add_mutually_exclusive_group(required=True);launch.add_argument('--binary');launch.add_argument('--go-run',action='store_true');p.add_argument('--go',default='go');p.add_argument('--config',required=True);asyncio.run(run(p.parse_args()))

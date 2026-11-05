#!/usr/bin/env python3
"""Optional interoperability check: pip install mcp==1.26.0 (Python >=3.10)."""
import argparse, asyncio, json, os, time
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
async def run(a):
    params=StdioServerParameters(command=os.path.abspath(a.binary),args=['--config',os.path.abspath(a.config)],env=dict(os.environ))
    async with stdio_client(params) as (read,write):
        async with ClientSession(read,write) as session:
            init=await session.initialize()
            tools=await session.list_tools()
            assert len(tools.tools)==12
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
            print(json.dumps({'sdk':'mcp==1.26.0','protocol':init.protocolVersion,'server':init.serverInfo.model_dump(),'tools':len(tools.tools),'snapshot':st['index_snapshot'],'search_items':len(result.structuredContent['items'])},indent=2))
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--binary',required=True);p.add_argument('--config',required=True);asyncio.run(run(p.parse_args()))

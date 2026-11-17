"""Local Codex/Claude wire fixture. Never contacts a model provider."""
import base64, json, os, pathlib, sys
runtime = pathlib.Path(sys.argv[0]).name
sequence = 0
model = ''
def send(value):
    print(json.dumps(value), flush=True)
def receive():
    return json.loads(sys.stdin.readline())
def control(request):
    global sequence
    sequence += 1
    send({'type':'control_request','request_id':str(sequence),'request':request})
    response = receive()['response']
    assert response['subtype'] == 'success', response
    return response['response']
def tool(name, arguments):
    global sequence
    sequence += 1
    if runtime == 'claude':
        reply = control({'subtype':'mcp_message','server_name':'mitos','message':{'jsonrpc':'2.0','id':sequence,'method':'tools/call','params':{'name':name,'arguments':arguments}}})['mcp_response']['result']
        assert not reply.get('isError'), reply
        return json.loads(reply['content'][0]['text'])
    send({'id':sequence,'method':'item/tool/call','params':{'threadId':'fixture-thread','turnId':'turn','callId':str(sequence),'tool':name,'arguments':arguments}})
    reply = receive()['result']
    assert reply['success'], reply
    return json.loads(reply['contentItems'][0]['text'])
def section(prompt, kind):
    for block in prompt.split('\n## ')[1:]:
        heading, _, body = block.partition('\n')
        if heading.split(' (')[0] == kind:
            return body.rstrip('\n')
    raise Exception('missing section ' + kind)
def execute(prompt, orchestrator):
    expected = runtime + '-test'
    assert model == expected, (model, expected)
    if runtime == 'claude':
        assert os.environ.get('MITOS_FIXTURE_ACCOUNT') == 'selected-profile', 'environment profile lost'
    if orchestrator:
        context = json.loads(section(prompt, 'graph_state'))
        library = json.loads(section(prompt, 'agent_library'))
        policy = library['execution_policy']
        rule = next(r for r in policy['harness_rules'] if r['match'].get('role') == 'mitos.architect')
        assert next(h for h in policy['harnesses'] if h['id'] == rule['harness'])['runtime'] == 'claude'
        latest = {}
        for entry in context['journal']: latest[entry['task']] = entry
        workers = [{'id':e['task'],'kind':e['kind'],'state':e['state'],'run_id':e['run_id']} for e in latest.values() if e['kind'] == 'application']
        plan = {'explanation':'Read fixture evidence.','operations':[],'accept':[],'complete':bool(workers),'result':{'title':'','markdown':'','source_tasks':[],'items':[]}}
        if not workers:
            for role in ['engineer','architect']:
                plan['operations'].append({'kind':'add','spec':{'id':role,'parent':context['root_task'],'kind':'application','required':True,'criteria':'Produce evidence.','acceptance':'operator','traits':{'nature':'','size':'small','intelligence':'low'},'brief':{'title':'Fixture '+role,'intent':'Report on the assignment.','app_id':'agent:mitos.'+role,'timeout_minutes':1}}})
        else:
            assert len(workers) == 2, workers
            for f in workers:
                assert f['state'] == 'done', f
                artifacts = tool('artifact_list', {'run_id':f['run_id'],'cursor':0,'limit':32})
                report = next(a for a in artifacts['artifacts'] if a['role'] == 'report')
                content = tool('artifact_read', {'run_id':f['run_id'],'artifact_id':report['id'],'offset':0,'limit':32768})
                assert b'evidence' in base64.b64decode(content['data'])
                plan['accept'].append({'task':f['id'],'rationale':'Read exact registered evidence.'})
            plan['result'] = {'title':'Mixed agents','markdown':'Codex implemented; Claude designed. Both reports inspected.','source_tasks':[f['id'] for f in workers],'items':[]}
        return plan
    text = runtime + ' evidence using ' + model
    pathlib.Path('report.md').write_text(text)
    artifact = tool('artifact_register', {'path':'report.md','role':'report'})
    tool('result_submit', {'version':'mitos.handoff/v1','key':'fixture-result','summary':text,'artifacts':[{'artifact_id':artifact['id'],'role':'report'}],'checks':[],'limitations':[],'unresolved_requests':[]})
    return text
if '--version' in sys.argv:
    print('1.0.0'); sys.exit(0)
if runtime == 'claude':
    model = sys.argv[sys.argv.index('--model')+1]
    init = receive()
    assert init['request']['hooks']['PreToolUse']
    send({'type':'control_response','response':{'subtype':'success','request_id':init['request_id'],'response':{}}})
    user = receive()
    send({'type':'system','subtype':'init','session_id':'fixture-thread'})
    control({'subtype':'mcp_message','server_name':'mitos','message':{'jsonrpc':'2.0','id':0,'method':'initialize','params':{}}})
    tools = control({'subtype':'mcp_message','server_name':'mitos','message':{'jsonrpc':'2.0','id':1,'method':'tools/list','params':{}}})
    assert any(t['name']=='result_submit' for t in tools['mcp_response']['result']['tools'])
    orchestrator = '--json-schema' in sys.argv
    result = execute(user['message']['content'], orchestrator)
    send({'type':'result','subtype':'success','session_id':'fixture-thread','result':'' if orchestrator else result,**({'structured_output':result} if orchestrator else {})})
    for _ in sys.stdin: pass
else:
    for line in sys.stdin:
        request = json.loads(line)
        method = request.get('method')
        if method == 'initialize': send({'id':request['id'],'result':{}})
        elif method in ['thread/start','thread/resume']:
            model = request['params']['model']
            send({'id':request['id'],'result':{'thread':{'id':'fixture-thread'},'model':model}})
        elif method == 'turn/start':
            send({'id':request['id'],'result':{'turn':{'id':'turn'}}})
            result = execute(request['params']['input'][0]['text'], 'outputSchema' in request['params'])
            text = result if isinstance(result,str) else json.dumps(result)
            send({'method':'item/completed','params':{'threadId':'fixture-thread','turnId':'turn','item':{'id':'final','type':'agentMessage','phase':'final_answer','text':text}}})
            send({'method':'turn/completed','params':{'threadId':'fixture-thread','turn':{'id':'turn','status':'completed'}}})

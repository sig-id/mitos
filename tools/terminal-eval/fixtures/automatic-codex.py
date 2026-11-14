import json,pathlib,sys,time
import subprocess,tempfile
if 'app-server' in sys.argv:
 def send(value):
  print(json.dumps(value),flush=True)
 for line in sys.stdin:
  request=json.loads(line);method=request.get('method')
  if method=='initialize':send({'id':request['id'],'result':{}})
  elif method=='thread/start':send({'id':request['id'],'result':{'thread':{'id':'fixture-thread'},'model':'fixture-project-model'}})
  elif method=='turn/start':
   send({'id':request['id'],'result':{'turn':{'id':'fixture-turn'}}})
   params=request['params'];prompt=params['input'][0]['text']
   if 'outputSchema' not in params:
    send({'method':'item/agentMessage/delta','params':{'delta':'Inspecting the assignment and checking saved evidence.\n'}})
    marker=pathlib.Path(__file__).with_suffix('.approval-requested')
    if not marker.exists():
     marker.touch()
     send({'id':99,'method':'item/commandExecution/requestApproval','params':{'threadId':'fixture-thread','turnId':'fixture-turn','itemId':'fixture-command','command':'printf checked','cwd':str(pathlib.Path.cwd())}})
     response=json.loads(next(sys.stdin))
     assert response['id']==99 and response['result']['decision']=='accept',response
    send({'method':'item/started','params':{'item':{'type':'commandExecution','command':'printf checked','cwd':str(pathlib.Path.cwd())}}})
    send({'method':'item/commandExecution/outputDelta','params':{'delta':'checked\n'}})
    send({'method':'item/completed','params':{'item':{'type':'commandExecution','command':'printf checked','exitCode':0}}})
    time.sleep(3)
    send({'method':'item/agentMessage/delta','params':{'delta':'Preparing the answer and its report.\n'}})
    time.sleep(3)
   with tempfile.TemporaryDirectory() as temporary:
    result=pathlib.Path(temporary,'result.txt')
    args=[sys.executable,__file__,'exec','--output-last-message',str(result),'--model','fixture-project-model']
    if 'outputSchema' in params:args+=['--output-schema','fixture-schema']
    subprocess.run(args,input=prompt,text=True,stdout=sys.stderr,check=True)
    text=result.read_text()
    send({'method':'item/agentMessage/delta','params':{'delta':text}})
    send({'method':'item/completed','params':{'item':{'id':'final','type':'agentMessage','phase':'final_answer','text':text}}})
   send({'method':'turn/completed','params':{'turn':{'id':'fixture-turn','status':'completed'}}})
 sys.exit(0)
prompt=sys.argv[sys.argv.index('--')+1] if '--' in sys.argv else sys.stdin.read();time.sleep(.4)
if 'You are the Mitos setup assistant.' in prompt:
 ctx=json.loads(prompt.split('Setup conversation:\n')[1])
 if ctx['target']=='defaults':
  reply={'message':'Your personal defaults are ready for review.','preferences':{'runtime':'codex','model':'fixture-global-model','minutes':120,'attempts':96,'guidance':'Keep personal responses concise.'}}
 elif len(ctx['messages'])==1:
  reply={'message':'Should every change require review?'}
 else:
  reply={'message':'Added review guidance and explicit execution settings.','document':'# Answer project\n\nUse **saved evidence**. Require review before publishing.\n\n```mitos\n{"model":"fixture-project-model","minutes":45}\n```\n'}
 if '--output-last-message' in sys.argv:pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1]).write_text(json.dumps(reply))
 else: print(json.dumps(reply))
 sys.exit(0)
assert pathlib.Path('AGENTS.md').exists() or pathlib.Path('agents.md').exists()
if '--model' in sys.argv:assert sys.argv[sys.argv.index('--model')+1]=='fixture-project-model'
out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if 'Previous conversation and saved evidence:' in prompt:
 prior=json.loads(prompt.split('Previous conversation and saved evidence:\n')[1].split('\nThese are prior results')[0])
 assert '42' in prior['prior_work']['result']['markdown']
 assert prior['saved_sources']
 for source in prior['saved_sources']:assert pathlib.Path(source['directory'],'report.md').exists()

if '--output-schema' in sys.argv:
 ctx=json.loads(prompt.split('\nGraph context:\n')[1].split('\nSaved upstream evidence')[0])
 workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 plan={'explanation':'Evidence meets the goal.','operations':[],'accept':[],'complete':bool(workers)}
 if not workers:
  for i in range(2):
   plan['operations'].append({'kind':'add','spec':{'id':'work'+str(i),'parent':ctx['root_task'],'kind':'application','required':True,'acceptance':'operator','criteria':'Produce evidence for the answer.','brief':{'title':['Implement change','Review change'][i],'intent':['Create answer.txt with 42.','Check upstream patch and evidence.'][i],'app_id':'agent:'+ (['mitos.engineer','mitos.reviewer'][i]),'timeout_minutes':30,'scheduling':{'deps':[{'from':ref,'guard':'candidate_available','payload':'data','artifacts':['report.md','changes.patch']} for ref in (['work0'] if i else [])]}},'agent':({'role':'Answer specialist' if i else '','instructions':'Check the answer carefully.' if i else ''} if ('Answer specialist' if i else '') else None)}})
 else:
  assert len(workers)==2
  for f in workers:
   assert f['state']=='done',str(f)
   plan['accept'].append({'task':f['id'],'rationale':'Read the saved native report and reviewed its evidence.'})
 plan['result']={'title':'Answer','markdown':'The answer is **42**.\n\n- [ ] Example open finding.\n\n[Answer file](mitos://'+workers[0]['id']+'/answer.txt) and [source](mitos://'+workers[0]['id']+'/README.md#L1).','source_tasks':[f['id'] for f in workers]} if workers else {'title':'','markdown':'','source_tasks':[]}
 if workers:plan['result']['items']=[{'kind':'changes','title':'Review implementation','task':workers[0]['id'],'path':'changes.patch','url':''},{'kind':'link','title':'Example pull request','task':'','path':'','url':'https://example.com/pull/1'}]
 out.write_text(json.dumps(plan))
else:
 if 'Check upstream patch and evidence.' in prompt:
  inputs=json.loads(prompt.split('\nSaved upstream evidence (read these files):\n')[1].split('\nWork only')[0])
  assert len(inputs)==1, inputs
  assert '42' in pathlib.Path(inputs[0]['directory'],'changes.patch').read_text()
  assert pathlib.Path('answer.txt').read_text().strip()=='42'
 else:
  pathlib.Path('answer.txt').write_text('42\n')
 out.write_text('# Result\n\nThe answer is **42**.\n\nEvidence checked. See [source file](README.md#L1), `answer.txt`, and answer.txt.')
print('native execution log')

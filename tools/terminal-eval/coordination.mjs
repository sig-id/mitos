import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:net';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temporary=await mkdtemp(path.join(tmpdir(),'mitos-coordination-')),data=path.join(temporary,'data');await mkdir(data);
await writeFile(path.join(temporary,'README.md'),'# Parser library\nA Go parser library. Run go test ./... to check changes.\n');
const python=execFileSync('which',['python3'],{encoding:'utf8'}).trim(),fixture=path.join(temporary,'agent.py');
await writeFile(fixture,`import os,sys,json,subprocess,time
if sys.argv[1]=='analysis':
 assert 'MITOS_SESSION_ID' not in os.environ
 assert 'analysis only' in ' '.join(sys.argv)
 if os.path.exists('fail-analysis'):
  print('Fixture sign-in failed: please sign in again',file=sys.stderr,flush=True)
  sys.exit(2)
 print('x'*200000,file=sys.stderr,flush=True)
 print('Reading README.md — analysis in progress',file=sys.stderr,flush=True)
 print("\\nexec\\n/bin/bash -lc 'cat README.md' in "+os.getcwd()+"\\n succeeded in 1ms:\\n# Parser library\\n<script>window.analysisInjected=true</script>\\n\\nthinking\\nRead the manifest next.",file=sys.stderr,flush=True)
 while not os.path.exists('progress-update'): time.sleep(0.05)
 print("\\nexec\\n/bin/bash -lc 'cat go.mod' in "+os.getcwd()+"\\n succeeded in 1ms:\\npackage parser\\nfunc Parse() bool { return true }\\n",file=sys.stderr,flush=True)
 while not os.path.exists('release-analysis'): time.sleep(0.05)
 text=open('README.md').read()
 assert 'Parser library' in text
 print("codex\\n## Findings\\n- **Ready** to review\\n\\n\`\`\`go\\nfunc Parse() bool { return true }\\n\`\`\`\\ntokens used\\n1,234",file=sys.stderr,flush=True)
 print(json.dumps({'context':'A Go parser library. Evidence: README.md. Suggested check: go test ./... (not run).','goal':''}),flush=True)
 sys.exit(0)
mitos=os.environ['MITOS_BIN']
def desk(*args):
 return json.loads(subprocess.check_output([mitos,'desk',*args],text=True))
if sys.argv[1]=='coordinator':
 assert 'You are the Mitos coordinator.' in ' '.join(sys.argv)
 worker=desk('start-session','--agent','worker-test','--prompt','Write parser tests and publish evidence')
 print('DELEGATED:'+worker['id'],flush=True)
else:
 assert 'You are a Mitos worker.' in ' '.join(sys.argv)
 print('WORKSPACE:'+os.getcwd(),flush=True)
for line in sys.stdin:
 if line.startswith('ack '):
  desk('acknowledge',line.strip().split()[1]); print('ACKNOWLEDGED',flush=True)
`);
await writeFile(path.join(data,'agents.json'),JSON.stringify({applications:['coordinator','worker'].map(role=>({id:role+'-test',name:role+' fixture',kind:'agent',executables:[python],args:['-u',fixture,role],context_args:['-u',fixture,'analysis'],prompt:'positional'}))}));
const reservation=createServer();await new Promise(r=>reservation.listen(0,'127.0.0.1',r));const port=reservation.address().port;await new Promise(r=>reservation.close(r));const base=`http://127.0.0.1:${port}`;
const server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',data,'--workspace-root',temporary],{cwd:root,stdio:['ignore','ignore','pipe']});
let log='';server.stderr.on('data',b=>log=(log+b).slice(-8000));const exited=new Promise(r=>server.once('exit',r));
const api=async(p,body)=>{const r=await fetch(base+p,body===undefined?{}:{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});const text=await r.text();assert.equal(r.status,200,text);return JSON.parse(text)};
const wait=async(fn)=>{for(let i=0;i<150;i++){if(await fn())return;await new Promise(r=>setTimeout(r,100));}throw Error('Timed out: '+log);};
let browser;
try{
 await wait(async()=>{try{return(await fetch(base+'/healthz')).ok}catch{return false}});
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});
 const page=await browser.newPage({viewport:{width:1440,height:1050}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+'/workspace');await page.getByRole('button',{name:'Open a project',exact:true}).click();
 await page.locator('#project-directory').fill(temporary);assert.equal(await page.locator('#project-name').inputValue(),path.basename(temporary));
 await page.locator('#project-name').fill('Parser project');await page.locator('#project-form [type=submit]').click();
 await page.waitForURL('**/projects/*');
 const openedProject=new URL(page.url()).pathname.split('/').at(-1);await page.goto(base+'/workspace?project='+openedProject+'&setup=true');
 await page.locator('#session-dialog').waitFor({state:'visible'});
 await page.locator('#session-agent').selectOption('coordinator-test');
 await page.waitForFunction(()=>!document.querySelector('#session-form [type=submit]').disabled);
 assert.equal(await page.locator('#session-form [type=submit]').textContent(),'Analyze project');
 assert.equal(await page.locator('#session-prompt').inputValue(),'');
 const project=new URL(page.url()).searchParams.get('project');
 await page.locator('#session-form [type=submit]').click();
 await page.locator('#setup-output-panel').waitFor({state:'visible'});
 await page.waitForFunction(()=>document.querySelector('#setup-output').textContent.includes('Reading README.md'));
 assert.match(await page.locator('#coordinator-list').textContent(),/analysis running/);
 await page.locator('#analysis-activity .analysis-command').waitFor();
 assert.equal(await page.locator('#setup-output').isVisible(),false);
 const command=page.locator('#analysis-activity .analysis-command').first();
 assert.equal(await command.getAttribute('open'),null);
 await command.locator(':scope > summary').click();
 const commandOutput=command.locator('.analysis-command-output');
 await commandOutput.locator(':scope > summary').click();
 assert.match(await commandOutput.textContent(),/window.analysisInjected/);
 assert.equal(await page.evaluate(()=>window.analysisInjected),undefined,'log content must never become HTML');
 await writeFile(path.join(temporary,'progress-update'),'');
 await page.waitForFunction(()=>document.querySelectorAll('#analysis-activity .analysis-command').length===2);
 assert.notEqual(await command.getAttribute('open'),null,'new activity preserves expanded commands');
 assert.notEqual(await commandOutput.getAttribute('open'),null,'new activity preserves expanded output');
 await page.locator('#analysis-collapse').click();
 assert.equal(await page.locator('#analysis-activity details[open]').count(),0);
 await page.locator('#analysis-expand').click();
 assert.notEqual(await command.getAttribute('open'),null);
 await page.locator('#analysis-raw-mode').click();
 assert.equal(await page.locator('#setup-output').isVisible(),true);
 assert.equal(await page.locator('#analysis-activity').isVisible(),false);
 await page.locator('#analysis-activity-mode').click();

 assert.equal(await page.locator('.coordinator-link').getAttribute('aria-current'),'page');
 if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-analysis.png')});
 const colors=await page.evaluate(()=>[getComputedStyle(document.querySelector('.workspace-selector-trigger')).backgroundColor,getComputedStyle(document.querySelector('.coordinator-link')).backgroundColor]);assert.notEqual(colors[0],colors[1]);
 await page.reload();await page.waitForFunction(()=>document.querySelector('#setup-output').textContent.includes('Reading README.md'));
 assert.equal((await api(`/api/projects/${project}/coordination`)).draft.status,'running');
 // A failed refresh remains visible until that source recovers, even while
 // unrelated sidebar requests succeed. Network loss must not freeze the clock.
 const coordinationURL=base+`/api/projects/${project}/coordination`;
 await page.route(coordinationURL,route=>route.abort());
 await page.locator('#desk-connection').waitFor({state:'visible'});
 await page.waitForTimeout(3500);
 assert.equal(await page.locator('#desk-connection').isVisible(),true);
 assert.equal(await page.locator('#desk-error').isVisible(),false);
 await page.unroute(coordinationURL);
 await page.locator('#desk-connection').waitFor({state:'hidden'});
 await page.context().setOffline(true);
 await page.locator('#desk-connection').waitFor({state:'visible'});
 const clockText=await page.locator('#setup-output-status').textContent();
 await page.waitForFunction(previous=>document.querySelector('#setup-output-status').textContent!==previous,clockText,{timeout:1800});
 await page.context().setOffline(false);
 await page.locator('#desk-connection').waitFor({state:'hidden'});
 assert.equal(await page.locator('#desk-error').isVisible(),false);

 await writeFile(path.join(temporary,'release-analysis'),'');
 await page.getByRole('heading',{name:'Analysis complete',exact:true}).waitFor();
 assert.equal(await page.locator('#profile-dialog').isVisible(),false,'review is an explicit next step');
 assert.equal(await page.locator('#coordination-workspace').isVisible(),false);
 assert.equal(await page.locator('.coordination-panel').isVisible(),false);
 assert.equal(await page.locator('#setup-steps [aria-current=step]').getAttribute('data-step'),'2');
 const tabY=(await page.locator('.workspace-tabs').boundingBox()).y;
 for(const tab of ['terminal','changes','resources','coordination']){
  await page.locator('#tab-'+tab).click();
  assert.equal((await page.locator('.workspace-tabs').boundingBox()).y,tabY,'tabs stay in place');
  assert.match(await page.locator('#workspace-stage').textContent(),/Review/);
  assert.equal(await page.locator('.session-header').isVisible(),false);
 }
 await page.locator('#setup-output-panel > summary').click();
 await page.waitForFunction(()=>document.querySelector('#analysis-count').textContent.includes('1,234 tokens'));
 assert.equal(await page.getByText('Other output',{exact:true}).count(),0);
 const response=page.locator('.analysis-note').filter({has:page.locator('summary', {hasText:'Agent response'})});
 assert.doesNotMatch(await response.textContent(),/tokens used|1,234/);
 await response.locator(':scope > summary').click();
 assert.equal(await response.locator('h4').textContent(),'Findings');
 assert.equal(await response.locator('li strong').textContent(),'Ready');
 assert.ok(await response.locator('.syntax-keyword').count()>0);
 if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-review-step.png')});
 await page.getByRole('button',{name:'Review context & set goal',exact:true}).click();
 await page.locator('#profile-dialog').waitFor({state:'visible'});
 assert.match(await page.locator('#profile-context').inputValue(),/README.md/);
 assert.match(await page.locator('#profile-push-rules').inputValue(),/Ask the operator/);
 assert.equal((await api(`/api/projects/${project}/sessions`)).length,0,'analysis is not a working session');
 assert.equal((await api(`/api/projects/${project}/profile`)).revision,0,'draft did not overwrite saved profile');
 assert.equal(await page.locator('#profile-goal').inputValue(),'','analysis does not invent a work goal');
 await page.locator('#profile-save').click();
 assert.equal((await api(`/api/projects/${project}/sessions`)).length,0,'missing goal cannot start coordinator');
 await page.reload();await page.getByRole('button',{name:'Review context & set goal',exact:true}).click();await page.locator('#profile-dialog').waitFor({state:'visible'});
 assert.match(await page.locator('#profile-context').inputValue(),/README.md/,'draft survives navigation');
 await page.locator('#profile-context').fill((await page.locator('#profile-context').inputValue())+' Operator correction: maintain compatibility.');
 await page.locator('#profile-goal').fill('Ship a reviewed parser with tests');
 if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-profile-review.png')});
 await page.locator('#profile-save').click();
 await page.getByRole('heading',{name:'Ready to start',exact:true}).waitFor();
 assert.equal((await api(`/api/projects/${project}/sessions`)).length,0,'review does not launch work');
 assert.equal((await api(`/api/projects/${project}/profile`)).revision,0,'launch applies the reviewed profile');
 await page.reload();await page.locator('#start-coordinator').waitFor();
 assert.equal(await page.locator('#setup-steps [aria-current=step]').getAttribute('data-step'),'3');
 assert.match(await page.locator('.setup-goal').textContent(),/Ship a reviewed parser/);
 if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-start-step.png')});
 await page.locator('#start-coordinator').click();await page.waitForURL(u=>!!u.searchParams.get('session'));
 assert.equal(await page.locator('#view-terminal').isVisible(),true);
 await page.waitForFunction(()=>document.querySelector('.coordinator-link')?.getAttribute('aria-current')==='page');
 assert.equal(await page.locator('#coordinator-list a').count(),1,'one coordinator entry across tabs');
 await page.locator('#tab-coordination').click();
 assert.equal(await page.locator('#view-coordination').isVisible(),true);
 await wait(async()=> (await api(`/api/projects/${project}/sessions`)).length===2);
 const sessions=await api(`/api/projects/${project}/sessions`),coordinator=sessions.find(s=>s.role==='coordinator'),worker=sessions.find(s=>s.role==='agent');
 assert.equal(coordinator.initial_prompt,'Ship a reviewed parser with tests');
 assert.match((await api(`/api/projects/${project}/profile`)).context,/Operator correction/);
 assert.equal(worker.coordinator_id,coordinator.id);assert.equal(worker.assignment.requested_by,coordinator.id);
 await page.locator('[data-start-role="agent"]').last().click();await page.locator('#session-agent').selectOption('worker-test');
 await page.waitForFunction(()=>!document.querySelector('#session-form [type=submit]').disabled);
 assert.equal(await page.locator('#session-prompt').inputValue(),'');
 await page.locator('#session-form [type=submit]').click();assert.equal((await api(`/api/projects/${project}/sessions`)).length,2,'agent requires an explicit work goal');
 await page.locator('#session-dialog [data-close]').click();
 await page.locator('.assignment-edge').waitFor();await page.locator('.assignment-edge').focus();await page.keyboard.press('Enter');
 assert.match(await page.locator('#assignment-detail').textContent(),/Write parser tests/);
 assert.equal(await page.locator('#session-list [data-session-id="'+coordinator.id+'"]').count(),0);
 assert.equal(await page.locator('#coordinator-list').isVisible(),true);
 await page.locator('#project-attention > summary').click();
 await page.getByRole('button',{name:'Send attention',exact:true}).click();await page.locator('#attention-body').fill('Hold the shared Git index');await page.locator('#attention-form input[name=pause]').check();await page.locator('#attention-form [type=submit]').click();
 await page.locator('.attention-card').waitFor();let v=await api(`/api/projects/${project}/coordination`);const notice=v.notices[0];assert.equal(notice.targets.length,2);
 // Both fixture sessions acknowledge through the actual inherited Mitos CLI.
 for(const s of sessions)await api(`/api/sessions/${s.id}/input`,{key:'ack-'+s.id,data:'ack '+notice.id+'\r'});
 await wait(async()=>{const v=await api(`/api/projects/${project}/coordination`);return v.notices[0].targets.every(t=>t.acknowledged_at)});
 await page.getByText('Breakpoint requested · 2/2 acknowledged',{exact:true}).waitFor();
 if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-coordination.png')});
 await page.getByRole('button',{name:'Release breakpoint',exact:true}).click();await page.getByText('Released breakpoint · 2/2 acknowledged',{exact:true}).waitFor();
 await page.locator('#coordinator-list .coordinator-link').click();assert.equal(await page.locator('#view-terminal').isVisible(),true);
 await page.locator('#tab-coordination').click();assert.equal(await page.locator('#view-coordination').isVisible(),true);
 await page.setViewportSize({width:390,height:844});await page.locator('#edit-project-profile').click();await page.locator('#profile-dialog').waitFor({state:'visible'});
 assert.equal(await page.locator('#profile-goal').inputValue(),'Ship a reviewed parser with tests');await page.locator('#profile-dialog [data-close]').click();
 const failedDir=path.join(temporary,'failure');await mkdir(failedDir);await writeFile(path.join(failedDir,'fail-analysis'),'');
 const failedProject=await api('/api/projects',{name:'Failed analysis',directory:failedDir});
 await page.goto(base+'/workspace?project='+failedProject.id);
 await api(`/api/projects/${failedProject.id}/profile-drafts`,{key:'failure',agent_id:'coordinator-test'});
 await page.getByRole('heading',{name:'Project analysis failed',exact:true}).waitFor();
 await page.waitForFunction(()=>document.querySelector('#setup-output').textContent.includes('Fixture sign-in failed'));
 await page.getByRole('button',{name:'Retry analysis',exact:true}).click();await page.locator('#session-dialog').waitFor({state:'visible'});
 if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-analysis-failure.png')});
 assert.deepEqual(errors,[]);console.log('PASS: explicit analyze/review/start workflow, review recovery without premature launch, stable tabs, Markdown and syntax highlighting, separate token usage, collapsible activity tree, expand/collapse and raw modes, safe log rendering, retained expansion, per-source refresh recovery, offline recovery without stale errors, local clock while offline, verbose live analysis, reload recovery, distinct navigation colors and selection, visible failure and retry, background project analysis, persisted editable draft, no worker or coordinator before review, required first goals, name autofill, profile defaults/save, terminal opens after coordinator launch, real CLI delegation, recorded graph edge, single coordinator navigation, CLI acknowledgments, breakpoint release, terminal tab, mobile profile, no browser errors');
}finally{await browser?.close();server.kill('SIGTERM');await exited;await rm(temporary,{recursive:true,force:true});}

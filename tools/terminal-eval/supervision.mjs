import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:net';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temporary=await mkdtemp(path.join(tmpdir(),'mitos-supervision-')),data=path.join(temporary,'data');await mkdir(data);
await writeFile(path.join(temporary,'README.md'),'# Parser library\nA Go parser library. Run go test ./... to check changes.\n');
const python=execFileSync('which',['python3'],{encoding:'utf8'}).trim(),fixture=path.join(temporary,'agent.py');
await writeFile(fixture,`import os,sys,json,subprocess
if sys.argv[1]=='analysis':
 assert 'MITOS_SESSION_ID' not in os.environ
 assert 'analysis only' in ' '.join(sys.argv)
 text=open('README.md').read()
 assert 'Parser library' in text
 print(json.dumps({'context':'A Go parser library. Evidence: README.md. Suggested check: go test ./... (not run).','goal':''}),flush=True)
 sys.exit(0)
mitos=os.environ['MITOS_BIN']
def desk(*args):
 return json.loads(subprocess.check_output([mitos,'desk',*args],text=True))
if sys.argv[1]=='supervisor':
 assert 'You are the Mitos supervisor.' in ' '.join(sys.argv)
 worker=desk('start-session','--agent','worker-test','--prompt','Write parser tests and publish evidence')
 print('DELEGATED:'+worker['id'],flush=True)
else:
 assert 'You are a Mitos worker.' in ' '.join(sys.argv)
 print('WORKSPACE:'+os.getcwd(),flush=True)
for line in sys.stdin:
 if line.startswith('ack '):
  desk('acknowledge',line.strip().split()[1]); print('ACKNOWLEDGED',flush=True)
`);
await writeFile(path.join(data,'agents.json'),JSON.stringify({applications:['supervisor','worker'].map(role=>({id:role+'-test',name:role+' fixture',kind:'agent',executables:[python],args:['-u',fixture,role],context_args:['-u',fixture,'analysis'],prompt:'positional'}))}));
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
 await page.locator('#session-dialog').waitFor({state:'visible'});
 await page.locator('#session-agent').selectOption('supervisor-test');
 await page.waitForFunction(()=>!document.querySelector('#session-form [type=submit]').disabled);
 assert.equal(await page.locator('#session-form [type=submit]').textContent(),'Analyze project');
 assert.equal(await page.locator('#session-prompt').inputValue(),'');
 const project=new URL(page.url()).searchParams.get('project');
 await page.locator('#session-form [type=submit]').click();
 await page.locator('#profile-dialog').waitFor({state:'visible'});
 assert.match(await page.locator('#profile-context').inputValue(),/README.md/);
 assert.match(await page.locator('#profile-push-rules').inputValue(),/Ask the operator/);
 assert.equal((await api(`/api/projects/${project}/sessions`)).length,0,'analysis is not a working session');
 assert.equal((await api(`/api/projects/${project}/profile`)).revision,0,'draft did not overwrite saved profile');
 assert.equal(await page.locator('#profile-goal').inputValue(),'','analysis does not invent a work goal');
 await page.locator('#profile-save').click();
 assert.equal((await api(`/api/projects/${project}/sessions`)).length,0,'missing goal cannot start supervisor');
 await page.reload();await page.locator('#profile-dialog').waitFor({state:'visible'});
 assert.match(await page.locator('#profile-context').inputValue(),/README.md/,'draft survives navigation');
 await page.locator('#profile-context').fill((await page.locator('#profile-context').inputValue())+' Operator correction: maintain compatibility.');
 await page.locator('#profile-goal').fill('Ship a reviewed parser with tests');
 if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-profile-review.png')});
 await page.locator('#profile-save').click();await page.waitForURL(u=>!!u.searchParams.get('session'));
 assert.equal(await page.locator('#view-supervision').isVisible(),true);
 assert.equal(await page.locator('#view-terminal').isVisible(),false);
 await wait(async()=> (await api(`/api/projects/${project}/sessions`)).length===2);
 const sessions=await api(`/api/projects/${project}/sessions`),supervisor=sessions.find(s=>s.role==='coordinator'),worker=sessions.find(s=>s.role==='agent');
 assert.equal(supervisor.initial_prompt,'Ship a reviewed parser with tests');
 assert.match((await api(`/api/projects/${project}/profile`)).context,/Operator correction/);
 assert.equal(worker.supervisor_id,supervisor.id);assert.equal(worker.assignment.requested_by,supervisor.id);
 await page.locator('[data-start-role="agent"]').last().click();await page.locator('#session-agent').selectOption('worker-test');
 await page.waitForFunction(()=>!document.querySelector('#session-form [type=submit]').disabled);
 assert.equal(await page.locator('#session-prompt').inputValue(),'');
 await page.locator('#session-form [type=submit]').click();assert.equal((await api(`/api/projects/${project}/sessions`)).length,2,'agent requires an explicit work goal');
 await page.locator('#session-dialog [data-close]').click();
 await page.locator('.assignment-edge').waitFor();await page.locator('.assignment-edge').focus();await page.keyboard.press('Enter');
 assert.match(await page.locator('#assignment-detail').textContent(),/Write parser tests/);
 assert.equal(await page.locator('#session-list [data-session-id="'+supervisor.id+'"]').count(),0);
 assert.equal(await page.locator('#supervisor-list').isVisible(),true);
 await page.getByRole('button',{name:'Send attention',exact:true}).click();await page.locator('#attention-body').fill('Hold the shared Git index');await page.locator('#attention-form input[name=pause]').check();await page.locator('#attention-form [type=submit]').click();
 await page.locator('.attention-card').waitFor();let v=await api(`/api/projects/${project}/supervision`);const notice=v.notices[0];assert.equal(notice.targets.length,2);
 // Both fixture sessions acknowledge through the actual inherited Mitos CLI.
 for(const s of sessions)await api(`/api/sessions/${s.id}/input`,{key:'ack-'+s.id,data:'ack '+notice.id+'\r'});
 await wait(async()=>{const v=await api(`/api/projects/${project}/supervision`);return v.notices[0].targets.every(t=>t.acknowledged_at)});
 await page.getByText('Breakpoint requested · 2/2 acknowledged',{exact:true}).waitFor();
 if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-supervision.png')});
 await page.getByRole('button',{name:'Release breakpoint',exact:true}).click();await page.getByText('Released breakpoint · 2/2 acknowledged',{exact:true}).waitFor();
 await page.locator('#supervisor-list .supervisor-terminal').click();assert.equal(await page.locator('#view-terminal').isVisible(),true);
 await page.locator('#tab-supervision').click();assert.equal(await page.locator('#view-supervision').isVisible(),true);
 await page.setViewportSize({width:390,height:844});await page.locator('#edit-project-profile').click();await page.locator('#profile-dialog').waitFor({state:'visible'});
 assert.equal(await page.locator('#profile-goal').inputValue(),'Ship a reviewed parser with tests');await page.locator('#profile-dialog [data-close]').click();
 assert.deepEqual(errors,[]);console.log('PASS: background project analysis, persisted editable draft, no worker or supervisor before review, required first goals, name autofill, profile defaults/save, supervisor-first view, real CLI delegation, recorded graph edge, separate supervisor navigation, CLI acknowledgments, breakpoint release, secondary terminal, mobile profile, no browser errors');
}finally{await browser?.close();server.kill('SIGTERM');await exited;await rm(temporary,{recursive:true,force:true});}

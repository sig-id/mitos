import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:net';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temporary=await mkdtemp(path.join(tmpdir(),'mitos-agent-picker-'));
const bin=path.join(temporary,'bin'),data=path.join(temporary,'data');
await mkdir(bin);await mkdir(data);
const bash=execFileSync('which',['bash'],{encoding:'utf8'}).trim();
// All selectable built-in fixtures are local processes; no model is called.
for(const id of ['codex','claude','pi','opencode','aider']){
 await writeFile(path.join(bin,id),`#!${bash}\nprintf 'SESSION_READY profile=%s\\n' "$PICKER_PROFILE"\nprintf 'arg=%s\\n' "$@"\nwhile read -r line; do printf '%s\\n' "$line"; done\n`,{mode:0o700});
}
await writeFile(path.join(data,'agents.json'),JSON.stringify({
 applications:[{id:'custom-agent',name:'Custom coding agent',kind:'agent',executables:[path.join(bin,'opencode')],prompt:'positional'}],
 profiles:[{id:'pi-work',name:'Pi work account',agent_id:'pi',env:{PICKER_PROFILE:'MITOS_PICKER_PROFILE'}}]
}));
const reserve=createServer();await new Promise(r=>reserve.listen(0,'127.0.0.1',r));
const port=reserve.address().port;await new Promise(r=>reserve.close(r));
const base=`http://127.0.0.1:${port}`;
const server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',data,'--workspace-root',temporary],{
 cwd:root,env:{...process.env,PATH:bin+path.delimiter+process.env.PATH,MITOS_PICKER_PROFILE:'selected-work-account'},stdio:['ignore','ignore','pipe']
});
let log='',browser,page;server.stderr.on('data',b=>log=(log+b).slice(-4000));
const exited=new Promise(r=>server.once('exit',r));
async function wait(fn){for(let i=0;i<150;i++){try{if(await fn())return}catch{}await new Promise(r=>setTimeout(r,100))}throw Error('Timed out: '+log)}
async function api(url,body){const response=await fetch(base+url,body===undefined?{}:{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});assert.equal(response.status,200,await response.clone().text());return response.json()}
try{
 await wait(async()=>(await fetch(base+'/healthz')).ok);
 const project=await api('/api/projects',{name:'Agent discovery',directory:temporary});
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});
 page=await browser.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+'/projects/'+project.id);
 const picker=page.locator('#agent-session-form [name=agent_id]'),submit=page.locator('#agent-session-form [type=submit]');
 const note=page.locator('#agent-session-capabilities');
 await page.locator('#start-agent-session').click();
 await wait(async()=>await picker.locator('option[value=pi]').count()===1);
 const catalog=await api('/api/agents?project='+project.id);
 assert.deepEqual(await picker.locator('option').evaluateAll(options=>options.map(o=>o.value)),catalog.filter(a=>a.installed&&a.kind==='agent').map(a=>a.id));
 assert.equal(await picker.locator('option[value=aider]').evaluate(option=>option.disabled),true);
 assert.match(await picker.locator('option[value=aider]').textContent(),/initial assignment unavailable/);
 // Managed agents keep their request bridge. Intercept launch to avoid model calls.
 for(const id of ['codex','claude']){
  await picker.selectOption(id);assert.match(await note.textContent(),/Questions and approvals appear in Notifications/);
  await page.locator('#agent-session-form [name=prompt]').fill('Inspect the selected workspace');
  let request;
  await page.route('**/api/projects/*/sessions',async route=>{request=route.request().postDataJSON();await route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({error:'Fixture intercepted launch'})})});
  await submit.click();await wait(async()=>await page.locator('#agent-session-form .form-error').textContent()==='Fixture intercepted launch');
  assert.equal(request.agent_id,id);assert.equal(request.bridge,true);
  await page.unrouteAll({behavior:'wait'});
 }
 await page.locator('#agent-session-dialog [data-close]').click();
 for(const id of ['pi','opencode','custom-agent']){
  await page.locator('#start-agent-session').click();await picker.selectOption(id);
  assert.match(await note.textContent(),/native session/);assert.match(await note.textContent(),/not routed to Mitos Notifications/);
  if(id==='pi')await page.locator('#agent-session-form [name=profile_id]').selectOption('pi-work');
  const assignment='Inspect the project with '+id;
  await page.locator('#agent-session-form [name=prompt]').fill(assignment);
  await submit.click();await page.waitForURL('**/workspace?**');
  const sessionID=new URL(page.url()).searchParams.get('session');
  const session=await api('/api/sessions/'+sessionID);
  assert.equal(session.agent_id,id);assert.equal(!!session.bridge,false);assert.equal(session.initial_prompt,assignment);assert.ok(session.task_id);
  if(id==='pi')assert.equal(session.profile_id,'pi-work');
  await page.locator('.xterm-screen').waitFor();
  let output='';await wait(async()=>{output=Buffer.from((await api('/api/sessions/'+sessionID+'/output?offset=0')).data||'','base64').toString();return output.includes('SESSION_READY')});
  assert.match(output,new RegExp(assignment));
  if(id==='pi')assert.match(output,/selected-work-account/);
  await api('/api/sessions/'+sessionID+'/stop',{});
  await wait(async()=>(await api('/api/sessions/'+sessionID)).status==='stopped');
  await page.goto(base+'/projects/'+project.id);
 }
 // Discovery failure or an empty result must not retain a previous selection.
 for(const [status,body,message] of [[200,[],/No coding agents were detected/],[500,{error:'Discovery unavailable'},/Discovery unavailable/]]){
  await page.route('**/api/agents?*',route=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)}));
  await page.locator('#start-agent-session').click();
  await wait(async()=>message.test(await page.locator('#agent-session-form .form-error').textContent()));
  assert.equal(await picker.locator('option').count(),0);assert.equal(await submit.isDisabled(),true);
  await page.locator('#agent-session-dialog [data-close]').click();await page.unrouteAll({behavior:'wait'});
 }
 assert.deepEqual(errors,[]);
 console.log('PASS: detected agent picker, managed transport, Pi/OpenCode/custom native launches, profile and assignment preservation, unsupported capabilities, empty and failed discovery');
}finally{
 await browser?.close();if(server.exitCode===null){server.kill('SIGTERM');await exited}
 await rm(temporary,{recursive:true,force:true});
}

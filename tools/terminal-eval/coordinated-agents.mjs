import './api-version.mjs';
import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,readFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:net';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temporary=await mkdtemp(path.join(tmpdir(),'mitos-coordinated-agents-'));
const bin=path.join(temporary,'bin'),data=path.join(temporary,'data'),workspace=path.join(temporary,'project');
for(const dir of [bin,data,workspace])await mkdir(dir);
await writeFile(path.join(workspace,'README.md'),'# Mixed agents\n');
const python=execFileSync('which',['python3'],{encoding:'utf8'}).trim();
const fixture=await readFile(path.join(root,'tools/terminal-eval/fixtures/coordinated-agents.py'),'utf8');
for(const id of ['codex','claude'])await writeFile(path.join(bin,id),'#!'+python+'\n'+fixture,{mode:0o700});
await writeFile(path.join(data,'agents.json'),JSON.stringify({profiles:[{id:'claude-work',name:'Claude work account',agent_id:'claude',env:{MITOS_FIXTURE_ACCOUNT:'MITOS_TEST_ACCOUNT'}}]}));
const reserve=createServer();await new Promise(r=>reserve.listen(0,'127.0.0.1',r));const port=reserve.address().port;await new Promise(r=>reserve.close(r));const base=`http://127.0.0.1:${port}`;
const server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',data,'--workspace-root',temporary],{cwd:root,env:{...process.env,PATH:bin+path.delimiter+process.env.PATH,MITOS_TEST_ACCOUNT:'selected-profile'},stdio:['ignore','ignore','pipe']});
let log='',browser;server.stderr.on('data',b=>log=(log+b).slice(-4000));const exited=new Promise(r=>server.once('exit',r));
async function wait(fn){for(let i=0;i<200;i++){try{if(await fn())return}catch{}await new Promise(r=>setTimeout(r,100))}throw Error('Timed out: '+log)}
async function api(url,body){const r=await fetch(base+url,body===undefined?{}:{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});assert.ok(r.ok,await r.clone().text());return r.json()}
try{
 await wait(async()=>(await fetch(base+'/healthz')).ok);
 const project=await api('/api/projects',{name:'Mixed execution',directory:workspace});
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});
 const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+'/projects/'+project.id);await page.locator('#new-work').click();
 const editor=page.locator('#graph-execution');await editor.getByRole('combobox',{name:'Orchestrator',exact:true}).waitFor();
 assert.equal(await editor.getByRole('combobox',{name:'Orchestrator',exact:true}).isVisible(),true);
 await editor.getByText('Execution presets & permitted workers',{exact:true}).click();
 await editor.locator('[data-preset=default]').getByLabel('Model',{exact:true}).fill('codex-test');
 await editor.locator('[data-preset=claude]').getByLabel('Model',{exact:true}).fill('claude-test');
 await editor.locator('[data-preset=claude]').getByLabel('Environment profile',{exact:true}).selectOption('claude-work');
 await editor.getByText('Agent role rules',{exact:true}).click();
 await editor.getByRole('combobox',{name:'Architect',exact:true}).selectOption('claude');
 await page.locator('#graph-prompt').fill('Implement and design using the selected agents');
 await page.locator('#graph-create').click();
 await wait(async()=>new URL(page.url()).searchParams.has('graph'));
 const graphID=new URL(page.url()).searchParams.get('graph');
 await wait(async()=>(await api('/api/graphs/'+graphID)).status==='accepted');
 const graph=await api('/api/graphs/'+graphID);
 assert.equal(graph.setup.execution.roles['mitos.architect'],'claude');
 const bindings=Object.values(graph.members).filter(m=>m.executor?.application.agent).map(m=>m.executor.application.agent);
 assert.ok(bindings.some(b=>b.runtime==='codex'&&b.model==='codex-test'));
 assert.ok(bindings.some(b=>b.runtime==='claude'&&b.model==='claude-test'&&b.profile_id==='claude-work'));
 // Save independent project rules and verify the launch screen receives them.
 await page.locator('[data-view=resources]').click();
 const settings=page.locator('section').filter({has:page.getByRole('heading',{name:'Coordinated work agents',exact:true})}).last();
 await settings.getByRole('combobox',{name:'Orchestrator',exact:true}).selectOption('claude');
 await settings.getByText('Execution presets & permitted workers',{exact:true}).click();
 await settings.locator('[data-preset=claude]').getByLabel('Model',{exact:true}).fill('claude-test');
 await settings.locator('[data-preset=claude]').getByLabel('Environment profile',{exact:true}).selectOption('claude-work');
 await settings.locator('[data-preset=default]').getByLabel('Model',{exact:true}).fill('codex-test');
 await settings.getByText('Agent role rules',{exact:true}).click();await settings.getByRole('combobox',{name:'Architect',exact:true}).selectOption('claude');
 await settings.getByRole('button',{name:'Save agent rules',exact:true}).click();
 await settings.getByText('Agent rules saved. New coordinated work will use these choices.',{exact:true}).waitFor();
 await page.locator('#new-work').click();
 await wait(async()=>await editor.getByRole('combobox',{name:'Orchestrator',exact:true}).inputValue()==='claude');
 await page.reload();await page.locator('#new-work').click();
 await wait(async()=>await editor.getByRole('combobox',{name:'Orchestrator',exact:true}).inputValue()==='claude');
 // The same selection survives recurring-work serialization.
 await page.locator('#graph-prompt').fill('Repeat mixed execution');
 await page.getByText('When to run',{exact:false}).first().click();
 await page.locator('#graph-trigger').selectOption('event');await page.locator('[name=event]').fill('fixture.repeat');
 await page.locator('#graph-create').click();await page.waitForURL('**/services');
 const services=await api('/api/services');const brief=Object.values(services.catalog.work).find(b=>b.intent==='Repeat mixed execution');
 assert.equal(brief.execution.orchestrator,'claude');assert.equal(brief.execution.roles['mitos.architect'],'claude');
 // Mobile launch fields remain usable.
 await page.setViewportSize({width:390,height:844});await page.goto(base+'/projects/'+project.id);await page.locator('#new-work').click();
 await editor.getByRole('combobox',{name:'Orchestrator',exact:true}).waitFor();assert.ok(await editor.evaluate(e=>e.scrollWidth<=e.clientWidth+1));
 await editor.scrollIntoViewIfNeeded();await page.screenshot({path:'/tmp/mitos-coordinated-agents-mobile.png'});
 assert.deepEqual(errors,[]);console.log('PASS: mixed runtime execution, role rules, model/profile bindings, saved defaults, recurring work and mobile launch');
}finally{await browser?.close();if(server.exitCode===null){server.kill('SIGTERM');await exited}await rm(temporary,{recursive:true,force:true})}

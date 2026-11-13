import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:net';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temporary=await mkdtemp(path.join(tmpdir(),'mitos-boundary-'));
const reservation=createServer();await new Promise(r=>reservation.listen(0,'127.0.0.1',r));const port=reservation.address().port;await new Promise(r=>reservation.close(r));const base=`http://127.0.0.1:${port}`;
const server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',path.join(temporary,'data'),'--workspace-root',temporary],{cwd:root,stdio:['ignore','ignore','pipe']});let log='';server.stderr.on('data',b=>log=(log+b).slice(-6000));const exited=new Promise(r=>server.once('exit',r));
async function api(p,body,token){const r=await fetch(base+p,{method:body===undefined?'GET':'POST',headers:{'Content-Type':'application/json',...(token?{Authorization:'Bearer '+token}:{})},body:body===undefined?undefined:JSON.stringify(body)});const text=await r.text();let value;try{value=JSON.parse(text)}catch{value={error:text}}return {status:r.status,value}}
let browser;
try{
 for(let i=0;i<100;i++){try{if((await fetch(base+'/healthz')).ok)break}catch{}await new Promise(r=>setTimeout(r,100));if(i===99)throw Error(log)}
 const opened=await api('/api/projects',{name:'Boundary review',directory:temporary});assert.equal(opened.status,200);const project=opened.value.id;
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});const page=await browser.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+'/projects/'+project);await page.locator('[data-view=resources]').click();
 await page.getByText('Autonomy & approvals',{exact:true}).click();
 await page.getByRole('button',{name:'Add command rule',exact:true}).click();await page.getByLabel('Exact command',{exact:true}).fill('forbidden operation');await page.getByLabel('Resolution',{exact:true}).selectOption('deny');
 await page.getByRole('button',{name:'Save autonomy & approvals',exact:true}).click();await page.getByText('Saved approval policy revision 1.',{exact:true}).waitFor();
 const session=await api('/api/agent-sessions',{adapter:'codex',title:'External bridge',workspace:temporary,project_id:'',desk_session_id:''});assert.equal(session.status,201,JSON.stringify(session.value));
 const observed=await api('/api/agent-sessions/'+session.value.session.session_id+'/observe',{method:'item/started',params:{item:{id:'blocked',type:'commandExecution',command:'forbidden operation',cwd:temporary}}},session.value.token);assert.notEqual(observed.status,200);
 await page.getByRole('heading',{name:'Project held by supervision',exact:true}).waitFor();assert.equal(await page.locator('#start-agent-session').isDisabled(),true);assert.equal(await page.locator('#new-work').isDisabled(),true);
 await page.getByRole('button',{name:'Resolve incident',exact:true}).click();await page.locator('#action-error').waitFor({state:'visible'});
 await page.getByLabel('Incident resolution',{exact:true}).fill('Inspected and stopped the external process');await page.getByLabel('I verified that outstanding processes have stopped',{exact:true}).check();await page.getByRole('button',{name:'Resolve incident',exact:true}).click();
 await page.getByRole('heading',{name:'Supervision incidents',exact:true}).waitFor();assert.equal(await page.locator('#start-agent-session').isDisabled(),false);
 const legacy=await fetch(base+'/api/projects/'+project+'/supervision');assert.equal(legacy.status,404);
 assert.deepEqual(errors,[]);console.log('PASS: policy editing, independent incident evidence, held launch controls, explicit operator resolution, removed legacy API');
}finally{await browser?.close();if(server.exitCode===null){server.kill('SIGTERM');await exited}await rm(temporary,{recursive:true,force:true})}

import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp,rm,readFile,stat} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:http';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temp=await mkdtemp(path.join(tmpdir(),'mitos-settings-'));
const provider=createServer(async(req,res)=>{let body='';for await(const part of req)body+=part;assert.equal(req.headers.authorization,'Bearer fixture-secret');const choices=Object.keys(JSON.parse(body).questions.action.criteria);res.setHeader('Content-Type','application/json');res.end(JSON.stringify({model:'fixture',answers:{action:{type:'choice',choice:'connected',confidence:1,probabilities:Object.fromEntries(choices.map(c=>[c,c==='connected'?1:0]))}}}))});
await new Promise(r=>provider.listen(0,'127.0.0.1',r));
const endpoint=`http://127.0.0.1:${provider.address().port}/decisions`;
const reservation=createServer();await new Promise(r=>reservation.listen(0,'127.0.0.1',r));const port=reservation.address().port;await new Promise(r=>reservation.close(r));
const base=`http://127.0.0.1:${port}`;let server,exited,browser,log='';
function start(){server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',path.join(temp,'data'),'--workspace-root',temp],{cwd:root,env:{...process.env,MITOS_UI_KEY:'fixture-secret'},stdio:['ignore','ignore','pipe']});server.stderr.on('data',b=>log=(log+b).slice(-6000));exited=new Promise(r=>server.once('exit',r))}
async function ready(){for(let i=0;i<150;i++){try{if((await fetch(base+'/healthz')).ok)return}catch{}await new Promise(r=>setTimeout(r,100))}throw Error(log)}
async function api(url,body){const r=await fetch(base+url,{method:body?'POST':'GET',headers:{'Content-Type':'application/json'},body:body?JSON.stringify(body):undefined});assert.ok(r.ok,await r.clone().text());return r.json()}
try{
 start();await ready();browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});
 const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+'/');await page.getByRole('link',{name:'System Configuration',exact:true}).click();
 await page.getByLabel('Connection',{exact:true}).selectOption('direct');for(const [name,protocol,endpoint,model] of [
  ['TypeSafe Jev','systemone','https://api.typesafe.ai/v1/systemone','jev-latest'],
  ['OpenAI','openai_decisions','https://api.openai.com/v1/decisions','gpt-6-luna'],
  ['Gemini','gemini_decisions','https://generativelanguage.googleapis.com/v1beta/decisions','gemini-decision-1'],
  ['OpenRouter Jev','openrouter_decisions','https://openrouter.ai/api/alpha/decisions','typesafe/jev-1.14']
 ]){await page.getByRole('button',{name,exact:true}).click();assert.equal(await page.getByLabel('Protocol',{exact:true}).inputValue(),protocol);assert.equal(await page.getByLabel('Full endpoint URL').inputValue(),endpoint);assert.equal(await page.getByLabel('Model',{exact:true}).inputValue(),model)}
 await page.getByRole('button',{name:'TypeSafe Jev',exact:true}).click();await page.getByLabel('API key',{exact:true}).fill('fixture-secret');
 await page.getByRole('button',{name:'Gemini',exact:true}).click();assert.equal(await page.getByLabel('API key',{exact:true}).inputValue(),'');
 await page.getByLabel('Authentication',{exact:true}).selectOption('env');assert.equal(await page.getByLabel('API key environment variable').inputValue(),'GEMINI_API_KEY');await page.getByLabel('Authentication',{exact:true}).selectOption('key');
 await page.getByRole('button',{name:'TypeSafe Jev',exact:true}).click();await page.getByLabel('API key',{exact:true}).fill('fixture-secret');await page.getByLabel('Full endpoint URL').fill(endpoint);await page.getByLabel('Model',{exact:true}).fill('fixture');
 await page.getByRole('button',{name:'Test API connection'}).click();await page.getByText('Settings have not been saved.',{exact:false}).waitFor();assert.equal((await api('/api/settings')).configuration.revision,0);
 const codex=page.locator('details').filter({has:page.locator('summary').filter({hasText:/^Codex/})});await codex.locator('summary').click();await codex.getByLabel('Customize launch defaults').check();await codex.getByLabel('Additional launch arguments (JSON array)').fill('["--model", "shared"]');
 await page.getByRole('button',{name:'Save system configuration'}).click();await page.getByText('System configuration saved.',{exact:true}).waitFor();
 await page.reload();await page.getByLabel('Model',{exact:true}).waitFor();assert.equal(await page.getByLabel('Model',{exact:true}).inputValue(),'fixture');assert.deepEqual((await api('/api/settings')).configuration.agents.codex.args,['--model','shared']);
 assert.equal(await page.getByLabel('API key',{exact:true}).inputValue(),'');assert.match(await page.getByLabel('API key',{exact:true}).getAttribute('placeholder'),/Saved key/);
 const credential=(await api('/api/settings')).configuration.decision_provider;assert.ok(credential.api_key_ref);assert.equal(credential.api_key,undefined);
 assert.ok(!(await readFile(path.join(temp,'data/events.jsonl'),'utf8')).includes('fixture-secret'));assert.equal((await stat(path.join(temp,'data/credentials',credential.api_key_ref))).mode&0o777,0o600);
 const project=await api('/api/projects',{name:'Settings fixture',directory:temp});await page.goto(base+'/settings?project='+project.id);await page.getByText('Editing project overrides.',{exact:true}).waitFor();assert.equal(await page.getByLabel('Connection',{exact:true}).inputValue(),'inherit');await page.getByText('System default: fixture',{exact:false}).waitFor();
 await page.getByLabel('Connection',{exact:true}).selectOption('direct');await page.getByLabel('Model',{exact:true}).fill('project-model');await codex.locator('summary').click();await codex.getByLabel('Override system defaults for this agent').check();await codex.getByLabel('Additional launch arguments (JSON array)').fill('[]');
 await page.getByRole('button',{name:'Save project overrides'}).click();await page.getByText('Project overrides saved.',{exact:true}).waitFor();let saved=(await api('/api/projects/'+project.id+'/settings')).configuration;assert.equal(saved.decision_provider.model,'project-model');assert.deepEqual(saved.agents.codex.args,[]);assert.equal((await api('/api/settings')).configuration.decision_provider.model,'fixture');
 await page.getByLabel('Connection',{exact:true}).selectOption('inherit');await codex.getByLabel('Override system defaults for this agent').uncheck();await page.getByRole('button',{name:'Save project overrides'}).click();await page.getByText('Project overrides saved.',{exact:true}).waitFor();saved=(await api('/api/projects/'+project.id+'/settings')).configuration;assert.ok(!saved.decision_provider);assert.ok(!saved.agents?.codex);
 for(const width of [1440,390]){await page.setViewportSize({width,height:950});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))}
 server.kill('SIGTERM');await exited;start();await ready();await page.goto(base+'/settings');await page.getByLabel('Model',{exact:true}).waitFor();assert.equal(await page.getByLabel('Model',{exact:true}).inputValue(),'fixture');assert.equal((await api('/api/projects/'+project.id+'/settings')).configuration.decision_provider,undefined);
 await page.getByRole('button',{name:'Test API connection'}).click();await page.getByText('Settings have not been saved.',{exact:false}).waitFor();
 await page.getByRole('button',{name:'Remove saved key'}).click();await page.getByRole('button',{name:'Save system configuration'}).click();await page.getByText('System configuration saved.',{exact:true}).waitFor();assert.equal((await api('/api/settings')).configuration.decision_provider.api_key_ref,undefined);
 await page.getByLabel('Authentication',{exact:true}).selectOption('env');await page.getByLabel('API key environment variable').fill('MITOS_UI_KEY');await page.getByRole('button',{name:'Save system configuration'}).click();await page.getByText('System configuration saved.',{exact:true}).waitFor();await page.reload();await page.getByLabel('API key environment variable').waitFor();assert.equal(await page.getByLabel('Authentication',{exact:true}).inputValue(),'env');await page.getByRole('button',{name:'Test API connection'}).click();await page.getByText('Settings have not been saved.',{exact:false}).waitFor();
 await page.goto(base+'/projects/'+project.id+'?view=resources');const approvals=page.locator('#decisions-api');await approvals.getByLabel('Decision provider',{exact:true}).selectOption('direct');await approvals.getByRole('button',{name:'Gemini',exact:true}).click();assert.equal(await approvals.getByLabel('Decision model').inputValue(),'gemini-decision-1');await approvals.getByRole('button',{name:'TypeSafe Jev',exact:true}).click();await approvals.getByLabel('Endpoint URL',{exact:true}).fill(endpoint);await approvals.getByLabel('API key',{exact:true}).fill('fixture-secret');await approvals.getByRole('button',{name:'Save autonomy & approvals'}).click();await approvals.getByText('Saved. Approval handling is now Ask me.',{exact:true}).waitFor();assert.equal(await approvals.getByLabel('API key',{exact:true}).inputValue(),'');await approvals.getByRole('button',{name:'Test API connection'}).click();await approvals.getByText('Connected. The provider returned a valid decision.',{exact:true}).waitFor();
 assert.deepEqual(errors,[]);console.log('System settings: dedicated provider presets, direct/env authentication, secret redaction, saved-key restart, removal, project approval form, overrides, defaults, and responsive layout passed.');
}finally{await browser?.close();if(server&&server.exitCode===null){server.kill('SIGTERM');await exited}await new Promise(r=>provider.close(r));await rm(temp,{recursive:true,force:true})}

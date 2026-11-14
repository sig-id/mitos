import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp,mkdir,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:http';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temp=await mkdtemp(path.join(tmpdir(),'mitos-notifications-')),repo=path.join(temp,'project');await mkdir(repo);
let reviewGate=null;
const provider=createServer(async(req,res)=>{let body='';for await(const chunk of req)body+=chunk;const q=JSON.parse(body).questions.action;const choices=Object.keys(q.criteria),choice=choices.includes('connected')?'connected':'approve';if(choice!=='connected'&&reviewGate)await reviewGate;res.setHeader('Content-Type','application/json');res.end(JSON.stringify({model:'fixture',answers:{action:{type:'choice',choice,confidence:1,probabilities:Object.fromEntries(choices.map(c=>[c,c===choice?1:0]))}}}))});
await new Promise(r=>provider.listen(0,'127.0.0.1',r));
const reservation=createServer();await new Promise(r=>reservation.listen(0,'127.0.0.1',r));const port=reservation.address().port;await new Promise(r=>reservation.close(r));const base=`http://127.0.0.1:${port}`;
const server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',path.join(temp,'data'),'--workspace-root',repo],{cwd:root,stdio:['ignore','ignore','pipe']});let log='';server.stderr.on('data',b=>log=(log+b).slice(-8000));const exited=new Promise(r=>server.once('exit',r));
async function wait(fn){for(let i=0;i<150;i++){if(await fn())return;await new Promise(r=>setTimeout(r,100))}throw Error('Timeout: '+log)}
async function api(url,body,method='POST',token){const r=await fetch(base+url,{method:body===undefined?'GET':method,headers:{'Content-Type':'application/json',...(token?{Authorization:'Bearer '+token}:{})},body:body===undefined?undefined:JSON.stringify(body)});assert.ok(r.ok,await r.clone().text());return r.json()}
let browser;
try{
 await wait(async()=>{try{return(await fetch(base+'/healthz')).ok}catch{return false}});
 const project=await api('/api/projects',{name:'Notification fixture',directory:repo});
 const session=await api('/api/agent-sessions',{adapter:'codex',title:'Inventory PRs',workspace:repo,project_id:'',desk_session_id:''});
 const pending=await api(`/api/agent-sessions/${session.session.session_id}/requests`,{id:1,method:'item/commandExecution/requestApproval',params:{threadId:'t',turnId:'u',command:'gh api repos/example/project/pulls',cwd:repo,reason:'Read open pull requests'}},'POST',session.token);
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});const page=await browser.newPage({viewport:{width:1500,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+`/projects/${project.id}`);await page.getByRole('button',{name:'Review request',exact:true}).waitFor();await page.getByRole('button',{name:'Review request',exact:true}).click();
 await page.getByRole('dialog',{name:'Notifications'}).waitFor();assert.ok(await page.getByRole('dialog').getByText('Inventory PRs',{exact:true}).isVisible());
 await page.getByRole('dialog').getByRole('button',{name:'Close',exact:true}).click();
 await page.goto(base+'/inbox');await page.locator('#home-content .panel').first().waitFor();assert.equal(await page.locator('#home-content').getByText('Allow this recorded tool request once?').count(),0);
 await page.getByRole('button',{name:/Notifications/}).click();
 for(const width of [1500,390]){await page.setViewportSize({width,height:900});const size=await page.evaluate(()=>({w:innerWidth,scroll:document.documentElement.scrollWidth}));assert.ok(size.scroll<=size.w+1);if(process.env.MITOS_SCREENSHOT_DIR){await mkdir(process.env.MITOS_SCREENSHOT_DIR,{recursive:true});await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,`notifications-${width}.png`)})}}
 await page.getByRole('dialog').getByRole('button',{name:'Allow once',exact:true}).click();await wait(async()=>!(await api('/api/workspace-home')).attention.length);await page.locator('.attention-banner').waitFor({state:'hidden'});
 const answer=await api(`/api/agent-sessions/${session.session.session_id}/requests/${pending.request_id}`,undefined,'GET',session.token);assert.equal(answer.state,'accepted');
 // Free-text controls must be driven by the serialized input profile for every adapter.
 for(const [index,kind] of ['codex','claude','async'].entries()){
  const active=kind==='claude'?await api('/api/agent-sessions',{adapter:'claude',title:'Claude clarification',workspace:repo,project_id:'',desk_session_id:''}):session;
  const question='Clarify '+kind+' requirements';
  const native=kind==='claude'?{session_id:'claude-thread',tool_use_id:'clarification',hook_event_name:'PreToolUse',tool_name:'AskUserQuestion',cwd:repo,tool_input:{questions:[{question,header:'Requirements',options:[{label:'Brief',description:'Brief answer'},{label:'Detailed',description:'Detailed answer'}]}]}}:{id:10+index,method:kind==='async'?'mitos/asyncQuestion':'item/tool/requestUserInput',params:{threadId:'t',turnId:'u',questions:[{id:'q',question}]}};
  const pendingText=await api(`/api/agent-sessions/${active.session.session_id}/requests`,native,'POST',active.token);
  const home=await api('/api/workspace-home'),notice=home.attention.find(a=>a.card.question===question);
  assert.equal(notice.input_profile,'text');assert.equal(notice.blocking,kind!=='async');
  await page.setViewportSize({width:index===1?390:1500,height:900});
  if(!await page.getByRole('dialog',{name:'Notifications'}).isVisible())await page.getByRole('button',{name:/Notifications/}).click();
  const card=page.locator('#notice-'+notice.id);await card.getByLabel('Your answer').waitFor();
  const text='Reviewed '+kind+' requirements';await card.getByLabel('Your answer').fill(text);
  await card.getByRole('button',{name:'Send answer',exact:true}).click();
  await wait(async()=>!(await api('/api/workspace-home')).attention.some(a=>a.id===notice.id));
  const delivered=await api(`/api/agent-sessions/${active.session.session_id}/requests/${pendingText.request_id}`,undefined,'GET',active.token);
  assert.equal(delivered.state,'accepted');assert.ok(JSON.stringify(delivered.body).includes(text),'native response lost text answer');
 }
 await page.goto(base+`/projects/${project.id}?view=resources#decisions-api`);await page.getByRole('heading',{name:'Decisions API & approvals'}).waitFor();assert.equal(await page.locator('#decisions-api').evaluate(n=>document.activeElement===n),true);
 await page.getByLabel('Automatic handling',{exact:true}).selectOption('decision');await page.getByLabel('Approval boundaries',{exact:true}).fill('Allow read-only GitHub PR inspection.');await page.getByLabel('Decision provider',{exact:true}).selectOption('direct');await page.getByLabel('API protocol',{exact:true}).selectOption('systemone');await page.getByLabel('Endpoint URL',{exact:true}).fill(`http://127.0.0.1:${provider.address().port}`);await page.getByLabel('Decision model',{exact:true}).fill('fixture');
 await page.getByRole('button',{name:'Test API connection',exact:true}).click();await page.getByText('Connected. The provider returned a valid decision.',{exact:true}).waitFor();
 await page.getByRole('button',{name:'Save autonomy & approvals',exact:true}).click();await page.getByText('Saved. Approval handling is now Decisions API review.',{exact:true}).waitFor();await page.reload();await page.getByRole('heading',{name:'Decisions API & approvals'}).waitFor();assert.equal(await page.getByLabel('Automatic handling',{exact:true}).inputValue(),'decision');assert.equal(await page.getByLabel('Decision model',{exact:true}).inputValue(),'fixture');
 for(const width of [1500,390]){await page.setViewportSize({width,height:1000});await page.getByRole('heading',{name:'Decisions API & approvals'}).scrollIntoViewIfNeeded();if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,`decisions-setup-${width}.png`)});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))}
 // A slow independent reviewer must stay off the human notification banner.
 let releaseReview;reviewGate=new Promise(resolve=>{releaseReview=resolve});
 try{
  const reviewing=await api(`/api/agent-sessions/${session.session.session_id}/requests`,{id:99,method:'item/commandExecution/requestApproval',params:{threadId:'t',turnId:'u',command:'gh api repos/example/project/pulls',cwd:repo,reason:'Read open pull requests'}},'POST',session.token);
  await page.getByRole('button',{name:/Notifications/}).click();
  await page.getByRole('dialog').getByText('Reviewing automatically',{exact:true}).waitFor();
  assert.equal(await page.locator('.attention-banner').isVisible(),false);
  assert.equal(await page.getByRole('dialog').getByRole('button',{name:'Allow once',exact:true}).count(),0);
  releaseReview();reviewGate=null;
  await wait(async()=>(await api(`/api/agent-sessions/${session.session.session_id}/requests/${reviewing.request_id}`,undefined,'GET',session.token)).state==='accepted');
 }finally{releaseReview();reviewGate=null}
 assert.deepEqual(errors,[]);console.log('PASS: notifications at desktop/mobile widths, direct approval and Codex/Claude/async text-answer round trips, Inbox summaries, provider test, save and reload, and independent automatic approval without human prompts');
}finally{await browser?.close();server.kill('SIGTERM');await exited;provider.close();await rm(temp,{recursive:true,force:true})}

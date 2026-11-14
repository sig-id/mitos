import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,readFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';import {createServer} from 'node:net';import path from 'node:path';import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..'),temporary=await mkdtemp(path.join(tmpdir(),'mitos-automatic-ui-')),data=path.join(temporary,'data'),workspace=path.join(temporary,'project'),bin=path.join(temporary,'bin');
for(const dir of [data,workspace,bin])await mkdir(dir);await writeFile(path.join(workspace,'README.md'),'# Answer project\nProduce an answer with evidence.\n');
const python=execFileSync('which',['python3'],{encoding:'utf8'}).trim();await writeFile(path.join(bin,'codex'),'#!'+python+'\n'+await readFile(path.join(root,'tools/terminal-eval/fixtures/automatic-codex.py'),'utf8'),{mode:0o700});
await writeFile(path.join(bin,'pi'),'#!'+python+'\n'+await readFile(path.join(root,'tools/terminal-eval/fixtures/automatic-codex.py'),'utf8'),{mode:0o700});
const reserve=createServer();await new Promise(r=>reserve.listen(0,'127.0.0.1',r));const port=reserve.address().port;await new Promise(r=>reserve.close(r));const base=`http://127.0.0.1:${port}`;
let server,exited,logs='',browser,page;function start(){server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',data,'--workspace-root',workspace],{cwd:root,env:{...process.env,PATH:bin+path.delimiter+process.env.PATH},stdio:['ignore','ignore','pipe']});server.stderr.on('data',b=>logs=(logs+b).slice(-6000));exited=new Promise(r=>server.once('exit',r))}
let home;async function api(url,body){if(!home)home=await(await fetch(base+'/api/server')).json();const r=await fetch(base+url,{method:body?'POST':'GET',headers:{'Content-Type':'application/json','X-Mitos-Server':home.id},body:body?JSON.stringify(body):undefined});const data=await r.json();if(!r.ok)throw Error(JSON.stringify(data));return data}
async function wait(fn,ms=30000){const until=Date.now()+ms;while(Date.now()<until){try{if(await fn())return}catch{}await new Promise(r=>setTimeout(r,100))}throw Error('Timed out. '+logs)}
try{
 start();await wait(async()=>(await fetch(base+'/healthz')).ok);const project=await api('/api/projects',{name:'Answer project',directory:workspace});
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});page=await browser.newPage({viewport:{width:1600,height:1040}});const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+'/projects/'+project.id);await page.locator('#graph-create:not([disabled])').waitFor();assert.equal(await page.locator('#graph-picker').count(),0);assert.equal(await page.getByRole('button',{name:'Add step',exact:true}).count(),0);
 await page.locator('[data-view=resources]').click();await page.getByRole('heading',{name:'AGENTS.md',exact:true}).waitFor();
 await page.locator('.setup-assistant-options>summary').click();await page.getByRole('combobox',{name:'Setup assistant runtime',exact:true}).selectOption('pi');await page.getByRole('textbox',{name:'Describe project setup',exact:true}).fill('Require review before publishing.');await page.getByRole('button',{name:'Send',exact:true}).click();await page.getByText('Should every change require review?',{exact:true}).waitFor();
 const setupEditor=page.locator('.setup-edit-surface mitos-markdown');assert.equal(await page.getByText('Edit Markdown',{exact:true}).count(),0);await setupEditor.getByRole('button',{name:'Edit',exact:true}).click();await setupEditor.locator('textarea').fill('# My unsaved note');
 await page.getByRole('textbox',{name:'Describe project setup',exact:true}).fill('Yes. Use saved evidence and allow 45 minutes.');await page.getByRole('button',{name:'Send',exact:true}).click();await page.getByText('Added review guidance and explicit execution settings.',{exact:true}).waitFor();assert.equal(await setupEditor.locator('textarea').inputValue(),'# My unsaved note');await page.getByRole('button',{name:'Discard edits',exact:true}).click();await page.reload();await page.getByRole('button',{name:'Apply to AGENTS.md',exact:true}).waitFor();
 await assert.rejects(readFile(path.join(workspace,'AGENTS.md')));await page.getByRole('button',{name:'Compare with saved',exact:true}).click();assert.equal(await page.locator('.setup-comparison>section').count(),2);
 await page.getByRole('button',{name:'Apply to AGENTS.md',exact:true}).click();await page.getByText('AGENTS.md saved. New work will use these instructions.',{exact:true}).waitFor();assert.ok((await readFile(path.join(workspace,'AGENTS.md'),'utf8')).includes('saved evidence'));
 await page.getByRole('button',{name:'My defaults',exact:true}).click();const dialog=page.locator('.setup-default-dialog');await dialog.getByRole('textbox',{name:'Describe your defaults',exact:true}).fill('Keep replies concise and use fixture-global-model.');await dialog.getByRole('button',{name:'Send',exact:true}).click();await dialog.getByRole('button',{name:'Save proposed defaults',exact:true}).click();await dialog.getByText('Personal defaults saved.',{exact:true}).waitFor();await dialog.getByRole('button',{name:'Close',exact:true}).click();assert.ok(!(await readFile(path.join(workspace,'AGENTS.md'),'utf8')).includes('Keep personal'));
 await page.reload();await page.getByRole('heading',{name:'AGENTS.md',exact:true}).waitFor();await page.locator('.setup-edit-surface .md-preview').getByText('saved evidence',{exact:true}).waitFor();
 await page.setViewportSize({width:390,height:844});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));await page.screenshot({path:'/tmp/mitos-project-setup-mobile.png'});await page.setViewportSize({width:1600,height:1040});await page.screenshot({path:'/tmp/mitos-project-setup.png'});await page.locator('[data-view=work]').click();
 await page.locator('#graph-prompt').fill('Produce and review the answer');await page.locator('#graph-create').click();await page.locator('#graph-current').waitFor({state:'visible'});const graphID=new URL(page.url()).searchParams.get('graph');assert.ok(graphID);
 // A running worker exposes incremental messages before any report exists.
 await page.locator('[data-view=workflow]').click();
 const liveWorker=page.locator('.graph-node[data-kind=step]').first();await liveWorker.waitFor();await liveWorker.click();
 await page.locator('.graph-live-text').filter({hasText:'Inspecting the assignment'}).waitFor();
 await page.locator('#graph-status.waiting_input').waitFor();
 await page.locator('[data-view=work]').click();await page.locator('#graph-conversation').getByRole('button',{name:'Review request',exact:true}).click();
 await page.locator('.graph-request-card').getByRole('button',{name:/Allow once/}).waitFor();
 const waitingURL=page.url();
 await page.locator('.graph-node[data-kind=scope]').click();
 assert.deepEqual(await page.locator('#graph-node-detail .graph-tabs button').allTextContents(),['Progress','Request','Result']);
 await page.locator('.graph-goal-current .graph-request-card').waitFor();
 await page.screenshot({path:'/tmp/mitos-goal-progress.png'});
 await page.setViewportSize({width:390,height:844});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));await page.getByLabel('Work graphs',{exact:true}).getByRole('button',{name:'Review request',exact:true}).click();await page.screenshot({path:'/tmp/mitos-goal-progress-mobile.png'});await page.setViewportSize({width:1600,height:1040});
 await page.locator('.graph-node[data-kind=scope]').click();
 await page.locator('#graph-node-detail').getByRole('button',{name:'Result',exact:true}).click();
 await page.getByRole('button',{name:'View progress',exact:true}).click();
 await page.getByLabel('Work graphs',{exact:true}).getByRole('button',{name:'Review request',exact:true}).click();
 assert.equal(page.url(),waitingURL);
 await page.locator('#graph-node-detail').getByRole('button',{name:'Attempts',exact:true}).click();
 await page.locator('.graph-attempt-card .graph-request-card').waitFor();
 assert.ok(!(await page.locator('.graph-attempt-card').textContent()).includes('Queued'));
 assert.equal(await page.locator('#graph-node-detail a[href^="/runs/"]').count(),0);
 await page.locator('.graph-execution-events summary').click();await page.locator('.graph-event-list').getByText('Agent connected',{exact:true}).waitFor();
 await page.locator('.graph-request-card').getByRole('button',{name:/Allow once/}).click();
 await page.locator('.graph-live-text').filter({hasText:'Finished command: printf checked (exit 0)'}).waitFor();
 await page.locator('.graph-live-text').filter({hasText:'Preparing the answer'}).waitFor();
 await page.locator('#graph-status.in_progress').waitFor();
 assert.equal(await page.locator('#graph-node-kind').textContent(),'Agent');
 await page.screenshot({path:'/tmp/mitos-live-agent-output.png'});
 await page.locator('#work-tree .work-tree-node').first().waitFor();await wait(async()=>{const state=await api('/api/graphs/'+graphID);const blocked=Object.values(state.expansions).find(x=>x.error);if(blocked)logs=JSON.stringify(state);return state.status==='accepted'});await page.locator('#graph-status.accepted').waitFor();
 await page.locator('[data-view=workflow]').click();
 const g=await api('/api/graphs/'+graphID);assert.equal(g.setup.model,'fixture-project-model');assert.ok(g.setup.document.includes('saved evidence'));assert.ok(g.setup.personal.includes('Keep personal'));
 const worker=Object.values(g.members).find(m=>m.spec.kind==='application'&&!m.controller_for);assert.equal(Object.values(g.members).filter(m=>m.executor?.application.agent?.specialization).length,1);
 const treeOrder=await page.locator('#work-tree .work-tree-node').evaluateAll(ns=>ns.map(n=>n.dataset.treeKey).filter(k=>k!=='planner'));
 const graphOrder=await page.locator('.graph-node[data-kind=step]').evaluateAll(ns=>ns.sort((a,b)=>parseFloat(a.style.left)-parseFloat(b.style.left)||parseFloat(a.style.top)-parseFloat(b.style.top)).map(n=>n.dataset.id));assert.deepEqual(treeOrder,graphOrder,'tree does not follow workflow order');
 assert.equal(await page.locator('.graph-node[data-kind=planner]').count(),0);assert.equal(await page.locator('[data-tree-key="planner"]').count(),1);
 // Repeat navigation against an unchanged snapshot, including the same round.
 const inspector=page.locator('#graph-node-detail');
 await page.locator('.graph-planner-history').click();
 await inspector.getByRole('button',{name:'Read this round',exact:true}).last().click();
 await inspector.locator('.graph-tabs button[aria-current=page]').filter({hasText:'Activity'}).waitFor();
 const latestHeading=await inspector.locator('h3').textContent();
 await inspector.getByRole('button',{name:'Planning rounds',exact:true}).click();
 await page.getByRole('button',{name:'Latest update',exact:true}).click();
 await inspector.locator('.graph-tabs button[aria-current=page]').filter({hasText:'Activity'}).waitFor();
 assert.equal(await inspector.locator('h3').textContent(),latestHeading);
 await page.locator('.graph-node[data-kind=scope]').first().click();
 assert.equal(await page.locator('#graph-node-kind').textContent(),'Goal');
 await page.getByRole('button',{name:'Latest update',exact:true}).click();
 await inspector.getByRole('heading',{name:latestHeading,exact:true}).waitFor();
 await page.locator('#graph-inspector-close').click();await page.locator('[data-tree-key=planner]').click();
 await inspector.getByRole('heading',{name:latestHeading,exact:true}).waitFor();
 await page.locator('.graph-planner-history').click();await page.getByRole('button',{name:'Read this round',exact:true}).first().click();await page.locator('.graph-plan-output').waitFor();await page.getByRole('heading',{name:'Coordinator summary',exact:true}).waitFor();
 assert.ok(await page.locator('#graph-inspector').evaluate(n=>n.getBoundingClientRect().width)>=560,'output pane is too narrow');
 assert.equal(await page.locator('.graph-structured-output details[data-read-key=raw]').getAttribute('open'),null,'raw JSON should start collapsed');
 const assignment=page.locator('.graph-plan-output details[data-read-key="step:0"]');assert.equal(await assignment.getAttribute('open'),'');await assignment.locator('summary').first().click();
 await page.locator('#graph-inspector-expand').click();assert.equal(await page.locator('.graph-map').isVisible(),false);assert.ok(await page.locator('#graph-inspector').evaluate(n=>n.getBoundingClientRect().width)>1000);
 await page.waitForTimeout(1600);assert.equal(await assignment.getAttribute('open'),null,'polling reset disclosure');
 await page.screenshot({path:'/tmp/mitos-planner-reader.png'});await page.locator('#graph-inspector-expand').click();assert.ok(await page.locator('.graph-map').isVisible());
 await page.locator(`[data-tree-key="${worker.spec.id}"]`).click();await page.locator('#graph-node-detail .analysis-prose').filter({hasText:'The answer is'}).waitFor();assert.ok(await page.locator('#graph-node-detail').getByText('The answer is', {exact:false}).count());
 await page.locator('#graph-node-detail').getByRole('button',{name:'source file',exact:true}).waitFor();await page.locator('#graph-node-detail').getByRole('button',{name:'source file',exact:true}).click();await page.locator('.graph-source-line.cited').waitFor();await page.getByRole('button',{name:'Back to graph',exact:true}).click();
 const beforeAttemptURL=page.url();await page.locator('#graph-node-detail').getByRole('button',{name:'Attempts',exact:true}).click();await page.locator('.graph-attempt-card').waitFor();await page.locator('.graph-attempt-card').getByRole('button',{name:'Browse saved files',exact:true}).waitFor();await page.locator('#graph-node-detail .analysis-prose').filter({hasText:'The answer is'}).waitFor();assert.equal(page.url(),beforeAttemptURL,'attempt inspection navigated away');
 // A previous failed attempt expands in place beside the current successful one.
 const previous='previous-attempt',snapshotPattern='**/api/graphs/'+graphID+'/snapshot?*',previousPath='**/api/graphs/'+graphID+'/nodes/'+worker.spec.id+'/runs/'+previous;
 await page.route(snapshotPattern,async route=>{const response=await route.fetch(),value=await response.json();value.graph.members[worker.spec.id].attempts=[previous,...worker.attempts];await route.fulfill({response,json:value})});
 await page.route(previousPath,route=>route.fulfill({json:{id:previous,status:'failed',summary:'Earlier execution stopped.',created_at:g.created_at,updated_at:g.updated_at,finished_at:g.updated_at,artifacts:[]}}));
 let previousActivityCalls=0;await page.route(previousPath+'/activity',route=>route.fulfill({json:{status:'running',created_at:g.created_at,started_at:g.created_at,updated_at:g.updated_at,requests:[],events:[{at:g.updated_at,kind:'agent_started',summary:'Activity update '+(++previousActivityCalls)},{at:g.updated_at,kind:'artifact_harvested',summary:'Report saved'},{at:g.updated_at,kind:'workspace_harvested',summary:'Workspace saved'}]}}));
 await page.locator('.graph-attempt-card').nth(1).waitFor();
 await page.locator('.graph-attempt-card').first().locator('summary').first().click();
 await page.locator('.graph-attempt-card').first().getByText('Earlier execution stopped.',{exact:true}).waitFor();
 // Returning to Attempts remembers a closed panel; reopening it must survive activity responses.
 await page.locator('.graph-attempt-card').first().locator('summary').first().click();
 await page.locator('#graph-node-detail').getByRole('button',{name:'Assignment',exact:true}).click();
 await page.locator('#graph-node-detail').getByRole('button',{name:'Attempts',exact:true}).click();
 assert.equal(await page.locator('.graph-attempt-card').first().getAttribute('open'),null);
 const activityBeforeOpen=previousActivityCalls;
 await page.locator('.graph-attempt-card').first().locator('summary').first().click();
 await wait(async()=>previousActivityCalls>=activityBeforeOpen+2);
 assert.notEqual(await page.locator('.graph-attempt-card').first().getAttribute('open'),null,'activity response collapsed the reopened attempt');
 const eventsPanel=page.locator('.graph-attempt-card').first().locator('.graph-execution-events');
 await eventsPanel.locator('summary').click();
 assert.equal(await eventsPanel.getByText('artifact harvested',{exact:true}).count(),0);await eventsPanel.getByRole('checkbox',{name:'Show technical events',exact:true}).check();await eventsPanel.getByText('artifact harvested',{exact:true}).waitFor();await eventsPanel.getByRole('checkbox',{name:'Show technical events',exact:true}).uncheck();
 const activityBeforeRead=previousActivityCalls;
 await wait(async()=>previousActivityCalls>=activityBeforeRead+2);
 assert.notEqual(await eventsPanel.getAttribute('open'),null,'activity update closed execution events');

 if(await page.locator('.graph-attempt-card').nth(1).getAttribute('open')===null)await page.locator('.graph-attempt-card').nth(1).locator('summary').first().click();
 await page.locator('.graph-attempt-card').nth(1).locator('.analysis-prose').filter({hasText:'The answer is'}).waitFor();
 assert.equal(page.url(),beforeAttemptURL);
 await page.unroute(snapshotPattern);await page.unroute(previousPath);await page.unroute(previousPath+'/activity');
 await wait(async()=>await page.locator('.graph-attempt-card').count()===1);
 await page.setViewportSize({width:390,height:844});await page.screenshot({path:'/tmp/mitos-output-mobile.png'});assert.ok(await page.locator('#graph-inspector').evaluate(n=>n.getBoundingClientRect().width)>320);assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));await page.setViewportSize({width:1600,height:1040});
 // Stored output is untrusted; JSON and Markdown cannot inject HTML or scripts.
 const safe=await page.evaluate(()=>{const n=window.MitosGraphOutput.render(JSON.stringify({explanation:'<img src=x onerror="window.injected=true">',steps:[null,{title:'<script>bad()</script>',assignment:'**Readable**',criteria:'done',after:[]}]}),{planner:true});document.body.append(n);const result={html:n.querySelectorAll('img,script').length,bold:!!n.querySelector('strong')};n.remove();return result});assert.deepEqual(safe,{html:0,bold:true});
 await page.locator('[data-view=result]').click();await page.locator('#graph-result .graph-result-reader').waitFor();assert.ok(await page.locator('#graph-result').getByText('The answer is', {exact:false}).count());assert.equal(await page.locator('.graph-body').isVisible(),false);
 assert.equal(await page.locator('.graph-result-content input[type=checkbox]').count(),1);await page.locator('.graph-result-content').getByRole('button',{name:'source',exact:true}).click();await page.locator('.graph-source-line.cited').waitFor();assert.ok((await page.locator('.graph-source-lines').textContent()).includes('Answer project'));await page.getByRole('button',{name:'Final answer',exact:true}).click();await page.locator('.graph-result-content').getByRole('button',{name:'Answer file',exact:true}).click();await page.locator('.graph-result-heading').getByRole('heading',{name:'answer.txt',exact:true}).waitFor();await wait(async()=> (await page.locator('.graph-result-content .md-preview').textContent()).trim()==='42');
 const savedDownload=page.waitForEvent('download');await page.getByRole('link',{name:'Download file',exact:true}).click();assert.equal((await savedDownload).suggestedFilename(),'answer.txt');
 await page.getByRole('button',{name:'Final answer',exact:true}).click();await page.setViewportSize({width:390,height:844});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));await page.setViewportSize({width:1600,height:1040});
 assert.equal(await page.locator('[data-view=result]').getAttribute('aria-current'),'page');await page.locator('[data-view=work]').click();await page.locator('#graph-conversation').waitFor({state:'visible'});await page.locator('[data-view=result]').click();await page.locator('#graph-result').waitFor({state:'visible'});
 await page.locator('.graph-result-items').getByRole('button',{name:'Review implementation',exact:true}).click();await page.locator('.graph-result-content .diff-table').waitFor();await page.getByRole('button',{name:'Final answer',exact:true}).click();assert.equal(await page.locator('.graph-result-items a').getAttribute('href'),'https://example.com/pull/1');
 await page.locator('[data-view=changes]').click();await page.locator('#changes-view').waitFor({state:'visible'});assert.ok(page.url().includes('/projects/'));await page.locator('#changes-source').selectOption('/runs/'+worker.current_run_id+'/artifacts/changes.patch');await page.locator('#git-diff .diff-table').waitFor();await page.locator('[data-view=result]').click();
 await page.screenshot({path:'/tmp/mitos-final-result.png'});await page.getByRole('button',{name:'Back to graph',exact:true}).click();assert.ok(await page.locator('.graph-body').isVisible());
 await page.locator('#graph-zoom-in').click();const zoom=await page.locator('#graph-zoom').textContent();await page.waitForTimeout(1600);assert.equal(await page.locator('#graph-zoom').textContent(),zoom);
 await page.locator('#work-tree .work-tree-toggle').click();assert.equal(await page.locator('#work-tree .work-tree-node').count(),0);await page.locator('#work-tree .work-tree-toggle').click();assert.ok(await page.locator('#work-tree .work-tree-node').count());
 // A stopped workflow explains its failure in every view, even without accepted work.
 const stoppedPattern='**/api/graphs/'+graphID+'/snapshot?*';
 await page.route(stoppedPattern,async route=>{const response=await route.fetch(),value=await response.json();value.graph.status='active';delete value.graph.result;value.graph.limits.deadline='2020-01-01T00:00:00Z';value.graph.expansions={};for(const [id,m]of Object.entries(value.graph.members)){delete m.accepted;if(id===worker.spec.id){m.state='failed';value.nodes[id].state='failed';value.nodes[id].summary='Codex stopped: invalid supervision observation';value.nodes[id].settled=true}}await route.fulfill({response,json:value})});
 await page.locator('#graph-message').getByRole('heading',{name:'Work stopped: time limit reached',exact:true}).waitFor();
 await page.locator('[data-view=work]').click();assert.equal(await page.locator('#graph-message').isVisible(),true);assert.match(await page.locator('#graph-message').textContent(),/could not validate an agent activity event/);
 await page.locator('#graph-message').getByRole('button',{name:'Read saved work',exact:true}).click();await page.getByRole('heading',{name:'No final answer yet',exact:true}).waitFor();assert.equal(await page.locator('#graph-message').isVisible(),true);assert.equal(await page.locator('#graph-followup-form').isVisible(),false);
 await page.locator('mitos-file-browser button[data-path="report.md"]').click();await page.locator('.graph-result-content').getByText('The answer is',{exact:false}).waitFor();
 await page.locator('#graph-message').getByRole('button',{name:'Inspect failed attempt',exact:true}).click();assert.equal(await page.getByRole('button',{name:'Retry step',exact:true}).count(),0);
 await page.screenshot({path:'/tmp/mitos-workflow-attention.png'});
 await page.locator('#graph-message').getByRole('button',{name:'Resume with more resources',exact:true}).click();
 const resumeDialog=page.getByRole('dialog',{name:'Resume with more resources',exact:true});await resumeDialog.waitFor();
 await resumeDialog.getByLabel('Additional workflow time (minutes)',{exact:true}).fill('90');
 await resumeDialog.getByLabel('Time per resumed attempt (minutes)',{exact:true}).fill('60');
 await page.setViewportSize({width:390,height:844});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));await page.screenshot({path:'/tmp/mitos-resume-resources-mobile.png'});await page.setViewportSize({width:1600,height:1040});
 let resumeBody;
 await page.route('**/api/graphs/'+graphID+'/resume',async route=>{resumeBody=route.request().postDataJSON();await route.fulfill({json:{id:graphID}})});
 await resumeDialog.getByRole('button',{name:'Resume workflow',exact:true}).click();await resumeDialog.waitFor({state:'hidden'});
 assert.equal(resumeBody.attempt_minutes,60);assert.ok(resumeBody.limits.attempts>0);assert.ok(Date.parse(resumeBody.limits.deadline)>Date.now()+89*60000);assert.ok(resumeBody.key);assert.ok(resumeBody.revision);
 await page.unroute('**/api/graphs/'+graphID+'/resume');

 await page.locator('#graph-message').getByRole('button',{name:'Start new work',exact:true}).click();assert.equal(await page.locator('#graph-prompt').inputValue(),g.members[g.root_task].spec.brief.intent);await page.locator('#graph-compose-close').click();
 await page.unroute(stoppedPattern);await page.locator('#graph-status.accepted').waitFor();
 // A follow-up is a new bounded continuation with the previous answer and evidence.
 await page.locator('[data-view=result]').click();await page.locator('#graph-followup').fill('Continue from the saved answer and produce it again');await page.locator('#graph-followup-send').click();await wait(async()=>new URL(page.url()).searchParams.get('graph')!==graphID);const continuationID=new URL(page.url()).searchParams.get('graph');await wait(async()=>(await api('/api/graphs/'+continuationID)).status==='accepted');const continuation=await api('/api/graphs/'+continuationID);assert.equal(continuation.predecessor,graphID);await page.locator('.conversation-history').waitFor();assert.equal((await api('/api/graphs/'+graphID)).status,'accepted');
 // Editing definitions is a normal product flow, with persisted revisions.
 await page.getByRole('link',{name:'Agent library',exact:true}).click();await page.locator('#agent-library-list .panel').first().waitFor();await page.locator('#agent-new').click();await page.locator('#agent-editor [name=name]').fill('Accessibility reviewer');await page.locator('#agent-editor [name=description]').fill('Review keyboard navigation and accessible names');await page.locator('#agent-editor [name=instructions]').fill('Inspect focus order, test keyboard access, and report evidence.');await page.getByRole('button',{name:'Save agent',exact:true}).click();await page.locator('#agent-editor').waitFor({state:'hidden'});await page.reload();const card=page.locator('#agent-library-list .panel').filter({has:page.getByRole('heading',{name:'Accessibility reviewer',exact:true})});await card.waitFor();await card.getByRole('button',{name:'Edit agent'}).click();await page.locator('#agent-editor [name=instructions]').fill('Also check dialogs and focus restoration.');await page.getByRole('button',{name:'Save agent',exact:true}).click();await page.locator('#agent-editor').waitFor({state:'hidden'});await card.getByText('Custom · Revision 2').waitFor();
 await page.getByRole('link',{name:'Plugins',exact:true}).first().click();await page.getByRole('heading',{name:'Plugins',exact:true}).waitFor();assert.equal(await page.getByText('Set up a graph planner').count(),0);await page.getByRole('link',{name:'Devices',exact:true}).click();await page.getByRole('heading',{name:'Devices',exact:true}).waitFor();
 // Existing idle work is not launched by reading it; explicit start is required.
 const limits={tasks:64,amendments:128,attempts:96,retries:2,expansions:24,controller_calls:24,depth:12,fanout:64,deadline:new Date(Date.now()+3600000).toISOString()};const old=await api('/api/graphs',{profile:'work-graph/v1',key:crypto.randomUUID(),project_id:project.id,goal_revision:1,activation:'manual',policy:{mode:'adaptive',applications:[]},limits,tasks:[{id:'goal',kind:'scope',required:true,criteria:'Produce answer',acceptance:'operator',brief:{title:'Earlier goal',intent:'Produce and review the answer'}}],seal:false});
 await page.goto(base+'/projects/'+project.id+'?graph='+old.id);await page.getByRole('button',{name:'Start automatic work',exact:true}).waitFor();assert.equal((await api('/api/graphs/'+old.id)).attempts,0);await page.getByRole('button',{name:'Start automatic work',exact:true}).click();await wait(async()=>(await api('/api/graphs/'+old.id)).status==='accepted');
 await page.setViewportSize({width:390,height:844});await page.reload();await page.locator('#graph-status.accepted').waitFor();assert.ok(await page.locator('#work-tree').isVisible());let sizes=await page.evaluate(()=>({width:document.documentElement.scrollWidth,viewport:innerWidth,height:document.documentElement.scrollHeight,vh:innerHeight}));assert.ok(sizes.width<=sizes.viewport+1,JSON.stringify(sizes));assert.ok(sizes.height<=sizes.vh+1,JSON.stringify(sizes));
 await page.setViewportSize({width:1600,height:1040});await page.screenshot({path:'/tmp/mitos-automatic-desktop.png'});await page.setViewportSize({width:390,height:844});await page.screenshot({path:'/tmp/mitos-automatic-mobile.png'});
 await page.context().setOffline(true);await page.locator('#graph-sync.offline').waitFor();assert.equal(await page.locator('#graph-status').textContent(),'Complete');await page.context().setOffline(false);await page.locator('#graph-sync:not(.offline)').waitFor();
 // An uncertain goal submission replays the exact body across reload.
 await page.setViewportSize({width:1600,height:1040});await page.locator('#new-work').click();await page.locator('#graph-prompt').fill('Produce and review another answer');let dropped=false;await page.route('**/api/graphs/automatic',async route=>{if(!dropped){dropped=true;await route.fetch();await route.abort('failed')}else await route.continue()});await page.locator('#graph-create').click();await page.getByRole('button',{name:'Retry same request',exact:true}).waitFor();const count=(await api('/api/graphs?project_id='+project.id)).graphs.length;await page.reload();await page.getByRole('button',{name:'Retry same request',exact:true}).click();await wait(async()=>new URL(page.url()).searchParams.get('graph')!==old.id);assert.equal((await api('/api/graphs?project_id='+project.id)).graphs.length,count);assert.deepEqual(errors,[]);
 // Save recurring instructions from the same composer; recover a lost response
 // without duplicating the service or executing the domain procedure during setup.
 await page.locator('#new-work').click();await page.locator('#graph-prompt').fill('Inspect current state, follow my procedure, and report this occurrence.');
 await page.locator('#graph-create-form summary').filter({hasText:'When to run'}).click();await page.locator('#graph-trigger').selectOption('both');
 await page.locator('#graph-frequency').selectOption('custom');await page.locator('[name=cron]').fill('0 0 1 1 *');await page.locator('[name=event]').fill('repository.changed');
 await page.setViewportSize({width:390,height:844});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
 let scheduleDropped=false;await page.route('**/api/services/workflows',async route=>{if(!scheduleDropped){scheduleDropped=true;await route.fetch();await route.abort('failed')}else await route.continue()});
 await page.locator('#graph-create').click();await page.getByRole('button',{name:'Retry same request',exact:true}).waitFor();
 await page.reload();await page.getByRole('button',{name:'Retry same request',exact:true}).click();await page.waitForURL('**/services');
 const scheduled=await api('/api/services');assert.equal(scheduled.catalog.config.units.length,1);assert.equal(scheduled.catalog.config.units[0].schedule.cron,'0 0 1 1 *');assert.deepEqual(scheduled.catalog.config.units[0].events,['repository.changed']);assert.equal(Object.values(scheduled.catalog.work)[0].intent,'Inspect current state, follow my procedure, and report this occurrence.');
 await page.getByText('Saved instructions',{exact:true}).click();await page.locator('p.prose').filter({hasText:'Inspect current state, follow my procedure, and report this occurrence.'}).waitFor();assert.deepEqual(errors,[]);
 console.log('PASS: inline approvals, automatic goal-to-result, dependent agents, library, mobile layout, reconnect, uncertain-request replay, and recurring workflow creation with cron/event triggers');
}finally{await browser?.close();if(server&&server.exitCode===null){server.kill('SIGTERM');await exited}if(!process.env.MITOS_KEEP_GRAPH_FIXTURE)await rm(temporary,{recursive:true,force:true});else console.log('Fixture:',temporary)}

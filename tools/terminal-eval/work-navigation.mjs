import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp,mkdir,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:net';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temporary=await mkdtemp(path.join(tmpdir(),'mitos-work-nav-')),data=path.join(temporary,'data');
const reservation=createServer();await new Promise(r=>reservation.listen(0,'127.0.0.1',r));const port=reservation.address().port;await new Promise(r=>reservation.close(r));
const base=`http://127.0.0.1:${port}`;let server,exited,log='',browser,home;
function start(){server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',data,'--workspace-root',temporary],{cwd:root,stdio:['ignore','ignore','pipe']});server.stderr.on('data',b=>log=(log+b).slice(-6000));exited=new Promise(r=>server.once('exit',r))}
async function wait(fn){for(let i=0;i<150;i++){try{if(await fn())return}catch{}await new Promise(r=>setTimeout(r,100))}throw Error('Timed out: '+log)}
async function api(url,body){const r=await fetch(base+url,{method:body?'POST':'GET',headers:{'Content-Type':'application/json',...(home?{'X-Mitos-Server':home.id}:{})},body:body?JSON.stringify(body):undefined});const result=await r.json();assert.ok(r.ok,JSON.stringify(result));return result}
try{
 start();await wait(async()=>(await fetch(base+'/healthz')).ok);home=await api('/api/server');
 for(const name of ['mitos','sigid'])await mkdir(path.join(temporary,name));
 const project=await api('/api/projects',{name:'mitos',directory:path.join(temporary,'mitos')});
 const other=await api('/api/projects',{name:'sigid',directory:path.join(temporary,'sigid')});
 const limits={tasks:32,amendments:64,attempts:32,retries:2,expansions:16,orchestrator_calls:8,depth:8,fanout:16,deadline:new Date(Date.now()+3600000).toISOString()};
 const scope=(id,parent,title)=>({id,parent,kind:'scope',required:true,criteria:'Review saved evidence',acceptance:'operator',brief:{title,intent:title}});
 async function create(title,projectID=project.id){return api('/api/graphs',{profile:'work-graph/v1',key:crypto.randomUUID(),project_id:projectID,goal_revision:1,activation:'manual',policy:{mode:'adaptive',applications:[]},limits,tasks:[scope('goal',undefined,title),scope('child','goal',title+' child'),scope('nested','child',title+' nested')],seal:false})}
 const first=await create('First work'),second=await create('Second work');await create('Other project work',other.id);
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});
 const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base+`/projects/${project.id}`);await page.locator('#overview-view').waitFor({state:'visible'});await page.locator('#project-work-list .project-work-row').first().waitFor();
 // Sidebar resizing works by pointer and keyboard, and persists across navigation.
 const divider=page.getByRole('separator',{name:'Sidebar width'}),rail=page.locator('.os-rail');
 const initialWidth=(await rail.boundingBox()).width,dividerBox=await divider.boundingBox();
 await page.mouse.move(dividerBox.x+dividerBox.width/2,dividerBox.y+80);await page.mouse.down();await page.mouse.move(dividerBox.x+dividerBox.width/2+90,dividerBox.y+80);await page.mouse.up();
 assert.equal((await rail.boundingBox()).width,initialWidth+90);await page.reload();await page.locator('#project-work-list .project-work-row').first().waitFor();assert.equal((await rail.boundingBox()).width,initialWidth+90);
 await page.goto(base+'/agents');assert.equal((await rail.boundingBox()).width,initialWidth+90);await page.goto(base+'/workspace?project='+project.id);assert.equal((await rail.boundingBox()).width,initialWidth+90);await divider.focus();await page.keyboard.press('ArrowLeft');assert.equal((await rail.boundingBox()).width,initialWidth+80);
 await page.keyboard.press('Home');assert.equal((await rail.boundingBox()).width,200);await page.keyboard.press('End');assert.equal((await rail.boundingBox()).width,480);await page.keyboard.press('Enter');assert.equal((await rail.boundingBox()).width,248);
 await page.goto(base+`/projects/${project.id}`);await page.locator('#project-work-list .project-work-row').first().waitFor();
 assert.equal(new URL(page.url()).searchParams.has('graph'),false);
 assert.equal(await page.locator('#work-filter').count(),0);
 assert.equal(await page.locator('#project-work-list .project-work-row').count(),2);
 assert.match(await page.locator('#project-statistics').textContent(),/Not available/);
 await page.locator('#project-work-search').fill('Second');await wait(async()=>await page.locator('#project-work-list .project-work-row').count()===1);
 assert.match(await page.locator('#project-work-filters [data-work-state=open]').textContent(),/2/);
 await page.locator('#project-work-search').fill('');await wait(async()=>await page.locator('#project-work-list .project-work-row').count()===2);
 await page.locator('#project-work-list a').filter({hasText:'First work'}).click();await page.locator('#graph-current').waitFor({state:'visible'});
 const row=id=>page.locator(`.work-tree-item[data-graph-id="${id}"]`);
 await row(second.id).waitFor();
 assert.equal(await page.locator('#work-tree').evaluate(el=>el.closest('.project-navigation').dataset.projectId),project.id);
 assert.equal(await page.locator('#work-tree .work-tree-item').count(),2);
 assert.equal(await page.locator(`.project-navigation[data-project-id="${other.id}"] #work-tree`).count(),0);
 assert.equal(await page.locator('#projects a[aria-current]').evaluate(el=>getComputedStyle(el).boxShadow),'none');
 // Expanding another work must neither select it nor collapse the current work.
 await row(second.id).locator(':scope > .work-tree-heading .work-tree-toggle').click();
 await row(second.id).getByRole('button',{name:/Second work nested/}).waitFor();
 assert.equal(new URL(page.url()).searchParams.get('graph'),first.id);
 await row(first.id).getByRole('button',{name:/First work nested/}).waitFor();
 // Nested scopes collapse independently, and refresh preserves their state.
 await row(first.id).getByRole('button',{name:'Collapse First work child',exact:true}).click();
 assert.equal(await row(first.id).getByRole('button',{name:/First work nested/}).count(),0);
 await row(first.id).locator(':scope > .work-tree-heading .work-tree-toggle').click();
 assert.equal(await row(first.id).locator('.work-tree-node').count(),0);
 assert.ok(await row(second.id).locator('.work-tree-node').count());
 const third=await create('Third work');await row(third.id).waitFor({timeout:20000});
 assert.equal(await row(first.id).locator('.work-tree-node').count(),0);
 assert.equal(await page.locator('#work-tree').evaluate(el=>el.closest('.project-navigation').dataset.projectId),project.id);
 await page.reload();await row(first.id).waitFor();assert.equal(await row(first.id).locator('.work-tree-node').count(),0);await row(second.id).getByRole('button',{name:/Second work nested/}).waitFor();
 await row(second.id).locator('.work-tree-goal').click();await wait(async()=>new URL(page.url()).searchParams.get('graph')===second.id);
 await row(second.id).locator('.work-tree-node').filter({hasText:'Second work child'}).click();await page.locator('#graph-inspector').waitFor({state:'visible'});
 assert.equal(await page.locator('#graph-conversation').isVisible(),true);
 const conversation=await page.locator('#graph-conversation').boundingBox(),inspector=await page.locator('#graph-inspector').boundingBox();assert.ok(inspector.x>=conversation.x+conversation.width);assert.ok(conversation.width>400);assert.ok(inspector.width>=400);assert.ok(Math.abs(inspector.y-conversation.y)<2);
 await page.screenshot({path:'/tmp/mitos-work-three-columns.png'});
 await page.setViewportSize({width:900,height:1000});await page.locator('#graph-inspector').waitFor({state:'visible'});assert.equal(await page.locator('#graph-conversation').isVisible(),false);assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
 await page.setViewportSize({width:1440,height:1000});await page.locator('[data-view=work]').click();assert.equal(await page.locator('#graph-conversation').isVisible(),true);assert.equal(await page.locator('#graph-inspector').isVisible(),true);
 // Returning to the conversation must also clear a stable workflow's inspector.
 await page.locator('#graph-inspector-expand').click();
 await row(second.id).locator('.work-tree-goal').click();assert.equal(await page.locator('#graph-conversation').isVisible(),true);
 assert.equal(new URL(page.url()).searchParams.has('node'),false);
 await page.locator('[data-view=workflow]').click();
 assert.equal(await page.locator('#graph-inspector').isVisible(),false);
 assert.equal(await page.locator('.graph-node[aria-pressed=true]').count(),0);
 assert.equal(await page.locator('.graph-body').evaluate(el=>el.classList.contains('reading-mode')),false);
 assert.equal(await page.locator('#graph-inspector-expand').getAttribute('aria-pressed'),'false');
 // Selecting the same step again must rebuild usable details.
 await row(second.id).locator('.work-tree-node').filter({hasText:'Second work child'}).click();
 await page.locator('#graph-inspector').waitFor({state:'visible'});
 await page.locator('#graph-node-detail').getByRole('button',{name:'Request',exact:true}).click();
 assert.deepEqual(await page.locator('#graph-node-detail .prose').allTextContents(),['Second work child','Review saved evidence']);
 await page.locator('#graph-inspector-close').click();
 assert.equal(await page.locator('#graph-inspector').isVisible(),false);
 assert.equal(await page.locator('.graph-node[aria-pressed=true]').count(),0);
 await row(second.id).locator('.work-tree-goal').click();
 // Archiving cancels active work and removes it from Open, including after reload.
 await page.getByLabel('Work actions',{exact:true}).click();await page.locator('#graph-archive').click();
 await wait(async()=>await row(second.id).count()===0);assert.equal((await api('/api/graphs/'+second.id)).work_state,'archived');
 await page.locator('[data-view=work]').click();await page.locator('#graph-compose').waitFor({state:'visible'});assert.equal(await page.locator('#graph-current').isVisible(),false);await page.locator('.work-overview-link').click();
 await page.reload();await row(first.id).waitFor();assert.equal(await row(second.id).count(),0);
 const openOverview=async state=>{await page.locator('.work-overview-link').click();await page.locator('#overview-view').waitFor({state:'visible'});await page.locator(`[data-work-state="${state}"]`).click();};
 const openListed=async title=>{await page.locator('#project-work-list a').filter({hasText:title}).click();await page.locator('#graph-current').waitFor({state:'visible'});};
 await openOverview('archived');await openListed('Second work');await page.locator('#graph-work-state').getByText('Archived',{exact:true}).waitFor();
 assert.equal(await row(second.id).count(),0);await row(first.id).waitFor();
 await page.reload();await row(first.id).waitFor();assert.equal(await row(second.id).count(),0);
 await page.getByLabel('Work actions',{exact:true}).click();await page.locator('#graph-restore').click();await page.locator('#overview-view').waitFor({state:'visible'});
 await openOverview('closed');await openListed('Second work');
 await page.getByLabel('Work actions',{exact:true}).click();await page.locator('#graph-reopen').click();await row(second.id).waitFor();
 assert.equal((await api('/api/graphs/'+second.id)).status,'cancelled');
 await row(first.id).locator('.work-tree-goal').click();await page.getByLabel('Work actions',{exact:true}).click();await page.locator('#graph-close').click();await wait(async()=>await row(first.id).count()===0);
 await openOverview('closed');await openListed('First work');assert.equal(await row(first.id).count(),0);
 // Independent projects never share work, even while both have saved state.
 await page.goto(base+'/projects/'+other.id);await page.locator('#overview-view').waitFor({state:'visible'});await page.locator('#work-tree .work-tree-item').waitFor();assert.equal(await page.locator('#work-tree .work-tree-item').count(),1);assert.match(await page.locator('#work-tree').textContent(),/Other project work/);
 await page.goto(base+'/projects/'+project.id);await row(second.id).waitFor();
 await page.locator('#project-work-list .project-work-row').first().waitFor();
 assert.equal(await page.locator('.work-tree-state').count(),0);
 assert.equal(await row(second.id).locator('.work-tree-status svg').first().count(),1);
 await page.screenshot({path:'/tmp/mitos-work-navigation-desktop.png'});
 await page.setViewportSize({width:390,height:844});assert.equal(await divider.isVisible(),false);assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));await page.screenshot({path:'/tmp/mitos-work-navigation-mobile.png'});
 // Review and attention have distinct shapes, with status available to assistive technology.
 await page.route('**/api/graphs?**',async route=>{const response=await route.fetch(),data=await response.json();for(const g of data.graphs){g.status='active';g.counts=g.id===second.id?{review:1}:{needs_attention:1}}await route.fulfill({response,json:data})});
 await page.reload();await row(second.id).locator('.work-tree-status[data-status=review]').first().waitFor();await row(third.id).locator('.work-tree-status[data-status=needs_attention]').first().waitFor();
 const reviewIcon=await row(second.id).locator('.work-tree-heading svg').innerHTML(),attentionIcon=await row(third.id).locator('.work-tree-heading svg').innerHTML();assert.notEqual(reviewIcon,attentionIcon);
 assert.match(await row(second.id).locator('.work-tree-goal').getAttribute('aria-label'),/Review ready/);assert.match(await row(third.id).locator('.work-tree-goal').getAttribute('title'),/Needs attention/);
 assert.equal(await row(second.id).locator('.work-tree-goal').textContent(),'Second work');assert.equal(await row(third.id).locator('.work-tree-goal').textContent(),'Third work');
 await page.unroute('**/api/graphs?**');
 // Saved archive is recovered after restarting the actual server.
 const current=await api('/api/graphs/'+second.id);await api('/api/graphs/'+second.id+'/work-state',{key:crypto.randomUUID(),revision:current.revision,state:'archived'});
 server.kill('SIGTERM');await exited;start();await wait(async()=>(await fetch(base+'/healthz')).ok);
 assert.equal((await api('/api/graphs?project_id='+project.id+'&state=archived')).graphs[0].id,second.id);
 assert.deepEqual(errors,[]);console.log('Work navigation passed: grouping, independent expansion, selection, filtering, archive/restore/reopen, mobile, restart.');
}catch(error){console.error(error);if(browser){for(const context of browser.contexts())for(const page of context.pages()){console.error((await page.locator('#work-tree').textContent().catch(()=>'')));await page.screenshot({path:'/tmp/mitos-work-navigation-failure.png'}).catch(()=>{})}}throw error}finally{await browser?.close();if(server?.exitCode===null){server.kill('SIGTERM');await exited}await rm(temporary,{recursive:true,force:true})}

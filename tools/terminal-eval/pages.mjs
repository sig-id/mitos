import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:net';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temporary=await mkdtemp(path.join(tmpdir(),'mitos-pages-')),data=path.join(temporary,'data'),repo=path.join(temporary,'project');
await mkdir(data);await mkdir(repo);await writeFile(path.join(repo,'hello.txt'),'Evidence fixture\n');
const bash=execFileSync('which',['bash'],{encoding:'utf8'}).trim();
await writeFile(path.join(data,'agents.json'),JSON.stringify({applications:[{id:'fixture',name:'Fixture',kind:'agent',executables:[bash],args:['-c','echo READY; cat'],prompt:'positional'}]}));
const reservation=createServer();await new Promise(r=>reservation.listen(0,'127.0.0.1',r));const port=reservation.address().port;await new Promise(r=>reservation.close(r));const base=`http://127.0.0.1:${port}`;
const server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',data,'--workspace-root',repo,'--demo'],{cwd:root,stdio:['ignore','ignore','pipe']});let log='';server.stderr.on('data',b=>log=(log+b).slice(-8000));const exited=new Promise(r=>server.once('exit',r));
async function wait(fn){for(let i=0;i<150;i++){if(await fn())return;await new Promise(r=>setTimeout(r,100));}throw Error('Timeout: '+log);}
async function api(p,body,method='POST'){const r=await fetch(base+p,body===undefined?{}:{method,headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});assert.ok(r.ok,await r.clone().text());return r.json();}
let browser,page;
try{
 await wait(async()=>{try{return (await fetch(base+'/healthz')).ok}catch{return false}});
 const project=await api('/api/projects',{name:'Design audit',directory:repo});
 const session=await api(`/api/projects/${project.id}/sessions`,{agent_id:'fixture',prompt:'Inspect the fixture',key:'fixture'});
 const examples=await api('/api/runs'),parked=examples.find(r=>r.sample&&r.status==='parked'),done=examples.find(r=>r.sample&&r.status==='done');
 const catalog={config:{units:[{id:'inspect',scope:'project',trigger:'manual'}],intent:{enable:['inspect']}},work:{inspect:{title:'Inspect files',intent:'Inventory this directory',workspace:repo,project_id:project.id,app_id:'workspace-inventory',timeout_minutes:1}}};
 await api('/api/services',{expected_revision:0,catalog},'PUT');
 const event=await api('/api/service-events',{name:'inspection.ready',idempotency_key:'event',payload:{message:'Fixture'}});
 const lifecycle=await api('/api/services/inspect/lifecycle',{action:'stop',idempotency_key:'stop'});
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});page=await browser.newPage({viewport:{width:1500,height:1000}});const errors=[];page.on('pageerror',e=>errors.push(e.message));
 const routes=['/','/inbox','/connections',`/projects/${project.id}`,'/work','/overview','/decisions','/tasks','/services','/apps','/apps/workspace-inventory','/new','/autonomy',`/runs/${parked.id}`,`/runs/${done.id}/report`,`/services/events/${event.seq}`,`/services/lifecycle/${lifecycle.seq}`,`/workspace?project=${project.id}`,`/workspace?project=${project.id}&session=${session.id}&view=terminal`,`/workspace?project=${project.id}&view=changes`,`/workspace?project=${project.id}&view=resources`];
 if(process.env.MITOS_SCREENSHOT_DIR)await mkdir(process.env.MITOS_SCREENSHOT_DIR,{recursive:true});
 for(const width of [1500,390]){
  await page.setViewportSize({width,height:width===390?844:1000});
  for(const [index,route] of routes.entries()){
   const response=await page.goto(base+route);assert.equal(response.status(),200,route);await page.locator('.os-rail .brand img').waitFor();
   assert.equal(await page.locator('link[href="/static/operating.css"]').count(),1,route);
   assert.equal(await page.locator('link[href="/static/studio.css"],.sidebar,.studio-tag,.brandmark').count(),0,route+' retained legacy shell');
   assert.equal(await page.locator('.os-rail a[href="/inbox"]').count(),1,route);
   const size=await page.evaluate(()=>({page:document.documentElement.scrollWidth,width:innerWidth,height:document.documentElement.scrollHeight,viewport:innerHeight,bg:getComputedStyle(document.body).backgroundColor}));
   assert.ok(size.page<=size.width+1,route+' overflows '+JSON.stringify(size));assert.ok(size.height<=size.viewport+1,route+' has a second page scrollbar');assert.equal(size.bg,'rgb(22, 25, 29)',route);
   if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,`${width}-${index}.png`)});
  }
 }
 await page.setViewportSize({width:1500,height:1000});
 const queued=examples.find(r=>r.sample&&r.status==='queued');await page.goto(base+'/runs/'+queued.id);await page.locator('#execution-activity summary').click();await page.waitForResponse(r=>r.url()===base+'/runs/'+queued.id&&r.request().headers()['hx-request']==='true');await page.waitForTimeout(100);assert.equal(await page.locator('#execution-activity').getAttribute('open'),'','polling preserves expanded activity');
 const redirect=await fetch(base+'/runs/'+session.attempt_id+'/report',{redirect:'manual'});assert.equal(redirect.status,303);assert.match(redirect.headers.get('location'),/tab=result/);
 // Cross-surface navigation must load the destination scripts, not HTMX-swap an incompatible shell.
 await page.goto(base+'/work');await page.locator('.os-rail a[href="/projects/'+project.id+'"]').click();await page.locator('#new-work').click();await page.locator('#work-dialog').waitFor({state:'visible'});await page.locator('#work-dialog [data-close]').click();await page.locator('.os-rail a[href="/apps"]').click();await page.locator('.app-card').first().click();await page.locator('a[href="/new?app=workspace-inventory"]').click();
 await page.locator('[name=title]').fill('Verify migrated launch form');await page.locator('[name=workspace]').fill(repo);await page.locator('form[action="/runs"] button[type=submit]').click();await page.waitForURL('**/runs/*');const launched=page.url().split('/').at(-1);
 await wait(async()=> (await api('/api/runs/'+launched)).status==='done');await page.reload();await page.locator('#execution-activity summary').click();await page.locator('.timeline li').first().waitFor();await page.locator('#execution-attempts summary').click();assert.equal(await page.locator('#execution-attempts .artifact').count(),1);
 await page.getByRole('link',{name:'Open report'}).click();await page.locator('.report').waitFor();const artifact=page.locator('.artifact[download]').first();const download=page.waitForEvent('download');await artifact.click();assert.ok((await download).suggestedFilename());
 await page.goto(base+'/services');await page.getByRole('button',{name:'Resume triggers',exact:true}).click();await page.waitForURL('**/services/lifecycle/*');await page.locator('.os-rail .system-navigation a[href="/services"]').click();await page.getByRole('button',{name:'Trigger task',exact:true}).click();await page.waitForURL('**/runs/*');await page.locator('.os-rail').waitFor();
 // The specific legacy example remains actionable in the new shell.
 await page.goto(base+'/runs/'+parked.id);await page.locator('.decision-options button[value=cancel]').click();await page.waitForURL('**/runs/'+parked.id);await wait(async()=> (await api('/api/runs/'+parked.id)).status==='cancelled');
 assert.deepEqual(errors,[]);
 console.log(`PASS: ${routes.length} page routes at desktop and mobile widths, one shared shell/logo, bounded scrolling, project/application navigation, launch, attempt history, report downloads, service controls, and decision resolution`);
}catch(e){if(page&&process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'failure.png')});throw e;}finally{await browser?.close();server.kill('SIGTERM');await exited;await rm(temporary,{recursive:true,force:true});}

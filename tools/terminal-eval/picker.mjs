import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:net';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temporary=await mkdtemp(path.join(tmpdir(),'mitos-picker-')),workspace=path.join(temporary,'projects'),folder=path.join(workspace,'Example & project');
await mkdir(path.join(folder,'child'),{recursive:true});await mkdir(path.join(workspace,'.hidden'));
await writeFile(path.join(workspace,'ordinary-file.txt'),'not a directory');
const reserve=createServer();await new Promise(r=>reserve.listen(0,'127.0.0.1',r));const port=reserve.address().port;await new Promise(r=>reserve.close(r));const base='http://127.0.0.1:'+port;
const server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr','127.0.0.1:'+port,'--data',path.join(temporary,'data'),'--workspace-root',workspace],{cwd:root,stdio:['ignore','ignore','pipe']});
let logs='',browser,page;server.stderr.on('data',b=>logs=(logs+b).slice(-4000));const exited=new Promise(r=>server.once('exit',r));
async function wait(fn){const end=Date.now()+15000;while(Date.now()<end){try{if(await fn())return}catch{}await new Promise(r=>setTimeout(r,100))}throw Error('Timed out. '+logs)}
async function browse(value){await page.locator('#project-directory').fill(value);await page.locator('#folder-go').click();await wait(async()=>await page.locator('#project-directory').inputValue()===path.normalize(value)&&await page.locator('#folder-list').getAttribute('aria-busy')==='false')}
async function shot(name){if(process.env.MITOS_SCREENSHOT_DIR){await mkdir(process.env.MITOS_SCREENSHOT_DIR,{recursive:true});await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,name+'.png')})}}
try{
 await wait(async()=>(await fetch(base+'/healthz')).ok);
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});page=await browser.newPage({viewport:{width:1440,height:1000}});const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base);await page.locator('#open-project-main').click();await page.locator('.folder-row').first().waitFor();assert.equal(await page.locator('#project-name').inputValue(),'projects');assert.equal(await page.locator('.folder-row').count(),1);
 await page.locator('#folder-hidden').check();await wait(async()=>await page.locator('.folder-row').count()===2);await page.locator('#folder-filter').fill('Example');assert.equal(await page.locator('.folder-row').count(),1);await page.locator('.folder-row').click();await wait(async()=>await page.locator('#project-directory').inputValue()===folder);assert.equal(await page.locator('#project-name').inputValue(),'Example & project');await shot('picker-desktop');
 await page.locator('#project-name').fill('Custom project');await page.locator('#folder-up').click();await wait(async()=>await page.locator('#project-directory').inputValue()===workspace);assert.equal(await page.locator('#project-name').inputValue(),'Custom project');
 await page.locator('#project-directory').fill(path.join(workspace,'missing'));await page.locator('#folder-go').click();await wait(async()=>!!await page.locator('#folder-error').textContent());
 // A delayed navigation response must not overwrite a newly typed path/name.
 let release;const gate=new Promise(r=>release=r);let seen;const started=new Promise(r=>seen=r);
 await page.route('**/api/directories?*',async route=>{if(new URL(route.request().url()).searchParams.get('path')===workspace){seen();await gate;}await route.continue().catch(()=>{})});
 await page.locator('#project-directory').fill(workspace);await page.locator('#folder-go').click();await started;await page.locator('#project-name').fill('');await page.locator('#project-directory').fill(folder+'/');release();await page.unrouteAll({behavior:'wait'});
 assert.equal(await page.locator('#project-directory').inputValue(),folder+'/');assert.equal(await page.locator('#project-name').inputValue(),'Example & project');await page.locator('#project-directory').press('Enter');await wait(async()=>await page.locator('#project-directory').inputValue()===folder);
 await page.locator('#project-form [type=submit]').click();await page.waitForURL('**/projects/*');const project=page.url().split('/').at(-1);await page.locator('#graph-compose').waitFor({state:'visible'});
 await page.setViewportSize({width:390,height:844});await page.goto(base+'/?open=project');await page.locator('.folder-row').first().waitFor();assert.ok(await page.locator('#project-dialog').evaluate(n=>n.scrollWidth<=n.clientWidth+1));assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await shot('picker-mobile');await page.locator('#project-dialog [data-close]').click();
 // Context/terminal pages use the same picker and return to the graph page.
 await page.goto(base+'/workspace?project='+project);await page.locator('#workspace-switcher > summary').click();await page.locator('#workspace-switcher [data-dialog=project-dialog]').click();await page.locator('.folder-row').first().waitFor();await browse(path.join(folder,'child'));assert.equal(await page.locator('#project-name').inputValue(),'child');await page.locator('#project-form [type=submit]').click();await page.waitForURL('**/projects/*');await page.locator('#graph-compose').waitFor({state:'visible'});
 assert.deepEqual(errors,[]);console.log('PASS: folder navigation, hidden folders, filtering, custom/default names, invalid paths, stale response protection, keyboard entry, project creation, graph integration, mobile layout, and workspace picker');
}finally{await browser?.close();if(server.exitCode===null){server.kill('SIGTERM');await exited}await rm(temporary,{recursive:true,force:true})}

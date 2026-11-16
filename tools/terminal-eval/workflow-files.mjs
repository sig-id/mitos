import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});
try{
 const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 const patch='diff --git a/src/main.go b/src/main.go\n--- a/src/main.go\n+++ b/src/main.go\n@@ -1 +1 @@\n-old\n+new\n';
 const files=[{path:'report.md',kind:'snapshot'},...Array.from({length:475},(_,i)=>({path:`src/file-${String(i).padStart(3,'0')}.go`,kind:'snapshot'}))];
 const run={id:'run',status:'failed',artifacts:[{name:'report.md'},{name:'output.txt'},{name:'empty.txt'},{name:'changes.patch'},{name:'workspace.tar.gz'}]};
 await page.route('http://fixture.test/**',route=>{
  const url=new URL(route.request().url());
  if(url.pathname==='/')return route.fulfill({contentType:'text/html',body:'<!doctype html><html><body><main id="result" class="graph-result"></main></body></html>'});
  if(url.pathname.endsWith('/files'))return route.fulfill({json:url.searchParams.has('scope')?files:[{path:'src/main.go',kind:'modified'}]});
  if(url.pathname.endsWith('/runs/missing'))return route.fulfill({status:404,json:{error:'Saved attempt unavailable'}});
  if(url.pathname.endsWith('/runs/run'))return route.fulfill({json:run});
  const file=url.pathname.split('/').at(-1)==='file'?url.searchParams.get('path'):url.pathname.split('/').at(-1);
  return route.fulfill({contentType:'text/plain',body:file==='empty.txt'?'':file==='report.md'?'# Partial diagnosis\n\nSaved before failure. [Source](src/file-474.go#L2)\n\n<img src=x onerror="window.injected=1">':file==='changes.patch'?patch:file==='output.txt'?'\x1b[31mERROR\x1b[0m '+('very-long-event-'.repeat(150)):'package main\n// retained source\n'});
 });
 await page.goto('http://fixture.test/');
 for(const name of ['operating.css','work-graph.css','markdown-editor.css'])await page.addStyleTag({path:path.join(root,'internal/web/static',name)});
 for(const name of ['analysis-format.js','markdown-editor.js','diff-view.js','graph-output.js','file-browser.js','graph-result.js'])await page.addScriptTag({path:path.join(root,'internal/web/static',name)});
 await page.evaluate(async()=>{
  const graph={id:'graph',root_task:'goal',status:'active',members:{goal:{spec:{id:'goal',kind:'scope',brief:{title:'Fix PRs'}}},worker:{spec:{id:'worker',kind:'application',brief:{title:'Diagnose execution'}},current_run_id:'run',state:'failed'},missing:{spec:{id:'missing',kind:'application',brief:{title:'Unavailable attempt'}},current_run_id:'missing',state:'failed'}}};
  await window.MitosGraphResult.open({graph,request:async path=>{const response=await fetch(path),value=await response.json();if(!response.ok)throw Error(value.error);return value},container:document.querySelector('#result'),onClose:()=>{}});
 });
 await page.getByText('No final answer yet',{exact:true}).waitFor();
 assert.equal(await page.getByText('Orchestrator completion review',{exact:true}).count(),0);
 await page.getByRole('combobox',{name:'Saved assignment'}).selectOption('1');await page.getByRole('heading',{name:'No saved report',exact:true}).waitFor();await page.getByRole('combobox',{name:'Saved assignment'}).selectOption('0');
 await page.locator('mitos-file-browser button[data-path="report.md"]').click();await page.getByRole('heading',{name:'Partial diagnosis',exact:true}).waitFor();
 assert.equal(await page.locator('#result img,#result script').count(),0);
 await page.locator('.graph-result-content').getByRole('button',{name:'Source',exact:true}).last().click();await page.locator('.graph-source-line.cited').waitFor();assert.equal(await page.locator('.graph-source-line.cited').getAttribute('data-line'),'2');
 await page.getByRole('combobox',{name:'File view'}).selectOption('workspace');
 await page.getByRole('button',{name:'Open folder src/'}).click();assert.equal(await page.locator('.file-list li').count(),100);
 await page.getByRole('button',{name:'Show more',exact:true}).click();assert.equal(await page.locator('.file-list li').count(),200);
 await page.getByRole('searchbox',{name:'Search saved files'}).fill('file-474');assert.equal(await page.locator('.file-list li').count(),1);await page.locator('.file-list button').click();await page.getByRole('heading',{name:'src/file-474.go',exact:true}).waitFor();
 await page.getByRole('combobox',{name:'File view'}).selectOption('changes');await page.locator('button[data-path="changes.patch"]').click();await page.locator('.diff-table').waitFor();assert.match(await page.locator('.diff-table').textContent(),/new/);
 await page.getByRole('combobox',{name:'File view'}).selectOption('reports');await page.locator('button[data-path="empty.txt"]').click();await page.getByText('No content was saved in this file.',{exact:true}).waitFor();await page.locator('button[data-path="output.txt"]').click();await page.locator('.graph-execution-log').waitFor();assert.ok(!(await page.locator('.graph-execution-log').textContent()).includes('\x1b'));
 for(const width of [1440,390]){await page.setViewportSize({width,height:1000});assert.ok(await page.locator('.graph-execution-log').evaluate(n=>getComputedStyle(n).whiteSpace==='pre-wrap'&&n.scrollWidth<=n.clientWidth+1));assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1))}
 await page.screenshot({path:'/tmp/mitos-workflow-files-mobile.png',fullPage:true});await page.setViewportSize({width:1440,height:1000});await page.screenshot({path:'/tmp/mitos-workflow-files.png',fullPage:true});
 // Long attempt reports scroll in the dedicated reader, with the navigation
 // header remaining available and source citations scrolled into view.
 await page.route('**/artifacts/report.md',route=>route.fulfill({contentType:'text/plain',body:'# Long saved report\n\n'+Array.from({length:80},(_,i)=>`Paragraph ${i+1}. Saved evidence for this attempt.\n\n`).join('')+'[Late source](src/file-474.go#L180)'}));
 await page.route('**/file?*',route=>route.fulfill({contentType:'text/plain',body:Array.from({length:220},(_,i)=>'// Source line '+(i+1)).join('\n')}));
 await page.evaluate(async()=>{
  const dialog=document.createElement('dialog');dialog.id='graph-files-dialog';dialog.setAttribute('aria-label','Files from this attempt');const container=document.createElement('div');container.id='graph-attempt-browser';container.className='graph-result';dialog.append(container);document.body.append(dialog);
  const graph={id:'graph',root_task:'worker',members:{worker:{spec:{id:'worker',kind:'application',brief:{title:'Diagnose execution'}}}}};
  const loading=window.MitosGraphResult.open({graph,request:async path=>(await fetch(path)).json(),container,attemptOnly:true,initial:{task:'worker',run:'run'},onClose:()=>dialog.close()});dialog.showModal();await loading;
 });
 const dialog=page.getByRole('dialog',{name:'Files from this attempt',exact:true}),reader=dialog.locator('.graph-result-reader');
 assert.equal(await dialog.getByRole('button',{name:'Back to activity',exact:true}).evaluate(n=>n===document.activeElement),true,'loading must preserve keyboard focus');
 for(const width of [1440,390]){
  await page.setViewportSize({width,height:1000});
  assert.ok(await dialog.locator('.md-preview').evaluate(n=>n.scrollHeight<=n.clientHeight+1),'report must not add a nested vertical scrollbar');
  assert.ok(await reader.evaluate(n=>n.scrollHeight>n.clientHeight),'long report should scroll in the reader');
 }
 await dialog.getByRole('button',{name:'Late source',exact:true}).click();await dialog.locator('.graph-source-line.cited').waitFor();
 assert.ok(await reader.evaluate(n=>{const line=n.querySelector('.cited').getBoundingClientRect(),pane=n.getBoundingClientRect();return line.top>=pane.top&&line.bottom<=pane.bottom}),'a late source citation must be visible');
 await dialog.getByRole('button',{name:'Back',exact:true}).click();await dialog.getByRole('heading',{name:'Long saved report',exact:true}).waitFor();assert.equal(await reader.evaluate(n=>n.scrollTop),0,'returning to a file should start at its heading');
 await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
 assert.deepEqual(errors,[]);console.log('PASS: unfinished report discovery, directory browsing, pagination, search, source citations, inline diff, safe previews, wrapped ANSI-free logs, mobile layout and full-window attempt reading');
}finally{await browser.close()}

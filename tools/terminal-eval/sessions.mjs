import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:net';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temporary=await mkdtemp(path.join(tmpdir(),'mitos-sessions-'));
const data=path.join(temporary,'data');await mkdir(data);
const bash=execFileSync('which',['bash'],{encoding:'utf8'}).trim();
const fixture=path.join(temporary,'agent.sh');
await writeFile(fixture,'printf "ARG:%s\\n" "$@"\nif test -n "$MITOS_HANDOVER"; then cat "$MITOS_HANDOVER"; fi\nprintf "\\nREADY\\n"\nwhile read -r line; do if test "$line" = snapshot; then printf "%180000s" ""; printf "\\033[2J\\033[H\\033[1;31mSaved heading\\033[0m\\033[3;70Hright edge\\033[5;1H<script>unsafe</script> café 中\\033[7;1HRESTORED"; else printf "INPUT:%s\\n" "$line"; fi; done\n');
await writeFile(path.join(data,'agents.json'),JSON.stringify({applications:['source-test','target-test'].map(id=>({id,name:id,kind:'agent',executables:[bash],args:[fixture],prompt:'positional',session_protocol:'claude'}))}));
const reservation=createServer();await new Promise(r=>reservation.listen(0,'127.0.0.1',r));const port=reservation.address().port;await new Promise(r=>reservation.close(r));
const base=`http://127.0.0.1:${port}`;
const server=spawn(process.env.MITOS_BIN||path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',data,'--workspace-root',temporary],{cwd:root,stdio:['ignore','ignore','pipe']});
let log='',startError;server.stderr.on('data',b=>log=(log+b).slice(-8000));server.on('error',e=>startError=e);
const exited=new Promise(resolve=>{server.once('exit',resolve);server.once('error',resolve)});
const api=async(p,body)=>{const r=await fetch(base+p,body===undefined?{}:{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});const text=await r.text();assert.equal(r.status,200,text);return JSON.parse(text)};
// Profile and handoff context can exceed the terminal viewport. Check durable
// bytes for payload assertions; connection/rendering behavior is checked below.
const outputContains=async(id,text)=>{let offset=0,output='';for(let i=0;i<100;i++){const v=await api(`/api/sessions/${id}/output?offset=${offset}`);output+=Buffer.from(v.data||'', 'base64').toString();offset=v.next;if(output.includes(text))return;await new Promise(r=>setTimeout(r,50));}assert.fail(`Missing session output: ${text}`);};
let browser;
try{
 let ready=false;
 for(let i=0;i<150;i++){
  if(startError)throw startError;if(server.exitCode!==null)throw Error(log);
  try{const r=await fetch(base+'/healthz');if(r.ok){ready=true;break}}catch{}
  await new Promise(r=>setTimeout(r,100));
 }
 if(!ready)throw Error('Server startup timed out: '+log);
 const p=await api('/api/projects',{name:'Session continuations',directory:temporary});
 await mkdir(path.join(temporary,'another'));const other=await api('/api/projects',{name:'Another workspace',directory:path.join(temporary,'another')});
 const source=await api(`/api/projects/${p.id}/sessions`,{agent_id:'source-test',prompt:'Implement the checkout flow'});
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});
 const page=await browser.newPage({permissions:['clipboard-read','clipboard-write']});const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(`${base}/workspace?project=${p.id}&session=${source.id}`);
 await page.waitForFunction(()=>document.querySelector('#connection-status').textContent.startsWith('Connected'));
 assert.equal(await page.locator('#session-input').count(),0);
 // Geometry checks exercise the actual terminal renderer, including saved output.
 const checkLayout=async()=>{
  await page.waitForFunction(()=>{
   const terminal=document.querySelector('#terminal'),screen=terminal.querySelector('.saved-terminal')||terminal.querySelector('.xterm-screen');
   const a=terminal.getBoundingClientRect(),b=screen.getBoundingClientRect();
   return b.width>0&&b.right<=a.right+1&&b.bottom<=a.bottom+1;
  });
  const geometry=await page.evaluate(()=>{
   const rect=s=>document.querySelector(s).getBoundingClientRect();
   const saved=document.querySelector('.saved-terminal'),viewport=saved||document.querySelector('.xterm-viewport'),screen=rect(saved?'.saved-terminal':'.xterm-screen');
   return {pageHeight:document.documentElement.scrollHeight,height:innerHeight,pageWidth:document.documentElement.scrollWidth,width:innerWidth,
    terminalBottom:rect('.session-foot').bottom,screenRight:screen.right,viewportRight:saved?rect('.saved-terminal').right:rect('.xterm-viewport').left+viewport.clientWidth,
    text:parseFloat(getComputedStyle(document.querySelector('.workspace-tabs button')).fontSize)};
  });
  assert.ok(geometry.pageHeight<=geometry.height+1&&geometry.pageWidth<=geometry.width+1,'page does not compete with terminal scroll: '+JSON.stringify(geometry));
  assert.ok(geometry.terminalBottom<=geometry.height+1,'footer remains visible');
  assert.ok(geometry.screenRight<=geometry.viewportRight+1,'terminal text leaves room for scrollbar');
  assert.ok(geometry.text>=14,'readable navigation text');
 };
 await checkLayout();
 await api(`/api/sessions/${source.id}/input`,{key:'scroll-fixture',data:Array.from({length:150},(_,i)=>`scroll line ${i}\r`).join('')});
 await page.waitForFunction(()=>{const v=document.querySelector('.xterm-viewport');return v.scrollHeight>v.clientHeight*2});
 await page.locator('.xterm-viewport').evaluate(v=>{v.scrollTop=0});
 await page.locator('#terminal').hover();await page.mouse.wheel(0,250);
 await page.waitForFunction(()=>document.querySelector('.xterm-viewport').scrollTop>0);
 assert.equal(await page.evaluate(()=>scrollY),0,'wheel scroll stays inside terminal');
 const context=page.locator('#desk-context-menu');
 const workspaceURL=`${base}/workspace?project=${p.id}&session=${source.id}`;
 assert.equal(await page.locator('.project-link').first().isVisible(),false,'projects are tucked into a workspace selector');
 await page.locator('#workspace-switcher > summary').click();
 await page.locator(`.project-link[data-project-id="${other.id}"]`).click({button:'right'});
 await context.getByRole('menuitem',{name:'New session…',exact:true}).click();
 await page.waitForURL(url=>url.searchParams.get('project')===other.id);
 await page.locator('#session-dialog').waitFor({state:'visible'});
 assert.equal((await api(`/api/projects/${other.id}/sessions`)).length,0,'opening dialog did not launch a process');
 await page.goto(workspaceURL);await page.waitForFunction(()=>document.querySelector('#connection-status').textContent.startsWith('Connected'));
 await page.locator('#workspace-switcher > summary').click({button:'right'});
 await context.getByRole('menuitem',{name:'Copy project path',exact:true}).click();
 assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),temporary);
 const sourceLink=page.locator(`.session-link[data-session-id="${source.id}"]`);
 await sourceLink.focus();await page.keyboard.press('Shift+F10');
 await context.getByRole('menuitem',{name:'Fork…',exact:true}).waitFor();
 assert.equal(await context.getByRole('menuitem',{name:'Resume…',exact:true}).isDisabled(),true);
 await page.locator('#terminal').dispatchEvent('scroll');assert.equal(await context.isVisible(),true,'live terminal scroll leaves menu open');
 if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-context-desktop.png')});
 await page.keyboard.press('End');assert.equal(await page.evaluate(()=>document.activeElement.textContent),'Copy session ID');
 await page.keyboard.press('Enter');assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),source.id);
 await sourceLink.focus();await page.keyboard.press('Shift+F10');await page.keyboard.press('Escape');
 assert.equal(await context.isVisible(),false);assert.equal(await sourceLink.evaluate(el=>el===document.activeElement),true);

 const menu=()=>page.locator('#session-actions > summary').click();
 const open=async kind=>{await menu();await page.locator(`[data-continuation=${kind}]`).click();await page.locator('#continuation-form [type=submit]').waitFor();await page.waitForFunction(()=>!document.querySelector('#continuation-form [type=submit]').disabled)};
 const submit=async()=>{const previous=page.url();await page.locator('#continuation-form [type=submit]').click();await page.waitForURL(url=>url.href!==previous);await page.waitForFunction(()=>document.querySelector('#connection-status').textContent.startsWith('Connected'));return new URL(page.url()).searchParams.get('session')};
 await menu();assert.equal(await page.locator('[data-continuation=resume]').isDisabled(),true);await menu();
 await open('fork');assert.equal(await page.locator('#continuation-mode').inputValue(),'native');
 await page.locator('#continuation-mode').selectOption('context');await page.locator('#continuation-notes').fill('Check cart.go and preserve the public API');
 await page.locator('#continuation-prompt').fill('Add checkout tests');
 const forkID=await submit();
 await outputContains(forkID,'Check cart.go');
 assert.equal((await api(`/api/sessions/${source.id}`)).status,'running');
 assert.ok((await page.locator('.session-lineage a').getAttribute('href')).includes(source.id));
 await page.locator(`.session-link[data-session-id="${source.id}"]`).click({button:'right'});
 await context.getByRole('menuitem',{name:'Fork…',exact:true}).click();
 await page.waitForFunction(()=>!document.querySelector('#continuation-form [type=submit]').disabled);
 await page.locator('#continuation-mode').selectOption('context');
 await page.locator('#continuation-prompt').fill('Branch the background source');
 const backgroundForkID=await submit();
 assert.equal((await api(`/api/sessions/${backgroundForkID}`)).continuation.source_id,source.id,'right-click targets the background source');
 await page.goto(`${base}/workspace?project=${p.id}&session=${forkID}`);await page.waitForFunction(()=>document.querySelector('#connection-status').textContent.startsWith('Connected'));
 await page.locator(`.session-link[data-session-id="${source.id}"]`).click({button:'right'});
 await context.getByRole('menuitem',{name:'Stop session',exact:true}).click();
 for(let i=0;i<50&&(await api(`/api/sessions/${source.id}`)).status!=='stopped';i++)await page.waitForTimeout(50);
 assert.equal((await api(`/api/sessions/${source.id}`)).status,'stopped');
 assert.equal((await api(`/api/sessions/${forkID}`)).status,'running','background stop leaves selected session running');

 await menu();await page.locator('#stop-session').click();await page.waitForFunction(()=>document.querySelector('#session-status').textContent==='stopped');
 await page.setViewportSize({width:1000,height:700});await checkLayout();
 await page.reload();await page.waitForFunction(()=>document.querySelector('#connection-status').textContent==='Session stopped');await checkLayout();
 if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-terminal-stopped.png')});
 await page.setViewportSize({width:1440,height:900});await checkLayout();
 await open('resume');const native=await page.locator('#continuation-native').inputValue();assert.ok(native);
 const resumedID=await submit();assert.notEqual(resumedID,forkID);
 await outputContains(resumedID,'ARG:--resume');
 assert.equal((await api(`/api/sessions/${resumedID}`)).native_session_id,native);
 await open('handover');await page.locator('#continuation-agent').selectOption('target-test');
 await page.waitForFunction(()=>!document.querySelector('#continuation-form [type=submit]').disabled);
 await page.locator('#continuation-notes').fill('Handoff to target: tests are next');
 const targetID=await submit();
 await outputContains(targetID,'Handoff to target: tests are next');
 assert.equal((await api(`/api/sessions/${targetID}`)).agent_id,'target-test');
 for(let i=0;i<50&&(await api(`/api/sessions/${resumedID}`)).status!=='stopped';i++)await page.waitForTimeout(50);
 assert.equal((await api(`/api/sessions/${resumedID}`)).status,'stopped');
 await page.setViewportSize({width:390,height:844});await checkLayout();await open('fork');
 assert.ok(await page.locator('#continuation-dialog').isVisible());
 await page.locator('#continuation-dialog [data-close]').click();
 const targetLink=page.locator(`.session-link[data-session-id="${targetID}"]`);
 await targetLink.evaluate(el=>el.dispatchEvent(new MouseEvent('contextmenu',{bubbles:true,cancelable:true,clientX:389,clientY:843})));
 await context.getByRole('menuitem',{name:'Fork…',exact:true}).waitFor();
 const bounds=await context.boundingBox();if(process.env.MITOS_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.MITOS_SCREENSHOT_DIR,'mitos-context-mobile.png')});assert.ok(bounds.x>=0&&bounds.x+bounds.width<=390&&bounds.y>=0&&bounds.y+bounds.height<=844,'menu fits viewport edges: '+JSON.stringify(bounds));
 await page.keyboard.press('Escape');

 // Replay a cursor-addressed screen over several deliberately slow frames.
 // Fitting before replay completes corrupts its positions; resizing afterward
 // must keep every character, color, and Unicode glyph in the saved transcript.
 const replay=await api(`/api/projects/${p.id}/sessions`,{agent_id:'source-test',prompt:'Saved terminal fixture'});
 await api(`/api/sessions/${replay.id}/input`,{key:'snapshot',data:'snapshot\r'});
 await outputContains(replay.id,'RESTORED');await api(`/api/sessions/${replay.id}/stop`,{});
 await page.routeWebSocket('**/stream?*',socket=>{const server=socket.connectToServer();server.onMessage(message=>setTimeout(()=>socket.send(message),200));});
 await page.goto(`${base}/workspace?project=${p.id}&session=${replay.id}`);
 await page.locator('.saved-terminal').waitFor();await checkLayout();
 const transcript=page.locator('.saved-terminal'),saved=await transcript.textContent();
 assert.match(saved,/Saved heading/);assert.match(saved,/right edge/);assert.match(saved,/<script>unsafe<\/script> café 中/);
 assert.equal(await transcript.locator('script').count(),0);
 const heading=transcript.locator('span').filter({hasText:'Saved heading'});
 assert.equal(await heading.evaluate(el=>getComputedStyle(el).fontWeight),'700');
 assert.notEqual(await heading.evaluate(el=>getComputedStyle(el).color),await transcript.evaluate(el=>getComputedStyle(el).color));
 await page.setViewportSize({width:1440,height:900});await checkLayout();assert.equal(await transcript.textContent(),saved);
 assert.deepEqual(errors,[]);console.log('PASS: session menu, live-source resume disabled, context fork, source lineage, stop, native resume, cross-agent handover, workspace selector, project/session context menus, keyboard navigation, background actions, viewport placement, mobile dialog, terminal scroll containment, live and stopped terminal resizing, readable text, no duplicate composer or browser errors');
}finally{
 if(browser)await browser.close();server.kill('SIGTERM');const force=setTimeout(()=>server.kill('SIGKILL'),5000);await exited;clearTimeout(force);await rm(temporary,{recursive:true,force:true});
}

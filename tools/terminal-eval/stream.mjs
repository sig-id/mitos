import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {mkdtemp,mkdir,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {createServer} from 'node:net';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const temporary=await mkdtemp(path.join(tmpdir(),'mitos-stream-'));
const data=path.join(temporary,'data');await mkdir(data);
const python=execFileSync('python3',['-c','import sys;print(sys.executable)'],{encoding:'utf8'}).trim();
const fixture=path.join(temporary,'keys.py');
await writeFile(fixture,`import os,tty
tty.setraw(0)
print('Ready\\r',flush=True)
while True:
 data=os.read(0,128)
 if not data:break
 print('KEY:'+data.hex()+'\\r',flush=True)
`);
await writeFile(path.join(data,'agents.json'),JSON.stringify({applications:[{id:'keys-test',name:'Stream test',kind:'application',executables:[python],args:[fixture],prompt:'none'}]}));
const reservation=createServer();await new Promise(resolve=>reservation.listen(0,'127.0.0.1',resolve));const port=reservation.address().port;await new Promise(resolve=>reservation.close(resolve));
const base=`http://127.0.0.1:${port}`;
const server=spawn(path.join(root,'bin/mitos'),['serve','--addr',`127.0.0.1:${port}`,'--data',data,'--workspace-root',temporary],{cwd:root,stdio:['ignore','ignore','pipe']});
let log='',startError;server.stderr.on('data',b=>log=(log+b).slice(-8000));server.on('error',e=>startError=e);
const exited=new Promise(resolve=>{server.once('exit',resolve);server.once('error',resolve)});
try{
 let ready=false;
 for(let i=0;i<100;i++){
  if(startError)throw startError;
  if(server.exitCode!==null)throw Error(log);
  try{const r=await fetch(base+'/healthz');if(r.ok){ready=true;break}}catch{}
  await new Promise(resolve=>setTimeout(resolve,100));
 }
 if(!ready)throw Error('Test server startup timed out: '+log);
 await verify(base,temporary);
}finally{
 server.kill('SIGTERM');
 const force=setTimeout(()=>server.kill('SIGKILL'),5000);
 await exited;clearTimeout(force);await rm(temporary,{recursive:true,force:true});
}
async function verify(base,directory){
 const api=async(path,body)=>{const r=await fetch(base+path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});assert.equal(r.status,200);return r.json()};
 const p=await api('/api/projects',{name:'Terminal interaction',directory});const s=await api(`/api/projects/${p.id}/sessions`,{agent_id:'keys-test'});
 const b=await chromium.launch({executablePath:process.env.CHROMIUM_PATH || undefined,args:['--no-sandbox']});
 try{
 const page=await b.newPage();const errors=[],streams=[],polls=[];page.on('pageerror',e=>errors.push(e.message));page.on('websocket',ws=>streams.push(ws.url()));page.on('request',r=>{if(r.url().includes('/output?'))polls.push(r.url())});
 await page.addInitScript(()=>{const Original=window.WebSocket;window.WebSocket=class extends Original{constructor(...args){super(...args);window.disconnectStream=()=>this.close()}}});
 await page.goto(`${base}/workspace?project=${p.id}&session=${s.id}`);await page.waitForFunction(()=>document.querySelector('#connection-status').textContent.startsWith('Connected'));
 await page.keyboard.type('x');await page.waitForFunction(()=>document.querySelector('#terminal').innerText.includes('KEY:78'));
 await page.evaluate(()=>window.disconnectStream());await page.waitForFunction(()=>document.querySelector('#connection-status').textContent.includes('reconnecting'));
 assert.equal(await page.locator('#interrupt').isDisabled(),true);
 await page.waitForFunction(()=>document.querySelector('#connection-status').textContent.startsWith('Connected'));
 assert.equal(streams.length,2);assert.ok(Number(new URL(streams[1]).searchParams.get('offset'))>0);
 assert.equal((await page.locator('#terminal').innerText()).split('KEY:78').length-1,1,'reconnect did not duplicate rendered output');
 await page.locator('#terminal').click();await page.keyboard.type('y');await page.waitForFunction(()=>document.querySelector('#terminal').innerText.includes('KEY:79'));
 await page.waitForTimeout(32000);
 assert.equal(streams.length,2,'stream survived HTTP write timeout using heartbeats');
 await page.keyboard.type('z');await page.waitForFunction(()=>document.querySelector('#terminal').innerText.includes('KEY:7a'));
 const tabs=[];
 for(let i=0;i<7;i++){const tab=await b.newPage();tabs.push(tab);await tab.goto(`${base}/workspace?project=${p.id}&session=${s.id}`);await tab.waitForFunction(()=>document.querySelector('#connection-status').textContent.startsWith('Connected'))}
 await tabs[6].keyboard.type('q');await page.waitForFunction(()=>document.querySelector('#terminal').innerText.includes('KEY:71'));
 for(const tab of tabs)await tab.close();
 assert.equal(polls.length,0,'no output polling');assert.deepEqual(errors,[]);
 console.log('PASS: streaming, byte-offset reconnect without duplication, disabled input during reconnect, 32-second idle connection, eight simultaneous viewers and no output polling');
 }finally{await b.close();await api(`/api/sessions/${s.id}/stop`,{})}
}

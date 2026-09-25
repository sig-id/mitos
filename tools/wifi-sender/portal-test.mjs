import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFile} from 'node:fs/promises';
const assets = new URL('../../internal/wifisetup/portal/', import.meta.url);
let state='ready', posts=[], disconnected=false;
const token='test-session-token';
const server=createServer(async (req,res)=>{
  if(disconnected){req.socket.destroy();return;}
  res.setHeader('Cache-Control','no-store');
  res.setHeader('Content-Security-Policy',"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'");
  if(req.url==='/api/status'){res.setHeader('Content-Type','application/json');res.end(JSON.stringify({ssid:'Cafe <&> WiFi',token,stage:state,message:state==='error'?'Password not accepted':''}));return;}
  if(req.url==='/api/join'){
    let body='';for await(const chunk of req)body+=chunk;
    assert.equal(req.headers['x-setup-token'],token);assert.equal(req.headers['content-type'],'application/json');
    posts.push(JSON.parse(body));state='joining';res.writeHead(202,{'Content-Type':'application/json'});res.end('{"accepted":true}');return;
  }
  const name={'/':'index.html','/style.css':'style.css','/app.js':'app.js'}[req.url];
  if(!name){res.writeHead(404);res.end();return;}
  res.setHeader('Content-Type',name.endsWith('js')?'text/javascript':name.endsWith('css')?'text/css':'text/html');res.end(await readFile(new URL(name,assets)));
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
let browser;
try {
 browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox','--disable-gpu','--disable-dev-shm-usage']});
 const context=await browser.newContext({viewport:{width:390,height:844}}), page=await context.newPage();
 const requests=[],errors=[];page.on('request',r=>requests.push(r));page.on('pageerror',e=>errors.push(e.message));
 const origin=`http://127.0.0.1:${server.address().port}`;
 await page.goto(origin);await page.locator('#status').filter({hasText:'Ready when you are'}).waitFor();
 assert.equal(await page.locator('#network').inputValue(),'Cafe <&> WiFi');
 await page.locator('#password').fill('short');await page.locator('#join').click();
 assert.equal(posts.length,0);assert.match(await page.locator('#status').textContent(),/8–63/);
 await page.locator('#password').fill('Symbols !Aa12345');await page.locator('#reveal').click();assert.equal(await page.locator('#password').getAttribute('type'),'text');
 await page.locator('#reveal').click();assert.equal(await page.locator('#password').getAttribute('type'),'password');
 await page.screenshot({path:'wifi-portal-phone.png',fullPage:true});
 await page.locator('#join').click();await page.locator('#status').filter({hasText:'Connecting…'}).waitFor();
 assert(await page.locator('#join').isDisabled());
 await page.waitForFunction(()=>document.querySelector('#join').disabled);
 // Wrong passwords are editable in the same session.
 await new Promise(resolve=>setTimeout(resolve,100));state='error';
 await page.locator('#status').filter({hasText:'Password not accepted'}).waitFor();
 assert.equal(await page.locator('#password').inputValue(),'Symbols !Aa12345');
 await page.locator('#password').fill('Correct !123');await page.locator('#join').click();
 await new Promise(resolve=>setTimeout(resolve,100));disconnected=true;
 await page.locator('#status').filter({hasText:'connection is changing'}).waitFor();
 assert(!/Connected\./.test(await page.locator('#status').textContent()));
 disconnected=false;state='connected';
 await page.locator('#status').filter({hasText:'Connected. Your watch is ready'}).waitFor();
 assert.equal(await page.locator('#password').inputValue(),'');assert(await page.locator('#setup').isHidden());
 assert.equal(posts.length,2);assert.equal(posts[1].password,'Correct !123');
 assert.equal(await page.evaluate(()=>localStorage.length+sessionStorage.length),0);
 assert.deepEqual(await page.evaluate(()=>caches.keys()),[]);
 assert(requests.every(r=>r.url().startsWith(origin+'/')));
 assert(!requests.some(r=>/Correct|Symbols|test-session-token/.test(r.url())));
 assert(requests.filter(r=>r.method()==='POST').every(r=>r.url()===origin+'/api/join'));
 assert.deepEqual(errors,[]);
 // Narrow phones must retain usable controls without horizontal scrolling.
 await page.setViewportSize({width:320,height:640});state='ready';await page.reload();
 await page.locator('#status').filter({hasText:'Ready when you are'}).waitFor();
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 console.log('Portal browser passed: validation, reveal, retry, channel loss, verified success, secret clearing, local-only requests and narrow layout.');
} finally {await browser?.close();server.closeAllConnections();await new Promise(resolve=>server.close(resolve));}

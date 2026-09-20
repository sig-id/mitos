import {chromium} from 'playwright';
import {createServer} from 'node:http';
import {readFile, mkdir, readdir, writeFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
const directory=path.dirname(fileURLToPath(import.meta.url));
const root=path.resolve(directory,'../..');
const results=path.join(directory,'results'); await mkdir(results,{recursive:true});
const assets={
 '/xterm.js':path.join(root,'internal/web/static/xterm.js'),
 '/xterm.css':path.join(root,'internal/web/static/xterm.css'),
 '/ghostty.js':path.join(directory,'node_modules/ghostty-web/dist/ghostty-web.umd.cjs'),
 '/ghostty-vt.wasm':path.join(directory,'node_modules/ghostty-web/ghostty-vt.wasm')
};
const server=createServer(async(req,res)=>{
 try {
  if(req.url==='/'){res.setHeader('Content-Type','text/html');res.end('<link rel="stylesheet" href="/xterm.css"><style>body{background:#14171b}#terminal{width:1000px;height:600px}</style><div id="terminal"></div><script src="/xterm.js"></script><script src="/ghostty.js"></script>');return}
  if(!assets[req.url]){res.writeHead(404).end();return}
  res.setHeader('Content-Type',req.url.endsWith('.wasm')?'application/wasm':req.url.endsWith('.css')?'text/css':'application/javascript');res.end(await readFile(assets[req.url]));
 }catch{res.writeHead(500).end()}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
const base=`http://127.0.0.1:${server.address().port}`;
const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH || undefined,headless:true,args:['--no-sandbox']});
const report={ghostty:'0.4.0',tests:[],recordings:[],performance:{}};
try {
 const page=await browser.newPage({viewport:{width:1200,height:800}});
 page.on('pageerror',e=>report.tests.push({name:'page error',pass:false,detail:e.message}));
 await page.goto(base);
 await page.evaluate(async()=>{window.ghostty=await GhosttyWeb.Ghostty.load('/ghostty-vt.wasm')});
 async function create(engine,fresh=true){if(fresh){await page.goto(base);await page.evaluate(async()=>{window.ghostty=await GhosttyWeb.Ghostty.load('/ghostty-vt.wasm')})}await page.evaluate(engine=>{
  window.term?.dispose();document.querySelector('#terminal').replaceChildren();window.sent=[];
  const options={cols:100,rows:28,fontSize:13,scrollback:10000,theme:{background:'#14171b',foreground:'#d8dee9',cursor:'#aac5ff',selectionBackground:'#334864'}};
  window.term=engine==='xterm'?new Terminal(options):new GhosttyWeb.Terminal({...options,ghostty:window.ghostty});
  term.open(document.querySelector('#terminal'));term.onData(data=>sent.push(data));term.focus();
 },engine)}
 async function write(text,split=0){await page.evaluate(async({bytes,split})=>{
  const data=Uint8Array.from(bytes);for(let i=0;i<data.length;i+=split||data.length)await new Promise(resolve=>term.write(data.slice(i,i+(split||data.length)),resolve));
 },{bytes:[...Buffer.from(text)],split})}
 async function screen(){return page.evaluate(()=>{
  const b=term.buffer.active;return Array.from({length:term.rows},(_,i)=>b.getLine(Math.max(0,b.length-term.rows)+i)?.translateToString(true)||'').join('\n').trimEnd();
 })}
 async function check(name,fn){try{const detail=await fn();report.tests.push({name,pass:true,detail})}catch(e){report.tests.push({name,pass:false,detail:String(e.message)})}}
 function equal(actual,expected){if(JSON.stringify(actual)!==JSON.stringify(expected))throw new Error(JSON.stringify({expected,actual}))}
 for(const engine of ['xterm','ghostty']){
  await check(engine+': recreate terminal clears previous state',async()=>{await create(engine);await write('old session contents');await create(engine,false);await write('new');equal(await screen(),'new')});
  await check(engine+': split UTF-8 / escape sequences',async()=>{await create(engine);await write('\x1b[31mred\x1b[0m café 界 e\u0301',1);equal(await screen(),'red café 界 e\u0301')});
  await check(engine+': alternate screen restore',async()=>{await create(engine);await write('original\x1b[?1049h\x1b[2J\x1b[Halternate\x1b[?1049l');equal(await screen(),'original')});
  await check(engine+': cursor and erase',async()=>{await create(engine);await write('old value\r\x1b[2Knew');equal(await screen(),'new')});
  await check(engine+': ANSI palette with Mitos theme',async()=>{await create(engine);await write('\x1b[31mred');const color=await page.evaluate(()=>term.buffer.active.getLine(0).getCell(0).getFgColor());if(!color)throw new Error('ANSI red became black');return {color}});
  await check(engine+': Ctrl+C/D and application arrows',async()=>{await create(engine);await write('\x1b[?1h');await page.keyboard.press('Control+c');await page.keyboard.press('Control+d');await page.keyboard.press('ArrowUp');equal(await page.evaluate(()=>sent),['\x03','\x04','\x1bOA'])});
  await check(engine+': bracketed paste',async()=>{await create(engine);await write('\x1b[?2004h');await page.evaluate(()=>term.paste('hello\nworld'));equal(await page.evaluate(()=>sent),['\x1b[200~hello\rworld\x1b[201~'])});
  await check(engine+': disabled keyboard',async()=>{await create(engine);await page.evaluate(()=>term.options.disableStdin=true);await page.keyboard.press('a');equal(await page.evaluate(()=>sent),[])});
  await check(engine+': cursor query',async()=>{await create(engine);await write('hello\x1b[6n');await page.waitForTimeout(20);equal(await page.evaluate(()=>sent),['\x1b[1;6R'])});
  await check(engine+': resize and selection',async()=>{await create(engine);await write('keep this text');await page.evaluate(()=>{term.resize(80,24);term.select(0,0,14)});equal(await page.evaluate(()=>term.getSelection()),'keep this text')});
  await check(engine+': Mitos application-mode API',async()=>{await create(engine);await write('\x1b[?1h');equal(await page.evaluate(()=>term.modes?.applicationCursorKeysMode),true)});
  const burst=Buffer.from(Array.from({length:5000},(_,i)=>`\r\n\x1b[32m${i}\x1b[0m agent output 界 café`).join(''));
  const samples=[];
  for(let trial=0;trial<5;trial++){
   await create(engine);
   samples.push(await page.evaluate(async bytes=>{const data=Uint8Array.from(bytes),start=performance.now();for(let i=0;i<data.length;i+=65536)await new Promise(resolve=>term.write(data.slice(i,i+65536),resolve));await new Promise(requestAnimationFrame);return performance.now()-start},[...burst]));
  }
  report.performance[engine]={bytes:burst.length,samples_ms:samples,median_ms:[...samples].sort((a,b)=>a-b)[2]};
 }
 const recordings=path.join(results,'recordings');
 for(const name of await readdir(recordings).catch(()=>[])){
  if(!name.endsWith('.bin'))continue;
  const bytes=await readFile(path.join(recordings,name));const snapshots={};
  for(const engine of ['xterm','ghostty']){
   await create(engine);await write(bytes,127);snapshots[engine]=await screen();await page.screenshot({path:path.join(results,`${name}.${engine}.png`)});
  }
  report.recordings.push({agent:name.replace('.bin',''),bytes:bytes.length,buffer_text_matches:snapshots.xterm===snapshots.ghostty});
 }
 report.decision=report.tests.some(t=>t.name.startsWith('ghostty:')&&!t.pass)?'retain xterm; candidate failed compatibility gate':'candidate eligible for further integration testing';
 if(report.tests.some(t=>!t.pass&&!t.name.startsWith('ghostty:')))process.exitCode=1;
 await writeFile(path.join(results,'report.json'),JSON.stringify(report,null,2)+'\n');
 console.log(JSON.stringify(report,null,2));
}finally{await browser.close();await new Promise(resolve=>server.close(resolve))}

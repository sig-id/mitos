import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import extension from '../pi_attention.mjs';
const n={id:'notice_test',author:'operator',body:'Hold the index',pause:true,targets:[{session_id:'session_test'}]},messages=[],events={};
let receivedRelease=false;
const server=createServer(async(req,res)=>{res.setHeader('Content-Type','application/json');if(req.method==='GET'){res.end(JSON.stringify([n]));return;}let body='';for await(const b of req)body+=b;receivedRelease=JSON.parse(body).release;res.end('{}');});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
process.env.MITOS_URL=`http://127.0.0.1:${server.address().port}`;process.env.MITOS_SESSION_ID='session_test';
extension({on:(name,fn)=>events[name]=fn,sendMessage:(body,options)=>messages.push({body,options})});
const wait=async condition=>{for(let i=0;i<100;i++){if(condition())return;await new Promise(r=>setTimeout(r,30));}throw Error('Timed out');};
try{
 events.session_start();await wait(()=>messages.length===1);
 assert.equal(messages[0].options.deliverAs,'steer');assert.equal(messages[0].options.triggerTurn,true);
 assert.equal((await events.tool_call({toolName:'bash',input:{command:'git status'}})).block,true);
 assert.equal(await events.tool_call({toolName:'bash',input:{command:'"$MITOS_BIN" desk acknowledge notice_test'}}),undefined);
 assert.equal((await events.tool_call({toolName:'bash',input:{command:'"$MITOS_BIN" desk inbox; git reset --hard'}})).block,true);
 n.targets[0].acknowledged_at=new Date().toISOString();assert.equal((await events.tool_call({toolName:'write',input:{}})).block,true);
 n.released_at=new Date().toISOString();await wait(()=>messages.length===2&&receivedRelease);
 assert.match(messages[1].body.content,/RELEASED/);assert.equal(await events.tool_call({toolName:'bash',input:{command:'git status'}}),undefined);
 console.log('Pi attention: steering, acknowledgment exception, pause guard and release wakeup passed');
}finally{events.session_shutdown();server.closeAllConnections();await new Promise(r=>server.close(r));}

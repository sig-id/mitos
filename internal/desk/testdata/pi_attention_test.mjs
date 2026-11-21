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

// Retry-policy scenarios: transient failures retry and steer, retry-after is
// honored and clamped, non-transient failures surface, bursts exhaust into
// fallback models, and success reports cumulative usage.
import {parseHTTPCode,parseRetryAfterMs,decide,normalizePolicy} from '../pi_attention.mjs';
assert.equal(parseHTTPCode('429: {"code":"1308"}'),429);
assert.equal(parseHTTPCode('OpenAI API error (401): {"code":"invalid_api_key"}'),401);
assert.equal(parseHTTPCode('no code'),0);
assert.equal(parseHTTPCode('model lists 131072 tokens'),0);
assert.equal(parseRetryAfterMs('HTTP 429: retry-after: 30'),30000);
assert.equal(parseRetryAfterMs('HTTP 429'),0);
const decidedPolicy=normalizePolicy({backoff_seconds:1,backoff_multiplier:1,max_attempts:2,fallback_models:['zai/glm-5.3-flash'],transient_codes:[429,503]});
assert.equal(decide(decidedPolicy,{code:429,attemptsMade:1,retryAfterMs:0,fallbackIndex:0}).action,'retry');
assert.equal(decide(decidedPolicy,{code:429,attemptsMade:1,retryAfterMs:9000000,fallbackIndex:0}).delayMs,decidedPolicy.retry_after_cap_seconds*1000);
assert.equal(decide(decidedPolicy,{code:401,attemptsMade:1,retryAfterMs:0,fallbackIndex:0}).action,'surface');
assert.equal(decide(decidedPolicy,{code:0,attemptsMade:1,retryAfterMs:0,fallbackIndex:0}).action,'surface');
assert.equal(decide(decidedPolicy,{code:429,attemptsMade:2,retryAfterMs:0,fallbackIndex:0}).action,'switch');
assert.equal(decide(decidedPolicy,{code:429,attemptsMade:2,retryAfterMs:0,fallbackIndex:1}).action,'surface');

const retryEvents={},retryMessages=[];const reports=[];
let switchedTo=null;
const retryServer=createServer(async(req,res)=>{res.setHeader('Content-Type','application/json');
  if(req.method==='GET'){res.end('[]');return;}
  let body='';for await(const b of req)body+=b;
  if(req.url.endsWith('/agent-status'))reports.push(JSON.parse(body));
  res.end('{}');});
await new Promise(r=>retryServer.listen(0,'127.0.0.1',r));
process.env.MITOS_URL=`http://127.0.0.1:${retryServer.address().port}`;
process.env.MITOS_RETRY_POLICY=JSON.stringify({backoff_seconds:1,backoff_multiplier:1,max_attempts:2,fallback_models:['zai/glm-5.3-flash'],transient_codes:[429,503]});
const retryCtx={model:{provider:'zai',id:'glm-5.3'},modelRegistry:{find:(provider,id)=>({provider,id})},isIdle:()=>true};
extension({on:(name,fn)=>retryEvents[name]=fn,sendMessage:(body,options)=>retryMessages.push({body,options}),setModel:async model=>{switchedTo=`${model.provider}/${model.id}`;return true;}});
try{
  retryEvents.session_start();
  // Non-transient failure surfaces immediately with no retry.
  await retryEvents.message_end({message:{role:'assistant',stopReason:'error',errorMessage:'401: invalid key'}},retryCtx);
  await retryEvents.agent_settled({aborted:false},retryCtx);
  await wait(()=>reports.some(r=>r.state==='error'&&r.http_code===401));
  assert.equal(retryMessages.length,0);
  // A success clears the failure and reports cumulative usage.
  await retryEvents.message_end({message:{role:'assistant',stopReason:'stopReasonNone',usage:{input:100,output:20,cacheRead:5,cacheWrite:0}}},retryCtx);
  await wait(()=>reports.some(r=>r.state==='healthy'&&r.usage&&r.usage['zai/glm-5.3']?.input_tokens===100));
  // Transient failure: first retry is scheduled and steered, second exhausts
  // into the fallback model, and the fallback's next failure surfaces.
  await retryEvents.message_end({message:{role:'assistant',stopReason:'error',errorMessage:'429: usage limit'}},retryCtx);
  await retryEvents.agent_settled({aborted:false},retryCtx);
  await wait(()=>reports.some(r=>r.state==='retry_scheduled'&&r.attempt===2&&r.http_code===429));
  await wait(()=>retryMessages.length===1);
  assert.match(retryMessages[0].body.content,/Automatic retry 2 of 2/);assert.equal(retryMessages[0].options.triggerTurn,true);
  await retryEvents.message_end({message:{role:'assistant',stopReason:'error',errorMessage:'429: usage limit'}},retryCtx);
  await retryEvents.agent_settled({aborted:false},retryCtx);
  await wait(()=>reports.some(r=>r.state==='model_switched'));
  assert.equal(switchedTo,'zai/glm-5.3-flash');
  assert.equal(reports.find(r=>r.state==='model_switched').next_model,'zai/glm-5.3-flash');
  await wait(()=>retryMessages.length===2&&retryMessages.some(m=>/switched this session/.test(m.body.content)));
  // The fallback model gets a fresh burst: first failure retries, the second
  // exhausts the policy and surfaces without another steer.
  await retryEvents.message_end({message:{role:'assistant',stopReason:'error',errorMessage:'429: usage limit'}},retryCtx);
  await retryEvents.agent_settled({aborted:false},retryCtx);
  await wait(()=>retryMessages.length===3&&/Automatic retry 2 of 2/.test(retryMessages[2].body.content));
  await retryEvents.message_end({message:{role:'assistant',stopReason:'error',errorMessage:'429: usage limit'}},retryCtx);
  await retryEvents.agent_settled({aborted:false},retryCtx);
  await wait(()=>reports.some(r=>r.state==='retry_exhausted'));
  // Usage stays attributed per model: a success on the fallback adds a new
  // bucket and never relabels the original model's totals.
  await retryEvents.message_end({message:{role:'assistant',stopReason:'stopReasonNone',usage:{input:50,output:10}}},{...retryCtx,model:{provider:'zai',id:'glm-5.3-flash'}});
  await wait(()=>reports.some(r=>r.state==='healthy'&&r.usage&&r.usage['zai/glm-5.3']?.input_tokens===100&&r.usage['zai/glm-5.3-flash']?.input_tokens===50));
  // Operator aborts are never retried.
  await retryEvents.message_end({message:{role:'assistant',stopReason:'error',errorMessage:'503: unavailable'}},retryCtx);
  await retryEvents.agent_settled({aborted:true},retryCtx);
  await new Promise(r=>setTimeout(r,1100));
  assert.equal(retryMessages.filter(m=>/Automatic retry|switched this session/.test(m.body.content)).length,3);
  // Fallback ids containing slashes keep the full model id after the
  // provider: openrouter/anthropic/claude-sonnet resolves as
  // provider openrouter, model anthropic/claude-sonnet.
  const slashEvents={},slashFound=[];const slashServer=createServer(async(req,res)=>{res.setHeader('Content-Type','application/json');if(req.method==='GET'){res.end('[]');return;}res.end('{}');});
  await new Promise(r=>slashServer.listen(0,'127.0.0.1',r));
  process.env.MITOS_URL=`http://127.0.0.1:${slashServer.address().port}`;
  process.env.MITOS_RETRY_POLICY=JSON.stringify({backoff_seconds:1,backoff_multiplier:1,max_attempts:1,fallback_models:['openrouter/anthropic/claude-sonnet'],transient_codes:[429]});
  const slashCtx={model:{provider:'zai',id:'glm-5.3'},modelRegistry:{find:(provider,id)=>{slashFound.push(provider+'|'+id);return {provider,id};}},isIdle:()=>true};
  extension({on:(name,fn)=>slashEvents[name]=fn,sendMessage:()=>{},setModel:async()=>true});
  try{
    slashEvents.session_start();
    await slashEvents.message_end({message:{role:'assistant',stopReason:'error',errorMessage:'429: limit'}},slashCtx);
    await slashEvents.agent_settled({aborted:false},slashCtx);
    await wait(()=>slashFound.length===1);
    assert.equal(slashFound[0],'openrouter|anthropic/claude-sonnet');
  }finally{slashEvents.session_shutdown();slashServer.closeAllConnections();await new Promise(r=>slashServer.close(r));}
  console.log('Pi attention: retry policy, retry-after clamp, fallback switch, per-model usage and slashed fallback ids passed');
}finally{retryEvents.session_shutdown();retryServer.closeAllConnections();await new Promise(r=>retryServer.close(r));}

// Per-invocation Mitos extension. Never writes the operator's Pi settings.
export default function(pi) {
  const base=process.env.MITOS_URL, session=process.env.MITOS_SESSION_ID;
  if(!base||!session)return;
  let timer, stopped=false, busy=false, notices=[], seen=new Set();
  const request=async(path,body)=>{
    const r=await fetch(base+path,{method:body===undefined?'GET':'POST',headers:{'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(4000)});
    if(!r.ok)throw new Error('Mitos attention unavailable'); return r.json();
  };
  async function refresh() {
    if(busy||stopped)return;busy=true;
    try {
      notices=await request(`/api/sessions/${session}/inbox`);
      for(const n of notices){
        const target=n.targets.find(t=>t.session_id===session);
        const key=n.id+(n.released_at?':release':'');
        if(seen.has(key)||(!n.released_at&&target?.acknowledged_at))continue;
        const text=`Mitos attention ${n.id} from ${n.author}:\n${n.body}\n${n.released_at?'RELEASED: recheck other breakpoints, then continue.':n.pause?'BREAKPOINT: stop starting new work and acknowledge when safe. Wait for release.':'Read and acknowledge.'}\nUse "$MITOS_BIN" desk acknowledge ${n.id}. Respect the saved project rules.`;
        pi.sendMessage({customType:'mitos-attention',content:text,display:true},{deliverAs:'steer',triggerTurn:true});
        seen.add(key);
        await request(`/api/sessions/${session}/inbox/${n.id}/delivered`,{release:!!n.released_at});
      }
    } finally {busy=false;}
  }
  pi.on('session_start',()=>{stopped=false;seen.clear();clearInterval(timer);timer=setInterval(()=>refresh().catch(()=>{}),1000);refresh().catch(()=>{});});
  pi.on('session_shutdown',()=>{stopped=true;clearInterval(timer);});
  pi.on('tool_call',async(event)=>{
    // Check the durable inbox afresh at the boundary, independent of push timing.
    try {notices=await request(`/api/sessions/${session}/inbox`);}catch {return {block:true,reason:'Mitos attention is unavailable. Wait for the local server before starting another tool.'};}
    const held=notices.filter(n=>n.pause&&!n.released_at);
    if(!held.length)return;
    const command=event.input?.command||'';
    const control=/^(?:"\$MITOS_BIN"|mitos) desk (?:inbox(?: --wait)?|acknowledge [A-Za-z0-9_-]+|release-notice [A-Za-z0-9_-]+)$/.test(command);
    if(event.toolName==='bash'&&control)return;
    return {block:true,reason:`Mitos breakpoint ${held.map(n=>n.id).join(', ')}: ${held.map(n=>n.body).join('\n')}. Acknowledge with "$MITOS_BIN" desk acknowledge ID, then wait for release. Already-running work is not cancelled.`};
  });
}

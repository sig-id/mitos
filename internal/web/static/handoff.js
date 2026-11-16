(() => {
 'use strict';
 const el=(tag,text)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;return n};
 window.MitosHandoff={render(holder,run,request,accepted){
  if(run.project_id){
   const requests=el('details'),title=el('summary','Cooperation requests'),rows=el('div'),load=el('button','Load requests');load.type='button';requests.append(title,rows,load);holder.append(requests);let cursor=0;
   const fetchRequests=async()=>{if(cursor===0)rows.replaceChildren();load.disabled=true;try{const page=await request(`/api/projects/${encodeURIComponent(run.project_id)}/cooperation?run_id=${encodeURIComponent(run.id)}&cursor=${cursor}&limit=8`);for(const rec of page.messages){const m=rec.message;rows.append(el('p',`${m.kind}: ${rec.state} · ${m.body}`));if(m.permitted_resolvers?.length)rows.append(el('small','Resolvers: '+m.permitted_resolvers.map(r=>r.run_id).join(', ')));for(const b of m.blocked_actions||[])rows.append(el('p',`Waiting: ${b.action} of ${b.run_id} at revision ${b.revision}`));if(m.expires_at)rows.append(el('p','Expires: '+m.expires_at))}cursor=page.next_cursor;load.textContent=cursor===null?'Refresh states':'More requests';if(cursor===null)cursor=0;load.disabled=false}catch(e){rows.append(el('p',e.message));load.disabled=false}};load.onclick=fetchRequests;
  }
  if(!run.result)return;
  const result=run.result,box=el('section');box.append(el('h4','Result and evidence'),el('p',result.submission.summary));
  box.append(el('p',`Result ${result.id} · attempt ${result.run_id} · revision ${result.revision} · journal ${result.seq}`));
  box.append(el('p',`${accepted?'Accepted result':run.status==='done'?'Review candidate':'Saved attempt evidence'} · ${run.handoff_bytes||0} retrieval bytes used by this agent. Submission does not imply acceptance.`));
  for(const check of result.submission.checks||[])box.append(el('p',`${check.name}: ${check.outcome} (reported by agent)`));
  for(const limitation of result.submission.limitations||[])box.append(el('p',limitation));
  const list=el('div'),more=el('button','Browse evidence');more.type='button';box.append(list,more);holder.append(box);
  const base=`/api/projects/${encodeURIComponent(run.project_id)}/runs/${encodeURIComponent(run.id)}/evidence/`;
  const call=(name,input)=>request(base+name,{run_id:run.id,...input});
  let cursor=0;
  more.onclick=async()=>{
   more.disabled=true;
   try{const page=await call('artifact_list',{cursor,limit:16});
    for(const a of page.artifacts){
     const row=el('details'),title=el('summary',`${a.name} · ${a.role} · ${a.bytes} bytes · ${a.availability}`),preview=el('pre'),read=el('button','Read first section');read.type='button';row.append(title,el('small',a.id),preview,read);list.append(row);
     let offset=0;const decoder=new TextDecoder();
     read.onclick=async()=>{read.disabled=true;try{const range=await call('artifact_read',{artifact_id:a.id,offset,limit:32768});const bytes=Uint8Array.from(atob(range.data),c=>c.charCodeAt(0));preview.textContent=decoder.decode(bytes,{stream:!range.complete});offset=range.next_offset;read.textContent=range.complete?'End of file':`Read next section (byte ${offset})`;read.disabled=range.complete;title.textContent=`${a.name} · showing bytes ${range.offset}–${range.offset+bytes.length} of ${a.bytes}`;}catch(e){preview.textContent=e.message;read.disabled=false}};
    }
    cursor=page.next_cursor;more.textContent=cursor===null?'All evidence listed':'More evidence';more.disabled=cursor===null;
   }catch(e){list.append(el('p',e.message));more.disabled=false}
  };
 }};
})();

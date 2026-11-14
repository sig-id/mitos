(() => {
 'use strict';
 const $=id=>document.getElementById(id);
 if(!$('overview-view'))return;
 const project=document.body.dataset.project,server=document.body.dataset.server;
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const states={open:'Open',closed:'Closed',archived:'Archived'};
 const labels={idle:'Not started',queued:'Queued',starting:'Starting',running:'Running',stopping:'Stopping',accepted:'Complete',cancelled:'Stopped',failed:'Failed',needs_attention:'Needs attention',needs_inspection:'Needs inspection',needs_executor:'Needs executor',reported_complete:'Review ready',review:'Review ready',blocked:'Waiting',waiting_input:'Waiting for you',reviewing_approval:'Reviewing automatically',resuming:'Applying answer'};
 let state=new URLSearchParams(location.search).get('work_state')||'open',offset=0,sequence=0,alive=true,timer,signature='';
 if(!states[state])state='open';
 const search=$('project-work-search');search.value=new URLSearchParams(location.search).get('q')||'';
 function updateURL(){const url=new URL(location.href);url.searchParams.set('work_state',state);if(search.value.trim())url.searchParams.set('q',search.value.trim());else url.searchParams.delete('q');history.replaceState(null,'',url)}
 function button(text,fn){const b=el('button',text);b.type='button';b.onclick=fn;return b}
 function render(data){
  const stats=$('project-statistics');stats.replaceChildren();
  for(const [label,value,detail] of [['Open work',data.stats.open,'Ready, running, or awaiting review'],['Underway',data.stats.underway,'Work with active agents'],['Active agents',data.stats.active_agents,'Running or waiting for input'],['Cost',data.stats.cost_usd===null?'Not available':new Intl.NumberFormat(undefined,{style:'currency',currency:'USD'}).format(data.stats.cost_usd),'Billing totals are not reported']]){
   const card=el('article',undefined,'project-stat');card.append(el('span',label,'muted'),el('strong',String(value)),el('small',detail));stats.append(card);
  }
  for(const b of document.querySelectorAll('[data-work-state]')){b.textContent=states[b.dataset.workState]+' '+data.stats[b.dataset.workState];if(b.dataset.workState===state)b.setAttribute('aria-current','page');else b.removeAttribute('aria-current')}
  const list=$('project-work-list');list.replaceChildren();
  if(!data.work.length){const empty=el('div',undefined,'empty');empty.append(el('h2',search.value.trim()?'No matching work':'No '+states[state].toLowerCase()+' work'),el('p',search.value.trim()?'Try a different title or status.':state==='open'?'Start coordinated work or an agent session to get going.':'Work will appear here when it is '+(state==='closed'?'closed.':'archived.')));list.append(empty)}
  for(const work of data.work){
   const a=el('a',undefined,'project-work-row'),url=new URL('/projects/'+encodeURIComponent(project),location.origin);url.searchParams.set(work.kind,work.id);a.href=url.pathname+url.search;
   const copy=el('span',undefined,'project-work-copy');copy.append(el('strong',work.title||'Untitled work'),el('small',(work.kind==='graph'?'Coordinated work':'Native assignment')+' · Created '+new Date(work.created_at).toLocaleDateString()+ (work.agents?' · '+work.agents+' agent'+(work.agents===1?'':'s'):'')));
   a.append(copy,el('span',labels[work.status]||work.status,'graph-badge '+work.status));list.append(a);
  }
  const pagination=$('project-work-pagination');pagination.replaceChildren();
  if(data.total){pagination.append(el('span',`${data.offset+1}–${data.next_offset} of ${data.total}`,'muted'));if(data.offset>0)pagination.append(button('Previous',()=>{offset=Math.max(0,data.offset-50);refresh()}));if(data.next_offset<data.total)pagination.append(button('Next',()=>{offset=data.next_offset;refresh()}))}
 }
 async function refresh(){
  const expected=++sequence,control=new AbortController(),timeout=setTimeout(()=>control.abort(),12000);
  try{
   const query=new URLSearchParams({state,offset:String(offset),q:search.value.trim()});
   const response=await fetch('/api/projects/'+encodeURIComponent(project)+'/overview?'+query,{cache:'no-store',signal:control.signal});const data=await response.json();
   if(expected!==sequence||!alive)return;
   if(!response.ok)throw Error(data.error||'Could not load project overview.');
   if(data.server.id!==server)throw Error('Home server identity changed. Reconnect to continue.');
   const next=JSON.stringify([state,data.stats,data.work,data.total,data.offset]);if(next!==signature){signature=next;render(data)}
   $('project-overview-sync').textContent='Updated '+new Date(data.observed_at).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
  }catch(error){if(expected===sequence)$('project-overview-sync').textContent='Could not refresh. Displayed statistics may be out of date.'}finally{clearTimeout(timeout)}
 }
 document.querySelectorAll('[data-work-state]').forEach(b=>b.onclick=()=>{state=b.dataset.workState;offset=0;updateURL();refresh()});
 search.oninput=()=>{offset=0;sequence++;clearTimeout(timer);updateURL();timer=setTimeout(refresh,180)};
 document.addEventListener('mitos-view',e=>{if(e.detail==='overview')refresh()});
 window.addEventListener('pagehide',()=>{alive=false;sequence++});
 async function poll(){if(!alive)return;if(!document.hidden&&!$('overview-view').hidden)await refresh();if(alive)setTimeout(poll,5000)}
 poll();
})();

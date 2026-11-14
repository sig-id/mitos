(() => {
 'use strict';
 const toggle=document.getElementById('notifications-toggle');if(!toggle)return;
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const button=(text,fn)=>{const n=el('button',text);n.type='button';n.onclick=fn;return n};
 const link=(text,href)=>{const n=el('a',text);n.href=href;return n};
 const drawer=el('dialog',undefined,'notification-dialog');drawer.setAttribute('aria-label','Notifications');
 const heading=el('header'),list=el('div'),error=el('p');error.setAttribute('role','status');heading.append(el('h2','Notifications'),button('Close',()=>drawer.close()));drawer.append(heading,error,list);document.body.append(drawer);
 const banner=el('aside',undefined,'attention-banner');banner.setAttribute('aria-label','Work needs attention');banner.hidden=true;document.body.append(banner);
 const live=el('div',undefined,'notification-live');live.setAttribute('role','status');live.setAttribute('aria-live','polite');document.body.append(live);
 let actions=[],reviews=[],projects=[],signature='',server='',busy=false,alive=true,seen=new Set(),dismissed=new Set(),bannerKeys=[],storageKey='';
 async function api(path,body){const response=await fetch(path,{method:body===undefined?'GET':'POST',cache:'no-store',headers:{'Content-Type':'application/json','X-Mitos-Server':server},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(15000)});const value=await response.json();if(!response.ok)throw Error(value.error||'Request failed');return value}
 const reviewURL=d=>d.graph_id?`/projects/${d.project_id}?graph=${d.graph_id}`:d.task_id?`/projects/${d.project_id}?task=${d.task_id}&tab=result`:'/runs/'+d.run_id;
 const workURL=a=>a.graph_id?`/projects/${a.project_id}?graph=${a.graph_id}&node=${a.task_id}&view=workflow`:'/runs/'+a.card.run_id;
 const name=a=>projects.find(p=>p.project.id===a.project_id)?.project.name||'Workspace';
 function dismissBanner(){for(const key of bannerKeys)dismissed.add(key);banner.hidden=true;try{localStorage.setItem(storageKey+'.dismissed',JSON.stringify([...dismissed]))}catch{}}
 function open(id){dismissBanner();draw(id);if(!drawer.open)drawer.showModal();if(id)document.getElementById('notice-'+id)?.scrollIntoView({block:'nearest'})}
 toggle.onclick=()=>open();
 function draw(focus){
  const drafts=new Map([...list.querySelectorAll('textarea[data-answer]')].map(n=>[n.dataset.answer,{value:n.value,focused:n===document.activeElement,start:n.selectionStart,end:n.selectionEnd}]));const expanded=new Set([...list.querySelectorAll('.notification-card details[open]')].map(n=>n.closest('article').id));
  list.replaceChildren();error.textContent='';
  const enable=button('Enable desktop notifications',async()=>{if(!('Notification' in window)){error.textContent='Desktop notifications are unavailable in this browser.';return}try{const permission=await Notification.requestPermission();error.textContent=permission==='granted'?'Desktop notifications enabled while Mitos is open.':'Desktop notifications are disabled. In-app notifications remain available.'}catch(e){error.textContent=e.message}});list.append(enable);
  if(!actions.length&&!reviews.length)list.append(el('p','Nothing needs your attention.'));
  for(const a of actions){
   const card=el('article',undefined,'notification-card');card.id='notice-'+a.id;
   card.append(el('small',name(a)),el('h3',a.title),el('strong',a.setup_required?'Configure Decisions API':a.resolver==='human'?(a.blocking?'Waiting for you':'Your input requested · work can continue'):'Reviewing automatically'),el('p',a.card.question),el('p',a.reason==='automatic resolution is disabled'?'Approval handling is set to Ask me.':a.reason,'muted'));
   const raw=a.card.context?.attempted||'';let description=raw;
   try{const payload=JSON.parse(raw.slice(raw.indexOf('{')));description=[payload.params?.reason,payload.params?.command].filter(Boolean).join('\n')||raw}catch{}
   card.append(el('pre',description,'notification-command'));
   const details=el('details');details.append(el('summary','Request details'),el('pre',raw,'notification-command'));details.open=expanded.has(card.id);card.append(details);
   let value;if(a.input_profile==='text'){const label=el('label','Your answer');value=el('textarea');value.dataset.answer=a.id;value.maxLength=a.card.value_schema?.maxLength||1024;value.value=drafts.get(a.id)?.value||'';label.append(value);card.append(label)}
   const controls=el('div',undefined,'notification-options');
   if(a.resolver==='human'&&(a.card.kind!=='supply_value'||value)){for(const option of a.card.options||[]){const b=button(option.label,async()=>{if(busy)return;busy=true;controls.querySelectorAll('button').forEach(n=>n.disabled=true);error.textContent='';try{await api(`/api/runs/${a.card.run_id}/decisions/${a.id}`,{choice:option.id,...(value?{value:value.value}:{})});signature='';busy=false;await refresh()}catch(e){const message=e.message+' The request was refreshed; check its status before answering again.';signature='';busy=false;await refresh();error.textContent=message}});b.title=option.consequence;controls.append(b,el('small',option.consequence))}}
   controls.append(link('Open work',workURL(a)));if(a.project_id)controls.append(link(a.setup_required?'Configure Decisions API':'Approval settings',a.setup_required?'/settings':`/projects/${a.project_id}?view=resources#decisions-api`));card.append(controls);list.append(card);const draft=drafts.get(a.id);if(value&&draft?.focused){value.focus();value.setSelectionRange(draft.start,draft.end)}
  }
  for(const d of reviews){const card=el('article',undefined,'notification-card');card.append(el('small',name(d)),el('h3',d.title),el('p',d.question),link('Review result',reviewURL(d)));list.append(card)}
 }
 async function refresh(){
  try{
   const home=await api('/api/workspace-home');server=home.server.id;projects=home.projects;
   if(!storageKey){storageKey='mitos.notifications.'+server;try{seen=new Set(JSON.parse(localStorage.getItem(storageKey)||'[]'))}catch{}try{dismissed=new Set(JSON.parse(localStorage.getItem(storageKey+'.dismissed')||'[]'))}catch{}}
   const next=JSON.stringify([home.attention,home.decisions]);if(next===signature)return;signature=next;actions=home.attention||[];reviews=(home.decisions||[]).filter(d=>d.needs_review!==false);
   const notices=[...actions.filter(a=>a.resolver==='human').map(a=>({id:a.id,title:a.title,body:a.card.question})),...reviews.map(d=>({id:d.graph_id||d.task_id||d.run_id,title:d.title,body:d.question}))];
   for(const n of notices){if(seen.has(n.id))continue;seen.add(n.id);live.textContent=n.title+': '+n.body;if('Notification' in window&&Notification.permission==='granted'&&document.visibilityState!=='visible'){const notification=new Notification(n.title,{body:n.body,tag:n.id});notification.onclick=()=>{window.focus();open(n.id);notification.close()}}}
   try{localStorage.setItem(storageKey,JSON.stringify([...seen].slice(-500)))}catch{}
   document.getElementById('notifications-count').textContent=notices.length||'';
   const reviewKey=d=>'review:'+(d.graph_id||d.task_id||d.run_id)+':'+d.question;
   bannerKeys=[...actions.filter(a=>a.resolver==='human').map(a=>'request:'+a.id),...reviews.map(reviewKey)];
   const activeKeys=new Set(bannerKeys);dismissed=new Set([...dismissed].filter(key=>activeKeys.has(key)));try{localStorage.setItem(storageKey+'.dismissed',JSON.stringify([...dismissed]))}catch{}
   const relevant=actions.filter(a=>a.resolver==='human'&&!dismissed.has('request:'+a.id)).sort((a,b)=>Number(b.project_id===document.body.dataset.project)-Number(a.project_id===document.body.dataset.project));
   const relevantReviews=reviews.filter(d=>!dismissed.has(reviewKey(d))).sort((a,b)=>Number(b.project_id===document.body.dataset.project)-Number(a.project_id===document.body.dataset.project));
   banner.hidden=!relevant.length&&!relevantReviews.length;banner.replaceChildren();if(relevant.length)banner.append(el('strong',relevant[0].setup_required?'Configure Decisions API':relevant.some(a=>a.blocking)?'Waiting for you':'Your input requested'),el('span',relevant.length===1?relevant[0].title:`${relevant.length} requests need your answer`),button('Review request',()=>open(relevant[0].id)));else if(relevantReviews.length){const review=link('Review result',reviewURL(relevantReviews[0]));review.addEventListener('click',dismissBanner);banner.append(el('strong','Review ready'),el('span',relevantReviews[0].title),review)}
   if(!banner.hidden){const close=button('×',()=>{dismissBanner();toggle.focus({preventScroll:true})});close.className='attention-dismiss';close.setAttribute('aria-label','Dismiss notification');close.title='Dismiss notification';banner.append(close)}
   if(drawer.open&&!busy)draw();document.dispatchEvent(new CustomEvent('mitos-attention',{detail:actions}));
  }catch(e){if(drawer.open)error.textContent='Notifications could not refresh. '+e.message}
 }
 window.MitosNotifications={open,forRun:id=>actions.filter(a=>a.owner_run_id===id)};
 document.addEventListener('mitos-decision-resolved',()=>{signature='';refresh()});
 window.addEventListener('pagehide',()=>{alive=false});
 async function poll(){if(!alive)return;await refresh();if(alive)setTimeout(poll,3000)}poll();
})();

(() => {
 'use strict';
 const $=id=>document.getElementById(id);
 if(!$('overview-view'))return;
 const project=document.body.dataset.project,server=document.body.dataset.server;
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const states={open:'Open',closed:'Closed',archived:'Archived'};
 const labels={idle:'Not started',queued:'Queued',starting:'Starting',running:'Running',stopping:'Stopping',accepted:'Complete',cancelled:'Stopped',failed:'Failed',needs_attention:'Needs attention',needs_inspection:'Needs inspection',needs_executor:'Needs executor',reported_complete:'Review ready',review:'Review ready',blocked:'Waiting',waiting_input:'Waiting for you',reviewing_approval:'Reviewing automatically',resuming:'Applying answer'};
 const params = new URLSearchParams(location.search);
 const search = $('project-work-search'), kind = $('project-work-kind'), sort = $('project-work-sort');
 const filters = document.querySelectorAll('[data-work-state]');
 let state = Object.hasOwn(states, params.get('work_state')) ? params.get('work_state') : 'open';
 let offset = 0, sequence = 0, alive = true, timer, signature = '';
 search.value = params.get('q') || '';
 kind.value = params.get('kind') || '';
 sort.value = params.get('sort') || 'newest';
 if (!sort.value) sort.value = 'newest';
 if (kind.selectedIndex < 0) kind.value = '';

 function updateURL() {
  const url = new URL(location.href);
  for (const [key, value] of Object.entries({work_state:state, q:search.value.trim(), kind:kind.value, sort:sort.value === 'newest' ? '' : sort.value})) {
   if (value) url.searchParams.set(key, value);
   else url.searchParams.delete(key);
  }
  history.replaceState(null, '', url);
 }
 function button(text,fn){const b=el('button',text);b.type='button';b.onclick=fn;return b}

 function workRow(work) {
  const link = el('a', undefined, 'project-work-row');
  const url = new URL('/projects/'+encodeURIComponent(project), location.origin);
  url.searchParams.set(work.kind, work.id);
  link.href = url.pathname + url.search;
  link.dataset.state = work.state;

  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('class', 'project-work-icon');
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS(svg.namespaceURI, 'use');
  use.setAttribute('href', '#work-icon-' + work.kind);
  svg.append(use);

  const id = el('code', '#' + work.id.replace(/^[^_]+_/, '').slice(0, 7));
  id.title = work.id;
  const date = new Date(sort.value === 'updated' ? work.updated_at : work.created_at);
  const time = el('time', (sort.value === 'updated' ? 'Updated ' : 'Opened ') + date.toLocaleDateString(undefined, {month:'short', day:'numeric', year:'numeric'}));
  time.dateTime = date.toISOString();
  time.title = date.toLocaleString();
  const meta = el('span', undefined, 'project-work-meta');
  meta.append(id, el('span', work.kind === 'graph' ? 'Coordinated work' : 'Native assignment'), time);
  if (work.agents) meta.append(el('span', work.agents + (work.agents === 1 ? ' agent' : ' agents')));
  const copy = el('span', undefined, 'project-work-copy');
  copy.append(el('strong', work.title || 'Untitled work'), meta);
  link.append(svg, copy, el('span', labels[work.status] || work.status, 'project-work-status ' + work.status));
  return link;
 }

 function render(data) {
  const cost = data.stats.cost_usd === null ? 'Not available' : new Intl.NumberFormat(undefined, {style:'currency', currency:'USD'}).format(data.stats.cost_usd);
  $('project-statistics').replaceChildren(el('span', data.stats.underway + ' underway'), el('span', data.stats.active_agents + (data.stats.active_agents === 1 ? ' active agent' : ' active agents')), el('span', 'Cost · ' + cost));
  for (const filter of filters) {
   filter.querySelector('span').textContent = data.stats[filter.dataset.workState];
   if (filter.dataset.workState === state) filter.setAttribute('aria-current', 'page');
   else filter.removeAttribute('aria-current');
  }
  const list = $('project-work-list');
  list.replaceChildren(...data.work.map(workRow));
  if (!data.work.length) {
   const filtered = search.value.trim() || kind.value;
   const empty = el('div', undefined, 'empty');
   empty.append(el('h2', filtered ? 'No matching work' : 'No ' + state + ' work'));
   if (filtered) empty.append(el('p', 'Try another work type, title, or ID.'));
   else if (state === 'open') empty.append(el('p', 'Start coordinated work or an agent session to get going.'));
   list.append(empty);
  }
  const pagination=$('project-work-pagination');pagination.replaceChildren();
  if(data.offset || data.next_offset < data.total){pagination.append(el('span',`${data.offset+1}–${data.next_offset} of ${data.total}`,'muted'));if(data.offset>0)pagination.append(button('Previous',()=>{offset=Math.max(0,data.offset-50);refresh()}));if(data.next_offset<data.total)pagination.append(button('Next',()=>{offset=data.next_offset;refresh()}))}
 }
 async function refresh(){
  const expected=++sequence,control=new AbortController(),timeout=setTimeout(()=>control.abort(),12000);
  try{
   const query=new URLSearchParams({state,kind:kind.value,sort:sort.value,offset:String(offset),q:search.value.trim()});
   const response=await fetch('/api/projects/'+encodeURIComponent(project)+'/overview?'+query,{cache:'no-store',signal:control.signal});const data=await response.json();
   if(expected!==sequence||!alive)return;
   if(!response.ok)throw Error(data.error||'Could not load project overview.');
   if(data.server.id!==server)throw Error('Home server identity changed. Reconnect to continue.');
   if (offset && offset >= data.total) {
    offset = Math.floor(Math.max(0, data.total - 1) / 50) * 50;
    return refresh();
   }
   const next=JSON.stringify([query.toString(),data.stats,data.work,data.total,data.offset]);if(next!==signature){signature=next;render(data)}
   $('project-overview-sync').textContent='Updated '+new Date(data.observed_at).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
  }catch(error){if(expected===sequence)$('project-overview-sync').textContent='Could not refresh. Displayed work may be out of date.'}finally{clearTimeout(timeout)}
 }
 function changeFilters() {
  offset = 0;
  clearTimeout(timer);
  updateURL();
  refresh();
 }
 for (const filter of filters) filter.onclick = () => { state = filter.dataset.workState; changeFilters() };
 kind.onchange = sort.onchange = changeFilters;
 search.oninput=()=>{offset=0;sequence++;clearTimeout(timer);updateURL();timer=setTimeout(refresh,180)};
 document.addEventListener('mitos-view',e=>{if(e.detail==='overview')refresh()});
 window.addEventListener('pagehide',()=>{alive=false;sequence++;clearTimeout(timer);clearTimeout(pollTimer)});
 window.addEventListener('pageshow',event=>{if(event.persisted){alive=true;poll()}});
 let pollTimer;
 async function poll(){if(!alive)return;if(!document.hidden&&!$('overview-view').hidden)await refresh();if(alive)pollTimer=setTimeout(poll,5000)}
 poll();
})();

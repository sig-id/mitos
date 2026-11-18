(() => {
 'use strict';
 const $=id=>document.getElementById(id);
 if(!$('overview-view'))return;
 const project=document.body.dataset.project,server=document.body.dataset.server;
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const states={open:'Open',closed:'Closed',archived:'Archived'};
 const labels={idle:'Not started',queued:'Queued',starting:'Starting',running:'Running',stopping:'Stopping',accepted:'Complete',cancelled:'Stopped',failed:'Failed',needs_attention:'Needs attention',needs_inspection:'Needs inspection',needs_executor:'Needs executor',reported_complete:'Review ready',review:'Review ready',blocked:'Waiting',waiting_input:'Waiting for you',reviewing_approval:'Reviewing automatically',resuming:'Applying answer'};
 const list = $('project-work-list'), selectPage = $('work-select-page'), bulk = $('work-bulk');
 const selected = new Set();
 let visible = [], busy = false;
 const currency = new Intl.NumberFormat(undefined, {style:'currency',currency:'USD'});
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
  const row = el('div', undefined, 'project-work-row');
  const check = el('input', undefined, 'work-check');
  check.type = 'checkbox';
  check.value = work.id;
  check.setAttribute('aria-label', 'Select ' + (work.title || 'untitled work'));
  check.onchange = () => {
   if (check.checked) selected.add(work.id);
   else selected.delete(work.id);
   updateSelection();
  };
  const link = el('a', undefined, 'project-work-link');
  const url = new URL('/projects/'+encodeURIComponent(project), location.origin);
  url.searchParams.set(work.kind, work.id);
  link.href = url.pathname + url.search;
  row.dataset.state = work.state;

  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('class', 'project-work-icon');
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS(svg.namespaceURI, 'use');
  use.setAttribute('href', '#work-icon-' + work.kind);
  svg.append(use);

  const checkout = el('code', work.branch || work.workspace.split('/').filter(Boolean).pop() || work.workspace);
  checkout.title = [work.branch, work.workspace].filter(Boolean).join(' · ');
  const date = new Date(sort.value === 'updated' ? work.updated_at : work.created_at);
  const time = el('time', (sort.value === 'updated' ? 'Updated ' : 'Opened ') + date.toLocaleDateString(undefined, {month:'short', day:'numeric', year:'numeric'}));
  time.dateTime = date.toISOString();
  time.title = date.toLocaleString();
  const meta = el('span', undefined, 'project-work-meta');
  meta.append(checkout, el('span', work.kind === 'graph' ? 'Coordinated work' : 'Native assignment'), time);
  if (work.agents) meta.append(el('span', work.agents + (work.agents === 1 ? ' agent' : ' agents')));
  const copy = el('span', undefined, 'project-work-copy');
  const heading = el('span', undefined, 'project-work-title');
  heading.append(el('strong', work.title || 'Untitled work'));
  for (const name of work.labels || []) {
   const chip = el('span', name, 'work-label-chip');
   chip.dataset.color = Array.from(name).reduce((hash, char) => (hash * 31 + char.codePointAt(0)) >>> 0, 0) % 5;
   heading.append(chip);
  }
  const duration = el('span', 'Attempt time · ' + (work.attempt_ms == null ? 'Unreported' : formatDuration(work.attempt_ms)));
  duration.title = 'Total time across attempts or sessions, including overlapping work';
  const cost = el('span', 'Cost · ' + formatCost(work.cost_usd, work.unpriced_runs, 'Unreported'));
  cost.title = 'Estimated model spend from journaled harness token usage at published list prices' + (work.unpriced_runs ? '; a lower bound while ' + work.unpriced_runs + (work.unpriced_runs === 1 ? ' attempt lacks' : ' attempts lack') + ' a matching price' : '');
  meta.append(duration, cost);
  copy.append(heading, meta);
  link.append(svg, copy, el('span', labels[work.status] || work.status, 'project-work-status ' + work.status));
  row.append(check, link);
  return row;
 }

 function formatDuration(ms) {
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return seconds + 's';
  const minutes = Math.floor(seconds / 60);
  return minutes < 60 ? minutes + 'm ' + seconds % 60 + 's' : Math.floor(minutes / 60) + 'h ' + minutes % 60 + 'm';
 }

 function updateSelection() {
  const items = visible.filter(work => selected.has(work.id));
  for (const check of list.querySelectorAll('.work-check')) {
   check.checked = selected.has(check.value);
   check.disabled = busy;
  }
  selectPage.disabled = busy || !visible.length;
  selectPage.checked = !!items.length && items.length === visible.length;
  selectPage.indeterminate = !!items.length && items.length < visible.length;
  bulk.hidden = !items.length;
  bulk.inert = busy;
  $('work-selected-count').textContent = items.length + ' selected';
  for (const btn of bulk.querySelectorAll('[data-bulk-state]')) {
   const target = btn.dataset.bulkState;
   btn.hidden = !items.length || items.some(work => work.state === target || (work.kind === 'task' && (target !== 'closed' || work.active_agents > 0)) || (work.state === 'archived' && target === 'open'));
   if (target === 'closed') btn.textContent = items[0]?.state === 'archived' ? 'Restore to closed' : 'Close';
  }
 }
 selectPage.onchange = () => {
  selected.clear();
  if (selectPage.checked) for (const item of visible) selected.add(item.id);
  updateSelection();
 };

 async function post(path, body) {
  const response = await fetch(path, {method:'POST', headers:{'Content-Type':'application/json','X-Mitos-Server':server}, body:JSON.stringify(body)});
  if (!response.ok) throw Error((await response.json()).error || 'Request failed');
 }
 async function applyBulk(action, label) {
  if (busy) return;
  const items = visible.filter(work => selected.has(work.id));
  if (!items.length) return;
  if (['closed','archived'].includes(action) && items.some(work => work.state === 'open') &&
      !window.confirm('Move ' + items.length + ' selected to ' + action + '? This stops unfinished work.')) return;
  const message = $('work-bulk-message');
  const errors = [];
  busy = true;
  updateSelection();
  try {
   if (action === 'add' || action === 'remove') {
    await post('/api/projects/'+encodeURIComponent(project)+'/overview/labels', {ids:items.map(item => item.id), action, label});
    $('work-label').value = '';
    message.textContent = (action === 'add' ? 'Added' : 'Removed') + ' label “' + label + '” for ' + items.length + ' selected.';
   } else {
    for (const item of items) {
     try {
      if (item.kind === 'task') await post('/api/projects/'+encodeURIComponent(project)+'/work/'+encodeURIComponent(item.id)+'/cancel', {revision:item.revision});
      else await post('/api/graphs/'+encodeURIComponent(item.id)+'/work-state', {revision:item.revision, key:crypto.randomUUID(), state:action});
      selected.delete(item.id);
     } catch (error) { errors.push(item.title + ' — ' + error.message) }
    }
    message.textContent = (items.length-errors.length) + ' of ' + items.length + ' moved to ' + action + '. ' + errors.join(' · ');
   }
  } catch (error) { message.textContent = error.message }
  finally { await refresh(); busy = false; updateSelection() }
 }
 $('work-label-form').onsubmit = event => {
  event.preventDefault();
  applyBulk(event.submitter?.value || 'add', $('work-label').value.trim());
 };
 for (const btn of bulk.querySelectorAll('[data-bulk-state]')) btn.onclick = () => applyBulk(btn.dataset.bulkState);

 function formatCost(value, unpriced, empty) {
  if (value == null) return empty;
  return (unpriced ? '≥ ' : '') + currency.format(value);
 }

 function render(data) {
  const cost = formatCost(data.stats.cost_usd, data.stats.unpriced_runs, 'Not available');
  $('project-statistics').replaceChildren(el('span', data.stats.underway + ' underway'), el('span', data.stats.active_agents + (data.stats.active_agents === 1 ? ' active agent' : ' active agents')), el('span', 'Cost · ' + cost));
  for (const filter of filters) {
   filter.querySelector('span').textContent = data.stats[filter.dataset.workState];
   if (filter.dataset.workState === state) filter.setAttribute('aria-current', 'page');
   else filter.removeAttribute('aria-current');
  }
  visible = data.work;
  for (const id of selected) if (!visible.some(work => work.id === id)) selected.delete(id);
  const focused = list.contains(document.activeElement) ? document.activeElement : null;
  list.replaceChildren(...data.work.map(workRow));
  if (focused?.matches('.work-check')) list.querySelector('input[value="'+CSS.escape(focused.value)+'"]')?.focus({preventScroll:true});
  if (focused?.matches('a')) list.querySelector('a[href="'+CSS.escape(focused.getAttribute('href'))+'"]')?.focus({preventScroll:true});
  updateSelection();
  $('work-known-labels').replaceChildren(...data.labels.map(name => el('option', name)));
  if (!data.work.length) {
   const filtered = search.value.trim() || kind.value;
   const empty = el('div', undefined, 'empty');
   empty.append(el('h2', filtered ? 'No matching work' : 'No ' + state + ' work'));
   if (filtered) empty.append(el('p', 'Try another title, branch, label, or work type.'));
   else if (state === 'open') empty.append(el('p', 'Start coordinated work or an agent session to get going.'));
   list.append(empty);
  }
  const pagination=$('project-work-pagination');pagination.replaceChildren();
  if(data.offset || data.next_offset < data.total){pagination.append(el('span',`${data.offset+1}–${data.next_offset} of ${data.total}`,'muted'));if(data.offset>0)pagination.append(button('Previous',()=>{clearSelection();offset=Math.max(0,data.offset-50);refresh()}));if(data.next_offset<data.total)pagination.append(button('Next',()=>{clearSelection();offset=data.next_offset;refresh()}))}
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
   const next=JSON.stringify([query.toString(),data.labels,data.stats,data.work,data.total,data.offset]);if(next!==signature){signature=next;render(data)}
   $('project-overview-sync').textContent='Updated '+new Date(data.observed_at).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
  }catch(error){if(expected===sequence)$('project-overview-sync').textContent='Could not refresh. Displayed work may be out of date.'}finally{clearTimeout(timeout)}
 }
 function clearSelection() {
  selected.clear();
  updateSelection();
  $('work-bulk-message').textContent = '';
 }
 function changeFilters() {
  clearSelection();
  offset = 0;
  clearTimeout(timer);
  updateURL();
  refresh();
 }
 for (const filter of filters) filter.onclick = () => { state = filter.dataset.workState; changeFilters() };
 kind.onchange = sort.onchange = changeFilters;
 search.oninput=()=>{clearSelection();offset=0;sequence++;clearTimeout(timer);updateURL();timer=setTimeout(refresh,180)};
 document.addEventListener('mitos-view',e=>{if(e.detail==='overview')refresh()});
 window.addEventListener('pagehide',()=>{alive=false;sequence++;clearTimeout(timer);clearTimeout(pollTimer)});
 window.addEventListener('pageshow',event=>{if(event.persisted){alive=true;poll()}});
 let pollTimer;
 async function poll(){if(!alive)return;if(!document.hidden&&!$('overview-view').hidden)await refresh();if(alive)pollTimer=setTimeout(poll,5000)}
 poll();
})();

(() => {
 'use strict';
 const $ = id => document.getElementById(id);
 if (!$('graph-work')) return;
 const project = document.body.dataset.project, server = document.body.dataset.server;
 const endpoint = id => '/api/graphs/' + encodeURIComponent(id);
 const node = (tag, text, cls) => { const n = document.createElement(tag); if (text !== undefined) n.textContent = text; if (cls) n.className = cls; return n; };
 const button = (text, fn, cls = '') => { const b = node('button', text, cls); b.type = 'button'; b.onclick = () => Promise.resolve().then(fn).catch(showError); return b; };
 const link = (text, href) => { const a = node('a', text, 'text-link'); a.href = href; return a; };
 const labels = { finished:'Finished', idle:'Not started', queued:'Queued', running:'Working', accepted:'Complete', review:'Review ready', blocked:'Waiting', needs_attention:'Needs attention', failed:'Failed', cancelled:'Stopped', stopping:'Stopping', planning:'Planning', superseded:'Replaced', done:'Review ready' };
 const badge = status => node('span', labels[status] || status, 'graph-badge ' + status);
 const date = value => new Date(value).toLocaleString();
 const storageKey = `mitos.graph.pending.${server}.${project}`;
 let optionsLoaded = false, apps = [], graphs = [], nextPage = '', graphID = new URLSearchParams(location.search).get('graph') || '';
 let showHistory=false;
 let selectedAttempt='', renderedReaderKey='', readerStates=new Map();
 let selected = new URLSearchParams(location.search).get('node') || '', snapshot, graph, selectedTab = 'details';
 let snapshotRequest=0,snapshotApplied=0,summarySignature='',loadedMore=false;
 let epoch = 0, alive = true, polling = false, tick = 0, failureCount = 0, snapshotSignature = '', detailSignature = '';
 let zoom = 1, dimensions = { width:700, height:400 }, listMode = false, firstLayout = true;
 let collapsed = new Set(), order = [], positions = new Map(), cachedRuns = new Map(), artifactCache = new Map();
 let pending = null, createBody = null, actionSnapshot, actionTarget, actionType, stepSnapshot;
 try { pending = JSON.parse(sessionStorage.getItem(storageKey) || 'null'); } catch { /* Storage is optional. */ }
 function rememberPending(value) { pending = value; try { if (value) sessionStorage.setItem(storageKey, JSON.stringify(value)); else sessionStorage.removeItem(storageKey); } catch {} }
 async function request(path, body) {
  const abort = new AbortController(), timeout = setTimeout(() => abort.abort(), 12000);
  try {
   const response = await fetch(path, { method:body === undefined ? 'GET' : 'POST', headers:{'Content-Type':'application/json','X-Mitos-Server':server}, body:body === undefined ? undefined : JSON.stringify(body), signal:abort.signal, cache:'no-store' });
   const data = await response.json();
   if (!response.ok) { const err = new Error(data.error || `Request failed (${response.status})`); err.status = response.status; throw err; }
   return data;
  } catch (err) { if (err.name === 'AbortError' || err instanceof TypeError) { const error = new Error(body === undefined ? 'Connection lost. Showing the last known graph.' : 'The response was lost. Retry this same request to check its result.'); error.uncertain = body !== undefined; throw error; } throw err; }
  finally { clearTimeout(timeout); }
 }
 function showError(error) {
  const box = $('action-error'); box.replaceChildren(node('span', error.message)); box.hidden = false;
  if (pending) box.append(button('Retry same request', retryPending));
 }
 async function sendMutation(path, body, kind = 'amendment') {
  if (pending) throw new Error('A previous request needs reconciliation. Use “Retry same request” above.');
  rememberPending({ path, body, kind });
  try { const result = await request(path, body); rememberPending(null); $('action-error').hidden = true; return result; }
  catch (error) { if (error.status && error.status < 500) rememberPending(null); showError(error); if (error.status === 409) { error.message = 'The graph changed while you were reviewing it. Reopen this action to review the latest state.'; await refreshSelected(); } throw error; }
 }
 async function retryPending() {
  if (!pending) return;
  const old = pending;
  try { const result = await request(old.path, old.body); rememberPending(null); $('action-error').hidden = true;
   document.querySelectorAll('#graph-step-dialog[open],#graph-action-dialog[open]').forEach(d => d.close());
   if (old.kind === 'create') { createBody = null; await loadSummaries(true); await chooseGraph(result.id); } else await refreshSelected();
  } catch (error) { if (error.status && error.status < 500) rememberPending(null); showError(error); }
 }
 function envelope(g, operations, reads) { return { profile:g.profile, key:crypto.randomUUID(), graph_id:g.id, revision:g.revision, policy_revision:g.policy_revision, generation:g.controller_generation, deadline:g.limits.deadline, operations, ...(reads ? {reads} : {}) }; }
 function allReads(g) { return Object.fromEntries(Object.entries(g.members).map(([id,m]) => [id, {specification:m.revision, fact:m.fact_revision}])); }
 async function amend(g, operations, reads) { const result = await sendMutation(endpoint(g.id) + '/amendments', envelope(g,operations,reads)); if (graphID === g.id) await refreshSelected(); return result; }
 function title(m) { return m.spec.brief.title || (m.spec.kind === 'scope' ? 'Goal' : 'Untitled step'); }
 function kind(m) { return m.spec.kind === 'scope' ? 'scope' : m.controller_for?.startsWith('decision:') ? 'decision' : m.controller_for ? 'planner' : 'step'; }
 function kindLabel(m) { return {scope:'Goal', planner:'Planner', decision:'Decision', step:'Agent'}[kind(m)]; }
 function plannerRounds(){return Object.keys(graph.members).filter(id=>kind(graph.members[id])==='planner').sort((a,b)=>(graph.members[a].attempts?.length?0:1)-(graph.members[b].attempts?.length?0:1)||(snapshot.nodes[a]?.created_at||'').localeCompare(snapshot.nodes[b]?.created_at||'')||a.localeCompare(b))}
 function latestPlanner(){const rounds=plannerRounds();return rounds.at(-1)}
 function renderPlanner(){
  const box=$('graph-planner-summary'),rounds=plannerRounds();box.hidden=!rounds.length;if(!rounds.length)return;
  const id=rounds.at(-1),m=graph.members[id],status=graph.status==='accepted'?'Complete':m.state==='running'?(rounds.length===1?'Planning':'Reviewing results'):m.state==='queued'?'Queued':Object.values(graph.expansions).some(x=>x.error&&!x.outcome)?'Needs attention':graph.status==='cancelled'?'Stopped':'Waiting for agents';
  box.replaceChildren(node('strong','✦ Planner'),node('span',status,'muted'),button(`${rounds.length} planning ${rounds.length===1?'round':'rounds'}`,async()=>{await selectNode(id);selectedTab='planning';detailSignature='';renderInspector()},'graph-planner-history'),button('Latest update',()=>selectNode(id)));
 }
 $('graph-show-history').onchange=event=>{showHistory=event.target.checked;renderMap();renderTree()};
 function closeResult(){ $('graph-result').hidden=true;$('graph-current').classList.remove('showing-result'); }
 async function openResult(){
  if(!graph)return;
  $('graph-current').classList.add('showing-result');$('graph-result').hidden=false;
  await window.MitosGraphResult.open({graph:structuredClone(graph),nodes:snapshot.nodes,request,container:$('graph-result'),onClose:()=>{closeResult();scrollWorkTo($('graph-current'))}});
  if(!$('graph-result').hidden)scrollWorkTo($('graph-result'));
 }
 function displayState(id) { if(id===graph?.root_task)return overall();if(graph?.members[id]?.spec.kind==='scope'&&reviewable(id))return 'review';return snapshot?.nodes[id]?.state || graph?.members[id]?.state || 'queued'; }
 function overall() {
  if (graph.status === 'cancelled' && Object.values(snapshot.nodes).some(n=>!n.settled)) return 'stopping';
  if (graph.status !== 'active') return graph.status;
  if (Date.parse(graph.limits.deadline) <= Date.now()) return 'needs_attention';
  const values = Object.values(snapshot.nodes);
  if (Object.values(graph.expansions).some(x => !x.outcome && x.error) || values.some(n => ['failed','needs_attention'].includes(n.state))) return 'needs_attention';
  if (values.some(n => n.state === 'running')) return 'running';
  if (reviewable(graph.root_task)) return 'review';
  if (!graph.policy.controller_application && Object.keys(graph.members).length===1) return 'idle';
  if (Object.values(graph.expansions).some(x => !x.outcome)) return 'planning';
  return 'queued';
 }
 function descendants(g, id) { return Object.keys(g.members).filter(key => { let at = key, seen = new Set(); while (at && !seen.has(at)) { if (at === id) return true; seen.add(at); at = g.members[at]?.spec.parent; } return false; }); }
 function obligationAccepted(g,m) { const seen = new Set(); while (m && !seen.has(m.spec.id)) { if (m.accepted) return true; seen.add(m.spec.id); m = g.members[m.replacement]; } return false; }
 function reviewable(id) {
  if (!graph || graph.status !== 'active' || Date.parse(graph.limits.deadline) <= Date.now()) return false;
  const m = graph.members[id]; if (!m || m.accepted || m.cancel_requested || m.replacement) return false;
  if (m.spec.kind !== 'scope') return m.state === 'done' && snapshot.nodes[id]?.settled;
  if (descendants(graph,id).length < 2) return false;
  if (Object.values(graph.decisions || {}).some(d => ['queued','pending','selected'].includes(d.outcome))) return false;
  // Human review may close manual planning obligations, never a live planner.
  if (Object.values(graph.expansions).some(x => !x.outcome && x.controller_task && !snapshot.nodes[x.controller_task]?.settled)) return false;
  return descendants(graph,id).filter(key => key !== id).every(key => {
   const child = graph.members[key]; return snapshot.nodes[key]?.settled && (!child.spec.required || obligationAccepted(graph,child));
  });
 }
 async function loadOptions() {
  const library = await request('/api/graphs/agents');
  optionsLoaded=true; $('graph-create').disabled=!library.ready;
  $('graph-planner-note').textContent=library.ready ? `${library.agents.length} agent roles · ${library.runtime}` : library.reason;
 }
 function scrollWorkTo(element) { const view=$('work-view'); view.scrollTop += element.getBoundingClientRect().top-view.getBoundingClientRect().top-16; }
 function compose() { if($('work-view').hidden)document.querySelector('[data-view=work]').click(); $('graph-compose').hidden = false; $('graph-compose-close').hidden = !graph; $('graph-current').hidden = true; $('graph-prompt').focus({preventScroll:true}); scrollWorkTo($('graph-compose')); }
 $('new-work').addEventListener('click', compose);
 $('graph-compose-close').onclick = () => { $('graph-compose').hidden = true; $('graph-current').hidden = !graph; };
 $('graph-create-form').onsubmit = async event => {
  event.preventDefault(); const form = event.target, submit = $('graph-create'); submit.disabled = true; form.querySelector('.form-error').textContent = '';
  try {
   const goal = form.elements.goal.value.trim(); if (goal.length < 3) throw new Error('Describe what you want to accomplish.');
   createBody={key:crypto.randomUUID(),project_id:project,goal};
   const result=await sendMutation('/api/graphs/automatic',createBody,'create');createBody=null;form.elements.goal.value='';await loadSummaries(true);await chooseGraph(result.id);

  } catch (error) { form.querySelector('.form-error').textContent = error.message; } finally { submit.disabled = false; }
 };
 async function loadSummaries(reset = false) {
  const result = await request('/api/graphs?project_id=' + encodeURIComponent(project) + '&limit=100' + (!reset && nextPage ? '&after=' + encodeURIComponent(nextPage) : ''));
  const known = new Map(graphs.map(g => [g.id,g])); for (const row of result.graphs) known.set(row.id,row); graphs = [...known.values()].sort((a,b) => b.created_at.localeCompare(a.created_at)); if(!reset)loadedMore=true;if(!loadedMore||!reset)nextPage=result.next;
  const nextSignature=JSON.stringify([graphs.map(g=>[g.id,g.title,g.status,g.counts]),graphID,nextPage]);if(nextSignature===summarySignature)return;summarySignature=nextSignature;
  renderTree(); $('graph-load-more').hidden=!nextPage;
 }
 $('graph-load-more').onclick=()=>loadSummaries().catch(showError);
 const treeClosed=new Set(); let treeSignature='';
 function renderTree() {
  const tree=$('work-tree'); if(!tree)return;
  const sig=JSON.stringify([graphs,graphID,selected,showHistory,graph?.members,snapshot?.nodes,[...treeClosed]]);if(sig===treeSignature)return;treeSignature=sig;
  const focus=document.activeElement?.dataset.treeKey;tree.replaceChildren();
  for(const row of graphs){
   const current=row.id===graphID, expanded=current&&!treeClosed.has(row.id), counts=row.counts||{};
   const item=node('div',undefined,'work-tree-item'),heading=node('div',undefined,'work-tree-heading');
   const toggle=button(expanded?'▾':'▸',async()=>{if(current){if(treeClosed.has(row.id))treeClosed.delete(row.id);else treeClosed.add(row.id);renderTree()}else{treeClosed.delete(row.id);await chooseGraph(row.id)}},'work-tree-toggle');
   toggle.setAttribute('aria-label',(expanded?'Collapse ':'Expand ')+(row.title||'Work'));toggle.setAttribute('aria-expanded',String(expanded));toggle.dataset.treeKey='toggle:'+row.id;
   const choose=button(row.title||'Untitled work',async()=>{treeClosed.delete(row.id);if(current){await selectNode(graph.root_task)}else await chooseGraph(row.id);if($('work-view').hidden)document.querySelector('[data-view=work]').click()},'work-tree-goal');choose.title=row.title;choose.dataset.treeKey=row.id;
   if(current)choose.setAttribute('aria-current','page');
   const status=current&&graph?overall():row.status==='accepted'?'accepted':row.status==='cancelled'?'cancelled':counts.running?'running':counts.failed||counts.needs_attention?'needs_attention':counts.queued?'queued':'idle';
   heading.append(toggle,choose);item.append(heading,node('small',labels[status]||status,'work-tree-status '+status));
   if(expanded&&graph){const list=node('ul');const append=(parent,container)=>{for(const [id,m]of Object.entries(graph.members).filter(([,m])=>m.spec.parent===parent&&kind(m)!=='planner'&&(showHistory||!m.replacement))){const li=node('li'),b=button(title(m),()=>selectNode(id),'work-tree-node');b.dataset.treeKey=id;b.title=kindLabel(m)+': '+title(m)+' · '+(labels[displayState(id)]||displayState(id));if(selected===id)b.setAttribute('aria-current','page');const dot=node('span',undefined,'work-tree-dot '+displayState(id));dot.setAttribute('aria-label',labels[displayState(id)]||displayState(id));b.prepend(dot);li.append(b);if(Object.values(graph.members).some(c=>c.spec.parent===id)){const nested=node('ul');append(id,nested);li.append(nested)}container.append(li)}};const planner=latestPlanner();if(planner){const li=node('li'),b=button('✦ Planner',()=>selectNode(planner),'work-tree-node');b.dataset.treeKey='planner';if(selected&&kind(graph.members[selected])==='planner')b.setAttribute('aria-current','page');li.append(b);list.append(li)}append(graph.root_task,list);if(!list.children.length)list.append(node('li',graph.policy.automatic?'Preparing planner…':'Not started','work-tree-empty'));item.append(list)}
   tree.append(item);
  }
  if(!graphs.length)tree.append(node('p','No work yet','work-tree-empty'));
  if(focus)tree.querySelector(`[data-tree-key="${CSS.escape(focus)}"]`)?.focus({preventScroll:true});
 }
 async function chooseGraph(id, keepNode = false) {
  closeResult();epoch++; graphID = id;showHistory=false;$('graph-show-history').checked=false; if (!keepNode) selected = ''; selectedAttempt=''; readerStates.clear(); renderedReaderKey=''; graph = null; snapshot = null; snapshotSignature = ''; detailSignature = ''; collapsed = new Set(); order = []; firstLayout = true; positions.clear(); cachedRuns.clear(); artifactCache.clear();
  $('graph-compose').hidden = true; $('graph-current').hidden = true; $('graph-inspector').hidden = true; updateURL();
  await refreshSelected();
 }
 function updateURL() { const url = new URL(location.href); for (const key of ['task','tab']) url.searchParams.delete(key); if (graphID) url.searchParams.set('graph',graphID); else url.searchParams.delete('graph'); if (selected) url.searchParams.set('node',selected); else url.searchParams.delete('node'); history.replaceState(null,'',url); }
 async function refreshSelected() {
  if (!graphID) return;
  const id = graphID, expected = epoch, selection = selected,sequence=++snapshotRequest;
  const result = await request(endpoint(id) + '/snapshot?node=' + encodeURIComponent(selection));
  if (expected !== epoch || id !== graphID || selection !== selected || sequence<snapshotApplied) return;snapshotApplied=sequence;
  if (result.graph.project_id !== project || result.graph.home !== server) throw new Error('This graph belongs to a different project or server.');
  snapshot = result; graph = result.graph; if (selected && !graph.members[selected]) selected = '';
  const signature = JSON.stringify([graph,snapshot.nodes]);
  if (signature !== snapshotSignature) { snapshotSignature = signature; render(); }
  $('graph-sync').textContent = 'Live · ' + new Date(result.observed_at).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit'}); $('graph-sync').classList.remove('offline');
 }
 function render() {
  if ($('graph-compose').hidden) $('graph-current').hidden = false;
  $('graph-title').textContent = title(graph.members[graph.root_task]); const status = overall(); $('graph-status').className = 'graph-badge ' + status; $('graph-status').textContent = labels[status] || status;
  const workers = Object.entries(graph.members).filter(([,m]) => m.spec.kind !== 'scope' && !m.controller_for && !m.replacement), finished = workers.filter(([,m]) => m.accepted).length;
  $('graph-progress').textContent = `${finished} of ${workers.length} steps complete` + (graph.policy.controller_application ? ' · Planner managed' : ' · Not started');
  $('graph-review').hidden = graph.policy.automatic ? graph.status!=='accepted' : !reviewable(graph.root_task);$('graph-review').textContent=graph.status==='accepted'?'View result':'Review result'; $('graph-stop').disabled = graph.status !== 'active'; $('graph-replan').hidden = !graph.policy.controller_application || graph.status !== 'active';
  renderMessage(); renderPlanner(); renderMap(); renderInspector(); renderActivity(); renderTree();
  $('graph-announcement').textContent = `${labels[status] || status}. ${finished} of ${workers.length} steps complete.`;
 }
 function renderMessage() {
  const box = $('graph-message'); box.replaceChildren(); box.hidden = true;
  const errors = Object.values(graph.expansions).filter(x => !x.outcome && x.error);
  if (Date.parse(graph.limits.deadline) <= Date.now() && graph.status === 'active') { box.textContent = 'The work reached its time limit. Results are preserved; no new steps will start.'; box.hidden = false; }
  else if (errors.length) { const reasons = {controller_proposal_missing:'The planner did not produce a work proposal.',controller_proposal_invalid:'The planner returned a proposal the runtime could not read.',controller_proposal_stale_or_rejected:'The planner’s proposal no longer matches the current work.',controller_execution_needs_review:'The planner stopped before producing a verified proposal.',controller_allowance_exhausted:'The planning allowance has been used.',controller_context_limit:'This graph exceeds the planner’s context limit.'}; box.append(node('span', reasons[errors[0].error] || errors[0].error.replaceAll('_',' ')),button('Inspect planner',() => selectNode(errors[0].controller_task || graph.root_task))); box.hidden = false; }
  else if (!graph.policy.controller_application && Object.values(graph.expansions).some(x => !x.outcome) && Object.keys(graph.members).length === 1) { box.append(node('span','This goal has not started.'),button('Start automatic work',async()=>{await sendMutation(endpoint(graph.id)+'/automatic',{key:crypto.randomUUID(),revision:graph.revision});await refreshSelected()},'primary')); box.hidden = false; }
  else if (graph.status === 'accepted') { box.textContent = graph.policy.automatic ? 'Work complete · Open View result to read the saved reports and deliverables.' : 'Work complete. The graph and its results are preserved.'; box.hidden = false; }
  else if (Object.values(graph.decisions || {}).some(d => d.outcome === 'selected')) { const d = Object.values(graph.decisions).find(d => d.outcome === 'selected'); box.append(node('span','A decision is ready to apply.'),button('Review decision',() => selectNode(d.task_id))); box.hidden = false; }
 }
 function visibleNodes() {
  const members = graph.members;
  for (const id of Object.keys(members)) if (!order.includes(id)) order.push(id);
  const visible = order.filter(id => {if(kind(members[id])==='planner'||(!showHistory&&members[id]?.replacement))return false; let at = members[id]?.spec.parent, seen = new Set(); while (at && !seen.has(at)) { if (collapsed.has(at)) return false; seen.add(at); at = members[at]?.spec.parent; } return !!members[id]; });
  return visible;
 }
 function layout(ids) {
  const membersVisible=id=>graph.members[id]&&kind(graph.members[id])!=='planner'&&(showHistory||!graph.members[id].replacement);
  const shown = new Set(ids), incoming = new Map(ids.map(id => [id,new Set()])), edges = [];
  const representative = id => {if(!membersVisible(id))return null;let at = id, result = id, seen = new Set(); while (at && !seen.has(at)) { if (shown.has(at)) result = at; if (collapsed.has(at)) return at; seen.add(at); at = graph.members[at]?.spec.parent; } return shown.has(id) ? id : result; };
  for (const id of ids) {
   const m = graph.members[id]; if (m.spec.parent && shown.has(m.spec.parent)) { edges.push({from:m.spec.parent,to:id,type:'containment'}); incoming.get(id).add(m.spec.parent); }
   for (const dependency of m.spec.brief.scheduling?.deps || []) { const from = representative(dependency.from); if (shown.has(from) && from !== id) { edges.push({from,to:id,type:'dependency'}); incoming.get(id).add(from); } }
   if (m.replacement && shown.has(m.replacement)) edges.push({from:id,to:m.replacement,type:'supersedes'});
  }
  // Containment and execution are different relations. A fallback prevents a
  // combined visual cycle from hanging layout even when each backend DAG is valid.
  const ranks = new Map(), remaining = new Set(ids); let progress = true;
  while (remaining.size && progress) { progress = false; for (const id of remaining) { if ([...incoming.get(id)].every(from => ranks.has(from))) { ranks.set(id,Math.max(0,...[...incoming.get(id)].map(from => ranks.get(from)+1))); remaining.delete(id); progress = true; } } }
  for (const id of remaining) ranks.set(id,id === graph.root_task ? 0 : 1);
  const rows = new Map(); positions.clear();
  for (const id of ids) { const rank = ranks.get(id), row = rows.get(rank) || 0; rows.set(rank,row+1); positions.set(id,{x:40+rank*340,y:40+row*180}); }
  dimensions = {width:Math.max(620,...[...positions.values()].map(p => p.x+304)),height:Math.max(380,...[...positions.values()].map(p => p.y+182))}; return edges;
 }
 function renderMap() {
  const ids = visibleNodes(), edges = layout(ids), stage = $('graph-stage');
  stage.style.width = dimensions.width + 'px'; stage.style.height = dimensions.height + 'px';
  let svg = stage.querySelector('svg'); if (!svg) { svg = document.createElementNS('http://www.w3.org/2000/svg','svg'); svg.setAttribute('aria-hidden','true'); stage.prepend(svg); }
  svg.setAttribute('width',dimensions.width); svg.setAttribute('height',dimensions.height); svg.replaceChildren();
  const defs = document.createElementNS(svg.namespaceURI,'defs'), marker = document.createElementNS(svg.namespaceURI,'marker'); marker.id = 'graph-arrow'; marker.setAttribute('viewBox','0 0 10 10'); marker.setAttribute('refX','9'); marker.setAttribute('refY','5'); marker.setAttribute('markerWidth','5'); marker.setAttribute('markerHeight','5'); marker.setAttribute('orient','auto-start-reverse'); const arrow = document.createElementNS(svg.namespaceURI,'path'); arrow.setAttribute('d','M 0 0 L 10 5 L 0 10 z'); arrow.setAttribute('fill','#7796a2'); marker.append(arrow); defs.append(marker); svg.append(defs);
  for (const edge of edges) { const from = positions.get(edge.from), to = positions.get(edge.to); if (!from || !to) continue; const path = document.createElementNS(svg.namespaceURI,'path'); const x1=from.x+264,y1=from.y+66,x2=to.x,y2=to.y+66,curve=Math.max(35,Math.abs(x2-x1)/2); path.setAttribute('d',`M${x1},${y1} C${x1+curve},${y1} ${x2-curve},${y2} ${x2},${y2}`); path.setAttribute('class','graph-edge '+edge.type); if (edge.type !== 'containment') path.setAttribute('marker-end','url(#graph-arrow)'); svg.append(path); }
  const existing = new Map([...stage.querySelectorAll('.graph-node')].map(n => [n.dataset.id,n]));
  stage.querySelectorAll('.graph-collapse').forEach(n => n.remove());
  for (const id of ids) {
   const m = graph.members[id], p = positions.get(id), status = displayState(id); let card = existing.get(id);
   if (!card) { card = button('',() => selectNode(id),'graph-node'); card.dataset.id = id; stage.append(card); }
   existing.delete(id); card.style.left = p.x + 'px'; card.style.top = p.y + 'px'; card.style.height = '132px'; card.dataset.status = status; card.dataset.kind = kind(m); card.setAttribute('aria-pressed',String(id === selected)); card.setAttribute('aria-label',`${title(m)} · ${kindLabel(m)} · ${labels[status] || status}`);
   const contentKey = JSON.stringify([title(m),status,kind(m),snapshot.nodes[id]?.executor,m.attempts?.length,m.spec.required,m.sealed]);
   if (card.dataset.content !== contentKey) { card.dataset.content = contentKey; const top = node('span',undefined,'graph-node-top'), k = node('span',undefined,'graph-node-kind'); k.append(node('b',{scope:'◇',planner:'✦',decision:'◈',step:'○'}[kind(m)]),document.createTextNode(kindLabel(m))); top.append(k,badge(status)); const footer = node('span',undefined,'graph-node-bottom'); footer.append(node('span',m.spec.kind === 'scope' ? (m.accepted ? 'Result ready' : graph.policy.automatic ? 'Agent managed' : 'Not started') : snapshot.nodes[id]?.executor || 'Waiting for executor'),node('span',m.attempts?.length ? `↻ ${m.attempts.length}` : m.controller_for ? 'Planner' : '')); card.replaceChildren(top,node('strong',title(m),'graph-node-title'),footer); }
   const count = Object.values(graph.members).filter(child => child.spec.parent === id && kind(child)!=='planner' && (showHistory||!child.replacement)).length;
   if (m.spec.kind === 'scope' && count) { const toggle = button(collapsed.has(id) ? '+' : '−',() => { if (collapsed.has(id)) collapsed.delete(id); else { collapsed.add(id); if (selected && descendants(graph,id).includes(selected) && selected !== id) selectNode(id); } renderMap(); },'graph-collapse'); toggle.setAttribute('aria-expanded',String(!collapsed.has(id))); toggle.setAttribute('aria-label',(collapsed.has(id)?'Expand ':'Collapse ')+title(m)); toggle.style.left = (p.x+224)+'px'; toggle.style.top = (p.y+118)+'px'; stage.append(toggle); }
  }
  for (const card of existing.values()) card.remove();
  const list = $('graph-list-view'); list.replaceChildren(); for (const id of ids) { const m = graph.members[id], row = button('',() => selectNode(id),'graph-list-row'); row.setAttribute('aria-pressed',String(id === selected)); const text = node('span',title(m)); text.append(node('small',kindLabel(m)+(snapshot.nodes[id]?.executor?' · '+snapshot.nodes[id].executor:''))); row.append(text,badge(displayState(id))); list.append(row); }
  $('graph-node-count').textContent = ids.filter(id=>graph.members[id].spec.kind!=='scope').length+' assignments'+(Object.values(graph.members).some(m=>m.replacement)&&!showHistory?' · replaced work hidden':'');
  applyZoom(); if (firstLayout) { firstLayout = false; requestAnimationFrame(()=>fit(false)); }
 }
 function applyZoom() { $('graph-stage').style.transform = `scale(${zoom})`; $('graph-scale').style.width = dimensions.width*zoom+'px'; $('graph-scale').style.height = dimensions.height*zoom+'px'; $('graph-zoom').textContent = Math.round(zoom*100)+'%'; }
 function setZoom(value) { const viewport = $('graph-viewport'), cx=(viewport.scrollLeft+viewport.clientWidth/2)/zoom,cy=(viewport.scrollTop+viewport.clientHeight/2)/zoom; zoom=Math.max(.45,Math.min(1.6,value)); applyZoom(); viewport.scrollLeft=cx*zoom-viewport.clientWidth/2; viewport.scrollTop=cy*zoom-viewport.clientHeight/2; }
 function fit(all=true) { const viewport = $('graph-viewport'); if (!viewport.clientWidth) return; zoom=Math.max(all ? .15 : .8,Math.min(1,viewport.clientWidth/dimensions.width,viewport.clientHeight/dimensions.height)); applyZoom(); viewport.scrollLeft=0; viewport.scrollTop=0; }
 $('graph-fit').onclick=fit; $('graph-zoom-in').onclick=()=>setZoom(zoom+.15); $('graph-zoom-out').onclick=()=>setZoom(zoom-.15);
 function setLayout(list) { listMode=list; $('graph-viewport').hidden=list; $('graph-list-view').hidden=!list; $('graph-mode-map').setAttribute('aria-pressed',String(!list)); $('graph-mode-list').setAttribute('aria-pressed',String(list)); document.querySelector('.graph-map-controls').hidden=list; }
 $('graph-mode-map').onclick=()=>setLayout(false); $('graph-mode-list').onclick=()=>setLayout(true);
 async function selectNode(id) { if (!id || !graph.members[id]) return;closeResult();if(graph.members[id].replacement){showHistory=true;$('graph-show-history').checked=true} selected=id; selectedAttempt=''; selectedTab=graph.members[id].current_run_id?'output':'details'; detailSignature=''; epoch++; updateURL(); renderTree(); $('graph-inspector').hidden=false; renderMap(); await refreshSelected(); if (matchMedia('(max-width:900px)').matches) scrollWorkTo($('graph-inspector')); }
 function captureReader(){
  if(!renderedReaderKey)return;
  const box=$('graph-node-detail');readerStates.set(renderedReaderKey,{scroll:$('graph-inspector').scrollTop,artifact:box.querySelector('[aria-label="Output artifact"]')?.value,open:Object.fromEntries([...box.querySelectorAll('details[data-read-key]')].map(d=>[d.dataset.readKey,d.open]))});
 }
 function restoreReader(){const saved=readerStates.get(renderedReaderKey);if(!saved)return;for(const d of $('graph-node-detail').querySelectorAll('details[data-read-key]'))if(d.dataset.readKey in saved.open)d.open=saved.open[d.dataset.readKey];$('graph-inspector').scrollTop=saved.scroll;}
 $('graph-inspector-expand').onclick=()=>{const body=document.querySelector('.graph-body'),expanded=body.classList.toggle('reading-mode');$('graph-inspector-expand').textContent=expanded?'Show graph':'Expand';$('graph-inspector-expand').setAttribute('aria-pressed',String(expanded));scrollWorkTo($('graph-inspector'))};
 $('graph-inspector-close').onclick=()=>{selected='';epoch++;detailSignature='';$('graph-inspector').hidden=true;document.querySelector('.graph-body').classList.remove('reading-mode');$('graph-inspector-expand').textContent='Expand';$('graph-inspector-expand').setAttribute('aria-pressed','false');updateURL();renderMap();renderTree()};
 function renderInspector() {
  const m=graph.members[selected],box=$('graph-node-detail'); $('graph-inspector').hidden=!m; if(!m)return;
  $('graph-node-kind').textContent=kindLabel(m); const signature=JSON.stringify([selected,selectedTab,selectedAttempt,m,snapshot.nodes[selected],graph.decisions,graph.expansions,graph.status]); if(signature===detailSignature)return;captureReader();renderedReaderKey=graphID+':'+selected+':'+selectedTab+':'+selectedAttempt;detailSignature=signature;box.replaceChildren(node('h3',kind(m)==='planner'?'Planner · Round '+(plannerRounds().indexOf(selected)+1):title(m)),badge(displayState(selected)));
  const actions=node('div',undefined,'actions');
  if(!graph.policy.automatic&&reviewable(selected)) actions.append(button(m.spec.kind==='scope'?'Review result':'Review step',()=>openAction('accept',selected),'primary'));
  if(graph.status==='active'&&!m.accepted&&!m.cancel_requested&&!m.replacement){if(m.spec.kind!=='scope'&&['failed','cancelled'].includes(m.state)&&snapshot.nodes[selected]?.settled&&m.attempts.length<=graph.limits.retries)actions.append(button('Retry step',()=>openAction('retry',selected)));if(m.state==='parked')actions.append(link('Inspect execution','/runs/'+encodeURIComponent(m.current_run_id)));if(m.spec.kind!=='scope')actions.append(button('Stop step',()=>openAction('cancel',selected)));}
  box.append(actions);const tabs=node('nav',undefined,'graph-tabs');tabs.setAttribute('aria-label','Step details');for(const [id,label]of(kind(m)==='planner'?[['output','Output'],['planning','History'],['details','Details']]:[['details','Details'],['output','Output'],['attempts','Attempts']])){const b=button(label,()=>{selectedTab=id;detailSignature='';renderInspector()});if(id===selectedTab)b.setAttribute('aria-current','page');tabs.append(b)}box.append(tabs);
  if(selectedTab==='details'){
   const intent=m.spec.brief.intent||m.spec.criteria||'This step is preparing its assignment.';box.append(node('p',intent,'prose'));if(m.spec.criteria&&m.spec.criteria!==intent){box.append(node('h4','Success criteria'),node('p',m.spec.criteria,'prose'))}
   if(snapshot.nodes[selected]?.executor)box.append(node('h4','Agent'),node('p',snapshot.nodes[selected].executor));
   const deps=m.spec.brief.scheduling?.deps||[];if(deps.length){box.append(node('h4','Depends on'));for(const dep of deps){box.append(button((graph.members[dep.from]?title(graph.members[dep.from]):dep.from)+(snapshot.nodes[selected].waiting?.includes(dep.from)?' · waiting':''),()=>selectNode(dep.from),'graph-dependency-link'))}}
   if(m.replacement)box.append(node('h4','Replacement'),button(title(graph.members[m.replacement]),()=>selectNode(m.replacement),'graph-dependency-link'));
   if(m.accepted)box.append(node('h4','Accepted result'),window.MitosFormat?window.MitosFormat.markdown(m.accepted.rationale):node('p',m.accepted.rationale),node('p',m.accepted.evidence,'muted'));
   const decision=Object.values(graph.decisions||{}).find(d=>d.task_id===selected);if(decision)renderDecision(box,decision);
   if(m.spec.kind==='scope'){
    const unresolved=Object.values(graph.expansions).filter(x=>!x.outcome);if(unresolved.length){box.append(node('h4','Planning'));for(const x of unresolved)box.append(node('p',x.error?x.error.replaceAll('_',' '):x.controller_task?'Planner is considering the next steps.':'Waiting for steps to be defined.'))}
    if(!reviewable(selected)&&graph.status==='active')box.append(node('h4','Before completion'),node('p','Required steps must be accepted and execution settled before reviewing this goal.','muted'));
   }
  }else if(selectedTab==='planning'){renderPlanningHistory(box)}else if(selectedTab==='output'){renderOutput(box,m,selectedAttempt||m.current_run_id)}else{renderAttempts(box,m)}
  restoreReader();
 }
 function renderDecision(box,d){box.append(node('h4','Decision'),node('p',d.outcome==='selected'?'A candidate has been selected. Review its changes before applying.':d.reason?.replaceAll('_',' ')||d.outcome));if(d.answer)box.append(node('p','Selected candidate '+(Number(d.answer.choice)+1)));for(const [index,candidate]of(d.candidates||[]).entries()){const details=node('details');details.open=String(index)===d.answer?.choice;details.append(node('summary',`${index+1}. ${candidate.explanation}`));const ul=node('ul');for(const op of candidate.operations)ul.append(node('li',(op.kind.replaceAll('_',' '))+': '+(op.spec?.brief?.title||graph.members[op.task]?.spec.brief.title||op.rationale||'')));details.append(ul);box.append(details)}if(d.outcome==='selected')box.append(button('Apply selected changes',()=>openAction('apply-decision',d.key),'primary'));if(!['applied','dismissed'].includes(d.outcome))box.append(button('Dismiss decision',()=>openAction('dismiss-decision',d.key)));}
 async function runDetail(m,runID){const key=runID+':'+m.fact_revision; if(cachedRuns.has(key))return cachedRuns.get(key);const value=await request(endpoint(graphID)+'/nodes/'+encodeURIComponent(m.spec.id)+'/runs/'+encodeURIComponent(runID));cachedRuns.set(key,value);return value;}
 function renderOutput(box,m,runID=m.current_run_id){
  if(!runID){box.append(node('p',m.spec.kind==='scope'?'Select an agent or step to read its output.':'Output will appear when this step runs.','muted'));return}
  const holder=node('div');holder.append(node('p','Loading output…','muted'));box.append(holder);const token=epoch,chosen=selected;
  runDetail(m,runID).then(async run=>{
   if(token!==epoch||selected!==chosen||selectedTab!=='output'||!holder.isConnected)return;
   holder.replaceChildren();const attemptIndex=(m.attempts||[]).indexOf(run.id);const attemptHeader=node('div',undefined,'graph-attempt-summary');attemptHeader.append(node('strong','Attempt '+(attemptIndex+1)),badge(m.accepted?.run_id===run.id?'accepted':run.status==='done'?'finished':run.status));if(run.stage&&run.status==='running')attemptHeader.append(node('span',run.stage,'muted'));holder.append(attemptHeader);const files=run.artifacts||[];if(run.summary&&!files.some(a=>a.name==='report.md'))holder.append(node('p',run.summary));
   const readable=files.filter(a=>/\.(txt|md|json|patch)$/.test(a.name)&&a.bytes<=512*1024);const report=readable.find(a=>/report\.md$/.test(a.name))||[...readable].reverse().find(a=>/output\.txt$/.test(a.name))||readable[0];
   if(!report){holder.append(node('p',run.status==='running'?'The executor is running. Saved output appears after the current step finishes.':'No text output was saved.','muted'));}
   if(readable.length){const select=node('select');select.setAttribute('aria-label','Output artifact');for(const a of readable)select.append(new Option(a.name==='report.md'?'Report':a.name==='output.txt'?'Execution log':a.name==='graph-proposal.json'?'Planner proposal':a.name==='changes.patch'?'Changes':a.name,a.name));select.value=readerStates.get(renderedReaderKey)?.artifact||report.name;if(!select.value)select.value=report.name;const content=node('div');holder.append(select,content);const show=async()=>{const name=select.value,artifact=files.find(a=>a.name===name),cacheKey=run.id+'/'+artifact.sha256;content.textContent='Loading…';try{let text=artifactCache.get(cacheKey);if(text===undefined){const response=await fetch('/runs/'+encodeURIComponent(run.id)+'/artifacts/'+encodeURIComponent(name),{headers:{'X-Mitos-Server':server},cache:'no-store'});if(!response.ok)throw Error('Output is unavailable.');const buffer=await response.text();if(buffer.length>512*1024)throw Error('This output is too large to preview. Download the artifact instead.');text=buffer;artifactCache.set(cacheKey,text)}if(token!==epoch||select.value!==name||!content.isConnected)return;content.replaceChildren(window.MitosGraphOutput.render(text,{planner:!!m.controller_for,name,resolve:id=>graph.members[id]?title(graph.members[id]):id}));restoreReader();}catch(error){content.textContent=error.message}};select.onchange=()=>show();await show();}
   if(files.length){const details=node('details');details.dataset.readKey='artifacts';details.append(node('summary','Files & downloads'));const links=node('div',undefined,'graph-files');for(const artifact of files)links.append(link(artifact.name,'/runs/'+encodeURIComponent(run.id)+'/artifacts/'+encodeURIComponent(artifact.name)));details.append(links);holder.append(details)}
  }).catch(error=>{if(holder.isConnected)holder.textContent=error.message});
 }
 function renderPlanningHistory(box){
  box.append(node('p','One planner role, called again as results arrive. Each round preserves its own proposal and evidence.','muted'));
  for(const [i,id]of plannerRounds().entries()) {const m=graph.members[id],row=node('article',undefined,'graph-attempt-card'),head=node('div',undefined,'graph-attempt-heading');head.append(node('strong','Round '+(i+1)+' · '+title(m)),badge(displayState(id)));row.append(head,button('Read this round',()=>selectNode(id)));box.append(row)}
 }
 function renderAttempts(box,m){
  if(!m.attempts?.length){box.append(node('p','This agent has not started yet.','muted'));return}
  box.append(node('p','Each attempt keeps its own output and files.','muted'));
  for(const [index,id]of m.attempts.entries()){
   const row=node('article',undefined,'graph-attempt-card'),heading=node('div',undefined,'graph-attempt-heading');heading.append(node('strong','Attempt '+(index+1)));if(id===m.current_run_id)heading.append(badge(displayState(m.spec.id)));row.append(heading);
   const meta=node('p','Loading status…','muted');row.append(meta);const token=epoch;
   runDetail(m,id).then(run=>{if(token!==epoch||!row.isConnected)return;if(id!==m.current_run_id)heading.append(badge(run.status==='done'?'finished':run.status));meta.textContent=(Date.parse(run.started_at)>0?'Started '+date(run.started_at):'Queued')+(Date.parse(run.finished_at)>0?' · Finished '+date(run.finished_at):'');}).catch(error=>{if(row.isConnected)meta.textContent=error.message});
   row.append(button('View output & files',()=>{selectedAttempt=id;selectedTab='output';detailSignature='';renderInspector()},'primary'));
   const diagnostic=node('details');diagnostic.dataset.readKey='diagnostics:'+id;diagnostic.append(node('summary','Diagnostics'),link('Technical execution details','/runs/'+encodeURIComponent(id)));row.append(diagnostic);box.append(row);
  }
 }

 function renderActivity(){const entries=(graph.triggers||[]).slice(-12).reverse();$('graph-activity-count').textContent=entries.length?'· '+entries.length:'';const list=$('graph-activity-list');list.replaceChildren();for(const tr of entries){const li=node('li');li.append(node('span',graph.members[tr.task_id]?title(graph.members[tr.task_id]):'Step'),document.createTextNode(tr.kind.replaceAll('_',' ')+(tr.outcome?' · considered':' · awaiting planning')));list.append(li)}if(!entries.length)list.append(node('li','Goal created · '+date(graph.created_at)));}
 function openAction(type,target=graph.root_task){
  actionSnapshot=structuredClone(graph);actionType=type;actionTarget=target;const form=$('graph-action-form');form.reset();form.querySelector('.form-error').textContent='';const context=$('graph-action-context');context.replaceChildren();
  const accepting=type==='accept',m=graph.members[target];$('graph-evidence-label').hidden=!accepting;form.elements.evidence.required=accepting;
  const titles={accept:m?.spec.kind==='scope'?'Review the result':'Review this step',cancel:target===graph.root_task?'Stop this work?':'Stop this step?',retry:'Retry this step?',replan:'Replace the planner?', 'apply-decision':'Apply the selected changes?', 'dismiss-decision':'Dismiss this decision?'};
  $('graph-action-title').textContent=titles[type];$('graph-action-submit').textContent={accept:'Accept result',cancel:'Stop work',retry:'Start another attempt',replan:'Replace planner','apply-decision':'Apply changes','dismiss-decision':'Dismiss decision'}[type];
  if(accepting){context.append(node('p',m.spec.criteria||m.spec.brief.intent,'prose'));const list=node('div',undefined,'graph-review-list');for(const id of descendants(graph,target).filter(id=>id!==target)){const child=graph.members[id];if(!child.spec.required)continue;const row=node('div',title(child),'graph-review-item');row.append(node('span',obligationAccepted(graph,child)?'Accepted evidence available':labels[displayState(id)]||displayState(id)));if(child.current_run_id)row.append(link('Inspect result','/runs/'+encodeURIComponent(child.current_run_id)));list.append(row)}context.append(list);if(m.spec.kind==='scope')context.append(node('p','Accepting settles outstanding planning for this work and closes this scope. Confirm the evidence meets the goal and no further work is needed.','muted'));}
  else if(type==='cancel')context.append(node('p','Requests a stop for this work and its child steps. Saved output and history remain available.'));
  else if(type==='retry')context.append(node('p','Creates a new attempt with the same assignment. The previous attempt and its evidence remain in the graph.'));
  else if(type==='replan')context.append(node('p','A new planner will read the current graph. Existing worker steps keep running.'));
  else if(type==='apply-decision'){const d=graph.decisions[target];context.append(node('p',d.candidates?.[Number(d.answer?.choice)]?.explanation||'Apply the recorded model choice to the current graph.'));}
  else context.append(node('p','Retains the decision record and closes its outstanding planning obligation.'));
  $('graph-action-dialog').showModal();
 }
 $('graph-review').onclick=()=>graph.status==='accepted'?openResult():openAction('accept',graph.root_task);
 $('graph-replan').onclick=()=>openAction('replan');$('graph-stop').onclick=()=>openAction('cancel');
 $('graph-action-form').onsubmit=async event=>{
  event.preventDefault();const form=event.target,submit=$('graph-action-submit');submit.disabled=true;form.querySelector('.form-error').textContent='';
  try{
   const g=actionSnapshot,target=actionTarget,rationale=form.elements.rationale.value.trim();if(!rationale)throw new Error('Add a review note.');let operations=[],reads;
   if(actionType==='apply-decision'){await sendMutation(endpoint(g.id)+'/decisions/'+encodeURIComponent(target)+'/apply',{});await refreshSelected();}
   else{
    if(actionType==='accept'){
     const m=g.members[target];if(m.spec.kind==='scope'){
      for(const x of Object.values(g.expansions))if(!x.outcome)operations.push({kind:'close_expansion',expansion:x.id,rationale});
      for(const tr of g.triggers||[])if(!tr.outcome)operations.push({kind:'classify_trigger',trigger:tr.seq,rationale});
      if(!m.sealed)operations.push({kind:'seal',task:target,cursor:g.cursor});
     }
     operations.push({kind:'accept',task:target,evidence:form.elements.evidence.value.trim(),rationale});reads=allReads(g);
    }else if(actionType==='replan')operations=[{kind:'replace_controller',rationale}];
    else if(actionType==='dismiss-decision')operations=[{kind:'dismiss_decision',expansion:target,rationale}];
    else if(actionType==='retry')operations=[{kind:'retry',task:target}];
    else operations=[{kind:'cancel',task:target,rationale}];
    if(operations.length>128)throw new Error('This scope has more pending planning records than one review can settle. Review its planning records through the API before accepting.');
    await amend(g,operations,reads);
   }
   $('graph-action-dialog').close();await loadSummaries(true);
  }catch(error){form.querySelector('.form-error').textContent=error.message}finally{submit.disabled=false}
 };
 async function poll(){
  if(!alive)return;
  if(document.hidden||$('work-view').hidden){setTimeout(poll,1000);return}
  if(polling){setTimeout(poll,1000);return}polling=true;
  try{if(!optionsLoaded)await loadOptions();if(tick%5===0)await loadSummaries(true);await refreshSelected();failureCount=0;}
  catch(error){failureCount++;$('graph-sync').textContent='Disconnected · status may be out of date';$('graph-sync').classList.add('offline');if(!snapshot)showError(error);}
  finally{polling=false;tick++;if(alive)setTimeout(poll,Math.min(10000,failureCount?1000*2**Math.min(failureCount,3):1000))}
 }
 document.addEventListener('visibilitychange',()=>{if(!document.hidden)refreshSelected().catch(()=>{})});
 window.addEventListener('pagehide',()=>{alive=false;epoch++});
 async function start(){
  try{await Promise.all([loadOptions(),loadSummaries(true)]);if(graphID){await chooseGraph(graphID,true)}else if(graphs.length&&!new URLSearchParams(location.search).has('task')){await chooseGraph(graphs[0].id)}else compose();if(pending)showError(new Error('A previous request has no confirmed response. Reconcile it before starting another action.'));}
  catch(error){showError(error);$('graph-planner-note').textContent='Could not load the graph service. Reconnect to try again.';}
  finally{poll()}
 }
 start();
})();

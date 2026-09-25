(() => {
  'use strict';
  const project=document.body.dataset.project;
  if(!project)return;
  const $=id=>document.getElementById(id), active=s=>['starting','running','stopping','interrupted'].includes(s.status);
  const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;};
  const fail=e=>{$('desk-error').textContent=e.message;$('desk-error').hidden=false;};
  async function api(path,body){const r=await fetch(path,body===undefined?{cache:'no-store'}:{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});const v=await r.json();if(!r.ok)throw new Error(v.error||'Request failed');return v;}
  const endpoint=s=>`/api/projects/${project}/${s}`, terminal=s=>`/workspace?project=${project}&session=${s.id}&view=terminal`;
  const button=(text,fn)=>{const b=el('button',text);b.type='button';b.onclick=()=>Promise.resolve().then(fn).catch(fail);return b;};
  let snapshot, signature='', graphSignature='', profileRevision=0, selectedAssignment='', alive=true, reviewDraft=null, setupKey='', setupPayload='';
  function showProfile(draft=null){
    if(!snapshot)return;
    const f=$('profile-form'), p=draft?(draft.reviewed_profile||draft.profile):snapshot.profile;
    reviewDraft=draft;profileRevision=draft?draft.base_revision:p.revision;
    $('profile-dialog-title').textContent=draft?'Review project context':'Project profile';
    $('profile-introduction').textContent=draft?'The agent proposed this context in a separate analysis. Review the findings and rules, edit anything, and supply the goal you want the supervisor to work on.':'Edit the saved project context and working rules. Updated rules are announced to active agents.';
    $('profile-save').textContent=draft?'Save and start supervisor':'Save project profile';
    $('profile-goal').required=!!draft;
    for(const [key,value] of Object.entries(p)){const input=f.elements.namedItem(key);if(input)input.value=Array.isArray(value)?value.join('\n'):value;}
    f.querySelector('.form-error').textContent='';$('profile-dialog').showModal();
  }
  $('edit-project-profile').onclick=()=>showProfile();
  $('profile-dialog').addEventListener('close',()=>{if(reviewDraft)sessionStorage.setItem(`mitos:dismissedDraft:${reviewDraft.id}`,'true');});
  async function submit(form,fn){form.addEventListener('submit',async e=>{e.preventDefault();const b=form.querySelector('[type=submit]'),error=form.querySelector('.form-error');b.disabled=true;error.textContent='';try{await fn(Object.fromEntries(new FormData(form)));}catch(e){error.textContent=e.message;}finally{b.disabled=false;}});}
  submit($('profile-form'),async value=>{
    value.temp_paths=value.temp_paths.split('\n').map(s=>s.trim()).filter(Boolean);
    value.max_agents=Number(value.max_agents);value.min_free_mb=Number(value.min_free_mb);
    if(reviewDraft){const s=await api(endpoint(`profile-drafts/${reviewDraft.id}/start`),{profile:value,revision:profileRevision});location.href=terminal(s).replace('&view=terminal','');return;}
    await api(endpoint('profile'),{profile:value,revision:profileRevision});$('profile-dialog').close();await refresh();
  });
  submit($('attention-form'),async value=>{await api(endpoint('attention'),{body:value.body,pause:value.pause==='on'});$('attention-dialog').close();$('attention-form').reset();await refresh();});
  function chooseRole(role){
    $('session-role').value=role;$('session-role').dispatchEvent(new Event('change'));
    if(role==='coordinator'&&!$('session-prompt').value)$('session-prompt').value=snapshot?.profile.goal||'';
    $('session-dialog').showModal();
  }
  document.querySelectorAll('[data-start-role]').forEach(b=>b.onclick=()=>{
    if(b.dataset.startRole==='coordinator'&&['running','ready','reviewed'].includes(snapshot?.draft?.status)){
      if(snapshot.draft.status!=='running')showProfile(snapshot.draft);else $('setup-progress').scrollIntoView({block:'center'});return;
    }
    chooseRole(b.dataset.startRole);
  });
  window.MitosSupervision={startSetup:async value=>{
    const request={agent_id:value.agent_id,profile_id:value.profile_id,prompt:value.prompt||''};
    const serialized=JSON.stringify(request);if(serialized!==setupPayload||!setupKey){setupPayload=serialized;setupKey=crypto.randomUUID();}
    const d=await api(endpoint('profile-drafts'),{...request,key:setupKey});
    sessionStorage.removeItem(`mitos:dismissedDraft:${d.id}`);$('session-dialog').close();$('tab-supervision').click();await refresh();
  }};
  function renderDraft(){
    const d=snapshot.draft,box=$('setup-progress');box.hidden=!d||d.status==='started';box.replaceChildren();if(box.hidden)return;
    const names={running:'Analyzing project…',ready:'Project context ready for review',reviewed:'Profile saved · supervisor has not started',failed:'Project analysis failed',cancelled:'Project analysis cancelled',interrupted:'Project analysis interrupted'};
    box.append(el('h3',names[d.status]||d.status),el('p',d.status==='running'?'The selected agent is inspecting the project in a separate background conversation. You can leave this page and return.':d.detail||''));
    if(d.status==='ready'||d.status==='reviewed')box.append(button('Review project context',()=>showProfile(d)));
    if(d.status==='running'||d.status==='ready'||d.status==='reviewed')box.append(button('Discard setup',async()=>{await api(endpoint(`profile-drafts/${d.id}/cancel`),{});setupKey='';await refresh();}));
    else box.append(button('Retry analysis',()=>{setupKey='';chooseRole('coordinator');}));
  }
  function offerReview(){const d=snapshot?.draft;if(d?.status==='ready'&&document.body.dataset.view==='supervision'&&!document.querySelector('dialog[open]')&&!sessionStorage.getItem(`mitos:dismissedDraft:${d.id}`))showProfile(d);}
  function detail(s){
    selectedAssignment=s.id;
    const box=$('assignment-detail');box.replaceChildren(el('h3',s.name),el('p',s.initial_prompt||'No task prompt recorded.'));
    box.append(el('p',`Supervisor: ${snapshot.sessions.find(v=>v.id===s.supervisor_id)?.name||'Unassigned'} · ${s.status}`,'muted'),el('code',s.workspace||'Project checkout'));
    if(s.assignment){const a=s.assignment;box.append(el('p',`Goal: ${a.goal_id||'—'} · Task: ${a.task_id||'—'} · Requested by: ${a.requested_by||'operator'}`,'muted'));if(a.handoff_topic_id)box.append(el('p',`Handoff topic: ${a.handoff_topic_id}`,'muted'));}
    box.append(el('p',`Profile at launch: revision ${s.policy_revision||0} · Attention: ${transport(s)}`,'muted'));
    const link=el('a','Open terminal →','text-link');link.href=terminal(s);box.append(link);
  }
  function transport(s){
    if(s.attention_transport==='native-queue')return s.native_session_id?'Codex native queue · cooperative pause':'Codex needs its native conversation ID linked';
    if(s.attention_transport==='native-push')return 'Pi native steering · tool-boundary pause';
    if(s.attention_transport==='native-hook')return 'Claude prompt/tool hooks · tool-boundary pause';
    return 'Manual inbox · cooperative acknowledgment';
  }
  function graph(supervisor,workers){
    const box=$('supervision-graph');box.replaceChildren();
    const attached=workers.filter(s=>supervisor&&s.supervisor_id===supervisor.id);
    const others=workers.filter(s=>!supervisor||s.supervisor_id!==supervisor.id);
    if(!supervisor){box.append(el('p','Set the project goal, then start a supervisor to delegate work to Mitos agents.','graph-empty'));}
    else{
      const rows=Math.ceil(attached.length/3), height=135+rows*125;
      const nodeX=i=>{const count=Math.min(3,attached.length-Math.floor(i/3)*3);return 900/(count+1)*(i%3+1);};
      const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox',`0 0 900 ${height}`);svg.setAttribute('aria-label','Supervisor with clickable assignments and agent terminals');svg.setAttribute('role','group');
      const draw=(tag,attrs,text)=>{const n=document.createElementNS(svg.namespaceURI,tag);for(const [k,v]of Object.entries(attrs))n.setAttribute(k,v);if(text)n.textContent=text;svg.append(n);return n;};
      for(let i=0;i<attached.length;i++){
        const s=attached[i],x=nodeX(i),y=190+Math.floor(i/3)*125;
        const edge=draw('path',{d:`M450 90 C${x===450?480:450} ${y-65},${x} ${y-65},${x} ${y-32}`,class:'assignment-edge',tabindex:0,role:'button','aria-label':`Inspect assignment: ${s.name}`});
        edge.onclick=()=>detail(s);edge.onkeydown=e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();detail(s);}};
      }
      function node(s,x,y,supervisorNode){
        const a=draw('a',{href:terminal(s),'aria-label':`Open ${supervisorNode?'supervisor':'agent'} terminal: ${s.name}`});
        function child(tag,attrs,text){const n=document.createElementNS(svg.namespaceURI,tag);for(const[k,v]of Object.entries(attrs))n.setAttribute(k,v);if(text)n.textContent=text;a.append(n);return n;}
        child('rect',{x:x-127,y:y-32,width:254,height:70,rx:10,class:supervisorNode?'supervisor-node':'agent-node'});
        child('text',{x,y:y-6,'text-anchor':'middle',class:'node-title'},s.name.length>29?s.name.slice(0,27)+'…':s.name);
        child('text',{x,y:y+17,'text-anchor':'middle',class:'node-status'},`${supervisorNode?'SUPERVISOR':s.agent_id||'AGENT'} · ${s.status}`);
      }
      node(supervisor,450,52,true);
      attached.forEach((s,i)=>node(s,nodeX(i),190+Math.floor(i/3)*125,false));
      box.append(svg);
      if(!attached.length)box.append(el('p','No assigned agents yet. Give the supervisor a concrete objective in its terminal, or add an agent here.','graph-empty'));
      const choices=el('div',undefined,'assignment-buttons');attached.forEach(s=>choices.append(button(`Assignment: ${s.name}`,()=>detail(s))));box.append(choices);
    }
    if(others.length){box.append(el('h3','Other sessions'));for(const s of others){const row=el('div',undefined,'other-session');const a=el('a',s.name,'text-link');a.href=terminal(s);row.append(a,el('span',s.supervisor_id?'Assigned to an earlier supervisor':'Unassigned','muted'),button('Details',()=>detail(s)));box.append(row);}}
  }
  function render(){
    renderDraft();
    const {sessions,profile,notices}=snapshot, supervisors=sessions.filter(s=>s.role==='coordinator');
    const supervisor=supervisors.find(active)||supervisors.at(-1), workers=sessions.filter(s=>s.role!=='coordinator');
    $('project-objective').textContent=profile.goal||'Start a supervisor to analyze this project, then review its context and define a work goal.';
    $('supervision-summary').replaceChildren(el('span',`${workers.filter(active).length} / ${profile.max_agents} active agents`),el('span',profile.git_mode==='worktree'?'Separate agent worktrees':'Shared project checkout'),el('span',`Profile revision ${profile.revision}`));
    $('start-supervisor').hidden=!!supervisor&&active(supervisor);
    const rail=$('supervisor-list');rail.replaceChildren();
    const overview=el('a',supervisor?supervisor.name:'Set up supervision','session-link supervisor-link');overview.href=`/workspace?project=${project}`;
    if(supervisor){overview.dataset.sessionId=supervisor.id;overview.dataset.sessionName=supervisor.name;overview.setAttribute('aria-haspopup','menu');overview.setAttribute('aria-controls','desk-context-menu');overview.setAttribute('aria-keyshortcuts','Shift+F10');}
    overview.append(el('small',supervisor?`${supervisor.status} · ${transport(supervisor)}`:'Project overview & rules'));rail.append(overview);
    if(supervisor){const link=el('a','Supervisor terminal ↗','supervisor-terminal');link.href=terminal(supervisor);rail.append(link);}
    const graphKey=JSON.stringify(sessions);if(graphKey!==graphSignature){graphSignature=graphKey;graph(supervisor,workers);}
    if(selectedAssignment){const s=sessions.find(s=>s.id===selectedAssignment);if(s)detail(s);}
    const list=$('attention-list');list.replaceChildren();
    if(!notices.length)list.append(el('p','No attention requests. Use the topic board for ordinary progress updates.','muted'));
    for(const n of notices){
      const card=el('article',undefined,'attention-card');const ack=n.targets.filter(t=>t.acknowledged_at).length;
      card.append(el('h4',`${n.pause?(n.released_at?'Released breakpoint':'Breakpoint requested'):'Attention'} · ${ack}/${n.targets.length} acknowledged`),el('p',n.body));
      card.append(el('small',`${n.author} · ${new Date(n.created_at).toLocaleString()} · ${n.id}`));
      const targets=el('details');targets.append(el('summary','Delivery & acknowledgments'));
      for(const t of n.targets){const s=sessions.find(s=>s.id===t.session_id);targets.append(el('p',`${s?.name||t.session_id}: ${t.state.replaceAll('_',' ')}${n.released_at?(t.release_queued?' · release delivered':' · release pending'):''}. ${t.detail||''} ${s?transport(s):''}`));}
      card.append(targets);
      if(n.pause&&!n.released_at)card.append(button('Release breakpoint',async()=>{await api(`/api/attention/${n.id}/release`,{});await refresh();}));
      list.append(card);
    }
  }
  async function refresh(){const next=await api(endpoint('supervision')),key=JSON.stringify(next);snapshot=next;if(key!==signature){signature=key;render();}offerReview();}
  async function poll(){try{await refresh();}catch(e){fail(e);}if(alive)setTimeout(poll,3000);}
  refresh().then(()=>{if(new URLSearchParams(location.search).get('setup')==='true'&&!snapshot?.draft)chooseRole('coordinator');}).catch(fail);
  poll();window.addEventListener('pagehide',()=>{alive=false;});
})();

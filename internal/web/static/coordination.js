(() => {
  'use strict';
  const project=document.body.dataset.project;
  if(!project)return;
  const $=id=>document.getElementById(id), active=s=>['starting','running','stopping','interrupted'].includes(s.status);
  const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;};
  const fail=e=>{$('desk-error').textContent=e.message;$('desk-error').hidden=false;};
  const api=window.MitosConnection.request;
  const endpoint=s=>`/api/projects/${project}/${s}`, terminal=s=>`/workspace?project=${project}&session=${s.id}&view=terminal`;
  const button=(text,fn)=>{const b=el('button',text);b.type='button';b.onclick=()=>Promise.resolve().then(fn).catch(fail);return b;};
  let snapshot, signature='', graphSignature='', profileRevision=0, selectedAssignment='', alive=true, reviewDraft=null, setupKey='', setupPayload='', outputDraft='', outputStatus='';
  const reviewKey=id=>`mitos:review:${project}:${id}`;
  function reviewed(draft){
    if(!draft)return null;
    if(draft.reviewed_profile)return {profile:draft.reviewed_profile,revision:draft.base_revision};
    try{const saved=JSON.parse(sessionStorage.getItem(reviewKey(draft.id)));return saved?.revision===draft.base_revision?saved:null;}catch{return null;}
  }
  function showProfile(draft=null){
    if(!snapshot)return;
    const f=$('profile-form'), p=draft?(reviewed(draft)?.profile||draft.profile):snapshot.profile;
    reviewDraft=draft;profileRevision=draft?draft.base_revision:p.revision;
    $('profile-dialog-title').textContent=draft?'Review context & set a goal':'Project profile';
    $('profile-introduction').value=draft?'Continue to the final launch step when you are ready.':'Changes apply to this project’s agents.';
    $('profile-save').textContent=draft?'Continue':'Save project profile';
    $('profile-goal').required=!!draft;
    for(const [key,value] of Object.entries(p)){const input=f.elements.namedItem(key);if(input)input.value=Array.isArray(value)?value.join('\n'):value;}
    f.querySelector('.form-error').textContent='';$('profile-dialog').showModal();
  }
  $('edit-project-profile').onclick=()=>showProfile();
  if($('return-to-setup'))$('return-to-setup').onclick=()=>$('tab-coordination').click();
  async function submit(form,fn){form.addEventListener('submit',async e=>{e.preventDefault();const b=form.querySelector('[type=submit]'),error=form.querySelector('.form-error');b.disabled=true;error.textContent='';try{await fn(Object.fromEntries(new FormData(form)));}catch(e){error.textContent=e.message;}finally{b.disabled=false;}});}
  submit($('profile-form'),async value=>{
    value.temp_paths=value.temp_paths.split('\n').map(s=>s.trim()).filter(Boolean);
    value.max_agents=Number(value.max_agents);value.min_free_mb=Number(value.min_free_mb);
    if(reviewDraft){sessionStorage.setItem(reviewKey(reviewDraft.id),JSON.stringify({profile:value,revision:profileRevision}));$('profile-dialog').close();render();return;}
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
  window.MitosCoordination={startSetup:async value=>{
    const request={agent_id:value.agent_id,profile_id:value.profile_id,prompt:value.prompt||''};
    const serialized=JSON.stringify(request);if(serialized!==setupPayload||!setupKey){setupPayload=serialized;setupKey=crypto.randomUUID();}
    const d=await api(endpoint('profile-drafts'),{...request,key:setupKey});
    $('session-dialog').close();$('tab-coordination').click();await refresh();
  }};
  function renderDraft(){
    const d=snapshot.draft,box=$('setup-progress'),coordinator=snapshot.sessions.find(s=>s.role==='coordinator'&&active(s));
    const running=!!coordinator,ready=d&&['ready','reviewed'].includes(d.status),review=ready&&reviewed(d);
    const step=running?4:review?3:ready?2:1;
    document.body.dataset.setup=String(!running&&!snapshot.sessions.length);
    $('setup-workflow').hidden=running;
    for(const item of $('setup-steps').children){const n=Number(item.dataset.step);item.classList.toggle('complete',n<step);item.classList.toggle('current',n===step);if(n===step)item.setAttribute('aria-current','step');else item.removeAttribute('aria-current');item.firstElementChild.textContent=n<step?'✓':String(n);}
    const stage=$('workspace-stage');stage.replaceChildren(el('span',undefined,`state-dot ${running?coordinator.status:d?.status==='failed'?'failed':''}`),el('span',running?coordinator.status[0].toUpperCase()+coordinator.status.slice(1):step===3?'Ready to start':step===2?'Review':d?.status==='running'?'Analyzing':'Setup'));
    box.replaceChildren();if(running)return;
    const actions=el('div',undefined,'setup-actions');
    const primary=(label,fn)=>{const b=button(label,fn);b.classList.add('primary');actions.append(b);return b;};
    if(review){
      box.append(el('h2','Ready to start'),el('p',review.profile.goal,'setup-goal'));
      const launch=primary('Start coordinator',async()=>{launch.disabled=true;try{const session=await api(endpoint(`profile-drafts/${d.id}/start`),review);sessionStorage.removeItem(reviewKey(d.id));location.href=terminal(session);}finally{launch.disabled=false;}});launch.id='start-coordinator';
      actions.append(button('Edit context or goal',()=>showProfile(d)));
    }else if(ready){
      box.append(el('h2','Analysis complete'));
      primary('Review context & set goal',()=>showProfile(d));
    }else if(d?.status==='running'){
      box.append(el('h2','Analyzing project…'));
      actions.append(button('Cancel analysis',async()=>{await api(endpoint(`profile-drafts/${d.id}/cancel`),{});setupKey='';await refresh();}));
    }else{
      const failed=d&&['failed','interrupted','cancelled'].includes(d.status);
      box.append(el('h2',failed?({failed:'Project analysis failed',interrupted:'Analysis interrupted',cancelled:'Analysis cancelled'}[d.status]):'Set up your project'));
      if(failed&&d.detail)box.append(el('p',d.detail,'setup-error-detail'));
      primary(failed?'Retry analysis':'Analyze project',()=>{setupKey='';chooseRole('coordinator');});
    }
    box.append(actions);
  }
  function updateDraftClock(){
    const d=snapshot?.draft;if(!d||d.status==='started')return;
    const elapsed=Math.max(0,Math.floor(((d.finished_at?new Date(d.finished_at):new Date())-new Date(d.created_at))/1000));
    $('setup-output-status').textContent=`${d.agent_id} · ${d.status} · ${Math.floor(elapsed/60)}m ${elapsed%60}s`;
  }
  async function refreshDraftOutput(){
    const d=snapshot?.draft,panel=$('setup-output-panel');panel.hidden=!d||d.status==='started';if(panel.hidden){window.MitosConnection.recovered('analysis-output');return;}
    if(outputDraft!==d.id||outputStatus!==d.status){panel.open=d.status==='running'||d.status==='failed';outputDraft=d.id;outputStatus=d.status;}
    updateDraftClock();
    try{
      const output=await api(endpoint(`profile-drafts/${d.id}/output`));if(snapshot?.draft?.id!==d.id)return;
      window.MitosConnection.recovered('analysis-output');
      window.MitosAnalysisOutput.render(output,d);
      $('setup-output-note').hidden=!output.truncated;$('setup-output-note').textContent=output.truncated?'Earlier activity was trimmed.':'';
    }catch(e){window.MitosConnection.failed('analysis-output',e);$('setup-output-note').hidden=false;$('setup-output-note').textContent='Reconnecting…';}
  }
  function updateSelection(){
    const coordination=document.body.dataset.view==='coordination';
    document.querySelectorAll('#coordinator-list a').forEach(a=>{
      const selected=coordination||a.dataset.sessionId===document.body.dataset.session;
      a.classList.toggle('selected',selected);
      if(selected)a.setAttribute('aria-current','page');else a.removeAttribute('aria-current');
    });
  }
  document.querySelectorAll('[data-tab]').forEach(tab=>tab.addEventListener('click',updateSelection));
  function detail(s){
    selectedAssignment=s.id;
    const box=$('assignment-detail');box.hidden=false;box.replaceChildren(el('h3',s.name),el('p',s.initial_prompt||'No task prompt recorded.'));
    box.append(el('p',`Coordinator: ${snapshot.sessions.find(v=>v.id===s.coordinator_id)?.name||'Unassigned'} · ${s.status}`,'muted'),el('code',s.workspace||'Project checkout'));
    if(s.assignment){const a=s.assignment;box.append(el('p',`Goal: ${a.goal_id||'–'} · Task: ${a.task_id||'–'} · Requested by: ${a.requested_by||'operator'}`,'muted'));if(a.handoff_topic_id)box.append(el('p',`Handoff topic: ${a.handoff_topic_id}`,'muted'));}
    box.append(el('p',`Profile at launch: revision ${s.policy_revision||0} · Attention: ${transport(s)}`,'muted'));
    const link=el('a','Open terminal →','text-link');link.href=terminal(s);box.append(link);
  }
  function transport(s){
    if(s.attention_transport==='native-queue')return s.native_session_id?'Codex native queue · cooperative pause':'Codex needs its native conversation ID linked';
    if(s.attention_transport==='native-push')return 'Pi native steering · tool-boundary pause';
    if(s.attention_transport==='native-hook')return 'Claude prompt/tool hooks · tool-boundary pause';
    return 'Manual inbox · cooperative acknowledgment';
  }
  function graph(coordinator,workers){
    const box=$('coordination-graph');box.replaceChildren();
    const attached=workers.filter(s=>coordinator&&s.coordinator_id===coordinator.id);
    const others=workers.filter(s=>!coordinator||s.coordinator_id!==coordinator.id);
    if(!coordinator){box.append(el('p',snapshot.draft?.status==='running'?'Project analysis is running above. After review, the selected agent becomes the coordinator and its terminal opens here.':'The coordinator is a coding agent that coordinates other agents. Complete setup to open its terminal.','graph-empty'));}
    else{
      const rows=Math.ceil(attached.length/3), height=135+rows*125;
      const nodeX=i=>{const count=Math.min(3,attached.length-Math.floor(i/3)*3);return 900/(count+1)*(i%3+1);};
      const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox',`0 0 900 ${height}`);svg.setAttribute('aria-label','Coordinator with clickable assignments and agent terminals');svg.setAttribute('role','group');
      const draw=(tag,attrs,text)=>{const n=document.createElementNS(svg.namespaceURI,tag);for(const [k,v]of Object.entries(attrs))n.setAttribute(k,v);if(text)n.textContent=text;svg.append(n);return n;};
      for(let i=0;i<attached.length;i++){
        const s=attached[i],x=nodeX(i),y=190+Math.floor(i/3)*125;
        const edge=draw('path',{d:`M450 90 C${x===450?480:450} ${y-65},${x} ${y-65},${x} ${y-32}`,class:'assignment-edge',tabindex:0,role:'button','aria-label':`Inspect assignment: ${s.name}`});
        edge.onclick=()=>detail(s);edge.onkeydown=e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();detail(s);}};
      }
      function node(s,x,y,coordinatorNode){
        const a=draw('a',{href:terminal(s),'aria-label':`Open ${coordinatorNode?'coordinator':'agent'} terminal: ${s.name}`});
        function child(tag,attrs,text){const n=document.createElementNS(svg.namespaceURI,tag);for(const[k,v]of Object.entries(attrs))n.setAttribute(k,v);if(text)n.textContent=text;a.append(n);return n;}
        child('rect',{x:x-127,y:y-32,width:254,height:70,rx:10,class:coordinatorNode?'coordinator-node':'agent-node'});
        child('text',{x,y:y-6,'text-anchor':'middle',class:'node-title'},s.name.length>29?s.name.slice(0,27)+'…':s.name);
        child('text',{x,y:y+17,'text-anchor':'middle',class:'node-status'},`${coordinatorNode?'COORDINATOR':s.agent_id||'AGENT'} · ${s.status}`);
      }
      node(coordinator,450,52,true);
      attached.forEach((s,i)=>node(s,nodeX(i),190+Math.floor(i/3)*125,false));
      box.append(svg);
      if(!attached.length)box.append(el('p','No workers delegated yet.','graph-empty'));
      const choices=el('div',undefined,'assignment-buttons');attached.forEach(s=>choices.append(button(`Assignment: ${s.name}`,()=>detail(s))));box.append(choices);
    }
    if(others.length){box.append(el('h3','Other sessions'));for(const s of others){const row=el('div',undefined,'other-session');const a=el('a',s.name,'text-link');a.href=terminal(s);row.append(a,el('span',s.coordinator_id?'Assigned to an earlier coordinator':'Unassigned','muted'),button('Details',()=>detail(s)));box.append(row);}}
  }
  function render(){
    renderDraft();
    const {sessions,profile,notices}=snapshot, coordinators=sessions.filter(s=>s.role==='coordinator');
    const coordinator=coordinators.find(active)||coordinators.at(-1), workers=sessions.filter(s=>s.role!=='coordinator');
    $('coordination-workspace').hidden=!sessions.length;$('project-objective').textContent=profile.goal||'';$('project-objective').hidden=!profile.goal;
    $('active-agent-count').textContent=`${workers.filter(active).length} active`;
    const rail=$('coordinator-list');rail.replaceChildren();
    const overview=el('a',coordinator?coordinator.name:'Project setup','session-link coordinator-link');overview.href=coordinator?terminal(coordinator):`/workspace?project=${project}`;
    if(coordinator){overview.dataset.sessionId=coordinator.id;overview.dataset.sessionName=coordinator.name;overview.setAttribute('aria-haspopup','menu');overview.setAttribute('aria-controls','desk-context-menu');overview.setAttribute('aria-keyshortcuts','Shift+F10');}
    overview.append(el('small',coordinator?`${coordinator.agent_id} · ${coordinator.status}`:snapshot.draft?`${snapshot.draft.agent_id} · analysis ${snapshot.draft.status}`:'Project overview & rules'));rail.append(overview);
    updateSelection();
    const graphKey=JSON.stringify([sessions,snapshot.draft?.status]);if(graphKey!==graphSignature){graphSignature=graphKey;graph(coordinator,workers);}
    if(selectedAssignment){const s=sessions.find(s=>s.id===selectedAssignment);if(s)detail(s);}
    const list=$('attention-list');list.replaceChildren();
    if(!notices.length)list.append(el('p','No messages.','muted'));
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
  async function refresh(){const next=await api(endpoint('coordination')),key=JSON.stringify(next);snapshot=next;if(key!==signature){signature=key;render();}await refreshDraftOutput();}
  async function poll(){try{await refresh();window.MitosConnection.recovered('coordination');}catch(e){window.MitosConnection.failed('coordination',e);}if(alive)setTimeout(poll,3000);}
  refresh().then(()=>{if(new URLSearchParams(location.search).get('setup')==='true'&&!snapshot?.draft)chooseRole('coordinator');}).catch(e=>window.MitosConnection.failed('coordination',e)).finally(()=>{if(alive)setTimeout(poll,3000);});
  const clockTimer=setInterval(updateDraftClock,1000);
  window.addEventListener('pagehide',()=>{alive=false;clearInterval(clockTimer);});
})();

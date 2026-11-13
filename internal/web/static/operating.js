(() => {
 'use strict';
 const $=id=>document.getElementById(id),project=document.body.dataset.project,serverID=document.body.dataset.server,section=document.body.dataset.section;
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;};
 const link=(text,href,cls='text-link')=>{const a=el('a',text,cls);a.href=href;return a;};
 const button=(text,fn,cls='')=>{const b=el('button',text,cls);b.type='button';b.onclick=()=>Promise.resolve().then(fn).catch(showError);return b;};
 const key=()=>crypto.randomUUID(),active=s=>s&&['starting','running','stopping','interrupted'].includes(s.status);
 const labels={queued:'Ready to start',starting:'Starting',running:'Working',stopping:'Stopping',needs_executor:'Needs executor',needs_inspection:'Needs inspection',reported_complete:'Review pending',accepted:'Accepted',cancelled:'Cancelled',done:'Accepted',parked:'Needs decision',blocked:'Waiting',failed:'Failed'};
 const state=s=>el('span',labels[s]||s,'state '+s),date=v=>v?new Date(v).toLocaleString():'';
 const terminal=s=>`/workspace?project=${encodeURIComponent(project||s.project_id)}&session=${encodeURIComponent(s.id)}&view=terminal`;
 const endpoint=path=>`/api/projects/${encodeURIComponent(project)}/${path}`;
 let home,snapshot,selected=document.body.dataset.task||'',detailTab=new URLSearchParams(location.search).get('tab')==='result'?'result':'assignment',view=new URLSearchParams(location.search).get('view')||'work',lastObserved='',offset=0,alive=true,signature='',homeSignature='';
 async function api(path,body,method='POST'){
  const control=new AbortController(),timer=setTimeout(()=>control.abort(),15000);
  try{
   const r=await fetch(path,{method:body===undefined?'GET':method,headers:{'Content-Type':'application/json','X-Mitos-Server':serverID},body:body===undefined?undefined:JSON.stringify(body),cache:'no-store',signal:control.signal});
   const value=await r.json();if(!r.ok)throw Error(value.error||`Request refused (${r.status})`);
   if(value.server&&value.server.id!==serverID)throw Error('Home server identity changed. Reconnect before continuing.');
   return value;
  }catch(e){if(e.name==='AbortError'||e instanceof TypeError)throw Error(body===undefined?'Server unreachable.':'Response not received. Check the original operation before retrying; do not start another request.');throw e;}finally{clearTimeout(timer)}
 }
 function showError(e){$('action-error').textContent=e.message;$('action-error').hidden=false;}
 function clearError(){$('action-error').hidden=true;}
 function connection(ok){$('connection-warning').hidden=ok;$('server-status').classList.toggle('stale',!ok);if(!ok)$('connection-warning').textContent=`Connection lost · showing state from ${date(lastObserved)||'the last refresh'}. Execution status is unknown until this server reconnects.`;}
 function dialog(id){clearError();const d=$(id);d.querySelector('.form-error').textContent='';d.showModal();}
 document.querySelectorAll('[data-close]').forEach(b=>b.onclick=()=>b.closest('dialog').close());
 async function submit(form,fn){const b=form.querySelector('[type=submit]');b.disabled=true;form.querySelector('.form-error').textContent='';try{await fn()}catch(e){form.querySelector('.form-error').textContent=e.message}finally{b.disabled=false}}
 function empty(title,text,action){const n=el('div',undefined,'empty');n.append(el('h2',title),el('p',text));if(action)n.append(action);return n;}
 function projectCounts(c){const parts=[];for(const [s,label]of [['accepted','accepted'],['running','working'],['reported_complete','to review'],['needs_executor','need an executor'],['needs_inspection','need inspection'],['queued','ready']])if(c[s])parts.push(`${c[s]} ${label}`);return parts.join(' · ')||'No active work';}
 async function refreshHome(){
  home=await api('/api/workspace-home');lastObserved=home.observed_at;
  const next=JSON.stringify([home.projects,home.decisions]);if(next===homeSignature)return;homeSignature=next;
  const nav=$('projects');nav.replaceChildren();for(const row of home.projects){const a=link(row.project.name,'/projects/'+row.project.id,'');a.append(el('small',home.server.name));if(row.project.id===project)a.setAttribute('aria-current','page');nav.append(a)}
  $('inbox-count').textContent=home.decisions.length||'';
  if(project)return;
  const content=$('home-content');content.replaceChildren();
  if(section==='connections'){
   const panel=el('section',undefined,'panel connections-panel');panel.append(el('h2',home.server.name),el('p','This workstation · connected','muted'),el('p','Your projects and agents run on this device.','muted'));
   const details=el('details');details.append(el('summary','Technical details'),el('code',home.server.id));for(const [k,v]of Object.entries(home.server.capabilities||{})){const row=el('div',undefined,'capability');row.append(el('span',k.replaceAll('_',' ')),el('span',v?'Available':'Not available','muted'));details.append(row)}panel.append(details);content.append(panel);return;
  }
  if(section==='inbox'){
   if(!home.decisions.length)content.append(empty('Nothing needs your decision','Pending choices appear here with their project and home server.'));
   for(const d of home.decisions){const card=el('article',undefined,'panel inbox-card');card.append(el('small',home.server.name+' · '+(home.projects.find(p=>p.project.id===d.project_id)?.project.name||'Batch work')),el('h2',d.title),el('p',d.question),link('Review →',d.graph_id?`/projects/${d.project_id}?graph=${d.graph_id}`:d.task_id?`/projects/${d.project_id}?task=${d.task_id}&tab=result`:'/runs/'+d.run_id));content.append(card)}return;
  }
  if(!home.projects.length){content.append(empty('Give your work a home','Open a project on this server to set an outcome, run workers, and keep their results.',button('Open project',()=>dialog('project-dialog'),'primary')));return;}
  const grid=el('div',undefined,'project-grid');for(const row of home.projects){const card=link('','/projects/'+row.project.id,'project-card');card.append(el('h2',row.project.name),el('small',home.server.name),el('small',row.project.directory),el('p',projectCounts(row.counts)));if(row.unbound)card.append(el('small',`${row.unbound} earlier sessions`));grid.append(card)}content.append(grid);
 }
 $('open-project').onclick=e=>{e.preventDefault();dialog('project-dialog')};if(new URLSearchParams(location.search).get('open')==='project')dialog('project-dialog');$('open-project-main')?.addEventListener('click',()=>dialog('project-dialog'));
 $('project-form').onsubmit=e=>{e.preventDefault();const f=e.target;submit(f,async()=>{const p=await api('/api/projects',{name:f.elements.name.value,directory:f.elements.directory.value});location.href='/projects/'+p.id})};
 function currentTask(){return snapshot?.work.tasks.find(t=>t.id===selected)}
 function selectTask(id){if($('earlier-work'))$('earlier-work').open=true;selected=id;detailTab='assignment';signature='';const u=new URL(location);u.searchParams.delete('graph');u.searchParams.delete('node');u.searchParams.set('task',id);history.replaceState(null,'',u);refreshProject().catch(showError)}
 function renderWork(){
  const w=snapshot.work,counts={};for(const t of w.tasks)counts[t.status]=(counts[t.status]||0)+1;$('work-counts').textContent=projectCounts(counts);
  const resp=$('responsibilities');resp.replaceChildren();for(const r of w.responsibilities){const t=w.tasks.find(t=>t.id===r.task_id);if(!t||['accepted','cancelled'].includes(t.status))continue;const s=w.sessions.find(s=>s.id===r.coordinator_session_id);const row=el('article',undefined,'responsibility'),info=el('div');info.append(el('h3',r.title),el('p',s?`Coordinator: ${s.command} · ${s.status}`:'Coordination ready · no coordinator running'));row.append(info);const actions=el('div',undefined,'actions');actions.append(button('Inbox'+(r.messages.length?` · ${r.messages.length}`:''),()=>openMessages(r)));if(s)actions.append(link('Open session',terminal(s)));actions.append(button(active(s)?'View work':'Assign coordinator',()=>active(s)?selectTask(t.id):openWork(t)));row.append(actions);resp.append(row)}
  const list=$('work-list'),query=$('work-search').value.toLowerCase();list.replaceChildren();
  if(!w.tasks.length&&!snapshot.batch_tasks.length)list.append(empty('What should this project achieve?','Create a bounded assignment or maintain an ongoing responsibility.',button('New native session',()=>openWork(),'primary')));
  for(const goal of w.goals){const tasks=w.tasks.filter(t=>t.goal_id===goal.id&&(!query||(t.title+' '+t.intent).toLowerCase().includes(query)));if(!tasks.length)continue;
   const group=el(tasks.length>1?'details':'div',undefined,'goal-group');if(tasks.length>1){group.open=true;group.append(el('summary',goal.title,'goal-heading'));}
   // Parentage comes from admitted assignments, never inferred from terminal text.
   const append=(t,depth)=>{const row=button('',()=>selectTask(t.id),'task-row'+(depth?' child':'')+(selected===t.id?' selected':''));const text=el('span',t.title);const s=w.sessions.find(s=>s.id===t.current_session_id);text.append(el('small',s?`${s.command} · attempt ${s.generation}`:t.role==='coordinator'?'Coordinated work':'Unassigned'));row.append(text,state(t.status));row.setAttribute('aria-pressed',String(selected===t.id));group.append(row);tasks.filter(c=>c.parent_id===t.id).forEach(c=>append(c,depth+1));};
   tasks.filter(t=>!tasks.some(p=>p.id===t.parent_id)).forEach(t=>append(t,0));list.append(group);
  }
  if(snapshot.batch_tasks.length){const box=el('section',undefined,'goal-group');box.append(el('h2','Application work','goal-heading'));for(const t of snapshot.batch_tasks){const a=link('','/runs/'+t.current_run_id,'task-row');a.append(el('span',t.payload.title),state(t.status));box.append(a)}list.append(box)}
  const legacy=w.sessions.filter(s=>!s.task_id);$('unbound').hidden=!legacy.length;$('unbound-list').replaceChildren();for(const s of legacy){const row=el('div',undefined,'legacy-row');row.append(link(s.name,terminal(s)),state(s.status));$('unbound-list').append(row)}
  $('work-pagination').replaceChildren();if(snapshot.total>200){$('work-pagination').append(el('p',`Showing ${offset+1}–${snapshot.next_offset} of ${snapshot.total} tasks`,'muted'));if(offset>0)$('work-pagination').append(button('Previous',()=>{offset=Math.max(0,offset-200);signature='';return refreshProject()}));if(snapshot.next_offset<snapshot.total)$('work-pagination').append(button('Next',()=>{offset=snapshot.next_offset;signature='';return refreshProject()}));}
  renderTask();
 }
 function renderTask(){const t=currentTask(),box=$('task-detail');box.hidden=!t;if(!t)return;box.replaceChildren();
  const meta=el('div',undefined,'detail-meta');meta.append(state(t.status),button('×',()=>{selected='';history.replaceState(null,'',location.pathname);renderTask()},''));box.append(meta,el('h2',t.title));
  const s=snapshot.work.sessions.find(s=>s.id===t.current_session_id),actions=el('div',undefined,'actions');
  if(s)actions.append(link('Open session',terminal(s),'button'));
  if(!active(s)&&!['accepted','cancelled'].includes(t.status))actions.append(button(s?'Replace executor':'Start executor',()=>openWork(t),'primary'));
  if(s&&['running','starting'].includes(s.status))actions.append(button('Stop process',async()=>{await api('/api/sessions/'+s.id+'/stop',{});await refreshProject()}));
  if(s?.status==='interrupted')actions.append(link('Inspect previous process',terminal(s)));
  if(!['accepted','cancelled'].includes(t.status)){if(s)actions.append(button('Submit report',()=>openResult('report')));actions.append(button('Add subtask',()=>openWork(null,t)));if(!active(s))actions.append(button('Cancel task',async()=>{await api(endpoint(`work/${t.id}/cancel`),{revision:t.revision});await refreshProject()}))}
  box.append(actions);const tabs=el('nav',undefined,'detail-tabs');for(const [id,label]of [['assignment','Assignment'],['result','Result'],['attempts','Attempts']]){const b=button(label,()=>{detailTab=id;renderTask()});if(id===detailTab)b.setAttribute('aria-current','page');tabs.append(b)}box.append(tabs);
  if(detailTab==='assignment'){const intent=el('section',undefined,'detail-section');intent.append(el('h3','Assignment'),el('p',t.intent,'prose'));const criteria=el('section',undefined,'detail-section');criteria.append(el('h3','Completion criteria'),el('p',t.criteria,'prose'));box.append(intent,criteria,el('p','Execution: trusted local · host access is not confined','muted'));if(t.status==='needs_executor')box.append(el('p','The previous process ended. This assignment remains open.','muted'));}
  if(detailTab==='result'){
   if(!t.result)box.append(el('p','No report has been submitted. Process exit does not establish completion.','muted'));
   else{const r=t.result;box.append(el('p',r.accepted_at?'Accepted report · human review':r.session_id!==t.current_session_id?'Earlier attempt report':'Submitted report · acceptance pending','muted'));if(window.MitosFormat)box.append(window.MitosMarkdown.view(r.body,{filename:'Report.md'}));else box.append(el('p',r.body,'prose'));const proof=el('details');proof.append(el('summary','Evidence and scope'),el('p','This review covers the frozen report. It does not verify the current checkout or claim independent test execution.','muted'),el('code',r.digest));if(r.review)proof.append(el('p',r.review,'prose'),el('p',date(r.accepted_at),'muted'));box.append(proof);if(!r.accepted_at&&s&&!active(s)&&r.session_id===s.id)box.append(button('Review report',()=>openResult('accept'),'primary'))}
  }
  if(detailTab==='attempts'){const sessions=snapshot.work.sessions.filter(s=>s.task_id===t.id).reverse();if(!sessions.length)box.append(el('p','No execution attempts yet.','muted'));for(const attempt of sessions){const row=el('article',undefined,'record');row.append(el('h3',`${attempt.command} · attempt ${attempt.generation}`),el('p',`${attempt.status} · ${date(attempt.created_at)}`,'muted'),el('code',attempt.attempt_id+' · '+attempt.process_id),link('Open saved session →',terminal(attempt)));for(const report of [...(t.previous_reports||[]),...(t.result?[t.result]:[])].filter(r=>r.session_id===attempt.id)){const d=el('details');d.append(el('summary','Report · '+date(report.submitted_at)));d.append(window.MitosFormat?window.MitosMarkdown.view(report.body,{filename:'Report.md'}):el('p',report.body,'prose'));row.append(d)}box.append(row)}}
 }
 let projectRequest=0,projectApplied=0;
 let supervisionSignature='';
 async function refreshSupervision(){
  const data=await api(endpoint('supervision/incidents')),panel=$('supervision-panel'),sig=JSON.stringify(data);if(sig===supervisionSignature)return;supervisionSignature=sig;
  panel.replaceChildren();panel.hidden=!data.incidents.length;
  for(const id of ['start-agent-session','new-work','new-session-work']){if($(id))$(id).disabled=data.held}
  if(panel.hidden)return;
  panel.append(el('h2',data.held?'Project held by supervision':'Supervision incidents'),el('p',data.coverage));
  for(const incident of data.incidents){
   const item=el('details');item.open=!incident.resolved_at;item.append(el('summary',incident.verdict.reason+' · '+incident.state));
   item.append(el('p',incident.stop_confirmed?'Owned termination confirmed.':incident.resolved_at?'Resolution recorded; see operator note.':'Termination is not fully confirmed. Inspect outstanding processes.'));
   for(const target of incident.targets)item.append(el('p',target.kind+' '+target.id+': '+target.detail));
   const evidence=el('details');evidence.append(el('summary','Review evidence'),el('pre',JSON.stringify(incident.verdict.evidence,null,2)));item.append(evidence);
   if(incident.resolved_at)item.append(el('p',incident.resolution));else{
    const note=el('textarea');note.placeholder='Explain what was inspected or corrected';note.maxLength=4000;note.setAttribute('aria-label','Incident resolution');
    const label=el('label','I verified that outstanding processes have stopped'),confirmed=el('input');confirmed.type='checkbox';label.prepend(confirmed);
    item.append(note,label,button('Resolve incident',async()=>{await api(endpoint('supervision/incidents/'+incident.id+'/resolve'),{note:note.value,confirmed_stopped:confirmed.checked});await refreshSupervision()}));
   }
   panel.append(item);
  }
 }
 async function refreshProject(){if(!project)return;await refreshSupervision();const request=++projectRequest;const result=await api(endpoint('work')+`?offset=${offset}&task=${encodeURIComponent(selected)}`);if(request<projectApplied)return;projectApplied=request;snapshot=result;lastObserved=snapshot.observed_at;const next=JSON.stringify([snapshot.work,snapshot.batch_tasks]);if(next!==signature){signature=next;renderWork()}}
 $('work-search')?.addEventListener('input',renderWork);
 let editing=null,parentTask=null,reviewed=false,createKey='',launchKey='',createdTask=null,agents=[];
 async function openWork(task=null,parent=null){editing=task;parentTask=parent;reviewed=!!task;createdTask=task;createKey=key();launchKey=key();const f=$('work-form');f.reset();$('work-fields').hidden=!!task;$('work-dialog-title').textContent=task?'Assign an executor':parent?'New subtask':'New work';f.elements.title.value=task?.title||'';f.elements.intent.value=task?.intent||'';f.elements.criteria.value=task?.criteria||'';f.elements.role.value=task?.role||'agent';for(const n of ['title','intent','criteria'])f.elements[n].required=!task;$('launch-review').hidden=!task;$('edit-work').hidden=true;$('work-submit').textContent=task?'Start executor':'Review work';if(task)$('launch-review').textContent=task.intent+'\n\nCriteria: '+task.criteria;
  dialog('work-dialog');$('work-submit').disabled=true;
  try{agents=await api('/api/agents?project='+project);f.elements.agent_id.replaceChildren();for(const a of agents.filter(a=>a.installed)){const o=el('option',a.name);o.value=a.id;f.elements.agent_id.append(o)}if(!f.elements.agent_id.value)throw Error('No installed executor is available. Configure an agent application first.');updateProfiles();}catch(e){f.querySelector('.form-error').textContent=e.message}finally{$('work-submit').disabled=!f.elements.agent_id.value}
 }
 function updateProfiles(){const f=$('work-form'),a=agents.find(a=>a.id===f.elements.agent_id.value);f.elements.profile_id.replaceChildren(new Option('Native configuration',''));for(const p of a?.profiles||[])f.elements.profile_id.append(new Option(p.name||p.id,p.id))}
 $('work-form').elements.agent_id.onchange=updateProfiles;
 let bridgeAgents=[], bridgeLaunchKey='';
 const bridgeForm=$('agent-session-form');
 function bridgeProfiles(){const a=bridgeAgents.find(a=>a.id===bridgeForm.elements.agent_id.value);bridgeForm.elements.profile_id.replaceChildren(new Option('Native configuration',''));for(const p of a?.profiles||[])bridgeForm.elements.profile_id.append(new Option(p.name||p.id,p.id))}
 bridgeForm.elements.agent_id.onchange=bridgeProfiles;
 $('start-agent-session')?.addEventListener('click',async()=>{bridgeForm.reset();bridgeLaunchKey=key();bridgeForm.querySelector('.form-error').textContent='';dialog('agent-session-dialog');try{bridgeAgents=(await api('/api/agents?project='+project)).filter(a=>a.installed&&['codex','claude'].includes(a.id));bridgeForm.elements.agent_id.replaceChildren(...bridgeAgents.map(a=>new Option(a.name,a.id)));bridgeProfiles();if(!bridgeAgents.length)throw Error('Install Codex or Claude to start an agent session.')}catch(e){bridgeForm.querySelector('.form-error').textContent=e.message}});
 bridgeForm.onsubmit=e=>{e.preventDefault();submit(bridgeForm,async()=>{const s=await api(endpoint('sessions'),{key:bridgeLaunchKey,agent_id:bridgeForm.elements.agent_id.value,profile_id:bridgeForm.elements.profile_id.value,prompt:bridgeForm.elements.prompt.value,bridge:true});window.location.href=terminal(s)})};
 $('new-session-work')?.addEventListener('click',()=>openWork().catch(showError));
 $('edit-work').onclick=()=>{reviewed=false;$('work-fields').hidden=false;$('launch-review').hidden=true;$('edit-work').hidden=true;$('work-submit').textContent='Review work';};
 $('work-form').onsubmit=e=>{e.preventDefault();const f=e.target;submit(f,async()=>{
  if(!reviewed){reviewed=true;$('work-fields').hidden=true;$('launch-review').textContent=f.elements.title.value+'\n\n'+f.elements.intent.value+'\n\nCriteria: '+f.elements.criteria.value+'\n\n'+(f.elements.role.value==='coordinator'?'Coordinator with delegated work':'One worker');$('launch-review').hidden=false;$('edit-work').hidden=false;$('work-submit').textContent='Start work';return}
  if(!createdTask)createdTask=await api(endpoint('work'),{key:createKey,title:f.elements.title.value,intent:f.elements.intent.value,criteria:f.elements.criteria.value,role:f.elements.role.value,parent_id:parentTask?.id||''});
  $('edit-work').hidden=true;const t=createdTask;await api(endpoint('sessions'),{key:launchKey,agent_id:f.elements.agent_id.value,profile_id:f.elements.profile_id.value,prompt:t.intent,role:t.role||'agent',expected_revision:t.revision,assignment:{task_id:t.id,goal_id:t.goal_id,requested_by:'operator'}});
  $('work-dialog').close();selected=t.id;signature='';await refreshProject();
 })};
 let resultAction='',resultTask=null;
 async function openResult(action){await refreshProject();resultAction=action;resultTask=currentTask();$('result-form').reset();$('result-title').textContent=action==='accept'?'Review the submitted report':'Submit a report';$('result-context').textContent=action==='accept'?'Record how this exact report satisfies the criteria. Acceptance covers this report, not unverified workspace changes.':'Submit findings and evidence for the current attempt. This does not mark the task accepted.';$('result-label-text').textContent=action==='accept'?'Review notes':'Report';$('result-submit').textContent=action==='accept'?'Accept report':'Submit report';dialog('result-dialog')}
 $('result-form').onsubmit=e=>{e.preventDefault();submit(e.target,async()=>{const t=resultTask;await api(endpoint(`work/${t.id}/${resultAction}`),{revision:t.revision,session_id:t.current_session_id,body:e.target.elements.body.value,digest:t.result?.digest||''});$('result-dialog').close();detailTab='result';signature='';await refreshProject()})};
 let responsibility=null;
 function openMessages(r){responsibility=r;$('message-history').replaceChildren();for(const m of r.messages){const row=el('div',undefined,'record');row.append(el('p',m.body,'prose'),el('small',date(m.created_at)));$('message-history').append(row)}$('message-form').elements.body.value='';dialog('message-dialog')}
 $('message-form').onsubmit=e=>{e.preventDefault();submit(e.target,async()=>{await api(endpoint(`responsibilities/${responsibility.id}/messages`),{body:e.target.elements.body.value});$('message-dialog').close();signature='';await refreshProject()})};
 let services;
 async function refreshServices(){services=await api('/api/services');const list=$('service-list');list.replaceChildren();const units=services.catalog.config.units||[],bindings=services.catalog.work||{};let count=0;for(const u of units){const binding=bindings[u.id];if(binding?.project_id!==project)continue;count++;const runtime=(services.states||[]).find(v=>v.service_id===u.id),desired=services.desired.rows.find(v=>v.id===u.id);const card=el('article',undefined,'service-card');card.append(el('h2',u.description||u.id),el('p',binding.intent,'muted'));const facts=el('div',undefined,'service-facts');for(const [label,value]of [['Configuration',desired?.state==='active'?'Enabled':desired?.state||'Disabled'],['Execution',runtime?.lifecycle_stops?.length?'Operational hold':runtime?.in_flight?`${runtime.in_flight} active`:'Idle'],['Activation',u.trigger],['Next',runtime?.next_at?date(runtime.next_at):'On request']]){const n=el('div',label);n.append(el('strong',value));facts.append(n)}card.append(facts);const actions=el('div',undefined,'actions');actions.append(button('Run now',async()=>{await api('/api/services/'+u.id+'/trigger',{idempotency_key:key()});await refreshServices()}));for(const [action,label]of [['stop','Stop'],['resume','Resume'],['restart','Restart']])actions.append(button(label,async()=>{await api('/api/services/'+u.id+'/lifecycle',{action,idempotency_key:key()});await refreshServices()}));if(runtime?.last_run_id)actions.append(link('Latest result','/runs/'+runtime.last_run_id));card.append(actions);list.append(card)}if(!count)list.append(empty('No scheduled jobs','Automations run a configured plugin repeatedly. Start ordinary agent work from Conversation.',button('Schedule a job',()=>openService(),'primary')))}
 async function openService(){if(!services)services=await api('/api/services');const apps=await api('/api/apps');const f=$('service-form');f.reset();f.elements.app_id.replaceChildren();for(const app of apps){f.elements.app_id.append(new Option(app.name,app.id))}f.dataset.id='service-'+key();dialog('service-dialog')}
 $('new-service')?.addEventListener('click',()=>openService().catch(showError));$('service-form').elements.trigger.onchange=e=>$('interval-label').hidden=e.target.value!=='interval';
 $('service-form').onsubmit=e=>{e.preventDefault();const f=e.target;submit(f,async()=>{const catalog=structuredClone(services.catalog);catalog.config.units??=[];catalog.config.intent??={};catalog.config.intent.enable??=[];catalog.work??={};const id=f.dataset.id,trigger=f.elements.trigger.value;const unit={id,description:f.elements.name.value,scope:'project',trigger,concurrency:{max_instances:1,if_running:'skip',queue_max:10}};if(trigger==='interval')unit.schedule={type:'interval',interval:Number(f.elements.interval_seconds.value)};catalog.config.units.push(unit);catalog.config.intent.enable.push(id);catalog.work[id]={project_id:project,title:f.elements.name.value,intent:f.elements.intent.value,workspace:snapshot.project.directory,app_id:f.elements.app_id.value,timeout_minutes:10};await api('/api/services',{expected_revision:services.catalog.revision,catalog},'PUT');$('service-dialog').close();await refreshServices()})};
 async function refreshResources(){await window.MitosProjectSetup.render({container:$('resource-list'),project,snapshot,api,onChanges:()=>selectView('changes')});}
 let changesRequest=0;
 async function refreshChanges(){
  const requestID=++changesRequest;
  const source=$('changes-source').value;
  if(source==='workspace'){const data=await api(endpoint('resources')+'?details=true');if(requestID!==changesRequest)return;window.MitosDiff.render(data);$('changes-context').textContent='Project working tree compared with HEAD. Agent workspace copies are separate.'}
  else{const response=await fetch(source);if(!response.ok)throw Error('Saved patch unavailable.');const diff=await response.text();if(requestID!==changesRequest)return;window.MitosDiff.render({diff});$('changes-context').textContent='Saved agent patch: original snapshot compared with the agent’s result. This has not been applied to the project.'}
 }
 async function selectView(next){
  view=['work','workflow','result','changes','services','resources'].includes(next)?next:'work';
  document.querySelectorAll('[data-view]').forEach(n=>{if(n.dataset.view===view)n.setAttribute('aria-current','page');else n.removeAttribute('aria-current')});
  for(const id of ['work','services','resources','changes'])$(id+'-view').hidden=id==='work'?!['work','workflow','result'].includes(view):id!==view;
  const url=new URL(location.href);url.searchParams.set('view',view);history.replaceState(null,'',url);
  document.dispatchEvent(new CustomEvent('mitos-view',{detail:view}));
  try{if(view==='services')await refreshServices();if(view==='resources'){if(!snapshot)await refreshProject();await refreshResources()}if(view==='changes')await refreshChanges()}catch(e){showError(e)}
 }
 window.MitosProjectNav={select:selectView,current:()=>view};
 document.querySelectorAll('[data-view]').forEach(b=>b.onclick=()=>selectView(b.dataset.view));
 $('refresh-changes')?.addEventListener('click',()=>refreshChanges().catch(showError));$('changes-source')?.addEventListener('change',()=>refreshChanges().catch(showError));
 document.addEventListener('mitos-graph',event=>{const g=event.detail,select=$('changes-source'),prior=select.value;select.replaceChildren(new Option('Project working tree','workspace'));for(const m of Object.values(g.members)){if(m.controller_for||!m.current_run_id)continue;select.append(new Option(m.spec.brief.title,'/runs/'+m.current_run_id+'/artifacts/changes.patch'))}if([...select.options].some(o=>o.value===prior))select.value=prior;});
 if(project)selectView(view);

 $('earlier-work')?.addEventListener('toggle',()=>{if($('earlier-work').open)refreshProject().catch(showError)});
 let ticks=0;
 async function refresh(){if(!alive)return;try{if(!home||!project||ticks%4===0)await refreshHome();if(project){if(!snapshot||selected||$('earlier-work')?.open||view==='resources')await refreshProject();if(view==='services')await refreshServices()}connection(true)}catch(e){connection(false)}finally{ticks++;if(alive)setTimeout(refresh,3000)}}
 if(selected&&$('earlier-work'))$('earlier-work').open=true;
 window.addEventListener('pagehide',()=>alive=false);refresh();
})();

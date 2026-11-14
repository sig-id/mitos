(() => {
 'use strict';
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const button=(label,fn,cls='')=>{const b=el('button',label,cls);b.type='button';b.onclick=fn;return b};
 function sourceEditor(title,value){const box=window.MitosMarkdown.editor(value,{filename:title,mode:'preview'});return {box,value:()=>box.value,set:v=>{box.value=v}}}
 function runtimeFields(value,defaults,global,installations=[],controls=[]){
  const form=el('div',undefined,'setup-fields');const field=(title,input,hint)=>{const label=el('label',title);label.append(input);if(hint)label.append(el('small',hint,'muted'));form.append(label);return input};
  const runtime=el('select');runtime.setAttribute('aria-label',global?'Default runtime':'Project runtime');runtime.append(new Option(global?'Codex':'Use my default ('+(defaults.runtime||'Codex')+')',global?'codex':''));if(!global)runtime.append(new Option('Codex','codex'));for(const r of installations.filter(r=>r.kind==='agent'&&r.installed&&r.id!=='codex')){const option=new Option(r.name+' – automatic task adapter unavailable',r.id);option.disabled=true;runtime.append(option)}runtime.value=value.runtime||(global?'codex':'');field('Runtime',runtime);
  const model=el('input');model.value=value.model||'';model.maxLength=200;model.setAttribute('aria-label',global?'Default model':'Project model');model.placeholder=global?'Use the runtime’s configured model':defaults.model||'Use my default model';field('Model',model,'Model ID; availability is checked by the runtime.');
  const budgets=MitosResourcePolicy.fields(value,controls);form.append(budgets.box);
  return {box:form,value:()=>({revision:value.revision,runtime:runtime.value,model:model.value.trim(),resources:budgets.value(),guidance:value.guidance||''})};
 }

 let generation=0;
 const markup=text=>window.MitosMarkdown.view(text||'_No instructions yet._');
 function defaultSummary(p,controls){const box=el('div',undefined,'setup-default-summary');box.append(markup(p.guidance||'_No personal guidance saved._'));const list=el('dl');for(const [k,v] of [['Runtime',p.runtime||'Codex'],['Model',p.model||'Runtime configuration']]){list.append(el('dt',k),el('dd',v))}box.append(list,MitosResourcePolicy.summary(p,controls));return box}
 function seed(setup){if(setup.exists)return setup.document;return [['Project context',setup.sections.context],['Git & review',setup.sections.git]].filter(([,v])=>v?.trim()).map(([k,v])=>'## '+k+'\n\n'+v.trim()).join('\n\n')}
 window.MitosProjectSetup={async render({container,project,snapshot,api,onChanges}){
  const token=++generation;container.replaceChildren(el('p','Loading AGENTS.md…','muted'));
  const base='/api/projects/'+encodeURIComponent(project);
  let setup;let installations;
  try{[setup,installations]=await Promise.all([api(base+'/setup'),api('/api/agents?project='+encodeURIComponent(project))])}catch(e){container.replaceChildren(el('p',e.message,'form-error'));return}
  if(token!==generation)return;
  async function surface(root,target){
   const personal=target==='defaults',name=personal?'My defaults':setup.file_name;
   const heading=el('header',undefined,'setup-file-heading'),titles=el('div');titles.append(el('h2',name),el('p',personal?'Personal preferences · saved in this Mitos server’s preferences.json, outside your repositories.':snapshot.project.directory+'/'+setup.file_name,'muted'));heading.append(titles);
   const feedback=el('p','','setup-feedback');feedback.setAttribute('role','status');
   root.replaceChildren(heading,feedback);
   if(!personal){const defaults=button('My defaults',async()=>{const dialog=el('dialog',undefined,'setup-default-dialog'),close=button('Close',()=>dialog.close(),'setup-dialog-close'),content=el('div');dialog.append(close,content);document.body.append(dialog);dialog.addEventListener('close',()=>dialog.remove());dialog.showModal();await surface(content,'defaults')});heading.append(defaults)}
   let conversation={status:'idle',messages:[]},proposal=null,comparing=false,reset=false,hydrated=false,loadedKey='',loadedDocument='',loadedPreferences=null;
   const layout=el('div',undefined,'setup-agent-layout'),file=el('section',undefined,'setup-card setup-file'),assistant=el('aside',undefined,'setup-card setup-assistant');layout.append(file,assistant);root.append(layout);
   const fileHeading=el('div',undefined,'setup-file-toolbar'),fileState=el('strong'),actions=el('div',undefined,'setup-file-actions'),compare=button('Compare with saved',()=>{comparing=!comparing;drawDocument()}),apply=button(personal?'Save proposed defaults':'Apply to '+name,applyProposal,'primary');actions.append(compare,apply);fileHeading.append(fileState,actions);
   const contents=el('div',undefined,'setup-document-content'),manual=el('div',undefined,'setup-edit-surface');file.append(fileHeading,manual,contents);
   let editor,fields,guidance;
   if(personal){guidance=sourceEditor('Personal guidance',setup.defaults.guidance||'');fields=runtimeFields(setup.defaults,{},true,installations,setup.resource_controls);manual.append(guidance.box,fields.box)}
   else{editor=sourceEditor(name,seed(setup));manual.append(editor.box)}
   const save=button(personal?'Save my defaults':'Save '+name,async()=>{save.disabled=true;try{
    if(personal){setup.defaults=await api('/api/preferences',{...fields.value(),guidance:guidance.value()},'PUT')}
    else{setup=await api(base+'/instructions',{file_hash:setup.file_hash,document:editor.value()},'PUT')}
    await reloadSetup();proposal=null;comparing=false;loadedKey='';drawDocument();feedback.textContent=personal?'Personal defaults saved.':name+' saved. New work will use these instructions.';feedback.classList.remove('error');
   }catch(e){showError(e)}finally{save.disabled=false}});
   const discard=button('Discard edits',()=>{if(personal){guidance.set(loadedPreferences.guidance||'');replaceFields(loadedPreferences)}else editor.set(loadedDocument);drawDocument()});
   const saveActions=el('div',undefined,'setup-save-actions');saveActions.append(save,discard);manual.append(saveActions);
   (guidance||editor).box.addEventListener('markdown-save',event=>{event.preventDefault();(loadedKey.startsWith('proposal:')&&!apply.hidden?apply:save).click()});
   manual.addEventListener('input',()=>drawDocument());
   function replaceFields(values){const next=runtimeFields(values,{},true,installations,setup.resource_controls);fields.box.replaceWith(next.box);fields=next}
   function dirty(){if(!loadedKey)return false;return personal?guidance.box.dirty||MitosResourcePolicy.signature(fields.value())!==MitosResourcePolicy.signature(loadedPreferences):editor.box.dirty}
   assistant.append(el('h3',personal?'Shape your defaults':'Set up with an agent'),el('p',personal?'Describe how you prefer agents to work.':'Describe this project and how you want agents to work. Review the proposed file, then apply it.','muted'));
   const messages=el('div',undefined,'setup-messages');messages.setAttribute('role','log');messages.setAttribute('aria-label','Setup conversation');assistant.append(messages);
   const form=el('form',undefined,'setup-chat-form'),prompt=el('textarea');prompt.rows=4;prompt.maxLength=8000;prompt.setAttribute('aria-label',personal?'Describe your defaults':'Describe project setup');prompt.placeholder=personal?'Keep replies concise. Prefer model …':'This is a safety-critical PLC project. Require tests and a review before opening PRs.';
   const options=el('details',undefined,'setup-assistant-options');options.append(el('summary','Assistant runtime'));
   const runtime=el('select');runtime.setAttribute('aria-label','Setup assistant runtime');
   for(const r of installations.filter(r=>r.kind==='agent'&&r.installed)){const option=new Option(r.name+(r.capabilities.project_context?'':' – setup adapter unavailable'),r.id);option.disabled=!r.capabilities.project_context;runtime.append(option)}
   const supported=[...runtime.options].filter(o=>!o.disabled);if(supported.length)runtime.value=supported.find(o=>o.value==='codex')?.value||supported[0].value;
   const model=el('input');model.placeholder='Use this runtime’s configured model';model.maxLength=200;model.setAttribute('aria-label','Setup assistant model');const label=el('label','Runtime');label.append(runtime);const ml=el('label','Model');ml.append(model);options.append(label,ml,el('p','This selects the agent helping edit setup. It does not change the runtime used to execute tasks.','muted'));
   const installed=el('details',undefined,'setup-runtime-inventory');installed.append(el('summary','Installed runtimes'));
   for(const r of installations.filter(r=>r.kind==='agent'&&r.installed))installed.append(el('p',r.name+' · '+(r.capabilities.project_context?'Setup assistant available. ':'Setup assistant adapter unavailable. ')+(r.id==='codex'?'Automatic tasks available.':'Automatic task adapter not implemented.')));
   options.append(installed);
   const send=el('button','Send','primary');send.type='submit';const stop=button('Stop reply',async()=>{try{await api(base+'/setup/conversation/stop',{target});stop.disabled=true}catch(e){showError(e)}});stop.hidden=true;
   const fresh=button('New conversation',()=>{reset=true;conversation={status:'idle',messages:[]};proposal=null;drawConversation();drawDocument();prompt.focus()});
   const controls=el('div',undefined,'setup-chat-controls');controls.append(send,stop,fresh);form.append(prompt,options,controls);assistant.append(form);
   if(!supported.length){send.disabled=true;assistant.append(el('p','Install or configure Codex, Claude Code or Pi to use the setup assistant. You can still edit the file manually.','form-error'))}
   const summary=el('details',undefined,'setup-execution-summary');if(!personal){file.append(summary);executionSummary()}
   function executionSummary(){const p=setup.effective;summary.replaceChildren(el('summary','Runtime & resource limits'),el('p',setup.settings_error||`${p.runtime} · ${p.model||'configured model'}`,'setup-effective'),MitosResourcePolicy.summary(p,setup.resource_controls),el('p','Used for new work. Ask the agent to change these values in '+name+', or change My defaults for all projects.','muted'),button('Change with agent',()=>{prompt.value='Update the runtime and limits in '+name+': ';prompt.focus();prompt.scrollIntoView({block:'center',behavior:'smooth'})}));if(Object.entries(setup.project).some(([k,v])=>['runtime','model','minutes','attempts','resources'].includes(k)&&v))summary.append(el('p','Earlier local project overrides apply where this file does not specify a value.','muted'))}
   function showError(e){feedback.textContent=e.message;feedback.classList.add('error')}
   async function reloadSetup(){setup=await api(base+'/setup');if(editor)editor.set(seed(setup));if(fields){const next=runtimeFields(setup.defaults,{},true,installations,setup.resource_controls);fields.box.replaceWith(next.box);fields=next;guidance.set(setup.defaults.guidance||'')}if(!personal)executionSummary()}
   function drawDocument(){
    const changed=personal?!!proposal?.preferences:typeof proposal?.document==='string',key=changed?'proposal:'+conversation.key+':'+(personal?JSON.stringify(proposal.preferences):proposal.document):'saved:'+(personal?setup.defaults.revision:setup.file_hash);
    if(key!==loadedKey&&!dirty()){
     loadedKey=key;
     if(personal){loadedPreferences={...(changed?proposal.preferences:setup.defaults),revision:changed?conversation.revision:setup.defaults.revision};guidance.set(loadedPreferences.guidance||'');replaceFields(loadedPreferences)}
     else{loadedDocument=changed?proposal.document:seed(setup);editor.set(loadedDocument)}
    }
    const pending=changed&&key!==loadedKey,editingProposal=changed&&!pending;
    apply.hidden=!editingProposal;compare.hidden=!changed;save.hidden=editingProposal;discard.hidden=!dirty();
    fileState.textContent=pending?'Proposal ready · your edits are preserved':editingProposal?'Proposed changes':personal?'My defaults':setup.exists?'Project instructions':'Draft · not saved yet';
    compare.textContent=comparing?'Back to document':'Compare with saved';manual.hidden=comparing&&changed;contents.hidden=!manual.hidden;
    if(!manual.hidden){contents.replaceChildren();return}
    contents.classList.add('setup-comparison');contents.replaceChildren();
    const before=el('section'),after=el('section');before.append(el('h3','Saved'),personal?defaultSummary(setup.defaults,setup.resource_controls):window.MitosMarkdown.view(seed(setup),{filename:name}));after.append(el('h3','Proposed'),personal?defaultSummary(proposal.preferences,setup.resource_controls):window.MitosMarkdown.view(editingProposal?editor.value():proposal.document,{filename:name}));contents.append(before,after);
   }
   function drawConversation(){
    messages.replaceChildren();for(const m of conversation.messages||[]){const item=el('div',undefined,'setup-message '+m.role);item.append(el('strong',m.role==='user'?'You':'Setup assistant'),markup(m.text));messages.append(item)}
    const running=conversation.status==='running';if(running)messages.append(el('p','Reading your request and preparing a reply…','muted'));if(conversation.error)messages.append(el('p',conversation.error,'form-error'));
    send.disabled=running||!supported.length;stop.hidden=!running;stop.disabled=false;fresh.disabled=running;apply.disabled=running;runtime.disabled=running;model.disabled=running;
   }
   function acceptConversation(c){if(!hydrated){hydrated=true;if(c.runtime&&[...runtime.options].some(o=>o.value===c.runtime&&!o.disabled)){runtime.value=c.runtime;model.value=c.model||''}}conversation=c;const candidate=c.suggestion;if(candidate){const applied=personal?!!candidate.preferences&&MitosResourcePolicy.signature(candidate.preferences)===MitosResourcePolicy.signature(setup.defaults):candidate.document===setup.document;proposal=applied?null:candidate}drawConversation();drawDocument()}
   let timer;
   async function poll(){if(!root.isConnected||token!==generation)return;try{acceptConversation(await api(base+'/setup/conversation?target='+target));if(conversation.status==='running')timer=setTimeout(poll,1000)}catch(e){showError(e);timer=setTimeout(poll,3000)}}
   form.onsubmit=async event=>{event.preventDefault();if(!prompt.value.trim())return;send.disabled=true;feedback.textContent='';feedback.classList.remove('error');try{const c=await api(base+'/setup/conversation',{key:crypto.randomUUID(),prompt:prompt.value,target,runtime:runtime.value,model:model.value.trim(),reset});reset=false;prompt.value='';acceptConversation(c);clearTimeout(timer);timer=setTimeout(poll,1000)}catch(e){showError(e);send.disabled=!supported.length}};
   async function applyProposal(){apply.disabled=true;try{if(personal){setup.defaults=await api('/api/preferences',{...fields.value(),guidance:guidance.value(),revision:conversation.revision},'PUT')}else{setup=await api(base+'/instructions',{file_hash:conversation.file_hash,document:editor.value()},'PUT')}await reloadSetup();proposal=null;comparing=false;loadedKey='';drawDocument();feedback.classList.remove('error');feedback.textContent=personal?'Personal defaults saved.':name+' saved. New work will use these instructions.'}catch(e){showError(e)}finally{apply.disabled=false}}
   if(setup.settings_error)showError(new Error('AGENTS.md execution settings need attention: '+setup.settings_error));drawDocument();await poll();
  }
  await surface(container,'project');
  window.MitosAgentPolicy.render(container,setup.agent_policy,base,api);
  const coop=el('details');coop.append(el('summary','COOPERATION.md'),el('p','Optional project conventions for delegation, shared work, handoffs and escalation. Link this file from AGENTS.md for other tools. Changes apply to new coordinated work.','muted'));
  let coopDoc=setup.cooperation;const coopEditor=sourceEditor('COOPERATION.md',coopDoc.text||''),coopStatus=el('p');coopStatus.setAttribute('role','status');
  coop.append(coopEditor.box,button('Save COOPERATION.md',async()=>{try{coopDoc=await api(base+'/cooperation-document',{hash:coopDoc.hash,text:coopEditor.value()},'PUT');coopEditor.set(coopDoc.text);coopStatus.textContent='Saved COOPERATION.md.'}catch(e){coopStatus.textContent=e.message}}),coopStatus);container.append(coop);
 }};
})();

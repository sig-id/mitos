(() => {
 'use strict';
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const button=(label,fn,cls='')=>{const b=el('button',label,cls);b.type='button';b.onclick=fn;return b};
 function markdownEditor(title,value,description){
  const box=el('section',undefined,'setup-editor'),heading=el('div',undefined,'setup-editor-heading'),tabs=el('div',undefined,'setup-editor-tabs');tabs.setAttribute('aria-label',title+' editor mode');
  heading.append(el('h3',title),tabs);box.append(heading);if(description)box.append(el('p',description,'muted'));
  const area=el('div',undefined,'setup-editor-body'),text=el('textarea'),preview=el('div',undefined,'setup-preview');text.value=value;text.rows=10;text.setAttribute('aria-label',title);text.spellcheck=false;area.append(text,preview);box.append(area);
  // Replace the preview as DOM, never evaluate authored HTML.
  const draw=()=>{value=text.value;preview.replaceChildren(value.trim()?window.MitosFormat.markdown(value.replace(/<!--[\s\S]*?-->/g,'')):el('p','No instructions yet. Choose Edit to add Markdown.','muted'))};
  const modes=new Map();function mode(name){area.dataset.mode=name;text.hidden=name==='preview';preview.hidden=name==='edit';for(const [key,b]of modes)b.setAttribute('aria-pressed',String(key===name));if(name!=='preview')text.focus({preventScroll:true});draw()}
  for(const name of ['preview','edit','split']){const b=button(name[0].toUpperCase()+name.slice(1),()=>mode(name));tabs.append(b);modes.set(name,b)}
  text.oninput=draw;mode('preview');return {box,value:()=>text.value,set:v=>{text.value=v;draw()}};
 }
 function runtimeFields(value,defaults,global,installations=[]){
  const form=el('div',undefined,'setup-fields');const field=(title,input,hint)=>{const label=el('label',title);label.append(input);if(hint)label.append(el('small',hint,'muted'));form.append(label);return input};
  const runtime=el('select');runtime.setAttribute('aria-label',global?'Default runtime':'Project runtime');runtime.append(new Option(global?'Codex':'Use my default ('+(defaults.runtime||'Codex')+')',global?'codex':''));if(!global)runtime.append(new Option('Codex','codex'));for(const r of installations.filter(r=>r.kind==='agent'&&r.installed&&r.id!=='codex')){const option=new Option(r.name+' – automatic task adapter unavailable',r.id);option.disabled=true;runtime.append(option)}runtime.value=value.runtime||(global?'codex':'');field('Runtime',runtime);
  const model=el('input');model.value=value.model||'';model.maxLength=200;model.setAttribute('aria-label',global?'Default model':'Project model');model.placeholder=global?'Use the runtime’s configured model':defaults.model||'Use my default model';field('Model',model,'Model ID; availability is checked by the runtime.');
  const minutes=el('input');minutes.type='number';minutes.min='1';minutes.max='43200';minutes.value=value.minutes||'';minutes.placeholder=String(defaults.minutes||120);minutes.setAttribute('aria-label',global?'Default time limit':'Project time limit');field('Time limit · minutes',minutes,global?'Blank uses 120 minutes.':'Blank inherits my default.');
  const attempts=el('input');attempts.type='number';attempts.min='1';attempts.max='2048';attempts.value=value.attempts||'';attempts.placeholder=String(defaults.attempts||96);attempts.setAttribute('aria-label',global?'Default attempt limit':'Project attempt limit');field('Agent attempts',attempts,'Includes planner rounds, workers and retries.');
  return {box:form,value:()=>({revision:value.revision,runtime:runtime.value,model:model.value.trim(),minutes:Number(minutes.value),attempts:Number(attempts.value),guidance:value.guidance||''})};
 }

 let generation=0;
 const markup=text=>window.MitosFormat.markdown((text||'_No instructions yet._').replace(/<!--[\s\S]*?-->/g,''));
 function defaultSummary(p){const box=el('div',undefined,'setup-default-summary');box.append(markup(p.guidance||'_No personal guidance saved._'));const list=el('dl');for(const [k,v] of [['Runtime',p.runtime||'Codex'],['Model',p.model||'Runtime configuration'],['Time limit',(p.minutes||120)+' minutes'],['Attempt limit',String(p.attempts||96)]]){list.append(el('dt',k),el('dd',v))}box.append(list);return box}
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
   let conversation={status:'idle',messages:[]},proposal=null,comparing=false,reset=false,hydrated=false;
   const layout=el('div',undefined,'setup-agent-layout'),file=el('section',undefined,'setup-card setup-file'),assistant=el('aside',undefined,'setup-card setup-assistant');layout.append(file,assistant);root.append(layout);
   const fileHeading=el('div',undefined,'setup-file-toolbar'),fileState=el('strong'),actions=el('div',undefined,'setup-file-actions'),compare=button('Compare with saved',()=>{comparing=!comparing;drawDocument()}),apply=button(personal?'Save proposed defaults':'Apply to '+name,applyProposal,'primary');actions.append(compare,apply);fileHeading.append(fileState,actions);
   const contents=el('div',undefined,'setup-document-content'),manual=el('details',undefined,'setup-manual');manual.append(el('summary',personal?'Edit defaults manually':'Edit Markdown manually'));file.append(fileHeading,contents,manual);
   let editor,fields,guidance;
   if(personal){guidance=markdownEditor('Personal guidance',setup.defaults.guidance||'');fields=runtimeFields(setup.defaults,{},true,installations);manual.append(guidance.box,fields.box,el('p','These are defaults for automatic work. At present its execution adapter supports Codex. The setup assistant runtime is chosen separately.','muted'));const save=button('Save my defaults',async()=>{save.disabled=true;try{setup.defaults=await api('/api/preferences',{...fields.value(),guidance:guidance.value()},'PUT');await reloadSetup();proposal=null;drawDocument();feedback.textContent='Personal defaults saved.'}catch(e){showError(e)}finally{save.disabled=false}});manual.append(save)}
   else{editor=markdownEditor(name,seed(setup));manual.append(editor.box);const save=button('Save '+name,async()=>{save.disabled=true;try{setup=await api(base+'/instructions',{file_hash:setup.file_hash,document:editor.value()},'PUT');proposal=null;drawDocument();feedback.textContent=name+' saved. New work will use these instructions.'}catch(e){showError(e)}finally{save.disabled=false}});manual.append(save)}
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
   const summary=el('details',undefined,'setup-execution-summary');summary.append(el('summary','Execution settings'),el('p','Runtime/model and numeric limits can be set in a mitos JSON block in AGENTS.md. Code validates and enforces these values when new work starts. Risk descriptions, Git policies and monetary budgets guide the agent; they do not grant permission to publish.','muted'));if(!personal){file.append(summary);executionSummary()}
   function executionSummary(){summary.querySelector('.setup-effective')?.remove();const p=setup.effective;summary.append(el('p',setup.settings_error||`${p.runtime} · ${p.model||'configured model'} · ${p.minutes} minutes · ${p.attempts} attempts`,'setup-effective'));if(Object.entries(setup.project).some(([k,v])=>['runtime','model','minutes','attempts'].includes(k)&&v))summary.append(el('p','Earlier local project overrides still apply where AGENTS.md does not specify a value.','muted'))}
   function showError(e){feedback.textContent=e.message;feedback.classList.add('error')}
   async function reloadSetup(){setup=await api(base+'/setup');if(editor)editor.set(seed(setup));if(fields){const next=runtimeFields(setup.defaults,{},true,installations);fields.box.replaceWith(next.box);fields=next;guidance.set(setup.defaults.guidance||'')}if(!personal)executionSummary()}
   function drawDocument(){
    const changed=personal?!!proposal?.preferences:typeof proposal?.document==='string';apply.hidden=!changed;compare.hidden=!changed;fileState.textContent=changed?'Proposed changes':personal?'Saved defaults':setup.exists?'Saved file':'File not created yet';compare.textContent=comparing?'Show proposed':'Compare with saved';
    contents.replaceChildren();contents.classList.toggle('setup-comparison',comparing&&changed);
    const render=(old)=>personal?defaultSummary(old?setup.defaults:proposal.preferences):markup(old?seed(setup):proposal.document);
    if(comparing&&changed){const before=el('section'),after=el('section');before.append(el('h3','Saved'),render(true));after.append(el('h3','Proposed'),render(false));contents.append(before,after)}else contents.append(render(!changed));
   }
   function drawConversation(){
    messages.replaceChildren();for(const m of conversation.messages||[]){const item=el('div',undefined,'setup-message '+m.role);item.append(el('strong',m.role==='user'?'You':'Setup assistant'),markup(m.text));messages.append(item)}
    const running=conversation.status==='running';if(running)messages.append(el('p','Reading your request and preparing a reply…','muted'));if(conversation.error)messages.append(el('p',conversation.error,'form-error'));
    send.disabled=running||!supported.length;stop.hidden=!running;stop.disabled=false;fresh.disabled=running;apply.disabled=running;runtime.disabled=running;model.disabled=running;
   }
   function acceptConversation(c){if(!hydrated){hydrated=true;if(c.runtime&&[...runtime.options].some(o=>o.value===c.runtime&&!o.disabled)){runtime.value=c.runtime;model.value=c.model||''}}conversation=c;const candidate=c.suggestion;if(candidate){const applied=personal?!!candidate.preferences&&['runtime','model','minutes','attempts','guidance'].every(k=>(candidate.preferences[k]||'')===(setup.defaults[k]||'')):candidate.document===setup.document;proposal=applied?null:candidate}drawConversation();drawDocument()}
   let timer;
   async function poll(){if(!root.isConnected||token!==generation)return;try{acceptConversation(await api(base+'/setup/conversation?target='+target));if(conversation.status==='running')timer=setTimeout(poll,1000)}catch(e){showError(e);timer=setTimeout(poll,3000)}}
   form.onsubmit=async event=>{event.preventDefault();if(!prompt.value.trim())return;send.disabled=true;feedback.textContent='';feedback.classList.remove('error');try{const c=await api(base+'/setup/conversation',{key:crypto.randomUUID(),prompt:prompt.value,target,runtime:runtime.value,model:model.value.trim(),reset});reset=false;prompt.value='';acceptConversation(c);clearTimeout(timer);timer=setTimeout(poll,1000)}catch(e){showError(e);send.disabled=!supported.length}};
   async function applyProposal(){apply.disabled=true;try{if(personal){setup.defaults=await api('/api/preferences',{...proposal.preferences,revision:conversation.revision},'PUT')}else{setup=await api(base+'/instructions',{file_hash:conversation.file_hash,document:proposal.document},'PUT')}await reloadSetup();proposal=null;comparing=false;drawDocument();feedback.classList.remove('error');feedback.textContent=personal?'Personal defaults saved.':name+' saved. New work will use these instructions.'}catch(e){showError(e)}finally{apply.disabled=false}}
   if(setup.settings_error)showError(new Error('AGENTS.md execution settings need attention: '+setup.settings_error));drawDocument();await poll();
  }
  await surface(container,'project');
 }};
})();

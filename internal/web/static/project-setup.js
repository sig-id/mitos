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
 function runtimeFields(value,defaults,global){
  const form=el('div',undefined,'setup-fields');const field=(title,input,hint)=>{const label=el('label',title);label.append(input);if(hint)label.append(el('small',hint,'muted'));form.append(label);return input};
  const runtime=el('select');runtime.setAttribute('aria-label',global?'Default runtime':'Project runtime');runtime.append(new Option(global?'Codex':'Use my default ('+(defaults.runtime||'Codex')+')',global?'codex':''));if(!global)runtime.append(new Option('Codex','codex'));const pi=new Option('Pi – native sessions only','pi');pi.disabled=true;runtime.append(pi);runtime.value=value.runtime||(global?'codex':'');field('Runtime',runtime);
  const model=el('input');model.value=value.model||'';model.maxLength=200;model.setAttribute('aria-label',global?'Default model':'Project model');model.placeholder=global?'Use the runtime’s configured model':defaults.model||'Use my default model';field('Model',model,'Model ID; availability is checked by the runtime.');
  const minutes=el('input');minutes.type='number';minutes.min='1';minutes.max='43200';minutes.value=value.minutes||'';minutes.placeholder=String(defaults.minutes||120);minutes.setAttribute('aria-label',global?'Default time limit':'Project time limit');field('Time limit · minutes',minutes,global?'Blank uses 120 minutes.':'Blank inherits my default.');
  const attempts=el('input');attempts.type='number';attempts.min='1';attempts.max='2048';attempts.value=value.attempts||'';attempts.placeholder=String(defaults.attempts||96);attempts.setAttribute('aria-label',global?'Default attempt limit':'Project attempt limit');field('Agent attempts',attempts,'Includes planner rounds, workers and retries.');
  return {box:form,value:()=>({revision:value.revision,runtime:runtime.value,model:model.value.trim(),minutes:Number(minutes.value),attempts:Number(attempts.value),guidance:value.guidance||''})};
 }
 let generation=0;
 window.MitosProjectSetup={async render({container,project,snapshot,api,onChanges}){
  const token=++generation;container.replaceChildren(el('p','Loading project setup…','muted'));
  const base='/api/projects/'+encodeURIComponent(project),setup=await api(base+'/setup');if(token!==generation)return;
  const heading=el('header',undefined,'setup-heading');heading.append(el('h2','Project setup'),el('p','Shared instructions live in '+setup.file_name+'. Your defaults stay on this workstation.','muted'));
  const status=el('p',setup.exists?setup.file_name+' · repository instructions':setup.file_name+' will be created when you save project instructions.','setup-file-status');heading.append(status);
  const layout=el('div',undefined,'setup-layout'),main=el('div',undefined,'setup-main'),aside=el('aside',undefined,'setup-aside');layout.append(main,aside);container.replaceChildren(heading,layout);
  const notice=el('p','','setup-feedback');notice.setAttribute('role','status');heading.append(notice);
  async function save(b,fn,message){b.disabled=true;notice.textContent='Saving…';try{await fn();notice.textContent=message;notice.classList.remove('error')}catch(e){notice.textContent=e.message+((e.message.includes('conflict')||e.message.includes('already changed'))?' Reload setup to review external changes before saving again.':'');notice.classList.add('error')}finally{b.disabled=false}}
  const shared=el('section',undefined,'setup-card');shared.append(el('h2','Shared project instructions'));
  const context=markdownEditor('Project context',setup.sections.context,'Purpose, architecture, conventions and useful background.');
  const constraints=markdownEditor('Risk & constraints',setup.sections.constraints,'Describe the risk level, quality bar, spending guidance or deadlines in your own words.');
  const git=markdownEditor('Git & review',setup.sections.git,'Branches, commits, pull requests, reviews and publishing rules.');
  shared.append(context.box,constraints.box,git.box);
  let hash=setup.file_hash;
  const saveShared=button('Save project instructions',()=>save(saveShared,async()=>{const saved=await api(base+'/instructions',{file_hash:hash,sections:{context:context.value(),constraints:constraints.value(),git:git.value()}},'PUT');hash=saved.file_hash;source.set(saved.document);status.textContent=saved.file_name+' · saved in the repository';},'Saved to '+setup.file_name+'. New work will use these instructions.'),'primary');shared.append(saveShared);main.append(shared);
  if(setup.warning){shared.prepend(el('p',setup.warning,'form-error'));saveShared.disabled=true}
  const document=el('details',undefined,'setup-card setup-document');document.append(el('summary','Full '+setup.file_name+' · existing instructions and generated sections'));
  const source=markdownEditor('Full instructions file',setup.document,'Existing content outside the marked Mitos section is preserved when you save project sections.');document.append(source.box);
  const saveSource=button('Save full document',()=>save(saveSource,async()=>{await api(base+'/instructions',{file_hash:hash,document:source.value()},'PUT');await window.MitosProjectSetup.render({container,project,snapshot,api,onChanges});},'Saved full instructions document.'));document.append(saveSource);if(setup.exists&&!setup.document.includes('<!-- mitos:project-setup:start -->')){document.open=true;document.querySelector('summary').textContent='Existing '+setup.file_name;main.prepend(document)}else main.append(document);
  const runtime=el('section',undefined,'setup-card');runtime.append(el('h2','Execution for this project'),el('p','Overrides your defaults on this workstation. These settings apply to new work.','muted'));
  let local=runtimeFields(setup.project,setup.defaults,false);runtime.append(local.box,el('p','Time and attempt limits are enforced. Monetary budgets in Markdown guide the agent; they are not a spending cap.','muted'));
  const saveLocal=button('Save project execution',()=>save(saveLocal,async()=>{const saved=await api(base+'/preferences',local.value(),'PUT');setup.project=saved;const next=runtimeFields(saved,setup.defaults,false);local.box.replaceWith(next.box);local=next;},'Project execution settings saved.'));runtime.append(saveLocal);main.append(runtime);
  const personal=el('section',undefined,'setup-card');personal.append(el('h2','My defaults'),el('p','Used across projects on this workstation. Never written to the repository.','muted'));
  const guidance=markdownEditor('Personal preferences',setup.defaults.guidance,'Communication style, preferred approaches and other personal guidance.');let global=runtimeFields(setup.defaults,{},true);personal.append(guidance.box,global.box);
  const saveGlobal=button('Save my defaults',()=>save(saveGlobal,async()=>{const saved=await api('/api/preferences',{...global.value(),guidance:guidance.value()},'PUT');setup.defaults=saved;const inherited=runtimeFields(local.value(),saved,false);local.box.replaceWith(inherited.box);local=inherited;const next=runtimeFields(saved,{},true);global.box.replaceWith(next.box);global=next;},'Personal defaults saved. Existing work keeps its original settings.'));personal.append(saveGlobal);aside.append(personal);
  const delivery=el('section',undefined,'setup-card setup-explanation');delivery.append(el('h3','How agents receive setup'),el('p','The planner receives the full project instructions and personal preferences directly. Workers get the same frozen instructions file in their workspace and read it with any nested instructions.'),el('p','Project instructions take precedence over personal defaults. The current request supplies the work goal.','muted'));aside.append(delivery);
  const environment=el('section',undefined,'setup-card');environment.append(el('h3','Workspace'),el('code',snapshot.project.directory),button('View changes',onChanges),el('p','Automatic runtime: Codex. Pi remains available for native sessions; its automatic planner adapter is not implemented.','muted'));aside.append(environment);
 }};
})();

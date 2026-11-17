(() => {
 'use strict';
 const el=(tag,text)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;return n};
 const field=(box,label,input)=>{const n=el('label',label);n.append(input);box.append(n);return input};
 const action=(label,fn)=>{const b=el('button',label);b.type='button';b.onclick=fn;return b};
 function initial(preferences,installations){
  if(preferences.execution)return structuredClone(preferences.execution);
  const runtime=preferences.runtime||'codex',presets=[{id:'default',name:'Project default',runtime,model:preferences.model||'',profile_id:''}];
  for(const r of installations.filter(r=>r.kind==='agent'&&['codex','claude'].includes(r.id)&&(r.installed||r.profiles?.length))){if(r.id!==runtime)presets.push({id:r.id,name:r.name,runtime:r.id,model:'',profile_id:''})}
  return {presets,orchestrator:'default',worker:'default',roles:{},allowed_workers:presets.map(p=>p.id)};
 }
 function editor(value,installations,roles){
  const policy=structuredClone(value),box=el('div');box.className='execution-policy';
  policy.roles||={};const picks=[];
  function selectPreset(label,current,onchange,parent=box,inherit=false){const select=el('select');select.setAttribute('aria-label',label);field(parent,label,select);const entry={select,inherit};picks.push(entry);select.onchange=()=>{onchange(select.value);refresh()};entry.current=current;return select}
  const orchestrator=selectPreset('Orchestrator',policy.orchestrator,v=>policy.orchestrator=v);
  const worker=selectPreset('Default worker',policy.worker,v=>policy.worker=v);
  const rules=el('details');rules.append(el('summary','Agent role rules'));box.append(rules);
  rules.append(el('p','Choose which preset runs each role. An explicitly selected task preset takes precedence within the permitted worker choices.'));
  for(const role of roles.filter(r=>r.kind!=='orchestrator'))selectPreset(role.name,policy.roles[role.id]||'',v=>{if(v)policy.roles[role.id]=v;else delete policy.roles[role.id]},rules,true);
  const presets=el('details');presets.append(el('summary','Execution presets & permitted workers'));box.append(presets);
  const rows=el('div');presets.append(rows);
  function refresh(){
   for(const p of picks){const value=p.select.options.length?p.select.value:p.current;p.select.replaceChildren();if(p.inherit)p.select.append(new Option('Use default worker',''));for(const preset of policy.presets)p.select.append(new Option(`${preset.name} · ${preset.runtime}${preset.model?' · '+preset.model:''}`,preset.id));p.select.value=value;}
  }
  function draw(){rows.replaceChildren();for(const preset of policy.presets){
   const row=el('fieldset');row.dataset.preset=preset.id;row.append(el('legend',preset.name));
   const name=el('input');name.value=preset.name;name.required=true;name.maxLength=100;field(row,'Preset name',name);name.oninput=()=>{preset.name=name.value;refresh()};
   const runtime=el('select');runtime.setAttribute('aria-label','Coding agent');field(row,'Coding agent',runtime);
   for(const id of ['codex','claude']){const r=installations.find(r=>r.id===id);const o=new Option((r?.name||id)+(!r?.installed&&!r?.profiles?.length?' — not installed':''),id);o.disabled=!r?.installed&&!r?.profiles?.length;runtime.append(o)}runtime.value=preset.runtime;
   const model=el('input');model.value=preset.model||'';model.maxLength=200;model.placeholder='Use the agent’s default model';model.setAttribute('aria-label','Model');field(row,'Model',model);model.oninput=()=>{preset.model=model.value.trim();refresh()};
   const profile=el('select');profile.setAttribute('aria-label','Environment profile');field(row,'Environment profile',profile);
   function profiles(){profile.replaceChildren(new Option('Native environment',''));for(const p of installations.find(r=>r.id===preset.runtime)?.profiles||[])profile.append(new Option(p.name||p.id,p.id));if(preset.profile_id&&![...profile.options].some(o=>o.value===preset.profile_id)){const o=new Option(preset.profile_id+' — unavailable',preset.profile_id);o.disabled=true;profile.append(o)}profile.value=preset.profile_id||''}
   profiles();runtime.onchange=()=>{preset.runtime=runtime.value;preset.model='';model.value='';preset.profile_id='';profiles();refresh()};profile.onchange=()=>preset.profile_id=profile.value;
   const allowed=el('input');allowed.type='checkbox';allowed.checked=policy.allowed_workers.includes(preset.id);field(row,'Allow workers to use this preset',allowed);allowed.onchange=()=>{policy.allowed_workers=allowed.checked?[...policy.allowed_workers,preset.id]:policy.allowed_workers.filter(id=>id!==preset.id)};
   const remove=action('Remove preset',()=>{policy.presets=policy.presets.filter(p=>p.id!==preset.id);policy.allowed_workers=policy.allowed_workers.filter(id=>id!==preset.id);if(policy.orchestrator===preset.id)policy.orchestrator=policy.presets[0]?.id||'';if(policy.worker===preset.id)policy.worker=policy.presets[0]?.id||'';for(const [role,id]of Object.entries(policy.roles))if(id===preset.id)delete policy.roles[role];orchestrator.value=policy.orchestrator;worker.value=policy.worker;draw();refresh()});remove.disabled=policy.presets.length===1;row.append(remove);rows.append(row);
  }}
  presets.append(action('Add execution preset',()=>{policy.presets.push({id:crypto.randomUUID(),name:'New preset',runtime:installations.find(r=>r.installed&&['codex','claude'].includes(r.id))?.id||'codex',model:'',profile_id:''});draw();refresh()}));
  const note=el('p','Codex and Claude Code support coordinated work. Other detected agents remain available in Start agent session.');note.className='muted';presets.append(note);
  draw();refresh();
  return {box,value:()=>{if(!policy.allowed_workers.includes(policy.worker))throw Error('Allow the default worker preset for workers.');for(const id of Object.values(policy.roles))if(!policy.allowed_workers.includes(id))throw Error('Every role rule must use a permitted worker preset.');return structuredClone(policy)}};
 }
 function documentWithPolicy(document,policy){const pattern=/^```mitos\s*\n([\s\S]*?)^```\s*$/m,match=document.match(pattern);const settings=match?JSON.parse(match[1]):{};settings.execution=policy;const block='```mitos\n'+JSON.stringify(settings,null,2)+'\n```';return match?document.replace(pattern,()=>block):document.trimEnd()+'\n\n'+block+'\n'}
 window.MitosExecutionPolicy={initial,editor,documentWithPolicy};
})();

(() => {
 'use strict';
 // agent-execution/v2: harness selection (role and nature rules over harnesses
 // that can run the task) is separate from model selection (an intelligence tier
 // within the harness). Size buckets set attempt budgets.
 const VERSION='agent-execution/v2',TIERS=['low','medium','high'],SIZES=['small','medium','large'];
 const NATURES=['context','design','implement','document','review','verify'];
 const el=(tag,text)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;return n};
 const field=(box,label,input)=>{const n=el('label',label);n.append(input);box.append(n);return input};
 const action=(label,fn)=>{const b=el('button',label);b.type='button';b.onclick=fn;return b};
 const select=(label,options,value,onchange)=>{const s=el('select');s.setAttribute('aria-label',label);for(const [v,t,disabled=false]of options){const o=new Option(t,v);o.disabled=disabled;s.append(o)}s.value=value;s.onchange=()=>onchange(s.value);return s};
 const environments=r=>r?.harness_environments||[];
 const canCoordinate=r=>r.capabilities.coordinated_work&&environments(r).length>0;
 function codingAgentOptions(installations,current){
  const options=[];
  for(const r of installations){
   if(r.kind!=='agent')continue;
   if(!environments(r).length){
    if(r.capabilities.coordinated_work||r.id===current)options.push([r.id,r.name+' – unavailable',true]);
   }else if(r.capabilities.coordinated_work){
    options.push([r.id,r.name,false]);
   }else{
    options.push([r.id,r.name+' – installed, no coordinated-work adapter',true]);
   }
  }
  return options;
 }
 function suggestedTiers(agent,environment,configuredModel){
  const models=environment?.models||[];
  // 'Agent default' is the default selection: the harness uses its own
  // configured model unless the operator explicitly picks one of the
  // detected models. Only a previously configured model preselects.
  return Object.fromEntries(TIERS.map(t=>{
   const model=configuredModel||'';
   const efforts=models.find(m=>m.id===model)?.efforts;
   return [t,{model,effort:(agent?.id!=='amp'||model)&&agent?.capabilities.reasoning_efforts?.includes(t)&&(!efforts||efforts.includes(t))?t:''}];
  }));
 }
 function initial(preferences,installations){
  if(preferences.execution?.version===VERSION)return structuredClone(preferences.execution);
  const available=installations.filter(canCoordinate);
  const requested=preferences.runtime||'codex',selected=available.find(r=>r.id===requested)||available.find(r=>r.installed)||available[0];
  const runtime=selected?.id||requested,environment=environments(selected)[0];
  const harnesses=[{id:'default',name:'Project default',runtime,profile_id:environment?.profile_id||'',tiers:suggestedTiers(selected,environment,runtime===requested&&preferences.model)}];
  for(const r of available){
   if(r===selected)continue;
   const env=environments(r)[0];
   harnesses.push({id:r.id,name:r.name,runtime:r.id,profile_id:env.profile_id,tiers:suggestedTiers(r,env)});
  }
  return {version:VERSION,harnesses,harness_rules:[],default_harness:'default',allowed_harnesses:harnesses.map(h=>h.id),orchestrator:{harness:'default',tier:'high'},size_budgets:{small:{minutes:15,max_turns:40},medium:{minutes:30,max_turns:80},large:{minutes:60,max_turns:160}}};
 }
 function tierFields(h,t,models,efforts){
  const tier=h.tiers[t]||(h.tiers[t]={model:'',effort:''});
  const tierRow=el('div');tierRow.className='execution-tier';
  const label=t[0].toUpperCase()+t.slice(1)+' tier';
  const options=[['','Agent default'],...models.map(m=>[m.id,m.name?m.name+' ('+m.id+')':m.id]),[':custom','Custom model…']];
  const known=()=>models.find(m=>m.id===tier.model);
  const choice=tier.model&&!known()?':custom':(tier.model||'');
  const custom=el('input');custom.maxLength=200;custom.placeholder='Model ID';custom.setAttribute('aria-label',label+' custom model');
  custom.value=choice===':custom'?tier.model:'';custom.hidden=choice!==':custom';
  custom.oninput=()=>{tier.model=custom.value.trim();renderEffort(true)};
  const picker=select(label+' model',options,choice,v=>{
   custom.hidden=v!==':custom';tier.model=custom.hidden?v:custom.value.trim();
   if(!custom.hidden)custom.focus();
   renderEffort(true);
  });
  const modelField=el('div');modelField.className='execution-tier-model';tierRow.append(modelField);
  field(modelField,label+' model',picker);modelField.append(custom);
  const effort=el('select');field(tierRow,label+' effort',effort);effort.setAttribute('aria-label',label+' effort');
  function renderEffort(changed){
   const supported=known()?.efforts,choices=h.runtime==='amp'&&!tier.model?[]:efforts.filter(e=>!supported||supported.includes(e));
   if(changed&&tier.effort&&!choices.includes(tier.effort))tier.effort=choices.includes(t)?t:'';
   effort.replaceChildren(new Option('Default effort',''));
   for(const e of new Set([...choices,...(tier.effort?[tier.effort]:[])]))effort.append(new Option(e,e));
   effort.value=tier.effort||'';
  }
  renderEffort(false);effort.onchange=()=>{tier.effort=effort.value};
  return tierRow;
 }
 function editor(value,installations,roles){
  const policy=structuredClone(value),box=el('div');box.className='execution-policy';
  policy.harness_rules||=[];policy.context_floor||={};
  const harnessOptions=(only)=>policy.harnesses.filter(h=>!only||policy.allowed_harnesses.includes(h.id)).map(h=>[h.id,`${h.name} · ${h.runtime}`]);
  const workerRoles=roles.filter(r=>r.kind!=='orchestrator');
  const top=el('div');top.className='execution-policy-row';box.append(top);
  const rules=el('details');rules.append(el('summary','Harness rules (by role and task nature)'));box.append(rules);
  rules.append(el('p','A task runs on the most specific rule that matches its role and nature, among harnesses that can run it under the enforcement policy. Rules of equal specificity that overlap need a more specific rule for the overlap. Without a match the default worker harness is used.'));
  const ruleRows=el('div');rules.append(ruleRows);
  rules.append(action('Add harness rule',()=>{policy.harness_rules.push({match:{role:'',nature:''},harness:policy.default_harness});draw()}));
  const budgets=el('details');budgets.append(el('summary','Size budgets and context floors'));box.append(budgets);
  budgets.append(el('p','A task’s declared size sets its attempt time and turn budget (capped by the agent definition). A context floor raises the model tier for large work; it never lowers the tier intelligence selects.'));
  const budgetRows=el('div');budgets.append(budgetRows);
  const harnesses=el('details');harnesses.append(el('summary','Harnesses, model tiers & permitted workers'));box.append(harnesses);
  harnesses.append(el('p','Each harness is a coding agent with a model and reasoning effort per intelligence tier. Declared intelligence (low, medium, high) picks the tier within the chosen harness.'));
  const harnessRows=el('div');harnesses.append(harnessRows);
  const available=installations.filter(canCoordinate);
  const add=action('Add harness',()=>{
   const r=available.find(r=>r.id===policy.harnesses[0]?.runtime)||available[0],env=environments(r)[0];
   policy.harnesses.push({id:crypto.randomUUID(),name:'New harness',runtime:r.id,profile_id:env.profile_id,tiers:suggestedTiers(r,env)});draw();
  });
  add.disabled=!available.length;harnesses.append(add);
  const note=el('p','Native environment uses the Mitos server’s PATH and environment variables. Named profiles in agents.json can select a different executable or account.');note.className='muted';harnesses.append(note);
  function draw(){
   top.replaceChildren();
   field(top,'Orchestrator harness',select('Orchestrator harness',harnessOptions(false),policy.orchestrator.harness,v=>{policy.orchestrator.harness=v}));
   field(top,'Orchestrator tier',select('Orchestrator tier',TIERS.map(t=>[t,t]),policy.orchestrator.tier,v=>{policy.orchestrator.tier=v}));
   field(top,'Default worker harness',select('Default worker harness',harnessOptions(true),policy.default_harness,v=>{policy.default_harness=v}));
   ruleRows.replaceChildren();
   policy.harness_rules.forEach((rule,i)=>{
    const row=el('fieldset');row.append(el('legend',`Rule ${i+1}`));
    field(row,'Role',select('Role',[['','Any role'],...workerRoles.map(r=>[r.id,r.name])],rule.match.role||'',v=>{rule.match.role=v}));
    field(row,'Nature',select('Nature',[['','Any nature'],...NATURES.map(n=>[n,n])],rule.match.nature||'',v=>{rule.match.nature=v}));
    field(row,'Harness',select('Harness',harnessOptions(true),rule.harness,v=>{rule.harness=v}));
    row.append(action('Remove rule',()=>{policy.harness_rules.splice(i,1);draw()}));ruleRows.append(row);
   });
   budgetRows.replaceChildren();
   for(const size of SIZES){
    const b=policy.size_budgets[size],row=el('fieldset');row.append(el('legend',`${size[0].toUpperCase()+size.slice(1)} tasks`));
    const minutes=el('input');minutes.type='number';minutes.min=1;minutes.max=120;minutes.value=b.minutes;field(row,'Minutes',minutes);minutes.oninput=()=>{b.minutes=Number(minutes.value)};
    const turns=el('input');turns.type='number';turns.min=1;turns.max=1000;turns.value=b.max_turns;field(row,'Turns',turns);turns.oninput=()=>{b.max_turns=Number(turns.value)};
    field(row,'Context floor',select('Context floor',[['','None'],...TIERS.map(t=>[t,t])],policy.context_floor[size]||'',v=>{if(v)policy.context_floor[size]=v;else delete policy.context_floor[size]}));
    budgetRows.append(row);
   }
   harnessRows.replaceChildren();
   for(const h of policy.harnesses){
    const row=el('fieldset');row.dataset.harness=h.id;row.append(el('legend',h.name));
    const name=el('input');name.value=h.name;name.required=true;name.maxLength=100;field(row,'Harness name',name);name.onchange=()=>{h.name=name.value;draw()};
    const agent=installations.find(r=>r.id===h.runtime),envs=environments(agent);
    const env=envs.find(p=>p.profile_id===(h.profile_id||''));
    field(row,'Coding agent',select('Coding agent',codingAgentOptions(installations,h.runtime),h.runtime,v=>{
     h.runtime=v;
     const selected=available.find(r=>r.id===v),first=environments(selected)[0];
     h.profile_id=first.profile_id;h.tiers=suggestedTiers(selected,first);draw();
    }));
    const profile=el('select');profile.setAttribute('aria-label','Environment profile');field(row,'Environment profile',profile);
    for(const p of envs)profile.append(new Option(agent.profiles?.find(x=>x.id===p.profile_id)?.name||p.profile_id||'Native environment',p.profile_id));
    if(!env){const option=new Option((h.profile_id||'Native environment')+' – unavailable',h.profile_id||'');option.disabled=true;profile.append(option)}
    profile.value=h.profile_id||'';profile.onchange=()=>{
     h.profile_id=profile.value;h.tiers=suggestedTiers(agent,envs.find(p=>p.profile_id===profile.value));draw();
    };
    const modelRows=el('div');modelRows.className='execution-tier-list';row.append(modelRows);
    if(env&&!env.models?.length)modelRows.append(el('small','Model list unavailable. Use the agent default or enter a model ID.'));
    else if(env?.models?.length)modelRows.append(el('small',env.models.length+' configured model'+(env.models.length===1?'':'s')+' detected. Leave a tier on Agent default to use the harness default model.'));
    for(const t of TIERS)modelRows.append(tierFields(h,t,env?.models||[],agent?.capabilities.reasoning_efforts||[]));
    const allowed=el('input');allowed.type='checkbox';allowed.checked=policy.allowed_harnesses.includes(h.id);
    const workerLabel=el('label');workerLabel.className='execution-worker-toggle';workerLabel.append(allowed,'Allow workers to use this harness');row.append(workerLabel);
    allowed.onchange=()=>{policy.allowed_harnesses=allowed.checked?[...policy.allowed_harnesses,h.id]:policy.allowed_harnesses.filter(id=>id!==h.id);draw()};
    const remove=action('Remove harness',()=>{policy.harnesses=policy.harnesses.filter(x=>x.id!==h.id);policy.allowed_harnesses=policy.allowed_harnesses.filter(id=>id!==h.id);const first=policy.harnesses[0]?.id||'';if(policy.orchestrator.harness===h.id)policy.orchestrator.harness=first;if(policy.default_harness===h.id)policy.default_harness=policy.allowed_harnesses[0]||first;policy.harness_rules=policy.harness_rules.filter(r=>r.harness!==h.id);draw()});
    remove.disabled=policy.harnesses.length===1;row.append(remove);harnessRows.append(row);
   }
  }
  draw();
  return {box,value:()=>{
   if(!policy.allowed_harnesses.includes(policy.default_harness))throw Error('Allow the default worker harness for workers.');
   const out=structuredClone(policy);
   for(const r of out.harness_rules){if(!r.match.role&&!r.match.nature)throw Error('Each harness rule must match a role, a nature, or both.');if(!out.allowed_harnesses.includes(r.harness))throw Error('Every harness rule must use a permitted worker harness.');if(!r.match.role)delete r.match.role;if(!r.match.nature)delete r.match.nature}
   if(!Object.keys(out.context_floor).length)delete out.context_floor;return out;
  }};
 }
 function documentWithPolicy(document,policy){const pattern=/^```mitos\s*\n([\s\S]*?)^```\s*$/m,match=document.match(pattern);const settings=match?JSON.parse(match[1]):{};settings.execution=policy;const block='```mitos\n'+JSON.stringify(settings,null,2)+'\n```';return match?document.replace(pattern,()=>block):document.trimEnd()+'\n\n'+block+'\n'}
 window.MitosExecutionPolicy={initial,editor,codingAgentOptions,documentWithPolicy};
})();

(() => {
 'use strict';
 // agent-execution/v2: harness selection (role and nature rules over harnesses
 // that can run the task) is separate from model selection (an intelligence tier
 // within the harness). Size buckets set attempt budgets.
 const VERSION='agent-execution/v2',TIERS=['low','medium','high'],SIZES=['small','medium','large'];
 const NATURES=['context','design','implement','document','review','verify'];
 const EFFORTS={codex:['minimal','low','medium','high','xhigh'],claude:['low','medium','high','xhigh','max']};
 const el=(tag,text)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;return n};
 const field=(box,label,input)=>{const n=el('label',label);n.append(input);box.append(n);return input};
 const action=(label,fn)=>{const b=el('button',label);b.type='button';b.onclick=fn;return b};
 const select=(label,options,value,onchange)=>{const s=el('select');s.setAttribute('aria-label',label);for(const [v,t]of options)s.append(new Option(t,v));s.value=value;s.onchange=()=>onchange(s.value);return s};
 const tiers=(model)=>Object.fromEntries(TIERS.map(t=>[t,{model:model||'',effort:t}]));
 function initial(preferences,installations){
  if(preferences.execution?.version===VERSION)return structuredClone(preferences.execution);
  const available=installations.filter(r=>r.kind==='agent'&&['codex','claude'].includes(r.id)&&(r.installed||r.profiles?.some(p=>p.executable)));
  const requested=preferences.runtime||'codex',selected=available.find(r=>r.id===requested)||available.find(r=>r.installed)||available[0],runtime=selected?.id||requested;
  const profile=r=>r?.installed?'':r?.profiles?.find(p=>p.executable)?.id||'';
  const harnesses=[{id:'default',name:'Project default',runtime,profile_id:profile(selected),tiers:tiers(runtime===requested?preferences.model:'')}];
  for(const r of available){if(r.id!==runtime)harnesses.push({id:r.id,name:r.name,runtime:r.id,profile_id:profile(r),tiers:tiers('')})}
  return {version:VERSION,harnesses,harness_rules:[],default_harness:'default',allowed_harnesses:harnesses.map(h=>h.id),orchestrator:{harness:'default',tier:'high'},size_budgets:{small:{minutes:15,max_turns:40},medium:{minutes:30,max_turns:80},large:{minutes:60,max_turns:160}}};
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
  harnesses.append(action('Add harness',()=>{const runtime=installations.find(r=>r.installed&&['codex','claude'].includes(r.id))?.id||'codex';policy.harnesses.push({id:crypto.randomUUID(),name:'New harness',runtime,profile_id:'',tiers:tiers('')});draw()}));
  const note=el('p','Codex and Claude Code support coordinated work. Other detected agents remain available in Start agent session.');note.className='muted';harnesses.append(note);
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
    const runtime=el('select');runtime.setAttribute('aria-label','Coding agent');field(row,'Coding agent',runtime);
    for(const id of ['codex','claude']){const r=installations.find(r=>r.id===id);const o=new Option((r?.name||id)+(!r?.installed&&!r?.profiles?.length?' — not installed':''),id);o.disabled=!r?.installed&&!r?.profiles?.length;runtime.append(o)}runtime.value=h.runtime;
    runtime.onchange=()=>{h.runtime=runtime.value;h.profile_id='';h.tiers=tiers('');draw()};
    const profile=el('select');profile.setAttribute('aria-label','Environment profile');field(row,'Environment profile',profile);
    profile.append(new Option('Native environment',''));for(const p of installations.find(r=>r.id===h.runtime)?.profiles||[])profile.append(new Option(p.name||p.id,p.id));
    if(h.profile_id&&![...profile.options].some(o=>o.value===h.profile_id)){const o=new Option(h.profile_id+' — unavailable',h.profile_id);o.disabled=true;profile.append(o)}
    profile.value=h.profile_id||'';profile.onchange=()=>{h.profile_id=profile.value};
    for(const t of TIERS){
     const tier=h.tiers[t]||(h.tiers[t]={model:'',effort:''});
     const model=el('input');model.value=tier.model||'';model.maxLength=200;model.placeholder='Agent’s default model';field(row,`${t} tier model`,model);model.oninput=()=>{tier.model=model.value.trim()};
     field(row,`${t} tier effort`,select(`${t} tier effort`,[['','Default effort'],...(EFFORTS[h.runtime]||[]).map(e=>[e,e])],tier.effort||'',v=>{tier.effort=v}));
    }
    const allowed=el('input');allowed.type='checkbox';allowed.checked=policy.allowed_harnesses.includes(h.id);field(row,'Allow workers to use this harness',allowed);
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
 window.MitosExecutionPolicy={initial,editor,documentWithPolicy};
})();

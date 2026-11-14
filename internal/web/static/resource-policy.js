(() => {
 'use strict';
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const paths=['workflow.elapsed_minutes','workflow.attempts','workflow.tokens','agent.cpu_millicores','agent.memory_mib'];
 const get=(policy,path)=>{const [scope,key]=path.split('.');return policy?.[scope]?.[key]||{mode:'inherit'}};
 function policy(preferences={}) {
  const result={workflow:{},agent:{}};
  for(const path of paths){const [scope,key]=path.split('.'),value=get(preferences.resources,path);result[scope][key]={mode:value.mode||'inherit',...(value.amount!==undefined?{amount:value.amount}:{})};}
  for(const [key,legacy] of [['elapsed_minutes','minutes'],['attempts','attempts']]){
   if(result.workflow[key].mode==='inherit'&&preferences[legacy])result.workflow[key]={mode:'limit',amount:preferences[legacy]};
  }
  return result;
 }
 function signature(preferences={}) {return JSON.stringify([preferences.runtime||'codex',preferences.model||'',preferences.guidance||'',policy(preferences)]);}
 function describe(limit,unit){return limit.mode==='limit'?`${limit.amount.toLocaleString()} ${unit}`:limit.mode==='unlimited'?'No cap':'Inherited';}
 function fields(preferences,controls) {
  const box=el('div',undefined,'resource-policy-fields'),values=policy(preferences),inputs=[];
  for(const scope of ['workflow','agent']){
   const group=el('fieldset');group.append(el('legend',scope==='workflow'?'Workflow budgets':'Per-agent resources'));
   group.firstElementChild.append(MitosInstructions.create(scope==='workflow'?'Shared across all agents and retries in one workflow run.':'Applies to each agent and its child processes, not the entire machine.',scope==='workflow'?'Workflow budgets':'Per-agent resources'));
   for(const control of controls.filter(c=>c.scope===scope)){
    const limit=get(values,control.path),row=el('div',undefined,'resource-setting'),label=el('label',control.label),select=el('select');
    select.setAttribute('aria-label',control.label+' policy');select.dataset.resource=control.path;
    select.append(new Option('Inherit', 'inherit'));
    const capped=new Option('Set a limit','limit');capped.disabled=!control.enforced;select.append(capped);
    if(control.allow_unlimited)select.append(new Option('No cap','unlimited'));
    select.value=limit.mode;label.append(select);row.append(label);
    const amountLabel=el('label',control.unit),amount=el('input');amount.type='number';amount.min='1';amount.max=String(control.max);amount.step='1';amount.value=limit.amount??'';amount.setAttribute('aria-label',control.label+' amount');amountLabel.append(amount);row.append(amountLabel);
    const sync=()=>{amountLabel.hidden=select.value!=='limit';amount.disabled=amountLabel.hidden;amount.required=!amount.disabled;};select.addEventListener('change',sync);sync();
    const support=el('span',control.enforced?'Enforced':'Cap unavailable','resource-support');support.append(MitosInstructions.create(control.detail,control.label));row.append(support);group.append(row);inputs.push({path:control.path,select,amount});
   }
   box.append(group);
  }
  box.append(MitosInstructions.create('**Inherit** uses the next settings layer. **No cap** is explicit; zero is not a valid limit.\n\nThe current runtime requires finite elapsed-time and attempt budgets.','Resource policies','popover'));
  return {box,value:()=>{const result={workflow:{},agent:{}};for(const {path,select,amount} of inputs){const [scope,key]=path.split('.');result[scope][key]={mode:select.value,...(select.value==='limit'?{amount:Number(amount.value)}:{})};}return result;}};
 }
 function summary(preferences,controls){
  const box=el('div',undefined,'resource-policy-summary'),values=policy(preferences);
  for(const scope of ['workflow','agent']){
   const section=el('section');section.append(el('h4',scope==='workflow'?'Workflow budgets':'Per-agent resources'));
   const list=el('dl');for(const c of controls.filter(c=>c.scope===scope)){list.append(el('dt',c.label),el('dd',describe(get(values,c.path),c.unit)+(c.enforced?'':' · cap unavailable')));}section.append(list);box.append(section);
  }
  return box;
 }
 globalThis.MitosResourcePolicy={policy,signature,fields,summary};
})();

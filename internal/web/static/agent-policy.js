(() => {
 'use strict';
 const el=(tag,text)=>{const e=document.createElement(tag);if(text!==undefined)e.textContent=text;return e};
 window.MitosAgentPolicy={render(root,initial,base,api){
  let policy=initial;
  const section=el('details');section.className='agent-policy';section.append(el('summary','Autonomy & approvals'));
  section.append(el('p','Guidance describes your preferences. Approval rules delegate permission for exact commands in the specified working directory, including their effects. Revoking a rule affects pending approvals.'));
  const form=el('form'),feedback=el('p');feedback.setAttribute('role','status');
  const field=(parent,label,input)=>{const l=el('label',label);input.setAttribute('aria-label',label);l.append(input);parent.append(l);return input};
  const select=(values,value)=>{const s=el('select');for(const [v,label] of values)s.append(new Option(label,v));s.value=value;return s};
  const mode=field(form,'Automatic handling',select([['manual','Ask me'],['delegated','Resolve within my approval rules']],policy.autonomy));
  const guidance=el('textarea');guidance.rows=3;guidance.maxLength=8000;guidance.value=policy.guidance||'';field(form,'Autonomy guidance',guidance);
  const provider=el('input');provider.value=policy.decision_application||'';provider.maxLength=100;field(form,'Configured decision application (optional)',provider);
  const fallback=field(form,'When evidence is insufficient',select([['human','Ask me'],['investigate','Request investigation']],policy.fallback));
  const budget=el('input');budget.type='number';budget.min=1;budget.max=64;budget.value=policy.max_calls;field(form,'Maximum model calls per request',budget);
  form.append(el('h3','Independent supervision'),el('p','Supervision checks project boundaries separately from routing and approvals. A violation or uncertain model review stops owned work and holds new execution until you resolve the incident.'));
  const supervisor=policy.supervision||{};
  const supervisionMode=field(form,'Boundary review',select([['rules','Explicit rules'],['model','Rules and independent model'],['off','Disabled']],supervisor.mode||'rules'));
  const supervisionApp=el('input');supervisionApp.value=supervisor.application||'';field(form,'Supervisor decision application',supervisionApp);
  const boundaries=el('textarea');boundaries.rows=4;boundaries.maxLength=8000;boundaries.value=supervisor.guidance||'';field(form,'Project boundaries for model review',boundaries);
  const workspaceOnly=el('input');workspaceOnly.type='checkbox';workspaceOnly.checked=!!supervisor.workspace_only;field(form,'Review paths outside the session workspace',workspaceOnly);
  const supervisionBudget=el('input');supervisionBudget.type='number';supervisionBudget.min=1;supervisionBudget.max=64;supervisionBudget.value=supervisor.max_calls||16;field(form,'Supervisor calls per session (separate allowance)',supervisionBudget);
  const rules=el('div');form.append(el('h3','Exact command rules'),rules);const rows=[];
  const button=(label,fn)=>{const b=el('button',label);b.type='button';b.onclick=fn;return b};
  function addRule(value={id:crypto.randomUUID(),command:'',directory:'.',action:'review'}){
   const row=el('fieldset'),command=el('input'),directory=el('input');command.value=value.command;command.required=true;directory.value=value.directory;directory.required=true;
   field(row,'Exact command',command);field(row,'Working directory relative to the session workspace',directory);
   const action=field(row,'Resolution',select([['allow','Allow automatically'],['review','Delegate to decision review'],['deny','Deny']],value.action));
   row.append(button('Remove rule',()=>{row.remove()}));rules.append(row);rows.push({row,value,command,directory,action});
  }
  for(const r of policy.rules||[])addRule(r);
  form.append(button('Add command rule',()=>addRule()));const save=el('button','Save autonomy & approvals');save.type='submit';form.append(save,feedback);section.append(form);root.append(section);
  form.onsubmit=async event=>{event.preventDefault();save.disabled=true;try{policy=await api(base+'/agent-policy',{...policy,supervision:{mode:supervisionMode.value,application:supervisionApp.value.trim(),guidance:boundaries.value,workspace_only:workspaceOnly.checked,max_calls:Number(supervisionBudget.value)},autonomy:mode.value,guidance:guidance.value,decision_application:provider.value.trim(),fallback:fallback.value,max_calls:Number(budget.value),rules:rows.filter(r=>r.row.isConnected).map(r=>({id:r.value.id,command:r.command.value,directory:r.directory.value,action:r.action.value}))},'PUT');feedback.textContent='Saved approval policy revision '+policy.revision+'.'}catch(e){feedback.textContent=e.message}finally{save.disabled=false}};
 }};
})();

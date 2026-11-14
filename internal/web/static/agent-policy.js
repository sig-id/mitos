(() => {
 'use strict';
 const el=(tag,text)=>{const e=document.createElement(tag);if(text!==undefined)e.textContent=text;return e};
 window.MitosAgentPolicy={render(root,initial,base,api){
  let policy=initial;
  const section=el('section');section.className='agent-policy setup-card';section.id='decisions-api';section.tabIndex=-1;section.append(el('h2','Decisions API & approvals'));
  section.append(el('p','Approval review runs separately from the coordinator and workers, using the Decisions API. Configure a connection and approval boundaries to handle routine requests automatically. Saved changes also apply to pending requests.'));
  const form=el('form'),feedback=el('p');feedback.setAttribute('role','status');
  const field=(parent,label,input)=>{const l=el('label',label);input.setAttribute('aria-label',label);l.append(input);parent.append(l);return input};
  const select=(values,value)=>{const s=el('select');for(const [v,label] of values)s.append(new Option(label,v));s.value=value;return s};
  const mode=field(form,'Automatic handling',select([['manual','Ask me'],['decision','Decisions API reviews commands within my boundaries'],['delegated','Only delegate exact command rules']],policy.autonomy));
  const guidance=el('textarea');guidance.rows=3;guidance.maxLength=8000;guidance.value=policy.guidance||'';field(form,'Approval boundaries',guidance);guidance.placeholder='For example: allow read-only repository and GitHub inspection. Ask me before publishing changes or accessing unrelated files.';
  const config=policy.decision_provider||{},providerFields=el('fieldset');providerFields.append(el('legend','Decisions API connection'));
  const source=field(form,'Decision provider',select([['none','Not configured'],['direct','Configure API connection'],['application','Use an installed decision application']],policy.decision_provider?'direct':policy.decision_application?'application':'none'));
  const provider=el('input');provider.value=policy.decision_application||'';provider.maxLength=100;const appField=el('div');field(appField,'Installed decision application ID',provider);form.append(appField,providerFields);
  const protocol=field(providerFields,'API protocol',select([['openai_decisions','OpenAI Decisions'],['systemone','System One']],config.protocol||'openai_decisions'));
  const input=(label,value,type='text')=>{const n=el('input');n.type=type;n.value=value;return field(providerFields,label,n)};
  const endpoint=input('Endpoint URL',config.endpoint||'');endpoint.type='url';endpoint.placeholder='https://your-provider.example/decisions';
  const model=input('Decision model',config.model||'');
  const credential=input('API key environment variable',config.api_key_env||'');credential.placeholder='OPENAI_API_KEY';providerFields.append(el('p','Enter the name of a credential environment variable available to the Mitos server. Its secret value stays outside the browser and project files. Restart the server after changing its environment.'));
  const timeout=input('Request timeout (milliseconds)',config.timeout_ms||30000,'number');timeout.min=1;timeout.max=120000;
  const confidence=input('Minimum confidence',config.min_confidence??0.8,'number'),probability=input('Minimum choice probability',config.min_probability??0.8,'number');for(const n of [confidence,probability]){n.min=0;n.max=1;n.step=0.01}
  function sourceChanged(){providerFields.hidden=source.value!=='direct';appField.hidden=source.value!=='application';endpoint.required=model.required=source.value==='direct'}source.onchange=sourceChanged;sourceChanged();
  const status=el('p');status.setAttribute('role','status');providerFields.append(status);
  const test=el('button','Test API connection');test.type='button';test.onclick=async()=>{test.disabled=true;status.textContent='Testing connection…';try{const result=await api(base+'/decision-provider/test',{config:providerConfig()});status.textContent=result.message}catch(e){status.textContent=e.message}finally{test.disabled=false}};providerFields.append(test);
  function providerConfig(){return {protocol:protocol.value,endpoint:endpoint.value.trim(),model:model.value.trim(),api_key_env:credential.value.trim(),hooks:['before_segment'],instructions:'Review requests within the operator’s approval boundaries.',timeout_ms:Number(timeout.value),max_calls:Number(budget.value),min_confidence:Number(confidence.value),min_probability:Number(probability.value)}}
  const fallback=field(form,'When evidence is insufficient',select([['human','Ask me'],['investigate','Request investigation']],policy.fallback));
  const budget=el('input');budget.type='number';budget.min=1;budget.max=64;budget.value=policy.max_calls;field(form,'Maximum model calls per request',budget);
  const advanced=el('details');advanced.append(el('summary','Advanced rules & supervision'));form.append(advanced);advanced.append(el('h3','Independent supervision'),el('p','Supervision checks project boundaries separately from routing and approvals. A violation or uncertain model review stops owned work and holds new execution until you resolve the incident.'));
  const supervisor=policy.supervision||{};
  const supervisionMode=field(advanced,'Boundary review',select([['rules','Explicit rules'],['model','Rules and independent model'],['off','Disabled']],supervisor.mode||'rules'));
  const supervisionApp=el('input');supervisionApp.value=supervisor.application||'';field(advanced,'Supervisor decision application',supervisionApp);
  const boundaries=el('textarea');boundaries.rows=4;boundaries.maxLength=8000;boundaries.value=supervisor.guidance||'';field(advanced,'Project boundaries for model review',boundaries);
  const workspaceOnly=el('input');workspaceOnly.type='checkbox';workspaceOnly.checked=!!supervisor.workspace_only;field(advanced,'Review paths outside the session workspace',workspaceOnly);
  const supervisionBudget=el('input');supervisionBudget.type='number';supervisionBudget.min=1;supervisionBudget.max=64;supervisionBudget.value=supervisor.max_calls||16;field(advanced,'Supervisor calls per session (separate allowance)',supervisionBudget);
  const rules=el('div');advanced.append(el('h3','Exact command rules'),rules);const rows=[];
  const button=(label,fn)=>{const b=el('button',label);b.type='button';b.onclick=fn;return b};
  function addRule(value={id:crypto.randomUUID(),command:'',directory:'.',action:'review'}){
   const row=el('fieldset'),command=el('input'),directory=el('input');command.value=value.command;command.required=true;directory.value=value.directory;directory.required=true;
   field(row,'Exact command',command);field(row,'Working directory relative to the session workspace',directory);
   const action=field(row,'Resolution',select([['allow','Allow automatically'],['review','Delegate to decision review'],['deny','Deny']],value.action));
   row.append(button('Remove rule',()=>{row.remove()}));rules.append(row);rows.push({row,value,command,directory,action});
  }
  for(const r of policy.rules||[])addRule(r);
  advanced.append(button('Add command rule',()=>addRule()));const save=el('button','Save autonomy & approvals');save.type='submit';form.append(save,feedback);section.append(form);root.insertBefore(section,root.querySelector('.setup-agent-layout'));if(location.hash==='#decisions-api'){section.scrollIntoView({block:'start'});section.focus({preventScroll:true})}
  form.onsubmit=async event=>{event.preventDefault();save.disabled=true;try{policy=await api(base+'/agent-policy',{...policy,supervision:{mode:supervisionMode.value,application:supervisionApp.value.trim(),guidance:boundaries.value,workspace_only:workspaceOnly.checked,max_calls:Number(supervisionBudget.value)},autonomy:mode.value,guidance:guidance.value,decision_application:source.value==='application'?provider.value.trim():'',decision_provider:source.value==='direct'?providerConfig():null,fallback:fallback.value,max_calls:Number(budget.value),rules:rows.filter(r=>r.row.isConnected).map(r=>({id:r.value.id,command:r.command.value,directory:r.directory.value,action:r.action.value}))},'PUT');feedback.textContent='Saved. Approval handling is now '+(policy.autonomy==='manual'?'Ask me':policy.autonomy==='decision'?'Decisions API review':'Exact command rules')+'.'}catch(e){feedback.textContent=e.message}finally{save.disabled=false}};
 }};
})();

(() => {
 'use strict';
 const project=document.body.dataset.project,server=document.body.dataset.server;
 const base=project?'/api/projects/'+encodeURIComponent(project)+'/settings':'/api/settings';
 const root=document.getElementById('settings-form'),status=document.getElementById('settings-status');
 const el=(tag,text)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;return n};
 const field=(parent,label,input)=>{const n=el('label',label);input.setAttribute('aria-label',label);n.append(input);parent.append(n);return input};
 const select=(options,value)=>{const n=el('select');for(const [id,label] of options)n.append(new Option(label,id));n.value=value;return n};
 async function api(path,body,method){
  const response=await fetch(path,{method:method||(body===undefined?'GET':'POST'),cache:'no-store',headers:{'Content-Type':'application/json','X-Mitos-Server':server},body:body===undefined?undefined:JSON.stringify(body)});
  const data=await response.json();if(!response.ok)throw Error(response.status===409?'Configuration changed. Reload this page before saving again.':data.error||'Request failed');return data;
 }
 async function load(){
  const [data,agents]=await Promise.all([api(base),api('/api/agents')]);
  let config=data.configuration;const defaults=data.defaults;
  const form=el('form');form.className='settings-form';
  const connection=el('fieldset');connection.append(el('legend','Decisions API'));
  connection.append(el('p',project?'Override the shared connection for this project, or inherit the system default.':'This connection is the default for all projects. Approval boundaries and permission to review commands are configured in each project.'));
  const source=field(connection,'Connection',select([['inherit',project?'Inherit system default':'Not configured'],['direct','Configure API connection'],['application','Use an installed decision application']],config.decision_provider?'direct':config.decision_application?'application':'inherit'));
  const inherited=el('p');inherited.className='muted';inherited.textContent=defaults.decision_provider?'System default: '+defaults.decision_provider.model+' · '+defaults.decision_provider.endpoint:defaults.decision_application?'System default: application '+defaults.decision_application:'No system Decisions API connection is configured.';connection.append(inherited);
  const direct=el('fieldset');direct.append(el('legend','API connection'));const values=config.decision_provider||defaults.decision_provider||{};
  const protocol=field(direct,'Protocol',select([['openai_decisions','OpenAI Decisions'],['systemone','System One']],values.protocol||'openai_decisions'));
  function input(parent,label,value,type='text'){const n=el('input');n.type=type;n.value=value;return field(parent,label,n)}
  const endpoint=input(direct,'Full endpoint URL',values.endpoint||'','url');endpoint.placeholder='https://your-provider.example/decisions';endpoint.maxLength=2048;
  const model=input(direct,'Model',values.model||'');model.maxLength=200;
  const credential=input(direct,'API key environment variable',values.api_key_env||'');credential.placeholder='OPENAI_API_KEY';credential.pattern='[A-Za-z_][A-Za-z0-9_]*';
  direct.append(el('p','Enter the environment variable name, not the API key. The secret must be available to the Mitos server. Restart Mitos after changing its environment.'));
  const grid=el('div');grid.className='settings-grid';direct.append(grid);
  const timeout=input(grid,'Timeout (milliseconds)',values.timeout_ms||30000,'number');timeout.min=1;timeout.max=120000;timeout.required=true;
  const budget=input(grid,'Maximum model calls per request',values.max_calls||4,'number');budget.min=1;budget.max=64;budget.required=true;
  const confidence=input(grid,'Minimum confidence',values.min_confidence??0.8,'number'),probability=input(grid,'Minimum choice probability',values.min_probability??0.8,'number');for(const n of [confidence,probability]){n.min=0;n.max=1;n.step=0.01;n.required=true}
  direct.append(el('p','Project request allowances can impose a lower call limit.'));
  function provider(){return {protocol:protocol.value,endpoint:endpoint.value.trim(),model:model.value.trim(),api_key_env:credential.value.trim(),timeout_ms:Number(timeout.value),max_calls:Number(budget.value),min_confidence:Number(confidence.value),min_probability:Number(probability.value),hooks:['before_segment'],instructions:'Review requests within the operator’s approval boundaries.'}}
  const test=el('button','Test API connection');test.type='button';const testStatus=el('p');testStatus.setAttribute('role','status');direct.append(test,testStatus);
  test.onclick=async()=>{if(![...direct.querySelectorAll('input')].every(n=>n.reportValidity()))return;test.disabled=true;testStatus.textContent='Testing connection…';try{const out=await api('/api/settings/decision-provider/test',{config:provider()});testStatus.textContent=out.message+' Settings have not been saved.'}catch(e){testStatus.textContent=e.message}finally{test.disabled=false}};
  const applicationBox=el('div'),application=input(applicationBox,'Installed decision application ID',config.decision_application||'');application.maxLength=100;
  connection.append(direct,applicationBox);
  function sourceChanged(){direct.hidden=direct.disabled=source.value!=='direct';applicationBox.hidden=application.disabled=source.value!=='application';application.required=source.value==='application';endpoint.required=model.required=source.value==='direct';inherited.hidden=!project||source.value!=='inherit'}
  source.onchange=sourceChanged;sourceChanged();form.append(connection);
  const launch=el('fieldset');launch.append(el('legend','Agent launch defaults'),el('p','Choose the default environment profile and additional launch arguments for each native agent session. Explicit session profiles take precedence. Project overrides replace the system settings for that agent. Changes apply to future launches, including resumes and forks.'),el('p','These CLI arguments apply to native sessions. Coordinated workers and read-only setup or investigation assistants use managed launch protocols and do not take these flags.'));
  const rows=[];
  for(const agent of agents.filter(a=>a.kind==='agent')){
   const box=el('details');box.append(el('summary',agent.name+(agent.installed?'':' · not installed')));
   const own=Object.hasOwn(config.agents||{},agent.id),value=config.agents?.[agent.id]||defaults.agents?.[agent.id]||{};
   const enabled=el('input');enabled.type='checkbox';enabled.checked=own;field(box,project?'Override system defaults for this agent':'Customize launch defaults',enabled);
   const fields=el('fieldset');fields.append(el('legend',agent.name+' launch settings'));
   const profile=field(fields,'Default environment profile',select([['','Native environment'],...(agent.profiles||[]).map(p=>[p.id,p.name])],value.profile_id||''));
   const args=el('textarea');args.rows=4;args.value=JSON.stringify(value.args||[],null,2);field(fields,'Additional launch arguments (JSON array)',args);fields.append(el('p','Example: ["--model", "your-model"]. Each string is one argument; Mitos does not evaluate shell commands. Do not put secrets in launch flags.'));
   enabled.onchange=()=>{fields.disabled=!enabled.checked;if(!enabled.checked){const inherited=project?defaults.agents?.[agent.id]||{}:{};profile.value=inherited.profile_id||'';args.value=JSON.stringify(inherited.args||[],null,2)}};enabled.onchange();box.append(fields);launch.append(box);rows.push({id:agent.id,enabled,profile,args});
  }
  form.append(launch);const actions=el('div');actions.className='settings-actions';const save=el('button',project?'Save project overrides':'Save system configuration');save.type='submit';save.className='primary';const feedback=el('p');feedback.setAttribute('role','status');actions.append(save,feedback);form.append(actions);
  form.onsubmit=async event=>{event.preventDefault();save.disabled=true;feedback.textContent='';try{
   const agents={};for(const row of rows){if(!row.enabled.checked)continue;let args;try{args=JSON.parse(row.args.value)}catch{throw Error('Launch arguments for '+row.id+' must be a JSON array of strings.')}if(!Array.isArray(args)||args.some(a=>typeof a!=='string'))throw Error('Launch arguments for '+row.id+' must be a JSON array of strings.');agents[row.id]={profile_id:row.profile.value,args}}
   config=await api(base,{revision:config.revision,decision_provider:source.value==='direct'?provider():null,decision_application:source.value==='application'?application.value.trim():'',agents},'PUT');feedback.textContent=project?'Project overrides saved.':'System configuration saved.';
  }catch(e){feedback.textContent=e.message}finally{save.disabled=false}};
  root.replaceChildren(form);status.textContent=project?'Editing project overrides.':'Editing system defaults.';
 }
 load().catch(e=>{status.textContent=e.message;const retry=el('button','Retry');retry.onclick=()=>location.reload();root.append(retry)});
})();

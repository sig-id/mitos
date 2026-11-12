(() => {
 'use strict';
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const prose=text=>window.MitosMarkdown.view(String(text||''));
 const list=value=>Array.isArray(value)?value:[];
 const label=value=>typeof value==='string'?value:'';
 function disclosure(title,key,open=false){const d=el('details',undefined,'graph-output-section');d.dataset.readKey=key;d.open=open;d.append(el('summary',title));return d}
 function structured(value,name='Data',depth=0,path=name){
  if(value===null||typeof value!=='object')return el('p',typeof value==='string'?value:JSON.stringify(value),'graph-json-value');
  const entries=Object.entries(value),d=disclosure(`${name} · ${entries.length} ${Array.isArray(value)?'items':'fields'}`,'json:'+path,depth===0);
  const body=el('div',undefined,'graph-json-children');let built=false;
  const build=()=>{if(built||!d.open)return;built=true;if(depth>=12){body.append(el('p','Expand the raw data below to inspect deeper values.'));return}for(const [key,v]of entries.slice(0,100)){const row=el('div',undefined,'graph-json-row');if(v&&typeof v==='object')row.append(structured(v,key,depth+1,path+'/'+key));else row.append(el('strong',key),structured(v));body.append(row)}if(entries.length>100)body.append(el('p','Showing the first 100 entries. Full data is available below.','muted'))};d.addEventListener('toggle',build);d.append(body);build();return d;
 }
 function plan(value,resolve){
  const box=el('div',undefined,'graph-plan-output');box.append(el('h4','Planner summary'),prose(value.explanation));
  if(value.complete===true)box.append(el('p','The planner proposes completing this goal.','graph-output-callout'));
  const steps=list(value.steps).filter(s=>s&&typeof s==='object'),local=new Map(steps.map(s=>[s.id,s.title]));
  const taskName=id=>local.get(id)||resolve(id)||id;
  if(steps.length){box.append(el('h4',`Proposed assignments · ${steps.length}`));steps.forEach((s,i)=>{
   const d=disclosure(label(s.title)||`Assignment ${i+1}`,'step:'+i,i===0);
   const role=label(s.role)||label(s.agent).replace(/^mitos\./,'');if(role)d.append(el('p',role,'graph-output-agent'));
   d.append(prose(s.assignment));if(s.criteria)d.append(el('h5','Success criteria'),prose(s.criteria));if(list(s.after).length)d.append(el('h5','After'),el('p',s.after.map(taskName).join(', ')));
   if(s.replaces)d.append(el('h5','Replaces'),el('p',taskName(s.replaces)));
   if(s.instructions){const extra=disclosure('Agent instructions','instructions:'+i);extra.append(prose(s.instructions));d.append(extra)}box.append(d);
  })}
  const reviews=list(value.accept).filter(r=>r&&typeof r==='object');if(reviews.length){box.append(el('h4','Reviewed results'));reviews.forEach((r,i)=>{const d=disclosure(taskName(r.task),'review:'+i,true);d.append(prose(r.rationale));box.append(d)})}
  if(list(value.retry).length){box.append(el('h4','Retry requested'));const ul=el('ul');for(const id of value.retry)ul.append(el('li',taskName(id)));box.append(ul)}
  if(list(value.operations).length){box.append(el('h4','Proposed changes'));value.operations.filter(op=>op&&typeof op==='object').forEach((op,i)=>{const d=disclosure((label(op.kind).replaceAll('_',' ')||'Change')+' · '+(op.spec?.brief?.title||taskName(op.task)||op.expansion||''),'operation:'+i,i===0);d.append(prose(op.spec?.brief?.intent||op.rationale||''));if(op.spec?.criteria)d.append(el('h5','Success criteria'),prose(op.spec.criteria));box.append(d)})}
  return box;
 }
 window.MitosGraphOutput={render(text,{planner=false,name='',resolve=()=>''}={}){
  let value,parsed=false;try{value=JSON.parse(text);parsed=true}catch{}
  if(!parsed)return name.endsWith('.patch')?window.MitosFormat.code(text,'diff'):name.endsWith('.txt')?window.MitosFormat.code(text,'text'):window.MitosMarkdown.view(text,{filename:name||'Output.md'});
  const box=el('div',undefined,'graph-structured-output');
  if(planner&&value&&typeof value==='object'&&typeof value.explanation==='string'&&(Array.isArray(value.steps)||Array.isArray(value.operations)))box.append(plan(value,resolve));else box.append(structured(value));
  const raw=disclosure('Raw data','raw');let loaded=false;raw.addEventListener('toggle',()=>{if(raw.open&&!loaded){loaded=true;raw.append(window.MitosFormat.code(text,'json'))}});box.append(raw);return box;
 }};
})();

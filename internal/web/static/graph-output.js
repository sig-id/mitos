(() => {
 'use strict';
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const hash=text=>{let value=2166136261;for(const char of text)value=Math.imul(value^char.charCodeAt(0),16777619);return (value>>>0).toString(36)};
 const prose=text=>window.MitosMarkdown.view(String(text||''));
 const list=value=>Array.isArray(value)?value:[];
 const label=value=>typeof value==='string'?value:'';
 function disclosure(title,key,open=false){const d=el('details',undefined,'graph-output-section');d.dataset.readKey=key;d.open=open;d.append(el('summary',title));return d}
 function plan(value,resolve){
  const box=el('div',undefined,'graph-plan-output');box.append(el('h4','Orchestrator summary'),prose(value.explanation));
  if(value.complete===true)box.append(el('p','The orchestrator proposes completing this goal.','graph-output-callout'));
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
  if(list(value.operations).length){box.append(el('h4','Proposed changes'));value.operations.filter(op=>op&&typeof op==='object').forEach((op,i)=>{const d=disclosure((label(op.kind).replaceAll('_',' ')||'Change')+' · '+(op.spec?.brief?.title||taskName(op.task)||op.expansion||''),'operation:'+i,i===0);d.append(prose(op.spec?.brief?.intent||op.rationale||''));if(op.spec?.criteria)d.append(el('h5','Success criteria'),prose(op.spec.criteria));const schedule=op.spec?.brief?.scheduling||{deps:op.dependencies,join:op.join},deps=list(schedule.deps);if(deps.length){const rule=schedule.join?.rule||'all';d.append(el('h5',rule==='k_of_n'?`Join: ${schedule.join.k} of ${deps.filter(dep=>dep.strength!=='soft').length}`:`Join: ${rule}`));for(const dep of deps)d.append(el('p',`${dep.inbox?'MQTT '+dep.inbox.project+'/'+dep.inbox.mailbox+' · '+dep.inbox.type:taskName(dep.from)} · ${dep.guard||'terminal'}${dep.strength==='soft'?' · optional':''}`))}box.append(d)})}
  return box;
 }
 function cleanLog(text){return String(text||'').replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g,'').replace(/\x1b\[[0-?]*[ -/]*[@-~]/g,'').replace(/\r\n?/g,'\n')}
 function log(text){const pre=el('pre',cleanLog(text)||'No execution output was saved.','graph-execution-log');pre.tabIndex=0;pre.setAttribute('aria-label','Execution log');return pre}
 // Only typed public records become prose. Unknown output stays in the raw log.
 function activity(text,{live=false,truncated=false}={}){
  const entries=[],byID=new Map();let message=null,structured=false;
  for(const line of cleanLog(text).split('\n')){
   let v;try{v=JSON.parse(line)}catch{continue}
   if(v?.mitos_activity!==1)continue;structured=true;
   if(v.kind==='message'){
    const id=typeof v.id==='string'?v.id:'';
    let entry=id?byID.get('message:'+id):message;
    if(!entry){entry={kind:'message',text:''};entries.push(entry);if(id)byID.set('message:'+id,entry)}
    if(typeof v.text==='string')entry.text=v.text;else if(typeof v.delta==='string')entry.text+=v.delta;
    message=entry;continue;
   }
   if(!['command','tool','file'].includes(v.kind))continue;
   message=null;
   const name=String(v.command||v.name||''),key=v.kind+':'+(v.id||name)+(v.kind==='file'?':'+name:'');
   let entry=byID.get(key);
   if(!entry||(!v.id&&v.status==='running')){entry={kind:v.kind,key:hash(key)+(!v.id?':'+entries.length:'')};entries.push(entry);byID.set(key,entry)}
   Object.assign(entry,{name,status:v.status,exit:v.exit_code});
  }
  if(!structured){
   // Historical logs have no reliable boundary between command output and prose.
   // Show known actions only; retain all other bytes in the optional raw view.
   for(const match of cleanLog(text).matchAll(/^(Started|Finished) (command|tool|file change): (.*)$/gm)){
    const kind=match[2]==='file change'?'file':match[2],name=match[3].replace(/ \(exit -?\d+\)$/,''),key=kind+':'+name;
    let entry=byID.get(key);if(!entry||match[1]==='Started'){entry={kind,name,key:hash(key)+':'+entries.length};entries.push(entry);byID.set(key,entry)}
    entry.status=match[1]==='Started'?'unknown':'completed';const exit=match[3].match(/ \(exit (-?\d+)\)$/);if(exit)entry.exit=Number(exit[1]);
   }
  }
  const feed=el('div',undefined,'graph-activity-feed');
  for(const entry of entries.slice(-80)){
   if(entry.kind==='message'){
    if(!entry.text.trim())continue;
    // Structured orchestrator answers are rendered as a proposal below the feed.
    if(/^\s*\{/.test(entry.text)||/^\s*\[\s*(?:\{|\[|"|\d|true|false|null|\])/.test(entry.text))continue;
    const row=el('article',undefined,'graph-agent-message');row.append(el('span','Agent','graph-activity-label'),prose(entry.text));feed.append(row);continue;
   }
   const failed=(typeof entry.exit==='number'&&entry.exit!==0)||['failed','declined'].includes(entry.status);
   const running=live&&entry.status==='running';
   let title=entry.kind==='command'?'Run command':entry.kind==='file'?'Edit file':'Use tool';
   let name=entry.name;
   if(entry.kind==='command'){
    name=name.replace(/^\S*\/?(?:bash|sh|zsh)\s+-[a-z]*c\s+/, '').replace(/^['"]/, '').split('\n')[0].replace(/['"]$/, '');
    // Multiline scripts and payloads belong in the disclosure, never its label.
    if(name.length>100)name=name.slice(0,97)+'…';
   }
   const d=disclosure('', 'activity:'+entry.key);d.className='graph-tool-row';
   const summary=d.querySelector('summary');summary.replaceChildren(el('span',failed?'!':running?'◌':'•','graph-tool-icon'),el('span',title,'graph-tool-label'),el('span',name,'graph-tool-name'),el('span',failed?'Failed':running?'Running':entry.status==='completed'?'Done':'Recorded','graph-tool-status'));
   if(failed)d.classList.add('failed');if(running)d.classList.add('running');
   d.append(window.MitosFormat.code(entry.name,entry.kind==='command'?'bash':'text'));
   if(typeof entry.exit==='number')d.append(el('p','Exit code '+entry.exit,'muted'));
   feed.append(d);
  }
  if(!feed.children.length)feed.append(el('p',live?'Waiting for the next agent update…':'No readable activity was recorded. The raw execution log is available below.','muted'));
  if(truncated||entries.length>80)feed.prepend(el('p','Showing recent activity. The saved execution log contains the full output.','muted'));
  return feed;
 }
 window.MitosGraphOutput={log,cleanLog,activity,render(text,{orchestrator=false,name='',compact=false,resolve=()=>''}={}){
  if(name==='output.txt'||name.endsWith('.log'))return log(text);
  if(name.endsWith('.patch')&&window.MitosDiff)return window.MitosDiff.preview(text);
  if(!String(text||'').trim())return el('p',name==='report.md'?'This attempt saved an empty report. Read its execution log for activity and failure details.':'No content was saved in this file.','muted');
  let value,parsed=false;try{value=JSON.parse(text);parsed=true}catch{}
  if(!parsed)return name.endsWith('.patch')?window.MitosFormat.code(text,'diff'):name.endsWith('.txt')?window.MitosFormat.code(text,'text'):window.MitosMarkdown.view(text,{filename:name||'Output.md',compact});
  const box=el('div',undefined,'graph-structured-output');
  if(orchestrator&&value&&typeof value==='object'&&typeof value.explanation==='string'&&(Array.isArray(value.steps)||Array.isArray(value.operations)))box.append(plan(value,resolve));
  const raw=disclosure('Raw data','raw');let loaded=false;raw.addEventListener('toggle',()=>{if(raw.open&&!loaded){loaded=true;raw.append(window.MitosFormat.code(text,'json'))}});box.append(raw);return box;
 }};
})();

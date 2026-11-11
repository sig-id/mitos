(() => {
  'use strict';
  const $ = id => document.getElementById(id), tree = $('analysis-activity');
  if (!tree) return;
  const el = (tag, text, cls) => { const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n; };
  let draftID='', previous='', expanded=new Map(), expandDefault=null;
  const labels={thinking:'Agent notes',codex:'Agent response',assistant:'Agent response',user:'Analysis instructions','tokens used':'Token usage'};
  function clean(text) { return text.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g,'').replace(/\r\n/g,'\n'); }
  function parse(text) {
    const entries=[];let entry={kind:'log',title:'Session setup',body:[]};
    const flush=()=>{if(entry.body.some(line=>line.trim())||entry.command)entries.push(entry);};
    const lines=clean(text).split('\n');
    for(let i=0;i<lines.length;i++) {
      const line=lines[i],next=lines[i+1]||'';
      if(line==='tokens used'&&/^\s*[\d,]+\s*$/.test(next)){flush();entries.push({kind:'usage',body:[next.trim()]});entry={kind:'message',title:'Agent response',body:[]};i++;continue;}
      if(line==='exec'&&next.trim()){flush();entry={kind:'command',command:next,body:[]};i++;continue;}
      if(labels[line]&&line!=='tokens used'){flush();entry={kind:'message',title:labels[line],body:[]};continue;}
      const result=/^\s*(succeeded in|exited |failed |timed out)/i.test(line);
      if(result){
        if(entry.kind!=='command'||entry.result){
          const partial=entry.kind==='log'&&entry.body.length===1&&/ in \//.test(entry.body[0])?entry.body.pop():'';
          flush();entry={kind:'command',command:partial||'Earlier command',partial:true,body:[]};
        }
        entry.result=line.trim().replace(/:$/,'');continue;
      }
      entry.body.push(line);
    }
    flush();return entries;
  }
  function key(text) { let h=2166136261;for(let i=0;i<text.length;i++)h=Math.imul(h^text.charCodeAt(i),16777619);return (h>>>0).toString(36); }
  function branch(id,title,meta,kind,open=false) {
    const details=el('details',undefined,`analysis-branch ${kind||''}`);details.dataset.key=id;
    details.open=expanded.has(id)?expanded.get(id):(expandDefault??open);
    const summary=el('summary'),titleNode=el('span',title,'analysis-title');summary.append(titleNode);
    if(meta)summary.append(el('span',meta,'analysis-badge'));details.append(summary);
    return details;
  }
  const prose=window.MitosFormat.markdown, code=window.MitosFormat.code;
  function formattedOutput(text,id){
    const box=el('div',undefined,'analysis-formatted-output');
    for(const [index,block] of window.MitosFormat.mixed(text).entries()){
      const lang=block.kind==='markdown'?'markdown':block.kind==='text'?window.MitosFormat.language(block.text):block.kind;
      const title=lang==='markdown'?'Document':lang==='text'?'Terminal output':`${lang==='go'?'Go':lang==='json'?'JSON':lang} source`;
      const section=branch(`${id}-block-${index}`,title,`${block.text.split('\n').length} lines`,'analysis-output-section',true);
      section.append(lang==='markdown'?prose(block.text):code(block.text,lang));box.append(section);
    }
    return box;
  }
  function commandTitle(command) {
    let title=command.replace(/\s+in\s+\/[^\n]*$/,'').replace(/^\S*\/(?:bash|sh|zsh)\s+-[a-z]*c\s+/, '');
    if((title.startsWith('"')&&title.endsWith('"'))||(title.startsWith("'")&&title.endsWith("'")))title=title.slice(1,-1);
    return title.length>130?title.slice(0,127)+'…':title;
  }
  function profileNode(profile) {
    const root=branch('profile','Project context','Ready for review','analysis-profile',true),body=el('div',undefined,'analysis-children');
    for(const [i,paragraph] of profile.context.trim().split(/\n\s*\n/).entries()) {
      const colon=paragraph.indexOf(':'),hasTitle=colon>0&&colon<85;
      const section=branch(`context-${i}`,hasTitle?paragraph.slice(0,colon):`Project notes ${i+1}`,'','analysis-context',i===0);
      section.append(prose(hasTitle?paragraph.slice(colon+1).trim():paragraph));body.append(section);
    }
    const fields=[['goal','Work goal'],['git_mode','Workspace layout'],['git_rules','Git'],['push_rules','Push'],['merge_rules','Merge'],['review_rules','Review'],['disk_rules','Disk and cleanup'],['rules','Coordination']];
    const rules=branch('profile-rules','Working rules','Proposed','analysis-context');
    for(const [field,label] of fields)if(profile[field]){const section=el('section',undefined,'analysis-rule');section.append(el('h4',label),prose(profile[field]));rules.append(section);}
    body.append(rules);root.append(body);return root;
  }
  function showMode(raw) {
    tree.hidden=raw;$('setup-output').hidden=!raw;$('analysis-tree-controls').hidden=raw;
    $('analysis-activity-mode').setAttribute('aria-pressed',String(!raw));$('analysis-raw-mode').setAttribute('aria-pressed',String(raw));
  }
  $('analysis-activity-mode').onclick=()=>showMode(false);$('analysis-raw-mode').onclick=()=>showMode(true);
  for(const [id,open] of [['analysis-expand',true],['analysis-collapse',false]])$(id).onclick=()=>{expandDefault=open;for(const node of tree.querySelectorAll('details')){node.open=open;expanded.set(node.dataset.key,open);}};
  window.MitosAnalysisOutput={render(output,draft){
    if(draftID!==draft.id){draftID=draft.id;previous='';expanded=new Map();expandDefault=null;showMode(false);tree.replaceChildren();}
    const text=output.text||'',signature=JSON.stringify([text,draft.status,draft.profile]);if(signature===previous)return;previous=signature;
    for(const node of tree.querySelectorAll('details'))expanded.set(node.dataset.key,node.open);
    const focus=document.activeElement?.closest('#analysis-activity details')?.dataset.key,scroll=tree.scrollTop;
    const raw=$('setup-output'),follow=raw.scrollHeight-raw.scrollTop-raw.clientHeight<40;
    raw.textContent=text||(draft.status==='running'?'Waiting for the agent to report progress…':'No output was recorded.');if(follow)raw.scrollTop=raw.scrollHeight;
    const entries=parse(text),commands=entries.filter(e=>e.kind==='command'),fragment=document.createDocumentFragment();
    const profile=draft.reviewed_profile||draft.profile;
    if(profile?.context)fragment.append(profileNode(profile));
    const usage=entries.filter(e=>e.kind==='usage').at(-1)?.body[0];$('analysis-count').textContent=[commands.length?`${commands.length} commands`:'',usage?`${usage} tokens`:''].filter(Boolean).join(' · ');
    if(commands.length){
      const group=branch('commands','Repository inspection',`${commands.length} commands`,'analysis-commands',true),children=el('div',undefined,'analysis-children'),occurrences=new Map();
      for(const entry of commands){
        const hash=key(entry.command),occurrence=occurrences.get(hash)||0;occurrences.set(hash,occurrence+1);const id=`command-${hash}-${occurrence}`;
        const failed=entry.result&&!/^(succeeded|exited 0(?:\D|$))/.test(entry.result);
        const status=entry.result?(failed?entry.result:entry.result.replace('succeeded in','Completed ·')):draft.status==='running'?'In progress':'No completion recorded';
        const node=branch(id,entry.partial?'Earlier command':commandTitle(entry.command),status,`analysis-command${failed?' analysis-failed':''}`);
        if(entry.command!=='Earlier command')node.append(code(entry.command,'bash'));
        const content=entry.body.join('\n').trim();
        if(content){const result=branch(id+'-output','Output',`${content.split('\n').length} lines`,'analysis-command-output');result.append(formattedOutput(content,id));node.append(result);}
        children.append(node);
      }
      group.append(children);fragment.append(group);
    }
    const other=entries.filter(e=>e.kind!=='command'&&e.kind!=='usage');let index=0;
    for(const entry of other){
      let content=entry.body.join('\n').trim();if(!content)continue;
      // The validated profile above is easier to read than repeated JSON copies.
      if(profile?.context){const lines=content.split('\n').filter(line=>{try{return !JSON.parse(line).context;}catch{return true;}});content=lines.join('\n').trim();if(!content)continue;}
      const failed=/^(?:error[: ]|fatal[: ]|.*sign-in failed)/im.test(content);
      const title=failed?'Diagnostics':entry.title||'Captured output';
      const node=branch(`note-${key(title)}-${index++}`,title,entry.kind==='log'?`${content.split('\n').length} lines`:'',failed?'analysis-failed':'analysis-note',entry.kind==='message'&&entry.title==='Agent notes');
      node.append(entry.kind==='message'?prose(content):formattedOutput(content,`captured-${index}`));fragment.append(node);
    }
    if(!fragment.childNodes.length)fragment.append(el('p',draft.status==='running'?'Waiting for the agent to report progress…':'No activity was recorded.','analysis-empty'));
    tree.replaceChildren(fragment);tree.scrollTop=scroll;
    if(focus)tree.querySelector(`[data-key="${CSS.escape(focus)}"] > summary`)?.focus({preventScroll:true});
  }};
})();

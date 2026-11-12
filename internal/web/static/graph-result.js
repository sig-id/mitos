(() => {
 'use strict';
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const button=(text,fn)=>{const b=el('button',text);b.type='button';b.onclick=fn;return b};
 const download=(text,url)=>{const a=el('a',text,'text-link');a.href=url;a.setAttribute('download','');return a};
 const artifact=(run,name)=>'/runs/'+encodeURIComponent(run.id)+'/artifacts/'+encodeURIComponent(name);
 const baseURL=(graph,task,run)=>'/api/graphs/'+encodeURIComponent(graph)+'/nodes/'+encodeURIComponent(task)+'/runs/'+encodeURIComponent(run);
 const fileURL=file=>file.artifact?artifact(file.entry.run,file.path):file.entry.base+'/file?path='+encodeURIComponent(file.path);
 const inventories=new Map();
 async function inventory(base,request){if(!inventories.has(base))inventories.set(base,request(base+'/files?scope=workspace').catch(e=>{inventories.delete(base);throw e}));return inventories.get(base)}
 function resolveFile(target,entries,context){
  let value;try{value=decodeURIComponent(target)}catch{return}
  let task='';const named=value.match(/^mitos:\/\/([^/]+)\/(.+)$/);if(named){task=named[1];value=named[2]}else if(/^[a-z][a-z0-9+.-]*:|^\/\//i.test(value))return;
  const location=value.match(/(?::|#L?)(\d+)(?:[-:]L?(\d+))?$/);let line=0,end=0;
  if(location){line=Number(location[1]);end=Number(location[2]||line);value=value.slice(0,location.index)}else value=value.split('#')[0];
  const candidates=task?entries.filter(e=>e.member.spec.id===task):context?[context.entry,...entries.filter(e=>e!==context.entry)]:entries;
  for(const entry of candidates){
   const paths=[value.replace(/^\.\//,'')];
   if(context?.entry===entry&&context.path){const folder=context.path.slice(0,context.path.lastIndexOf('/')+1);try{paths.unshift(new URL(value,'https://saved.invalid/'+folder).pathname.slice(1))}catch{}}
   const file=entry.files.find(f=>paths.includes(f.path)||value.endsWith('/'+f.path));
   if(file)return {...file,entry,line,end};
   if(entry.run.artifacts?.some(a=>a.name===value))return {path:value,entry,artifact:true,line,end};
  }
 }
 // Link known snapshot paths in prose, table cells and inline code. Never rewrite
 // fenced source, external links or paths that are not in a retained snapshot.
 function linkFiles(content,entries,context,open){
  const walk=document.createTreeWalker(content,NodeFilter.SHOW_TEXT),texts=[];
  while(walk.nextNode())if(!walk.currentNode.parentElement.closest('pre,a,button'))texts.push(walk.currentNode);
  const pattern=/\[([^\]\n]+)\]\(([^)\n]+)\)|(?:[\w.-]+\/)+[\w.-]+(?:(?::|#L?)\d+(?:[-:]L?\d+)?)?|\b[\w.-]+\.(?:md|txt|json|go|py|js|log)(?:(?::|#L?)\d+(?:[-:]L?\d+)?)?/g;
  for(const node of texts){const text=node.textContent,fragment=document.createDocumentFragment();let start=0;for(const m of text.matchAll(pattern)){const file=resolveFile(m[2]||m[0],entries,context);if(!file)continue;fragment.append(document.createTextNode(text.slice(start,m.index)),button(m[1]||m[0],()=>open(file)));start=m.index+m[0].length}if(start){fragment.append(document.createTextNode(text.slice(start)));node.replaceWith(fragment)}}
 }
 async function previewText(url){
  const response=await fetch(url,{cache:'no-store'});if(!response.ok)throw Error('This saved file is unavailable.');
  const reader=response.body.getReader(),chunks=[];let size=0;
  while(true){const {done,value}=await reader.read();if(done)break;size+=value.length;if(size>512*1024){await reader.cancel();throw Error('This file is too large to preview. Use Download file.')}chunks.push(value)}
  const body=new Uint8Array(size);let offset=0;for(const chunk of chunks){body.set(chunk,offset);offset+=chunk.length}
  const text=new TextDecoder('utf-8',{fatal:true}).decode(body);if(text.includes('\0'))throw Error('This is a binary file. Use Download file.');return text;
 }
 let generation=0;
 window.MitosGraphResult={
  async linkOutput(content,{graph,member,run,request,open}){
   if(!run.artifacts?.some(a=>a.name==='workspace.tar.gz'))return;
   const base=baseURL(graph.id,member.spec.id,run.id);
   try{const entry={member,run,base,files:await inventory(base,request)};if(content.isConnected)linkFiles(content,[entry],{entry,path:'report.md'},file=>open({task:member.spec.id,run:run.id,path:file.path,line:file.line,end:file.end,artifact:file.artifact}))}catch{/* Existing report stays readable if its archive is unavailable. */}
  },
  async open({graph,request,container,onClose,initial}){
   const token=++generation,current=()=>generation===token&&!container.hidden;
   const header=el('div',undefined,'graph-result-heading');header.append(el('h2',graph.result?'Result':'Saved work'),button('Back to graph',()=>{generation++;onClose()}));container.replaceChildren(header,el('p','Loading saved result…','muted'));
   try{
    const sources=graph.result?.sources||Object.values(graph.members).filter(m=>m.spec.kind==='application'&&!m.controller_for&&m.accepted&&!m.replacement).map(m=>({task:m.spec.id,run:m.accepted.run_id}));
    if(initial&&!sources.some(s=>s.task===initial.task&&s.run===initial.run))sources.push({task:initial.task,run:initial.run});
    const entries=await Promise.all(sources.map(async source=>{
     const member=graph.members[source.task],base=baseURL(graph.id,source.task,source.run),run=await request(base);let files=[],problem='';
     try{files=await inventory(base,request)}catch(e){problem=e.message}
     return {member,run,base,files,problem};
    }));
    if(!current())return;container.replaceChildren(header);
    const reader=el('article',undefined,'graph-result-reader'),readerHeading=el('div',undefined,'graph-result-heading'),body=el('div',undefined,'graph-result-content');reader.append(readerHeading,body);container.append(reader);
    let previewGeneration=0,history=[],location={kind:'result'};
    const open=file=>navigate({kind:'file',file});
    const options=context=>({link:(target,label)=>{const file=resolveFile(target,entries,context);return file?button(label,()=>open(file)):null}});
    function markup(text,context){const rendered=window.MitosFormat.markdown(text,options(context));linkFiles(rendered,entries,context,open);return rendered}
    function controls(title,url){readerHeading.replaceChildren();if(title)readerHeading.append(el('h3',title));const actions=el('div',undefined,'actions');if(history.length)actions.append(button('Back',()=>{location=history.pop();show()}));if(location.kind!=='result'&&graph.result)actions.append(button('Final answer',()=>navigate({kind:'result'})));if(url)actions.append(download('Download file',url));readerHeading.append(actions);readerHeading.hidden=!title&&!actions.children.length}
    async function show(){
     const version=++previewGeneration;
     if(location.kind==='result'){
      controls(graph.result?'':'Final answer missing');
      if(graph.result)body.replaceChildren(markup(graph.result.markdown));
      else body.replaceChildren(el('p','This older work completed without a final answer to the goal. Its worker reports and evidence are preserved below. A reviewer’s report is not a substitute for that answer.'));
      return;
     }
     const file=location.file;controls(file.path+(file.line?' · Line '+file.line:''),fileURL(file));body.replaceChildren(el('p','Loading saved file…'));
     try{
      const text=await previewText(fileURL(file));if(!current()||version!==previewGeneration)return;
      if(file.line){
       const pre=el('pre',undefined,'graph-source-lines'),lang=window.MitosFormat.language(text),lines=text.split('\n');pre.tabIndex=0;
       lines.forEach((line,i)=>{const row=el('span',undefined,'graph-source-line');row.dataset.line=String(i+1);row.append(el('span',String(i+1),'graph-line-number'),window.MitosFormat.highlight(line,lang));if(i+1>=file.line&&i+1<=file.end)row.classList.add('cited');pre.append(row,document.createTextNode('\n'))});body.replaceChildren(pre);const target=pre.querySelector('.cited');if(target)pre.scrollTop=Math.max(0,target.offsetTop-pre.offsetTop-70);
      }else body.replaceChildren(/\.(md|txt)$/i.test(file.path)?markup(text,file):window.MitosFormat.code(text));
     }catch(e){if(current()&&version===previewGeneration)body.replaceChildren(el('p',e.message,'muted'))}
    }
    async function navigate(next){history.push(location);location=next;await show();reader.scrollIntoView({block:'start'})}
    const evidence=el('details',undefined,'graph-result-evidence');evidence.append(el('summary','Worker reports & supporting evidence'));
    for(const entry of entries){
     const group=el('details',undefined,'graph-result-source');group.append(el('summary',entry.member.spec.brief.title));
     const links=el('div',undefined,'graph-deliverable-links');
     for(const name of ['report.md','changes.patch','workspace.tar.gz'])if(entry.run.artifacts?.some(a=>a.name===name))links.append(name==='report.md'?button('Read worker report',()=>open({path:name,entry,artifact:true})):download(name==='changes.patch'?'Download patch':'Download workspace',artifact(entry.run,name)));
     group.append(links);
     const search=el('input');search.type='search';search.placeholder='Find a saved file…';search.setAttribute('aria-label','Find a saved file in '+entry.member.spec.brief.title);
     const list=el('div',undefined,'graph-deliverable-links graph-saved-files');const render=()=>{const matching=entry.files.filter(f=>f.path.toLowerCase().includes(search.value.toLowerCase())).sort((a,b)=>a.path.localeCompare(b.path));list.replaceChildren(...matching.slice(0,100).map(f=>button(f.path,()=>open({...f,entry}))));if(matching.length>100)list.append(el('p','Showing 100 of '+matching.length+' files. Search to narrow the list.','muted'))};search.oninput=render;render();group.append(search,list);if(entry.problem)group.append(el('p',entry.problem,'muted'));evidence.append(group);
    }
    container.append(evidence);
    const review=el('details');review.append(el('summary','Planner completion review'),markup(graph.members[graph.root_task].accepted?.rationale||''));container.append(review);
    if(initial){const entry=entries.find(e=>e.member.spec.id===initial.task&&e.run.id===initial.run);if(entry)location={kind:'file',file:{...initial,entry}}}
    await show();
   }catch(e){if(current())container.append(el('p','Could not load the result: '+e.message,'form-error'))}
  }
 };
})();

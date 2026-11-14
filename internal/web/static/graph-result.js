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
   if(entry.run.artifacts?.some(a=>a.name===value))return {path:value,entry,artifact:true,line,end};
   const paths=[value.replace(/^\.\//,'')];
   if(context?.entry===entry&&context.path){const folder=context.path.slice(0,context.path.lastIndexOf('/')+1);try{paths.unshift(new URL(value,'https://saved.invalid/'+folder).pathname.slice(1))}catch{}}
   const file=entry.files.find(f=>paths.includes(f.path)||value.endsWith('/'+f.path));
   if(file)return {...file,entry,line,end};
  }
 }
 // Link known snapshot paths in prose, table cells and inline code. Never rewrite
 // fenced source, external links or paths that are not in a retained snapshot.
 function linkFiles(content,entries,context,open){
  const walk=document.createTreeWalker(content,NodeFilter.SHOW_TEXT),texts=[];
  while(walk.nextNode())if(!walk.currentNode.parentElement.closest('pre,a,button,.md-toolbar,.md-source,.md-status'))texts.push(walk.currentNode);
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
    const sources=[...(graph.result?.sources||[])];
    for(const m of Object.values(graph.members))if(m.spec.kind==='application'&&!m.orchestrator_for&&!m.replacement&&m.current_run_id&&!sources.some(s=>s.task===m.spec.id))sources.push({task:m.spec.id,run:m.accepted?.run_id||m.current_run_id});
    if(initial&&!sources.some(s=>s.task===initial.task&&s.run===initial.run))sources.push({task:initial.task,run:initial.run});
    const entries=await Promise.all(sources.map(async source=>{
     const member=graph.members[source.task],base=baseURL(graph.id,source.task,source.run);let run={id:source.run,status:'unavailable',artifacts:[]},files=[],problem='';
     try{run=await request(base);if(run.artifacts?.some(a=>a.name==='workspace.tar.gz'))files=await inventory(base,request)}catch(e){problem=e.message}
     return {member,run,base,files,problem};
    }));
    if(!current())return;container.replaceChildren(header);
    const reader=el('article',undefined,'graph-result-reader'),readerHeading=el('div',undefined,'graph-result-heading'),body=el('div',undefined,'graph-result-content');reader.append(readerHeading,body);
    const layout=el('div',undefined,'graph-file-layout'),sidebar=el('aside',undefined,'graph-file-sidebar');layout.append(sidebar,reader);container.append(layout);
    let previewGeneration=0,history=[],location={kind:'result'};
    const open=file=>navigate({kind:'file',file});
    const options=context=>({link:(target,label)=>{const file=resolveFile(target,entries,context);return file?button(label,()=>open(file)):null}});
    function markup(text,context){const rendered=window.MitosMarkdown.view(text,{...options(context),filename:context?.path||'Result.md'});linkFiles(rendered,entries,context,open);return rendered}
    function controls(title,url){readerHeading.replaceChildren();if(title)readerHeading.append(el('h3',title));const actions=el('div',undefined,'actions');if(history.length)actions.append(button('Back',()=>{location=history.pop();show()}));if(location.kind!=='result'&&graph.result)actions.append(button('Final answer',()=>navigate({kind:'result'})));if(url)actions.append(download('Download file',url));readerHeading.append(actions);if(location.file)readerHeading.append(el('p',location.file.entry.member.spec.brief.title+' · '+location.file.entry.run.status,'graph-preview-origin muted'));readerHeading.hidden=!title&&!actions.children.length}
    async function show(){
     const version=++previewGeneration;
     if(location.kind==='result'){
      controls(graph.result?'':'No final answer yet');
      if(graph.result){body.replaceChildren(markup(graph.result.markdown));const items=el('div',undefined,'graph-result-items');for(const item of graph.result.items||[]){if(item.kind==='link'){const a=el('a',item.title,'result-item');a.href=item.url;a.target='_blank';a.rel='noopener noreferrer';items.append(a)}else{const file=resolveFile('mitos://'+item.task+'/'+item.path,entries);if(file)items.append(button(item.title,()=>open(file)));else items.append(el('span',item.title+' · saved file unavailable','muted'))}}if(items.children.length)body.append(items);}
      else body.replaceChildren(el('p','This goal has no final answer. Saved reports may describe partial or failed work; they do not mean the goal is complete. Select an assignment and a file to read its report, inspect its changes, or browse its workspace snapshot.'));
      return;
     }
     const file=location.file;selectEntry(file.entry);browser.select(file.path);controls(file.path+(file.line?' · Line '+file.line:''),fileURL(file));body.replaceChildren(el('p','Loading saved file…'));
     try{
      const text=await previewText(fileURL(file));if(!current()||version!==previewGeneration)return;
      if(!text.trim()&&!file.path.endsWith('.patch')){body.replaceChildren(window.MitosGraphOutput.render(text,{name:file.path}));
      }else if(file.line){
       const pre=el('pre',undefined,'graph-source-lines'),lang=window.MitosFormat.language(text),lines=text.split('\n');pre.tabIndex=0;
       lines.forEach((line,i)=>{const row=el('span',undefined,'graph-source-line');row.dataset.line=String(i+1);row.append(el('span',String(i+1),'graph-line-number'),window.MitosFormat.highlight(line,lang));if(i+1>=file.line&&i+1<=file.end)row.classList.add('cited');pre.append(row,document.createTextNode('\n'))});body.replaceChildren(pre);const target=pre.querySelector('.cited');if(target)pre.scrollTop=Math.max(0,target.offsetTop-pre.offsetTop-70);
      }else body.replaceChildren(file.path.endsWith('.patch')&&window.MitosDiff?window.MitosDiff.preview(text):file.path==='output.txt'?window.MitosGraphOutput.log(text):/\.(md|txt)$/i.test(file.path)?markup(text,file):window.MitosFormat.code(text));
     }catch(e){if(current()&&version===previewGeneration)body.replaceChildren(el('p',e.message,'muted'))}
    }
    async function navigate(next){history.push(location);location=next;await show();reader.scrollIntoView({block:'start'})}
    sidebar.append(el('h3','Saved files'),el('p','Preview files here. Downloads are optional.','muted'));
    const source=el('select'),scope=el('select'),browser=el('mitos-file-browser'),note=el('p',undefined,'muted'),exports=el('details');
    const sourceLabel=el('label','Assignment');sourceLabel.append(source);const scopeLabel=el('label','Show');scopeLabel.append(scope);
    source.setAttribute('aria-label','Saved assignment');scope.setAttribute('aria-label','File view');
    entries.forEach((entry,i)=>source.append(new Option(entry.member.spec.brief.title+(entry.member.orchestrator_for?' · Orchestrator':'')+' · '+entry.run.status,String(i))));
    for(const [value,label]of [['reports','Reports & logs'],['changes','Changed files'],['workspace','Workspace snapshot']])scope.append(new Option(label,value));
    exports.append(el('summary','Export files'));sidebar.append(sourceLabel,scopeLabel,note,browser,exports);
    let activeEntry;
    function selectEntry(entry){if(activeEntry===entry)return;source.value=String(entries.indexOf(entry));activeEntry=entry;scope.value='reports';renderFiles()}
    let listingVersion=0;
    async function renderFiles(){
     const version=++listingVersion,entry=entries[Number(source.value)];activeEntry=entry;exports.replaceChildren(el('summary','Export files'));
     if(!entry){browser.configure([],{empty:'No saved attempts are available.'});note.textContent='Files appear after an agent saves its output.';return}
     for(const a of entry.run.artifacts||[])exports.append(download(a.name==='workspace.tar.gz'?'Download workspace archive':a.name,artifact(entry.run,a.name)));
     const mode=scope.value;
     note.textContent=mode==='reports'?(entry.problem?'Some saved files are unavailable: '+entry.problem:'Reports and execution logs saved by this attempt.'):mode==='changes'?'Files changed in this agent’s workspace copy. Review changes.patch for the full diff, including deletions.':'All files in the saved workspace, including unchanged source files.';
     let files;
     try{
      if(mode==='reports')files=(entry.run.artifacts||[]).filter(a=>a.name!=='workspace.tar.gz').map(a=>({path:a.name,artifact:true}));
      else if(mode==='workspace'){if(entry.problem)throw Error(entry.problem);files=entry.files}
      else{files=await request(entry.base+'/files');if(entry.run.artifacts?.some(a=>a.name==='changes.patch'))files=[{path:'changes.patch',artifact:true},...files]}
      if(!current()||version!==listingVersion)return;
      browser.configure(files,{open:file=>open({...file,entry}),empty:'No saved files in this view.'});if(location.file?.entry===entry)browser.select(location.file.path);
     }catch(e){if(version===listingVersion){browser.configure([],{empty:'File list unavailable.'});note.textContent=e.message}}
    }
    source.onchange=async()=>{await renderFiles();const entry=activeEntry,name=['report.md','output.txt','changes.patch'].find(name=>entry?.run.artifacts?.some(a=>a.name===name));if(name)open({path:name,entry,artifact:true});else{++previewGeneration;location={kind:'result'};controls('No saved report');body.replaceChildren(el('p',entry?.problem||'Select a saved file to preview it.'))}};scope.onchange=()=>renderFiles();await renderFiles();
    const rationale=graph.members[graph.root_task].accepted?.rationale?.trim();
    if(rationale){const review=el('details');review.append(el('summary','Orchestrator completion review'),markup(rationale));container.append(review)}
    if(initial){const entry=entries.find(e=>e.member.spec.id===initial.task&&e.run.id===initial.run);if(entry){const path=initial.path||(entry.run.artifacts?.some(a=>a.name==='report.md')?'report.md':entry.run.artifacts?.some(a=>a.name==='output.txt')?'output.txt':'');if(path)location={kind:'file',file:{...initial,path,artifact:initial.artifact??!initial.path,entry}};else selectEntry(entry)}}
    await show();
   }catch(e){if(current())container.append(el('p','Could not load the result: '+e.message,'form-error'))}
  }
 };
})();

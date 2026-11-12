(() => {
 'use strict';
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const button=(text,fn)=>{const b=el('button',text);b.type='button';b.onclick=fn;return b};
 const download=(text,url)=>{const a=el('a',text,'text-link');a.href=url;a.setAttribute('download','');return a};
 const artifact=(run,name)=>'/runs/'+encodeURIComponent(run.id)+'/artifacts/'+encodeURIComponent(name);
 const fileURL=(base,path)=>base+'/file?path='+encodeURIComponent(path);
 const references=text=>[...text.matchAll(/\[([^\]]+)\]\(([^)]+)\)/g)].map(m=>({label:m[1],path:m[2]}));
 function resolveFile(path,files){if(/^[a-z][a-z0-9+.-]*:|^\/\//i.test(path))return;let value;try{value=decodeURIComponent(path.split('#')[0])}catch{return}return files.find(f=>value===f.path||value==='./'+f.path||value.endsWith('/'+f.path))}
 async function previewText(url){const response=await fetch(url,{cache:'no-store'});if(!response.ok)throw Error('This saved file is unavailable.');const reader=response.body.getReader(),chunks=[];let size=0;while(true){const {done,value}=await reader.read();if(done)break;size+=value.length;if(size>512*1024){await reader.cancel();throw Error('This file is too large to preview. Use Download file.')}chunks.push(value)}const body=new Uint8Array(size);let offset=0;for(const chunk of chunks){body.set(chunk,offset);offset+=chunk.length}const text=new TextDecoder('utf-8',{fatal:true}).decode(body);if(text.includes('\0'))throw Error('This is a binary file. Use Download file.');return text}
 function linkFiles(content,files,open){
  const walk=document.createTreeWalker(content,NodeFilter.SHOW_TEXT),texts=[];while(walk.nextNode())if(!walk.currentNode.parentElement.closest('code,pre,a,button'))texts.push(walk.currentNode);
  for(const n of texts){const text=n.textContent,matches=[...text.matchAll(/\[([^\]]+)\]\(([^)]+)\)/g)];if(!matches.length)continue;const fragment=document.createDocumentFragment();let start=0;for(const m of matches){const file=resolveFile(m[2],files);if(!file)continue;fragment.append(document.createTextNode(text.slice(start,m.index)),button(m[1],()=>open(file)));start=m.index+m[0].length}if(start){fragment.append(document.createTextNode(text.slice(start)));n.replaceWith(fragment)}}
 }
 let generation=0;
 window.MitosGraphResult={async open({graph,nodes,request,container,onClose}){
  const token=++generation,current=()=>generation===token&&!container.hidden;
  const header=el('div',undefined,'graph-result-heading');header.append(el('h2','Result'),button('Back to graph',()=>{generation++;onClose()}));container.replaceChildren(header,el('p','Loading saved reports and deliverables…','muted'));
  try{
   const members=Object.values(graph.members).filter(m=>m.spec.kind==='application'&&!m.controller_for&&m.accepted&&!m.replacement);
   const entries=await Promise.all(members.map(async m=>{const base='/api/graphs/'+encodeURIComponent(graph.id)+'/nodes/'+encodeURIComponent(m.spec.id)+'/runs/'+encodeURIComponent(m.current_run_id),run=await request(base);let files=[],problem='';try{files=await request(base+'/files')}catch(e){problem=e.message}let report='';if(run.artifacts?.some(a=>a.name==='report.md')){try{report=await previewText(artifact(run,'report.md'))}catch(e){problem=e.message}}return {member:m,run,base,files,report,problem}}));
   if(!current())return;
   entries.sort((a,b)=>(b.run.finished_at||'').localeCompare(a.run.finished_at||''));container.replaceChildren(header);
   if(!entries.length){container.append(el('p','No accepted worker deliverables were saved for this goal.'));return}
   const files=new Map();for(const entry of entries)for(const f of entry.files)if(!files.has(f.path))files.set(f.path,{...f,entry});const allFiles=[...files.values()].sort((a,b)=>a.path.localeCompare(b.path));
   const reader=el('article',undefined,'graph-result-reader'),readerHeading=el('div',undefined,'graph-result-heading'),body=el('div',undefined,'graph-result-content');reader.append(readerHeading,body);let previewGeneration=0;
   async function openFile(file){const version=++previewGeneration;readerHeading.replaceChildren(el('h3',file.path),download('Download file',fileURL(file.entry.base,file.path)));body.replaceChildren(el('p','Loading saved file…'));try{const text=await previewText(fileURL(file.entry.base,file.path));if(!current()||previewGeneration!==version)return;body.replaceChildren(/\.(md|txt)$/i.test(file.path)?window.MitosFormat.markdown(text):window.MitosFormat.code(text));linkFiles(body,allFiles,openFile)}catch(e){if(current()&&previewGeneration===version)body.replaceChildren(el('p',e.message,'muted'))}}
   function openReport(entry){previewGeneration++;readerHeading.replaceChildren(el('h3',entry.member.spec.brief.title),download('Download report',artifact(entry.run,'report.md')));body.replaceChildren(window.MitosGraphOutput.render(entry.report||entry.run.summary||'No report was saved.',{name:'report.md'}));linkFiles(body,allFiles,openFile)}
   const linked=[];const seen=new Set();for(const entry of entries)for(const ref of references(entry.report)){const f=resolveFile(ref.path,allFiles);if(f&&!seen.has(f.path)){seen.add(f.path);linked.push({file:f,label:ref.label})}}
   const deliverables=el('section',undefined,'graph-deliverables');deliverables.append(el('h3','Deliverables'));
   if(linked.length){const list=el('div',undefined,'graph-deliverable-links');for(const item of linked)list.append(button(item.label===item.file.path?item.label:item.label+' · '+item.file.path,()=>openFile(item.file)));deliverables.append(list)}else deliverables.append(el('p','Saved worker reports and changed files are available below.','muted'));
   container.append(deliverables);
   const reports=el('label','Worker report'),select=el('select');select.setAttribute('aria-label','Worker report');entries.forEach((entry,i)=>select.append(new Option(entry.member.spec.brief.title,String(i))));select.onchange=()=>openReport(entries[Number(select.value)]);reports.append(select);container.append(reports,reader);
   const all=el('details',undefined,'graph-result-files');all.append(el('summary',`All changed files · ${allFiles.length}`));const list=el('div',undefined,'graph-deliverable-links');for(const file of allFiles)list.append(button(file.path,()=>openFile(file)));all.append(list);container.append(all);
   const archives=el('details');archives.append(el('summary','Patches & complete workspaces'));for(const entry of entries){const row=el('div',undefined,'graph-result-downloads');row.append(el('strong',entry.member.spec.brief.title));for(const name of ['changes.patch','workspace.tar.gz'])if(entry.run.artifacts?.some(a=>a.name===name))row.append(download(name==='changes.patch'?'Download patch':'Download workspace',artifact(entry.run,name)));archives.append(row)}container.append(archives);
   const review=el('details');review.append(el('summary','Planner completion review'),window.MitosFormat.markdown(graph.members[graph.root_task].accepted?.rationale||''));container.append(review);
   for(const entry of entries)if(entry.problem)container.append(el('p',entry.member.spec.brief.title+': '+entry.problem,'muted'));
   if(linked.length)await openFile(linked[0].file);else openReport(entries[0]);
  }catch(e){if(current())container.append(el('p','Could not load the result: '+e.message,'form-error'))}
 }};
})();

(() => {
 'use strict';
 const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const button=(text,fn)=>{const b=el('button',text);b.type='button';b.onclick=fn;return b};
 // A reusable, keyboard-accessible directory browser. File selection is separate
 // from fetching/previewing so callers can use immutable snapshots or artifacts.
 class MitosFileBrowser extends HTMLElement {
  configure(files,{open,empty='No files in this view.'}={}){
   this.files=files;this.openFile=open;this.empty=empty;this.folder='';this.limit=100;this.selected='';
   this.search=el('input');this.search.type='search';this.search.placeholder='Search files';this.search.setAttribute('aria-label','Search saved files');
   this.breadcrumb=el('nav',undefined,'file-breadcrumb');this.breadcrumb.setAttribute('aria-label','File directory');
   this.list=el('ul',undefined,'file-list');this.list.setAttribute('aria-label','Saved files');
   this.count=el('p',undefined,'muted file-count');this.count.setAttribute('aria-live','polite');
   this.search.oninput=()=>{this.limit=100;this.render()};this.replaceChildren(this.search,this.breadcrumb,this.list,this.count);this.render();return this;
  }
  select(path){this.selected=path;for(const row of this.list.querySelectorAll('button[data-path]'))row.setAttribute('aria-current',String(row.dataset.path===path))}
  render(){
   const query=this.search.value.toLowerCase().trim(),rows=new Map();
   this.breadcrumb.replaceChildren(button('All files',()=>{this.folder='';this.render()}));let prefix='';
   for(const segment of this.folder.split('/').filter(Boolean)){prefix+=segment+'/';const path=prefix;this.breadcrumb.append(el('span','/'),button(segment,()=>{this.folder=path;this.render()}))}
   for(const file of this.files){
    if(query){if(file.path.toLowerCase().includes(query))rows.set(file.path,{label:file.path,file});continue}
    if(!file.path.startsWith(this.folder))continue;const relative=file.path.slice(this.folder.length),slash=relative.indexOf('/');
    if(slash>=0){const label=relative.slice(0,slash+1);rows.set(this.folder+label,{label,folder:this.folder+label})}else rows.set(file.path,{label:relative,file});
   }
   const entries=[...rows.values()].sort((a,b)=>Number(!!b.folder)-Number(!!a.folder)||a.label.localeCompare(b.label));this.list.replaceChildren();
   for(const row of entries.slice(0,this.limit)){
    const li=el('li'),b=button('',()=>{if(row.folder){this.folder=row.folder;this.limit=100;this.render()}else{this.select(row.file.path);this.openFile?.(row.file)}});
    b.append(el('span',row.folder?'▸':'▤','file-icon'),el('span',row.label,'file-name'));
    if(row.folder)b.setAttribute('aria-label','Open folder '+row.label);else{b.dataset.path=row.file.path;b.setAttribute('aria-current',String(row.file.path===this.selected));if(row.file.kind&&row.file.kind!=='snapshot')b.append(el('small',row.file.kind,'file-kind'))}
    li.append(b);this.list.append(li);
   }
   this.count.textContent=entries.length?`${entries.length} ${query?'matches':'items'}${query?'':' in this folder'}`:query?'No files match your search.':this.empty;
   if(entries.length>this.limit)this.count.append(button('Show more',()=>{this.limit+=100;this.render()}));
  }
 }
 customElements.define('mitos-file-browser',MitosFileBrowser);
})();

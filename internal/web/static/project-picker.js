(() => {
 'use strict';
 const $=id=>document.getElementById(id),dialog=$('project-dialog'),path=$('project-directory'),name=$('project-name');
 if(!dialog||!path||!name)return;
 let listing=null,request=0,controller,edited=false;
 const basename=p=>p.trim().replace(/\/+$/,'').split('/').at(-1)||(p.startsWith('/')?'/':'');
 function suggest(){if(!edited)name.value=basename(path.value);}
 name.addEventListener('input',()=>{edited=!!name.value.trim();});
 path.addEventListener('input',()=>{request++;controller?.abort();$('folder-list').setAttribute('aria-busy','false');$('folder-status').textContent='';suggest();});
 function button(text,fn){const b=document.createElement('button');b.type='button';b.textContent=text;b.onclick=fn;return b;}
 function render(){
  const list=$('folder-list'),query=$('folder-filter').value.toLowerCase();list.replaceChildren();
  const dirs=listing.directories.filter(d=>d.name.toLowerCase().includes(query));
  for(const d of dirs){const b=button(d.name,()=>browse(d.path));b.dataset.folder=d.path;b.className='folder-row';const arrow=document.createElement('span');arrow.textContent='›';arrow.setAttribute('aria-hidden','true');b.append(arrow);list.append(b);}
  $('folder-status').textContent=listing.truncated?'Large folder: some entries are omitted. Enter a path to open another folder.':dirs.length?'':query?'No matching folders.':'No subfolders. You can open this folder as a project.';
 }
 async function browse(value){
  const token=++request;controller?.abort();controller=new AbortController();const activeController=controller,timer=setTimeout(()=>activeController.abort(),10000);
  const oldPath=path.value;
  $('folder-error').textContent='';$('folder-list').replaceChildren();$('folder-list').setAttribute('aria-busy','true');$('folder-status').textContent='Loading folders…';
  try{
   const response=await fetch('/api/directories?'+new URLSearchParams({path:value||'',hidden:$('folder-hidden').checked}),{signal:activeController.signal,cache:'no-store'});
   const next=await response.json();if(!response.ok)throw Error(next.error||'Could not read this folder.');
   if(token!==request||path.value!==oldPath)return;
   listing=next;path.value=next.path;suggest();$('folder-filter').value='';
   const crumbs=$('folder-breadcrumbs');crumbs.replaceChildren(button('/',()=>browse('/')));let current='';
   for(const part of next.path.split('/').filter(Boolean)){current+='/'+part;const target=current;crumbs.append(button(part,()=>browse(target)));}
   $('folder-up').disabled=next.path===next.parent;render();
  }catch(e){if(token!==request)return;$('folder-error').textContent=e.name==='AbortError'?'Folder lookup timed out. Try again.':e.message;$('folder-status').textContent='';}
  finally{clearTimeout(timer);if(token===request)$('folder-list').setAttribute('aria-busy','false');}
 }
 $('folder-go').onclick=()=>browse(path.value);
 path.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();browse(path.value);}});
 $('folder-up').onclick=()=>browse(listing?.parent||'');
 $('folder-home').onclick=()=>browse(listing?.home||'~');
 $('folder-workspace').onclick=()=>browse(listing?.workspace||'');
 $('folder-hidden').onchange=()=>browse(path.value);
 $('folder-filter').oninput=()=>{if(listing)render();};
 $('folder-filter').onkeydown=e=>{if(e.key==='Enter')e.preventDefault();};
 new MutationObserver(()=>{if(dialog.open)browse(path.value);else{request++;controller?.abort();}}).observe(dialog,{attributes:true,attributeFilter:['open']});
 if(dialog.open)browse(path.value);else suggest();
})();

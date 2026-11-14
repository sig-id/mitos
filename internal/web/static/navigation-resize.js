(() => {
 'use strict';
 const shell=document.querySelector('.os-shell'),rail=shell?.querySelector(':scope > .os-rail');
 if(!rail)return;
 const storageKey='mitos.sidebar.width',desktop=matchMedia('(min-width:641px)');
 let preferred=null,drag=null;
 try{const saved=Number(localStorage.getItem(storageKey));if(Number.isFinite(saved)&&saved>=200&&saved<=480)preferred=saved}catch{}
 const handle=document.createElement('div');handle.className='sidebar-resizer';handle.tabIndex=0;
 handle.setAttribute('role','separator');handle.setAttribute('aria-orientation','vertical');handle.setAttribute('aria-label','Sidebar width');
 rail.id ||= 'workspace-navigation';handle.setAttribute('aria-controls',rail.id);handle.title='Drag to resize sidebar. Arrow keys adjust width; Enter or double-click resets.';
 shell.append(handle);
 const maximum=()=>Math.min(480,Math.max(200,shell.clientWidth-440));
 function apply(){
  handle.hidden=!desktop.matches;
  const value=Math.round(Math.min(maximum(),Math.max(200,preferred??(innerWidth<=1100?210:248))));
  shell.style.setProperty('--sidebar-width',value+'px');
  handle.setAttribute('aria-valuemin','200');handle.setAttribute('aria-valuemax',String(maximum()));handle.setAttribute('aria-valuenow',String(value));handle.setAttribute('aria-valuetext',value+' pixels');
 }
 function persist(){try{if(preferred===null)localStorage.removeItem(storageKey);else localStorage.setItem(storageKey,String(preferred))}catch{}}
 function setWidth(value){preferred=Math.round(Math.min(maximum(),Math.max(200,value)));apply()}
 function reset(){preferred=null;apply();persist()}
 function finish(cancel=false){if(!drag)return;const previous=drag;drag=null;if(cancel){preferred=previous.preferred;apply()}else persist();document.body.classList.remove('resizing-sidebar');if(handle.hasPointerCapture(previous.id))handle.releasePointerCapture(previous.id)}
 handle.onpointerdown=event=>{if(event.button!==0||!desktop.matches)return;event.preventDefault();handle.focus();drag={id:event.pointerId,x:event.clientX,width:rail.getBoundingClientRect().width,preferred};handle.setPointerCapture(event.pointerId);document.body.classList.add('resizing-sidebar')};
 handle.onpointermove=event=>{if(drag&&event.pointerId===drag.id)setWidth(drag.width+event.clientX-drag.x)};
 handle.onpointerup=()=>finish();handle.onpointercancel=()=>finish(true);handle.onlostpointercapture=()=>finish();handle.ondblclick=reset;
 handle.onkeydown=event=>{
  if(event.key==='Escape'&&drag){event.preventDefault();finish(true);return}
  const step=event.shiftKey?50:10,current=Number(handle.getAttribute('aria-valuenow'));
  const values={ArrowLeft:current-step,ArrowRight:current+step,Home:200,End:maximum()};
  if(event.key==='Enter'){event.preventDefault();reset()}else if(event.key in values){event.preventDefault();setWidth(values[event.key]);persist()}
 };
 window.addEventListener('resize',()=>{if(!desktop.matches)finish(true);apply()});
 window.addEventListener('pagehide',()=>finish());
 apply();
})();

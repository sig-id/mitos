// Polling updates execution facts without discarding an operator's form or reading position.
(() => {
 let expanded=[];
 document.addEventListener('htmx:sendError', () => {
  const notice=document.getElementById('network-error');if(notice)notice.hidden=false;
 });
 document.addEventListener('htmx:beforeRequest', event => {
  if(event.detail.elt?.id==='live'&&document.activeElement?.closest('form'))event.preventDefault();
 });
 document.addEventListener('htmx:beforeSwap', event => {
  if(event.detail.target?.id==='live')expanded=[...document.querySelectorAll('#live details[id][open]')].map(n=>n.id);
 });
 document.addEventListener('htmx:afterSwap', event => {
  if(event.detail.target?.id==='live')for(const id of expanded){const n=document.getElementById(id);if(n)n.open=true;}
  const notice=document.getElementById('network-error');if(notice)notice.hidden=true;
 });
})();
